// SPDX-License-Identifier: AGPL-3.0-only

// Command sqsolve is a free, self-hosted server for DaVinci Resolve project
// libraries. One binary: server, volume initializer and recovery CLI.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/sys/unix"

	"github.com/donutinit/sqsolve/internal/api"
	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/web"
)

var (
	version = "dev"
	commit  = "desconocido"
)

const usage = `sqsolve: servidor libre de bibliotecas de proyectos de DaVinci Resolve

Uso:
  sqsolve serve                         arranca el panel y el servicio
  sqsolve init                          prepara volúmenes y secretos (contenedor sqsolve-init)
  sqsolve healthcheck                   comprobación para Docker
  sqsolve doctor [--json]               revisa volúmenes, secretos, PostgreSQL y catálogo
  sqsolve version
  sqsolve admin list
  sqsolve admin create <usuario> [--role admin|operator|viewer]
  sqsolve admin reset-password <usuario>
  sqsolve admin disable-totp <usuario>
  sqsolve library list
  sqsolve backup create <identificador> [--wait]
  sqsolve backup list <identificador>
  sqsolve restore <id-respaldo> --new <identificador> [--name <nombre>] [--wait]
  sqsolve restore <id-respaldo> --replace --confirm <identificador> [--wait]
  sqsolve catalog rebuild [--wait]
  sqsolve audit verify

Las acciones de la consola se encolan en la misma base que usa el servidor; el
servidor en marcha las ejecuta. Quedan en la auditoría con actor "consola".
`

func main() {
	app.Version, app.Commit = version, commit
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "init":
		err = initCmd()
	case "healthcheck":
		err = healthcheck()
	case "version", "--version", "-v":
		fmt.Printf("sqsolve %s (%s)\n", version, commit)
	case "doctor":
		err = doctor(args)
	case "admin":
		err = adminCmd(args)
	case "library":
		err = libraryCmd(args)
	case "backup":
		err = backupCmd(args)
	case "restore":
		err = restoreCmd(args)
	case "catalog":
		err = catalogCmd(args)
	case "audit":
		err = auditCmd(args)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "comando desconocido: %s\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sqsolve:", err)
		os.Exit(1)
	}
}

func newLogger(c *config.Config) *slog.Logger {
	level := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}[c.LogLevel]
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: redact}
	if c.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

var sensitive = []string{"password", "token", "secret", "authorization", "cookie", "key"}

func redact(groups []string, a slog.Attr) slog.Attr {
	k := strings.ToLower(a.Key)
	for _, s := range sensitive {
		if strings.Contains(k, s) {
			return slog.String(a.Key, "[redactado]")
		}
	}
	return a
}

func loadConfig() (*config.Config, error) { return config.Load() }

func serve() error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	log := newLogger(c)
	if err := app.ApplyPendingRecovery(c, os.Stderr); err != nil {
		return fmt.Errorf("aplicando la recuperación: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	a, err := app.Open(ctx, c, log)
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.EnsureKeyCheck(ctx); err != nil {
		log.Warn("comprobación de clave", "err", err)
	}
	if !c.InsecureHTTP {
		if err := a.LoadPanelCert(); err != nil {
			return err
		}
	}
	a.RecordStart(ctx)
	a.PrepareInstallCode(ctx)
	apiSrv := &api.API{A: a}
	h, err := web.New(a, apiSrv.Register)
	if err != nil {
		return err
	}
	a.Background(ctx)
	srv := a.HTTPServer(h)
	errCh := make(chan error, 2)
	go func() {
		log.Info("panel escuchando", "addr", c.Listen, "tls", !c.InsecureHTTP)
		var err error
		if c.InsecureHTTP {
			err = srv.ListenAndServe()
		} else {
			err = srv.ListenAndServeTLS("", "")
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	if c.HTTPRedirect != "" {
		go func() {
			red := &http.Server{Addr: c.HTTPRedirect, ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				host := r.Host
				if i := strings.LastIndexByte(host, ':'); i > 0 {
					host = host[:i]
				}
				_, port, _ := strings.Cut(c.Listen, ":")
				http.Redirect(w, r, "https://"+host+":"+port+r.URL.RequestURI(), http.StatusMovedPermanently)
			})}
			if err := red.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}
	select {
	case <-ctx.Done():
		log.Info("deteniendo sqsolve")
	case err := <-errCh:
		return err
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
	a.Shutdown(100 * time.Second)
	return nil
}

func initCmd() error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	return app.Init(c, os.Stdout)
}

func healthcheck() error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	addr := c.Listen
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	scheme := "https"
	if c.InsecureHTTP {
		scheme = "http"
	}
	cl := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := cl.Get(scheme + "://" + addr + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthz respondió %s", resp.Status)
	}
	return nil
}

// openCLI opens the app for console commands. PostgreSQL is connected when
// available but not required.
func openCLI(needPG bool) (*app.App, context.Context, func(), error) {
	c, err := loadConfig()
	if err != nil {
		return nil, nil, nil, err
	}
	c.LogLevel = "warn"
	log := newLogger(c)
	ctx := context.Background()
	a, err := app.Open(ctx, c, log)
	if err != nil {
		return nil, nil, nil, err
	}
	if needPG {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := a.PG.Open(cctx)
		cancel()
		if err != nil {
			a.Close()
			return nil, nil, nil, err
		}
	}
	return a, ctx, a.Close, nil
}

func doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "salida JSON")
	fs.Parse(args)
	a, ctx, done, err := openCLI(false)
	if err != nil {
		return err
	}
	defer done()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	a.PG.Open(cctx)
	cancel()
	checks := app.Doctor(ctx, a)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(checks)
	}
	fail := false
	for _, ch := range checks {
		mark := map[string]string{"ok": "ok  ", "warn": "aviso", "fail": "FALLA"}[ch.Status]
		fmt.Printf("[%s] %s: %s\n", mark, ch.Name, ch.Detail)
		if ch.Hint != "" && ch.Status != "ok" {
			fmt.Printf("        %s\n", ch.Hint)
		}
		fail = fail || ch.Status == "fail"
	}
	if fail {
		return errors.New("hay comprobaciones fallidas")
	}
	return nil
}

