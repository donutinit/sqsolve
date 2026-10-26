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
	"net"
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
	commit  = "unknown"
)

const usage = `sqsolve: free server for DaVinci Resolve project libraries

Usage:
  sqsolve serve                         starts the panel and the service
  sqsolve init                          prepares volumes and secrets (sqsolve-init container)
  sqsolve healthcheck                   check for Docker
  sqsolve doctor [--json]               checks volumes, secrets, PostgreSQL and the catalog
  sqsolve version
  sqsolve admin list
  sqsolve admin create <user> [--role admin|operator|viewer]
  sqsolve admin reset-password <user>
  sqsolve admin disable-totp <user>
  sqsolve admin unlock <user>
  sqsolve library list
  sqsolve backup create <identifier> [--wait]
  sqsolve backup list <identifier>
  sqsolve restore <backup-id> --new <identifier> [--name <name>] [--wait]
  sqsolve restore <backup-id> --replace --confirm <identifier> [--wait]
  sqsolve catalog rebuild [--wait]
  sqsolve catalog forget <backup-id>     removes a blocked row (invalid path) after review
  sqsolve audit verify

Console actions are queued in the same database the server uses; the running
server executes them. They appear in the audit log with the actor "console".
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
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", cmd, usage)
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
			return slog.String(a.Key, "[redacted]")
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
	if uid, gid := os.Getuid(), os.Getgid(); uid != c.UID || gid != c.GID {
		return fmt.Errorf("SQS-C053 the service runs as %d:%d but SQSOLVE_UID and SQSOLVE_GID say %d:%d. "+
			"In the compose file, the user: line of the sqsolve service must equal SQSOLVE_UID:SQSOLVE_GID; "+
			"then run sqsolve-init again so it fixes the owners", uid, gid, c.UID, c.GID)
	}
	if err := app.ApplyPendingRecovery(c, os.Stderr); err != nil {
		return fmt.Errorf("applying the recovery: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	a, err := app.Open(ctx, c, log)
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.EnsureKeyCheck(ctx); err != nil {
		log.Warn("key check", "err", err)
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
		log.Info("panel listening", "addr", c.Listen, "tls", !c.InsecureHTTP)
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
			_, port, err := net.SplitHostPort(c.Listen)
			if err != nil || port == "" {
				port = "9432"
			}
			red := &http.Server{Addr: c.HTTPRedirect, ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				host, _, err := net.SplitHostPort(r.Host)
				if err != nil {
					host = strings.Trim(r.Host, "[]")
				}
				http.Redirect(w, r, "https://"+net.JoinHostPort(host, port)+r.URL.RequestURI(), http.StatusMovedPermanently)
			})}
			if err := red.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}
	select {
	case <-ctx.Done():
		log.Info("stopping sqsolve")
	case err := <-errCh:
		return err
	}
	stopGracefully(srv, a)
	return nil
}

// Stop budget, inside the 120 s stop_grace_period of the compose file: up
// to 10 s for the HTTP requests, then 100 s shared by the jobs and the
// notifications, which go out last so that what the jobs emit while
// stopping (job.interrupted) is delivered. The jobs get the 100 s minus a
// reserve for one delivery attempt.
const (
	httpStopBudget = 10 * time.Second
	workStopBudget = 100 * time.Second
	notifyReserve  = 10 * time.Second
)

// stopGracefully stops the panel, then the jobs (cancellable ones end as
// interrupted, like after a crash), then the notifier.
func stopGracefully(srv *http.Server, a *app.App) {
	hctx, hcancel := context.WithTimeout(context.Background(), httpStopBudget)
	defer hcancel()
	srv.Shutdown(hctx)
	wctx, wcancel := context.WithTimeout(context.Background(), workStopBudget)
	defer wcancel()
	a.Shutdown(workStopBudget - notifyReserve)
	a.Notify.Flush(wctx)
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
		return fmt.Errorf("healthz answered %s", resp.Status)
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
	asJSON := fs.Bool("json", false, "JSON output")
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
		mark := map[string]string{"ok": "ok  ", "warn": "warn", "fail": "FAIL"}[ch.Status]
		fmt.Printf("[%s] %s: %s\n", mark, ch.Name, ch.Detail)
		if ch.Hint != "" && ch.Status != "ok" {
			fmt.Printf("        %s\n", ch.Hint)
		}
		fail = fail || ch.Status == "fail"
	}
	if fail {
		return errors.New("some checks failed")
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
		return errors.New("missing subcommand: list, create, reset-password, disable-totp, unlock")
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
		fmt.Fprintln(tw, "USER\tROLE\tTOTP\tDISABLED")
		for _, u := range users {
			fmt.Fprintf(tw, "%s\t%s\t%v\t%v\n", u.Username, u.Role.Label(), u.TOTPEnabled, u.DisabledAt != nil)
		}
		return tw.Flush()
	case "create":
		fs := flag.NewFlagSet("create", flag.ExitOnError)
		role := fs.String("role", "admin", "role")
		if len(args) < 2 {
			return errors.New("usage: sqsolve admin create <user> [--role admin]")
		}
		fs.Parse(args[2:])
		pw, err := readPassword("Password: ")
		if err != nil {
			return err
		}
		if _, err := a.Auth.CreateUser(ctx, audit.CLI, args[1], "", pw, auth.Role(*role)); err != nil {
			return err
		}
		a.ClearInstallCode()
		fmt.Println("User created.")
	case "reset-password":
		if len(args) < 2 {
			return errors.New("usage: sqsolve admin reset-password <user>")
		}
		u, err := a.Auth.UserByName(ctx, args[1])
		if err != nil {
			return err
		}
		pw, err := readPassword("New password: ")
		if err != nil {
			return err
		}
		if err := a.Auth.SetPassword(ctx, audit.CLI, u.ID, pw, ""); err != nil {
			return err
		}
		fmt.Println("Password changed; their sessions were closed.")
	case "disable-totp":
		if len(args) < 2 {
			return errors.New("usage: sqsolve admin disable-totp <user>")
		}
		u, err := a.Auth.UserByName(ctx, args[1])
		if err != nil {
			return err
		}
		if err := a.Auth.DisableTOTP(ctx, audit.CLI, u.ID); err != nil {
			return err
		}
		fmt.Println("Second factor disabled.")
	case "unlock":
		if len(args) < 2 {
			return errors.New("usage: sqsolve admin unlock <user>")
		}
		u, err := a.Auth.UserByName(ctx, args[1])
		if err != nil {
			return err
		}
		if err := a.Auth.Unlock(ctx, audit.CLI, u.ID); err != nil {
			return err
		}
		fmt.Println("Account unlocked; the count of wrong passwords is back to zero. After a burst of recent attempts the panel may still ask to wait (SQS-A003, at most 15 minutes).")
	default:
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
	return nil
}

func libraryCmd(args []string) error {
	if len(args) < 1 || args[0] != "list" {
		return errors.New("usage: sqsolve library list")
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
	fmt.Fprintln(tw, "IDENTIFIER\tNAME\tSTATE\tLAST VERIFIED")
	for _, l := range libs {
		last := "none"
		if b, _ := a.Svc.LatestVerified(ctx, l.ID); b != nil {
			last = b.StartedAt.In(a.Cfg.Location).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", l.Slug, l.DisplayName, l.State.Label(), last)
	}
	return tw.Flush()
}

func waitJob(ctx context.Context, a *app.App, j *jobs.Job) error {
	fmt.Printf("Job %s queued (%s).\n", j.ID, j.Title)
	for {
		got, err := a.Jobs.Get(ctx, j.ID)
		if err != nil {
			return err
		}
		if got.State.Finished() {
			fmt.Printf("Finished: %s\n", got.State.Label())
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
		return errors.New("usage: sqsolve backup create|list <identifier>")
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
		j, err := a.Jobs.Enqueue(ctx, core.JobBackupCreate, l.ID, core.BackupParams{Kind: core.KindManual}, audit.CLI, jobs.Priority(80),
			jobs.Audited("backup.create", "library", l.ID, nil))
		if err != nil {
			return err
		}
		if hasFlag(args, "--wait") {
			return waitJob(ctx, a, j)
		}
		fmt.Printf("Job %s queued.\n", j.ID)
	case "list":
		bs, err := a.Svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Limit: 100})
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tDATE\tKIND\tSTATUS\tVERIFICATION\tSIZE")
		for _, b := range bs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", b.ID, b.StartedAt.In(a.Cfg.Location).Format("2006-01-02 15:04"), b.Kind, b.Status, b.VerifyStatus, core.HumanBytes(b.SizeBytes))
		}
		return tw.Flush()
	default:
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
	return nil
}

func restoreCmd(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: sqsolve restore <backup-id> --new <identifier> | --replace --confirm <identifier>")
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
		return errors.New("pass --new <identifier> or --replace --confirm <identifier>")
	}
	if err != nil {
		return err
	}
	if hasFlag(args, "--wait") {
		return waitJob(ctx, a, j)
	}
	fmt.Printf("Job %s queued.\n", j.ID)
	return nil
}

func catalogCmd(args []string) error {
	const use = "usage: sqsolve catalog rebuild [--wait] | forget <backup-id>"
	if len(args) < 1 || args[0] != "rebuild" && (args[0] != "forget" || len(args) != 2) {
		return errors.New(use)
	}
	a, ctx, done, err := openCLI(false)
	if err != nil {
		return err
	}
	defer done()
	if args[0] == "forget" {
		if err := a.Svc.ForgetInvalidBackup(ctx, audit.CLI, args[1]); err != nil {
			return err
		}
		fmt.Printf("Backup %s removed from the catalog; no file was touched.\n", args[1])
		return nil
	}
	j, err := a.Jobs.Enqueue(ctx, core.JobCatalogRebuild, "", nil, audit.CLI, jobs.Audited("maintenance.catalog", "system", "", nil))
	if err != nil {
		return err
	}
	if hasFlag(args, "--wait") {
		return waitJob(ctx, a, j)
	}
	fmt.Printf("Job %s queued.\n", j.ID)
	return nil
}

func auditCmd(args []string) error {
	if len(args) < 1 || args[0] != "verify" {
		return errors.New("usage: sqsolve audit verify")
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
		return fmt.Errorf("the chain is broken at entry %d of %d", broken, n)
	}
	fmt.Printf("Audit log intact: %d entries.\n", n)
	return nil
}
