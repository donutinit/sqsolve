// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/donutinit/sqsolve/internal/api"
	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/testpg"
	"github.com/donutinit/sqsolve/internal/web"
)

type browser struct {
	t    *testing.T
	base string
	c    *http.Client
}

func (b *browser) do(method, path string, form url.Values) (int, string, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, b.base+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(data)
}

// page fetches a page following redirects and fails on server errors.
func (b *browser) page(path string) string {
	b.t.Helper()
	for i := 0; i < 5; i++ {
		st, loc, body := b.do("GET", path, nil)
		if st == http.StatusSeeOther || st == http.StatusFound {
			path = loc
			continue
		}
		if st != 200 || strings.Contains(body, "SQS-X99") {
			b.t.Fatalf("GET %s: %d\n%s", path, st, body[:min(len(body), 800)])
		}
		return body
	}
	b.t.Fatalf("too many redirects for %s", path)
	return ""
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func csrf(t *testing.T, body string) string {
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no csrf token in page")
	}
	return m[1]
}

func TestPanelEndToEnd(t *testing.T) {
	c := testpg.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	appDir, backups := t.TempDir(), t.TempDir()
	cfg := &config.Config{AppDir: appDir, BackupDir: backups, PGDir: c.Base, PGDataDir: c.DataDir, PGSocket: c.SocketDir,
		PGPort: c.Port, PGListen: "127.0.0.1", PGBinDir: c.BinDir, PGMaxConnections: 50, PGMemoryMB: 256,
		UID: os.Getuid(), GID: os.Getgid(), PGUID: os.Getuid(), PGGID: os.Getgid(), LogLevel: "warn", LogFormat: "text",
		Workers: 2, HeavyJobs: 1, Location: time.UTC, InsecureHTTP: true, Listen: "127.0.0.1:0"}
	if err := os.WriteFile(cfg.SuperuserPasswordFile(), []byte(c.SuperPass.Reveal()+"\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := app.Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.Open(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.EnsureKeyCheck(ctx)
	a.PrepareInstallCode(ctx)
	code := a.InstallCode()
	h, err := web.New(a, (&api.API{A: a}).Register)
	if err != nil {
		t.Fatal(err)
	}
	a.Background(ctx)
	defer a.Shutdown(5 * time.Second)
	for i := 0; i < 100 && !a.PGReady(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !a.PGReady() {
		t.Fatalf("PostgreSQL not ready\n%s", c.Log())
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	b := &browser{t: t, base: srv.URL, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	// Security headers.
	st, _, _ := b.do("GET", "/healthz", nil)
	resp, _ := b.c.Get(srv.URL + "/entrar")
	if st != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers missing: %v", resp.Header)
	}
	resp.Body.Close()

	// Setup refuses a wrong code, then works.
	setup := b.page("/instalar")
	pre := csrf(t, setup)
	form := url.Values{"csrf": {pre}, "code": {"WRONG-CODE"}, "username": {"diego"}, "password": {"luz de tungsteno 3200K"},
		"password2": {"luz de tungsteno 3200K"}, "host": {"192.168.1.10"}, "name": {"Estudio"}, "preset": {"diario"}}
	if _, loc, _ := b.do("POST", "/instalar", form); !strings.HasPrefix(loc, "/instalar") || strings.Contains(loc, "error") {
		t.Fatalf("wrong code accepted or error in URL: %s", loc)
	}
	if !strings.Contains(b.page("/instalar"), `class="flash error"`) {
		t.Fatal("wrong code error not shown")
	}
	form.Set("code", code)
	if _, loc, _ := b.do("POST", "/instalar", form); loc != "/instalar/kit" {
		t.Fatalf("setup failed: %s", loc)
	}
	b.page("/instalar/kit")
	if _, loc, _ := b.do("GET", "/instalar", nil); loc != "/entrar" {
		t.Fatal("setup reachable after admin exists")
	}
	dash := b.page("/")
	if !strings.Contains(dash, "Protección de los últimos 14 días") {
		t.Fatal("dashboard without the protection timeline")
	}
	tok := csrf(t, dash)

	// A POST without CSRF is refused.
	if st, _, _ := b.do("POST", "/bibliotecas/nueva", url.Values{"name": {"x"}}); st != http.StatusForbidden {
		t.Fatalf("POST without csrf: %d", st)
	}

	// Create a library and initialize it like Resolve would.
	_, loc, _ := b.do("POST", "/bibliotecas/nueva", url.Values{"csrf": {tok}, "name": {"Boda Ana y Luis"}, "strategy": {"resolve"}})
	if !strings.HasPrefix(loc, "/bibliotecas/") {
		t.Fatalf("create library: %s", loc)
	}
	libPath := loc
	libID := strings.TrimPrefix(libPath, "/bibliotecas/")
	var l *core.Library
	for i := 0; i < 50; i++ {
		l, _ = a.Svc.Library(ctx, libID)
		if l.State == core.StateAwaitingInit {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(b.page(libPath), "Inicializa la biblioteca desde Resolve") {
		t.Fatal("awaiting page without instructions")
	}
	_, _, reveal := b.do("POST", libPath+"/contrasena/ver", url.Values{"csrf": {tok}})
	pw := regexp.MustCompile(`id="r-pass">([^<]+)<`).FindStringSubmatch(reveal)
	if pw == nil {
		t.Fatalf("password not revealed:\n%s", reveal)
	}
	conn, err := c.ConnectTCP(ctx, l.OwnerRole, secrets.New(pw[1]), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	conn.Exec(ctx, "CREATE DATABASE "+l.DBName)
	conn.Close(ctx)
	conn, err = c.ConnectTCP(ctx, l.OwnerRole, secrets.New(pw[1]), l.DBName)
	if err != nil {
		t.Fatal(err)
	}
	conn.Exec(ctx, `CREATE TABLE "SM_Project"(id serial primary key, name text); INSERT INTO "SM_Project"(name) VALUES ('uno'), ('dos')`)
	conn.Close(ctx)
	for i := 0; i < 10; i++ {
		a.Svc.WatchInit(ctx)
	}
	if l, _ = a.Svc.Library(ctx, libID); l.State != core.StateActive {
		t.Fatalf("library not active: %s", l.State)
	}

	// Backup from the panel and wait for the job.
	_, jobLoc, _ := b.do("POST", libPath+"/respaldar", url.Values{"csrf": {tok}})
	waitJob(t, a, strings.TrimPrefix(jobLoc, "/tareas/"))
	b.page(jobLoc)

	// Every page renders.
	for _, p := range []string{"/", "/bibliotecas", "/bibliotecas?ver=papelera", "/bibliotecas/nueva", "/respaldos", "/respaldos/importar",
		"/tareas", "/tareas?estado=failed", "/actividad", "/actividad?tab=auditoria", "/salud", "/ajustes", "/ajustes?tab=usuarios",
		"/ajustes?tab=notificaciones", "/ajustes?tab=notificaciones&nuevo=smtp", "/ajustes?tab=mantenimiento", "/ajustes?tab=acerca",
		"/cuenta", libPath + "/instrucciones", libPath + "/eliminar"} {
		b.page(p)
	}
	for _, tab := range []string{"conexion", "respaldos", "programacion", "accesos", "actividad", "ajustes", "peligro"} {
		b.page(libPath + "?tab=" + tab)
	}

	// Experimental export: off by default; on, it refuses a library whose
	// tables are not Resolve's, and every error carries its code.
	if pg := b.page(libPath + "/exportar"); !strings.Contains(pg, "SQS-E002") || !strings.Contains(pg, "Experimental") {
		t.Fatalf("export tab with the feature off:\n%s", pg[:min(len(pg), 600)])
	}
	if st, _, _ := b.do("GET", libPath+"/exportar/proyectos/00000000-0000-0000-0000-000000000000", nil); st != http.StatusConflict {
		t.Fatalf("download with the feature off: %d", st)
	}
	expSettings := a.Svc.Settings(ctx)
	expSettings.ExperimentalExports = true
	if err := a.Svc.SaveSettings(ctx, audit.System, expSettings); err != nil {
		t.Fatal(err)
	}
	if pg := b.page(libPath + "/exportar"); !strings.Contains(pg, "SQS-E001") || !strings.Contains(pg, "SM_Project.ClipInfoSeed") {
		i := strings.Index(pg, "SQS-E")
		t.Fatalf("export tab without Resolve's schema: %s", pg[max(0, i-100):min(len(pg), i+400)])
	}
	if st, _, body := b.do("GET", libPath+"/exportar/stills/zzz", nil); st != http.StatusUnprocessableEntity || !strings.Contains(body, "SQS-E001") {
		t.Fatalf("download without Resolve's schema: %d", st)
	}
	if !strings.Contains(b.page("/ajustes"), `name="experimental_exports" checked`) {
		t.Fatal("experimental export setting not shown as enabled")
	}
	backupsPage := b.page("/respaldos")
	bid := regexp.MustCompile(`href="/respaldos/([0-9A-Z]{26})"`).FindStringSubmatch(backupsPage)
	if bid == nil {
		t.Fatal("no backup listed")
	}
	b.page("/respaldos/" + bid[1])
	b.page("/respaldos/" + bid[1] + "/restaurar")

	// Restore as a new library through the panel.
	_, rj, _ := b.do("POST", "/respaldos/"+bid[1]+"/restaurar", url.Values{"csrf": {tok}, "mode": {"nueva"}, "slug": {"boda_copia"}, "name": {"Copia"}})
	waitJob(t, a, strings.TrimPrefix(rj, "/tareas/"))
	if cp, err := a.Svc.LibraryBySlug(ctx, "boda_copia"); err != nil || cp.State != core.StateActive {
		t.Fatalf("restored copy: %v %+v", err, cp)
	}

	// Import by upload: the CSRF part comes first and the file is streamed
	// into the import folder, then moved into the catalog.
	_, _, archive := b.do("GET", "/respaldos/"+bid[1]+"/descargar", nil)
	if !strings.HasPrefix(archive, "PGDMP") {
		t.Fatal("download is not a pg_dump archive")
	}
	upload := func(fields [][2]string) (int, string) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		for _, f := range fields {
			if f[0] == "upload" {
				fw, _ := mw.CreateFormFile("upload", f[1])
				fw.Write([]byte(archive))
				continue
			}
			mw.WriteField(f[0], f[1])
		}
		mw.Close()
		req, _ := http.NewRequest("POST", srv.URL+"/respaldos/importar", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := b.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}
	if st, _ := upload([][2]string{{"library", libID}, {"csrf", tok}, {"upload", "x.backup"}}); st != http.StatusForbidden {
		t.Fatalf("multipart with the CSRF part after others: %d", st)
	}
	st, loc = upload([][2]string{{"csrf", tok}, {"library", libID}, {"upload", "subido.backup"}})
	if st != http.StatusSeeOther || !strings.HasPrefix(loc, "/tareas/") {
		t.Fatalf("upload import: %d %s", st, loc)
	}
	waitJob(t, a, strings.TrimPrefix(loc, "/tareas/"))
	if left, _ := os.ReadDir(a.Svc.ImportDir()); len(left) != 0 {
		t.Fatalf("upload left %d files in the import folder", len(left))
	}

	// A database created in Resolve with another name is pointed out.
	_, loc2, _ := b.do("POST", "/bibliotecas/nueva", url.Values{"csrf": {tok}, "name": {"Comercial"}, "slug": {"comercial"}, "strategy": {"resolve"}})
	lib2 := strings.TrimPrefix(loc2, "/bibliotecas/")
	for i := 0; i < 50; i++ {
		if l2, _ := a.Svc.Library(ctx, lib2); l2 != nil && l2.State == core.StateAwaitingInit {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, _, reveal2 := b.do("POST", loc2+"/contrasena/ver", url.Values{"csrf": {tok}})
	pw2 := regexp.MustCompile(`id="r-pass">([^<]+)<`).FindStringSubmatch(reveal2)
	// pg_hba.conf was just reloaded for the new role; the postmaster applies
	// the reload asynchronously, so give it a moment under load.
	var conn2 *pgx.Conn
	for i := 0; i < 30; i++ {
		if conn2, err = c.ConnectTCP(ctx, "comercial", secrets.New(pw2[1]), "postgres"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	conn2.Exec(ctx, `CREATE DATABASE "Comercial"`)
	conn2.Close(ctx)
	a.Svc.WatchInit(ctx)
	if !strings.Contains(b.page(loc2), "<code>Comercial</code>, pero la biblioteca debe llamarse exactamente") {
		t.Fatal("misnamed database not pointed out")
	}

	// Policy form round trip.
	if _, loc, _ := b.do("POST", libPath+"/politica", url.Values{"csrf": {tok}, "enabled": {"on"}, "preset": {"cadahora"},
		"keep_last": {"5"}, "keep_daily": {"7"}, "keep_weekly": {"4"}, "keep_monthly": {"3"}, "min_verified": {"1"}}); !strings.Contains(loc, "programacion") {
		t.Fatalf("policy: %s", loc)
	}
	if p, _ := a.Svc.Policy(ctx, libID); p.Cron != "0 * * * *" || p.KeepLast != 5 {
		t.Fatalf("policy not saved: %+v", p)
	}

	// API: a read-only token can list but not create.
	_, _, tokPage := b.do("POST", "/cuenta/tokens", url.Values{"csrf": {tok}, "name": {"ci"}, "scope": {"read"}, "days": {"30"}})
	token := regexp.MustCompile(`id="tok">([^<]+)<`).FindStringSubmatch(tokPage)
	if token == nil {
		t.Fatal("token not shown")
	}
	apiGet := func(path string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token[1])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if st, out := apiGet("/api/v1/libraries/" + libID + "/projects"); st != http.StatusUnprocessableEntity || out["code"] != "SQS-E001" {
		t.Fatalf("projects without Resolve's schema: %d %v", st, out)
	}
	expSettings.ExperimentalExports = false
	a.Svc.SaveSettings(ctx, audit.System, expSettings)
	if st, out := apiGet("/api/v1/libraries/" + libID + "/grades"); st != http.StatusConflict || out["code"] != "SQS-E002" {
		t.Fatalf("grades with the feature off: %d %v", st, out)
	}
	if st, out := apiGet("/api/v1/libraries/" + libID + "/projects/x/export"); st != http.StatusForbidden || out["code"] != "SQS-A030" {
		t.Fatalf("read token exported: %d %v", st, out)
	}
	if st, out := apiGet("/api/v1/libraries"); st != 200 || len(out["items"].([]any)) != 3 {
		t.Fatalf("api libraries: %d %v", st, out)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/libraries", strings.NewReader(`{"display_name":"x"}`))
	req.Header.Set("Authorization", "Bearer "+token[1])
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read token created a library: %d", resp.StatusCode)
	}
	if st, _ := apiGet("/api/v1/system/status"); st != 200 {
		t.Fatal("api status")
	}
	if st, _, _ := b.do("GET", "/readyz", nil); st != 200 {
		t.Fatal("readyz")
	}

	// Logout ends the session.
	b.do("POST", "/salir", url.Values{"csrf": {tok}})
	if _, loc, _ := b.do("GET", "/", nil); !strings.HasPrefix(loc, "/entrar") {
		t.Fatalf("still logged in after logout: %s", loc)
	}
}

func waitJob(t *testing.T, a *app.App, id string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		j, err := a.Jobs.Get(context.Background(), id)
		if err == nil && j.State.Finished() {
			if j.State != jobs.Succeeded {
				t.Fatalf("job %s %s: %s", j.Type, j.State, j.ErrorDetail)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("job %s timed out", id)
}
