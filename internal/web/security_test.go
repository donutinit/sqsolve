// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/api"
	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/web"
)

// sqliteApp builds a real App and router with SQLite only: no PostgreSQL and
// no background workers, enough for the authentication and settings paths.
func sqliteApp(t *testing.T) (*app.App, *httptest.Server) {
	t.Helper()
	base := t.TempDir()
	cfg := &config.Config{AppDir: filepath.Join(base, "app"), BackupDir: filepath.Join(base, "backups"), PGDir: filepath.Join(base, "pg"),
		PGDataDir: filepath.Join(base, "pg", "data"), PGSocket: filepath.Join(base, "sock"), PGPort: 1, PGListen: "127.0.0.1",
		PGMaxConnections: 50, PGMemoryMB: 256, UID: os.Getuid(), GID: os.Getgid(), PGUID: os.Getuid(), PGGID: os.Getgid(),
		LogLevel: "warn", LogFormat: "text", Workers: 1, HeavyJobs: 1, Location: time.UTC, InsecureHTTP: true, Listen: "127.0.0.1:0"}
	os.MkdirAll(filepath.Dir(cfg.SuperuserPasswordFile()), 0o700)
	if err := os.WriteFile(cfg.SuperuserPasswordFile(), []byte("x\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := app.Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	h, err := web.New(a, (&api.API{A: a}).Register)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return a, srv
}

func newBrowser(t *testing.T, srv *httptest.Server) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: srv.URL, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// signIn posts the login form and returns the browser and where it was sent.
func signIn(t *testing.T, srv *httptest.Server, user, pass string) (*browser, string) {
	t.Helper()
	b := newBrowser(t, srv)
	pre := csrf(t, b.page("/login"))
	_, loc, _ := b.do("POST", "/login", url.Values{"csrf": {pre}, "username": {user}, "password": {pass}})
	return b, loc
}

func mustSignIn(t *testing.T, srv *httptest.Server, user, pass string) *browser {
	t.Helper()
	b, loc := signIn(t, srv, user, pass)
	if loc != "/" {
		t.Fatalf("login %s: %s", user, loc)
	}
	return b
}

// flashOf reads the single-read notice the panel set with the last redirect.
func flashOf(b *browser) string {
	u, _ := url.Parse(b.base)
	for _, c := range b.c.Jar.Cookies(u) {
		if c.Name != "sqsolve_flash" {
			continue
		}
		j, err := base64.RawURLEncoding.DecodeString(c.Value)
		if err != nil {
			return ""
		}
		var f struct {
			M string `json:"m"`
		}
		json.Unmarshal(j, &f)
		return f.M
	}
	return ""
}

// Audit M4: next=/<TAB>/host was echoed into Location, and browsers strip
// the tab, which turns it into a cross-origin redirect.
func TestLoginNextRejectsOffsiteRedirects(t *testing.T) {
	a, srv := sqliteApp(t)
	a.Auth.CreateUser(context.Background(), audit.System, "boss", "", "tungsten light 3200K", auth.RoleAdmin)
	b := mustSignIn(t, srv, "boss", "tungsten light 3200K")
	su, _ := url.Parse(srv.URL)
	// RoundTrip directly: http.Client would parse Location and lose the
	// raw bytes a browser receives.
	raw := func(method, path, body string, cookies []*http.Cookie) string {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.Header.Get("Location")
	}
	pre := strings.Repeat("B", 24)
	for _, next := range []string{"/\t/evil.example/x", "/\n/evil.example", "/\r/evil.example", "/%2F/evil.example", "/%2f/evil.example",
		"/\\evil.example", "\\/evil.example", "/%5C/evil.example", "//evil.example", "https://evil.example/", "/ /evil.example",
		"/\x00/evil.example", "/ /evil.example", "evil.example", "javascript:alert(1)", "/%09/evil.example"} {
		if loc := raw("GET", "/login?next="+url.QueryEscape(next), "", b.c.Jar.Cookies(su)); loc != "/" {
			t.Errorf("GET /login signed in, next=%q: Location %q", next, loc)
		}
		loc := raw("POST", "/login", url.Values{"csrf": {pre}, "username": {"boss"}, "password": {"tungsten light 3200K"}, "next": {next}}.Encode(),
			[]*http.Cookie{{Name: "sqsolve_pre", Value: pre}})
		if loc != "/" {
			t.Errorf("POST /login, next=%q: Location %q", next, loc)
		}
	}
	// A same-origin path with its query survives.
	if loc := raw("GET", "/login?next="+url.QueryEscape("/libraries?view=trash"), "", b.c.Jar.Cookies(su)); loc != "/libraries?view=trash" {
		t.Errorf("legitimate next lost: %q", loc)
	}
}

// Audit M3: ten anonymous wrong passwords locked the administrator for good,
// closed the open session and broke the API tokens.
func TestLockoutSparesSessionsAndTokens(t *testing.T) {
	a, srv := sqliteApp(t)
	ctx := context.Background()
	a.Auth.CreateUser(ctx, audit.System, "boss", "", "tungsten light 3200K", auth.RoleAdmin)
	b := mustSignIn(t, srv, "boss", "tungsten light 3200K")
	tok := csrf(t, b.page("/account"))
	_, _, tokPage := b.do("POST", "/account/tokens", url.Values{"csrf": {tok}, "name": {"ci"}, "scope": {"read"}, "days": {"30"}})
	token := regexp.MustCompile(`id="tok">([^<]+)<`).FindStringSubmatch(tokPage)
	if token == nil {
		t.Fatal("token not shown")
	}
	clock := time.Now()
	a.Auth.Now = func() time.Time { return clock }
	t.Cleanup(func() { a.Auth.Now = time.Now })
	anon := newBrowser(t, srv)
	pre := strings.Repeat("A", 24)
	u, _ := url.Parse(srv.URL)
	anon.c.Jar.SetCookies(u, []*http.Cookie{{Name: "sqsolve_pre", Value: pre}})
	for i := 0; i < auth.LockoutAttempts; i++ {
		anon.do("POST", "/login", url.Values{"csrf": {pre}, "username": {"boss"}, "password": {"guess"}})
		clock = clock.Add(time.Minute) // waits out the per-IP back-off
	}
	if u, _ := a.Auth.UserByName(ctx, "boss"); u.LockedAt == nil {
		t.Fatal("ten wrong passwords did not lock the account")
	}
	if st, loc, _ := b.do("GET", "/", nil); st != http.StatusOK {
		t.Fatalf("the administrator's open session was closed by the lock: %d %s", st, loc)
	}
	if users := b.page("/settings?tab=users"); !strings.Contains(users, ">Locked<") || !strings.Contains(users, "Password logins refused until") {
		t.Fatal("the users table does not show the lock")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/jobs", nil)
	req.Header.Set("Authorization", "Bearer "+token[1])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the API token stopped working after the lock: %d", resp.StatusCode)
	}
	// A password login is refused while the lock lasts, with its code.
	b2, loc := signIn(t, srv, "boss", "tungsten light 3200K")
	if !strings.HasPrefix(loc, "/login") || !strings.Contains(flashOf(b2), "SQS-A004") {
		t.Fatalf("login during the lock: %s %q", loc, flashOf(b2))
	}
	// After LockoutDuration the lock is gone without a console.
	clock = clock.Add(auth.LockoutDuration + time.Minute)
	b3 := mustSignIn(t, srv, "boss", "tungsten light 3200K")
	if strings.Contains(b3.page("/settings?tab=users"), ">Locked<") {
		t.Fatal("the users table still shows an expired lock")
	}
	if u, _ := a.Auth.UserByName(ctx, "boss"); u.LockedAt != nil || u.FailedLogins != 0 {
		t.Fatalf("lock not cleared after it expired: %v %d", u.LockedAt, u.FailedLogins)
	}
}

// Audit M5: "Second factor required for administrators" only stopped an
// administrator from removing their own second factor.
func TestTOTPRequiredForAdministrators(t *testing.T) {
	a, srv := sqliteApp(t)
	ctx := context.Background()
	a.Auth.CreateUser(ctx, audit.System, "boss", "", "tungsten light 3200K", auth.RoleAdmin)
	boss2, _ := a.Auth.CreateUser(ctx, audit.System, "boss2", "", "daylight 5600K!", auth.RoleAdmin)
	op, _ := a.Auth.CreateUser(ctx, audit.System, "op", "", "reference monitor 709", auth.RoleOperator)
	for _, who := range []struct{ name, pass string }{{"boss2", "daylight 5600K!"}, {"op", "reference monitor 709"}} {
		_, p, _, err := a.Auth.Login(ctx, who.name, who.pass, "10.0.0.5", "")
		if err != nil {
			t.Fatal(err)
		}
		secret, _ := a.Auth.BeginTOTP(p.User)
		code, _ := auth.TOTPCode(secret, auth.Counter(time.Now()))
		if _, err := a.Auth.EnableTOTP(ctx, p, secret, code); err != nil {
			t.Fatal(err)
		}
	}
	st := a.Svc.Settings(ctx)
	st.TOTPRequiredAdmins = true
	if err := a.Svc.SaveSettings(ctx, audit.System, st); err != nil {
		t.Fatal(err)
	}
	// Straight to enrollment after the password, and every page sends back.
	b, loc := signIn(t, srv, "boss", "tungsten light 3200K")
	if loc != "/account/totp" {
		t.Fatalf("admin without a second factor landed on %q", loc)
	}
	for _, p := range []string{"/", "/settings?tab=users", "/account", "/libraries"} {
		if st, loc, _ := b.do("GET", p, nil); st != http.StatusSeeOther || loc != "/account/totp" {
			t.Errorf("GET %s: %d %s", p, st, loc)
		}
	}
	tok := csrf(t, b.page("/account/totp"))
	if st, loc, _ := b.do("POST", "/settings/users/"+boss2.ID+"/totp", url.Values{"csrf": {tok}}); st != http.StatusSeeOther || loc != "/account/totp" {
		t.Errorf("POST before enrolling: %d %s", st, loc)
	}
	// The API refuses the same browser session, with its code.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/jobs", nil)
	u, _ := url.Parse(srv.URL)
	for _, c := range b.c.Jar.Cookies(u) {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var prob api.Problem
	json.NewDecoder(resp.Body).Decode(&prob)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || prob.Code != "SQS-A051" || !strings.Contains(prob.Detail, "second factor is required") {
		t.Fatalf("API with an unenrolled admin session: %d %+v", resp.StatusCode, prob)
	}
	// Enrolling unlocks the panel.
	page := b.page("/account/totp")
	m := regexp.MustCompile(`name="secret" value="([A-Z2-7]+)"`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, "required for administrators") {
		t.Fatalf("enrollment page:\n%s", page[:min(len(page), 1500)])
	}
	code, _ := auth.TOTPCode(m[1], auth.Counter(time.Now()))
	if st, _, body := b.do("POST", "/account/totp/enable", url.Values{"csrf": {tok}, "secret": {m[1]}, "code": {code}}); st != http.StatusOK || !strings.Contains(body, "Recovery codes") {
		t.Fatalf("enable: %d", st)
	}
	b.page("/")
	// Another administrator's second factor cannot be removed; an
	// operator's can.
	if _, loc, _ := b.do("POST", "/settings/users/"+boss2.ID+"/totp", url.Values{"csrf": {tok}}); loc != "/settings?tab=users" ||
		flashOf(b) != "SQS-A040 the second factor is required for administrators on this installation" {
		t.Fatalf("removing an admin's second factor: %s %q", loc, flashOf(b))
	}
	if u, _ := a.Auth.UserByID(ctx, boss2.ID); !u.TOTPEnabled {
		t.Fatal("another administrator's second factor was removed")
	}
	b.page("/settings?tab=users")
	if b.do("POST", "/settings/users/"+op.ID+"/totp", url.Values{"csrf": {tok}}); flashOf(b) != "Second factor disabled for that user." {
		t.Fatalf("removing an operator's second factor: %q", flashOf(b))
	}
	if u, _ := a.Auth.UserByID(ctx, op.ID); u.TOTPEnabled {
		t.Fatal("operator's second factor not removed")
	}
}

// Audit Low: the CSRF check consumed the multipart body, so FormFile always
// failed and the certificate could never be installed from Settings.
func TestPanelCertificateUpload(t *testing.T) {
	a, srv := sqliteApp(t)
	a.Auth.CreateUser(context.Background(), audit.System, "boss", "", "tungsten light 3200K", auth.RoleAdmin)
	b := mustSignIn(t, srv, "boss", "tungsten light 3200K")
	tok := csrf(t, b.page("/settings"))
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "nas.studio"}, DNSNames: []string{"nas.studio"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	kder, _ := x509.MarshalECPrivateKey(key)
	upload := func(withKey bool) string {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		mw.WriteField("csrf", tok)
		fw, _ := mw.CreateFormFile("cert", "c.pem")
		pem.Encode(fw, &pem.Block{Type: "CERTIFICATE", Bytes: der})
		if withKey {
			fw, _ = mw.CreateFormFile("key", "k.pem")
			pem.Encode(fw, &pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
		}
		mw.Close()
		req, _ := http.NewRequest("POST", srv.URL+"/settings/certificate", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := b.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings" {
			t.Fatalf("upload: %d %s", resp.StatusCode, resp.Header.Get("Location"))
		}
		return flashOf(b)
	}
	if msg := upload(false); msg != "SQS-C021 upload the certificate and the key in PEM format" {
		t.Fatalf("upload without the key: %q", msg)
	}
	if msg := upload(true); msg != "Certificate installed. It is already in use." {
		t.Fatalf("upload: %q", msg)
	}
	info, err := a.PanelCertInfo()
	if err != nil || !strings.Contains(info.Subject, "nas.studio") {
		t.Fatalf("installed certificate: %+v %v", info, err)
	}
	if !strings.Contains(b.page("/settings"), info.Fingerprint) {
		t.Fatal("settings does not show the new certificate")
	}
}

// Audit AUD-007: the installation code was checked and cleared in separate
// steps, so concurrent setups with the same code each created an
// administrator.
func TestInstallationCodeCreatesOneAdministrator(t *testing.T) {
	ctx := context.Background()
	a, srv := sqliteApp(t)
	a.PrepareInstallCode(ctx)
	code := a.InstallCode()
	const n = 3
	forms := make([]url.Values, n)
	browsers := make([]*browser, n)
	for i := range forms {
		browsers[i] = newBrowser(t, srv)
		forms[i] = url.Values{"csrf": {csrf(t, browsers[i].page("/setup"))}, "code": {code}, "username": {"admin" + string(rune('a'+i))},
			"password": {"tungsten light 3200K"}, "password2": {"tungsten light 3200K"}, "host": {"192.168.1.10"}, "preset": {"daily"}}
	}
	gate := make(chan struct{})
	done := make(chan string, n)
	for i := range forms {
		go func() {
			<-gate
			_, loc, _ := browsers[i].do("POST", "/setup", forms[i])
			done <- loc
		}()
	}
	close(gate)
	kits := 0
	for range forms {
		if <-done == "/setup/kit" {
			kits++
		}
	}
	users, err := a.Auth.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kits != 1 || len(users) != 1 {
		t.Fatalf("%d setups completed, %d users created", kits, len(users))
	}
	if a.InstallCode() != "" {
		t.Fatal("the installation code still works after the first administrator")
	}
}

// A setup that fails (here a weak password) keeps the code for the next try.
func TestFailedSetupKeepsTheInstallationCode(t *testing.T) {
	ctx := context.Background()
	a, srv := sqliteApp(t)
	a.PrepareInstallCode(ctx)
	code := a.InstallCode()
	b := newBrowser(t, srv)
	form := url.Values{"csrf": {csrf(t, b.page("/setup"))}, "code": {code}, "username": {"diego"},
		"password": {"short"}, "password2": {"short"}, "host": {"192.168.1.10"}, "preset": {"daily"}}
	if _, loc, _ := b.do("POST", "/setup", form); !strings.HasPrefix(loc, "/setup?") {
		t.Fatalf("a weak password completed the setup: %s", loc)
	}
	if a.InstallCode() != code {
		t.Fatal("a failed setup spent the installation code")
	}
	form.Set("csrf", csrf(t, b.page("/setup")))
	form.Set("password", "tungsten light 3200K")
	form.Set("password2", "tungsten light 3200K")
	if _, loc, _ := b.do("POST", "/setup", form); loc != "/setup/kit" {
		t.Fatalf("setup failed: %s", loc)
	}
}

// Audit AUD-004: a backup file, the recovery kit and the diagnostics were
// served even when their audit entry could not be written. Now they are not.
func TestDisclosuresNeedTheirAuditEntry(t *testing.T) {
	ctx := context.Background()
	a, srv := sqliteApp(t)
	a.Auth.CreateUser(ctx, audit.System, "boss", "", "tungsten light 3200K", auth.RoleAdmin)
	b := mustSignIn(t, srv, "boss", "tungsten light 3200K")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES ('L1', 'lib', 'Lib', 'active', 'lib', 'lib', 'resolve', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO backups(id, library_id, kind, status, file_path, started_at) VALUES ('B1', 'L1', 'manual', 'ok', 'libraries/B1.backup', ?)`, now); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(a.Cfg.BackupDir, "libraries"), 0o700)
	os.WriteFile(filepath.Join(a.Cfg.BackupDir, "libraries", "B1.backup"), []byte("the dump bytes"), 0o600)
	for _, path := range []string{"/backups/B1/download", "/api/v1/backups/B1/download"} {
		if st, _, body := b.do("GET", path, nil); st != 200 || body != "the dump bytes" {
			t.Fatalf("%s with a working audit log: %d", path, st)
		}
	}
	if _, err := a.DB.Exec(`CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/backups/B1/download", "/api/v1/backups/B1/download"} {
		st, _, body := b.do("GET", path, nil)
		if st != http.StatusServiceUnavailable || strings.Contains(body, "the dump bytes") || !strings.Contains(body, "SQS-X040") {
			t.Errorf("%s without its audit entry: %d %q", path, st, body[:min(len(body), 200)])
		}
	}
	for _, path := range []string{"/settings/kit", "/settings/diagnostics"} {
		st, loc, body := b.do("GET", path, nil)
		if st != http.StatusSeeOther || loc != "/settings?tab=maintenance" || strings.Contains(body, "MASTER KEY") {
			t.Errorf("%s without its audit entry: %d %s", path, st, loc)
		}
		if f := flashOf(b); !strings.HasPrefix(f, "SQS-X040 the audit log could not be written, so the action was not done: ") {
			t.Errorf("%s notice: %q", path, f)
		}
	}
}

// PLAN P0.1: a backup whose catalog path leaves the backups volume is not
// served, by the panel or the API, and its page says it is blocked.
func TestBackupWithAnInvalidPathIsNotServed(t *testing.T) {
	ctx := context.Background()
	a, srv := sqliteApp(t)
	a.Auth.CreateUser(ctx, audit.System, "boss", "", "tungsten light 3200K", auth.RoleAdmin)
	b := mustSignIn(t, srv, "boss", "tungsten light 3200K")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES ('L1', 'lib', 'Lib', 'active', 'lib', 'lib', 'resolve', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.backup"), []byte("outside bytes"), 0o600)
	os.MkdirAll(filepath.Join(a.Cfg.BackupDir, "libraries", "lib"), 0o700)
	os.Symlink(outside, filepath.Join(a.Cfg.BackupDir, "libraries", "lib", "esc"))
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO backups(id, library_id, kind, status, file_path, started_at) VALUES ('B1', 'L1', 'manual', 'ok', 'libraries/lib/esc/secret.backup', ?)`, now); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/backups/B1/download", "/api/v1/backups/B1/download"} {
		st, _, body := b.do("GET", path, nil)
		if st != http.StatusConflict || strings.Contains(body, "outside bytes") || !strings.Contains(body, "SQS-B043") {
			t.Errorf("%s: %d %q", path, st, body[:min(len(body), 200)])
		}
	}
	entries, _ := a.Audit.List(ctx, 0, 50, "B1")
	if len(entries) != 0 {
		t.Errorf("a refused download was audited as done: %+v", entries)
	}
	page := b.page("/backups/B1")
	if !strings.Contains(page, "SQS-B043") || strings.Contains(page, `href="/backups/B1/download"`) || strings.Contains(page, `href="/backups/B1/restore"`) {
		t.Error("the backup page does not show the block, or still offers download or restore")
	}
}