func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	fd := int(os.Stdin.Fd())
	if old, err := unix.IoctlGetTermios(fd, unix.TCGETS); err == nil {
		noEcho := *old
		noEcho.Lflag &^= unix.ECHO
		noEcho.Lflag |= unix.ICANON | unix.ISIG
		if err := unix.IoctlSetTermios(fd, unix.TCSETS, &noEcho); err == nil {
			defer func() {
				unix.IoctlSetTermios(fd, unix.TCSETS, old)
				fmt.Fprintln(os.Stderr)
			}()
		}
	}
	var b strings.Builder
	buf := make([]byte, 1)
	for b.Len() < 1024 {
		n, err := os.Stdin.Read(buf)
		if n == 0 || err != nil || buf[0] == '\n' {
			break
		}
		if buf[0] != '\r' {
			b.WriteByte(buf[0])
		}
	}
	return b.String(), nil
}

func adminCmd(args []string) error {
	if len(args) < 1 {
		return errors.New("falta el subcomando: list, create, reset-password, disable-totp")
	}
	a, ctx, done, err := openCLI(false)
	if err != nil {
		return err
	}
	defer done()
	switch args[0] {
	case "list":
		users, err := a.Auth.Users(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "USUARIO\tROL\tTOTP\tDESACTIVADO")
		for _, u := range users {
			fmt.Fprintf(tw, "%s\t%s\t%v\t%v\n", u.Username, u.Role.Label(), u.TOTPEnabled, u.DisabledAt != nil)
		}
		return tw.Flush()
	case "create":
		fs := flag.NewFlagSet("create", flag.ExitOnError)
		role := fs.String("role", "admin", "rol")
		if len(args) < 2 {
			return errors.New("uso: sqsolve admin create <usuario> [--role admin]")
		}
		fs.Parse(args[2:])
		pw, err := readPassword("Contraseña: ")
		if err != nil {
			return err
		}
		if _, err := a.Auth.CreateUser(ctx, audit.CLI, args[1], "", pw, auth.Role(*role)); err != nil {
			return err
		}
		a.ClearInstallCode()
		fmt.Println("Usuario creado.")
	case "reset-password":
		if len(args) < 2 {
			return errors.New("uso: sqsolve admin reset-password <usuario>")
		}
		u, err := a.Auth.UserByName(ctx, args[1])
		if err != nil {
			return err
		}
		pw, err := readPassword("Contraseña nueva: ")
		if err != nil {
			return err
		}
		if err := a.Auth.SetPassword(ctx, audit.CLI, u.ID, pw, ""); err != nil {
			return err
		}
		fmt.Println("Contraseña cambiada; se cerraron sus sesiones.")
	case "disable-totp":
		if len(args) < 2 {
			return errors.New("uso: sqsolve admin disable-totp <usuario>")
		}
		u, err := a.Auth.UserByName(ctx, args[1])
		if err != nil {
			return err
		}
		if err := a.Auth.DisableTOTP(ctx, audit.CLI, u.ID); err != nil {
			return err
		}
		fmt.Println("Segundo factor desactivado.")
	default:
		return fmt.Errorf("subcomando desconocido: %s", args[0])
	}
	return nil
}

func libraryCmd(args []string) error {
	if len(args) < 1 || args[0] != "list" {
		return errors.New("uso: sqsolve library list")
	}
	a, ctx, done, err := openCLI(false)
	if err != nil {
		return err
	}
	defer done()
	libs, err := a.Svc.Libraries(ctx, core.ListOptions{})
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "IDENTIFICADOR\tNOMBRE\tESTADO\tÚLTIMO VERIFICADO")
	for _, l := range libs {
		last := "ninguno"
		if b, _ := a.Svc.LatestVerified(ctx, l.ID); b != nil {
			last = b.StartedAt.In(a.Cfg.Location).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", l.Slug, l.DisplayName, l.State.Label(), last)
	}
	return tw.Flush()
}

func waitJob(ctx context.Context, a *app.App, j *jobs.Job) error {
	fmt.Printf("Tarea %s encolada (%s).\n", j.ID, j.Title)
	for {
		got, err := a.Jobs.Get(ctx, j.ID)
		if err != nil {
			return err
		}
		if got.State.Finished() {
			fmt.Printf("Terminó: %s\n", got.State.Label())
			if got.State != jobs.Succeeded {
				return errors.New(got.ErrorDetail)
			}
			return nil
		}
		time.Sleep(time.Second)
	}
}

func hasFlag(args []string, f string) bool {
	for _, a := range args {
		if a == f {
			return true
		}
	}
	return false
}

func flagValue(args []string, f string) string {
	for i, a := range args {
		if a == f && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func backupCmd(args []string) error {
	if len(args) < 2 {
		return errors.New("uso: sqsolve backup create|list <identificador>")
	}
	a, ctx, done, err := openCLI(false)
	if err != nil {
		return err
	}
	defer done()
	l, err := a.Svc.LibraryBySlug(ctx, args[1])
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		a.Audit.Record(ctx, audit.CLI, "backup.create", "library", l.ID, audit.OK, nil)
		j, err := a.Jobs.Enqueue(ctx, core.JobBackupCreate, l.ID, core.BackupParams{Kind: core.KindManual}, audit.CLI, jobs.Priority(80))
		if err != nil {
			return err
		}
		if hasFlag(args, "--wait") {
			return waitJob(ctx, a, j)
		}
		fmt.Printf("Tarea %s encolada.\n", j.ID)
	case "list":
		bs, err := a.Svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Limit: 100})
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tFECHA\tTIPO\tESTADO\tVERIFICACIÓN\tTAMAÑO")
		for _, b := range bs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", b.ID, b.StartedAt.In(a.Cfg.Location).Format("2006-01-02 15:04"), b.Kind, b.Status, b.VerifyStatus, core.HumanBytes(b.SizeBytes))
		}
		return tw.Flush()
	default:
		return fmt.Errorf("subcomando desconocido: %s", args[0])
	}
	return nil
}

func restoreCmd(args []string) error {
	if len(args) < 2 {
		return errors.New("uso: sqsolve restore <id-respaldo> --new <identificador> | --replace --confirm <identificador>")
	}
	a, ctx, done, err := openCLI(true)
	if err != nil {
		return err
	}
	defer done()
	id := args[0]
	var j *jobs.Job
	if slug := flagValue(args, "--new"); slug != "" {
		name := flagValue(args, "--name")
		if name == "" {
			name = slug
		}
		_, j, err = a.Svc.RestoreAsNew(ctx, audit.CLI, id, slug, name)
	} else if hasFlag(args, "--replace") {
		j, err = a.Svc.RestoreReplace(ctx, audit.CLI, id, flagValue(args, "--confirm"), hasFlag(args, "--terminate"))
	} else {
		return errors.New("indica --new <identificador> o --replace --confirm <identificador>")
	}
	if err != nil {
		return err
	}
	if hasFlag(args, "--wait") {
		return waitJob(ctx, a, j)
	}
	fmt.Printf("Tarea %s encolada.\n", j.ID)
	return nil
}

func catalogCmd(args []string) error {
	if len(args) < 1 || args[0] != "rebuild" {
		return errors.New("uso: sqsolve catalog rebuild [--wait]")
	}
	a, ctx, done, err := openCLI(false)
	if err != nil {
		return err
	}
	defer done()
	j, err := a.Jobs.Enqueue(ctx, core.JobCatalogRebuild, "", nil, audit.CLI)
	if err != nil {
		return err
	}
	if hasFlag(args, "--wait") {
		return waitJob(ctx, a, j)
	}
	fmt.Printf("Tarea %s encolada.\n", j.ID)
	return nil
}

func auditCmd(args []string) error {
	if len(args) < 1 || args[0] != "verify" {
		return errors.New("uso: sqsolve audit verify")
	}
	a, ctx, done, err := openCLI(false)
	if err != nil {
		return err
	}
	defer done()
	broken, n, err := a.Audit.Verify(ctx)
	if err != nil {
		return err
	}
	if broken != 0 {
		return fmt.Errorf("la cadena está rota en la entrada %d de %d", broken, n)
	}
	fmt.Printf("Auditoría íntegra: %d entradas.\n", n)
	return nil
}
