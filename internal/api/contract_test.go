// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/config"
)

// apiServer runs the API router on a real App with SQLite only (no
// PostgreSQL, no workers: queued jobs stay queued). It returns an admin
// token, a read token and the raw session cookie and CSRF token of the admin.
type apiEnv struct {
	a               *app.App
	srv             *httptest.Server
	admin, read     string
	cookie, csrfTok string
}

func apiServer(t *testing.T) *apiEnv {
	t.Helper()
	ctx := context.Background()
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
	a, err := app.Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if _, err := a.Auth.CreateUser(ctx, audit.System, "boss", "", "tungsten light 3200K", auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	raw, p, _, err := a.Auth.Login(ctx, "boss", "tungsten light 3200K", "127.0.0.1", "")
	if err != nil {
		t.Fatal(err)
	}
	e := &apiEnv{a: a, cookie: raw, csrfTok: p.CSRF}
	if e.admin, _, err = a.Auth.CreateToken(ctx, p, "admin", []string{"admin"}, 0); err != nil {
		t.Fatal(err)
	}
	if e.read, _, err = a.Auth.CreateToken(ctx, p, "read", []string{"read"}, 0); err != nil {
		t.Fatal(err)
	}
	m := http.NewServeMux()
	(&API{A: a}).Register(m)
	e.srv = httptest.NewServer(m)
	t.Cleanup(e.srv.Close)
	return e
}

type reply struct {
	status  int
	header  http.Header
	body    string
	problem Problem
}

// call sends a request with a bearer token ("" for none), or with the admin
// session cookie when token is "cookie" (and the CSRF header when csrf).
func (e *apiEnv) call(t *testing.T, method, path, token, body string, csrf bool) reply {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	switch token {
	case "":
	case "cookie":
		req.AddCookie(&http.Cookie{Name: "sqsolve_session", Value: e.cookie})
		if csrf {
			req.Header.Set("X-CSRF-Token", e.csrfTok)
		}
	default:
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	r := reply{status: resp.StatusCode, header: resp.Header, body: string(b)}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/problem+json") {
		json.Unmarshal(b, &r.problem)
	}
	return r
}

// The request contract of the API (docs 10 §1): authentication, scopes, CSRF
// with a cookie, strict JSON bodies and RFC 9457 problems with the SQS code,
// whose status matches the kind of error.
func TestAPIRequestContract(t *testing.T) {
	e := apiServer(t)
	for _, c := range []struct {
		name, method, path, token, body string
		csrf                            bool
		status                          int
		code                            string
	}{
		{"no credentials", "GET", "/api/v1/libraries", "", "", false, 401, "SQS-A004"},
		{"unknown token", "GET", "/api/v1/libraries", "sqs_nope", "", false, 401, "SQS-A024"},
		{"read token reads", "GET", "/api/v1/libraries", e.read, "", false, 200, ""},
		{"read token cannot create", "POST", "/api/v1/libraries", e.read, `{"display_name":"x"}`, false, 403, "SQS-A030"},
		{"read token cannot see the audit log", "GET", "/api/v1/audit", e.read, "", false, 403, "SQS-A030"},
		{"cookie without CSRF on a POST", "POST", "/api/v1/libraries", "cookie", `{"display_name":"x"}`, false, 401, "SQS-A031"},
		{"cookie GET needs no CSRF", "GET", "/api/v1/jobs", "cookie", "", false, 200, ""},
		{"empty body", "POST", "/api/v1/libraries", e.admin, "", false, 400, "SQS-C061"},
		{"unknown field", "POST", "/api/v1/libraries", e.admin, `{"display_name":"x","owner":"me"}`, false, 400, "SQS-C061"},
		{"two JSON values", "POST", "/api/v1/libraries/L/maintenance", e.admin, `{"enabled":true}{"enabled":false}`, false, 400, "SQS-C061"},
		{"oversized body", "POST", "/api/v1/libraries", e.admin, `{"notes":"` + strings.Repeat("a", 2<<20) + `"}`, false, 400, "SQS-C061"},
		{"PostgreSQL down", "POST", "/api/v1/libraries", e.admin, `{"display_name":"Wedding"}`, false, 503, "SQS-P001"},
		{"unknown library", "POST", "/api/v1/libraries/NOPE/maintenance", e.admin, `{"enabled":true}`, false, 404, "SQS-L002"},
		{"delete an unknown library", "DELETE", "/api/v1/libraries/NOPE", e.admin, `{"confirm":""}`, false, 404, "SQS-L002"},
		{"unknown backup", "GET", "/api/v1/backups/NOPE", e.admin, "", false, 404, ""},
		{"unknown job", "GET", "/api/v1/jobs/NOPE", e.admin, "", false, 404, "SQS-J002"},
		{"restore with an unknown mode", "POST", "/api/v1/backups/NOPE/restore", e.admin, `{"mode":"sideways"}`, false, 400, "SQS-C063"},
		{"unknown route", "GET", "/api/v1/nope", e.admin, "", false, 404, "SQS-C062"},
	} {
		r := e.call(t, c.method, c.path, c.token, c.body, c.csrf)
		if r.status != c.status {
			t.Errorf("%s: status %d, want %d: %s", c.name, r.status, c.status, r.body[:min(len(r.body), 200)])
			continue
		}
		if c.status < 400 {
			continue
		}
		if !strings.HasPrefix(r.header.Get("Content-Type"), "application/problem+json") || r.problem.Status != c.status || r.problem.Instance != c.path {
			t.Errorf("%s: not an RFC 9457 problem: %s %s", c.name, r.header.Get("Content-Type"), r.body)
		}
		if c.code != "" && (r.problem.Code != c.code || r.problem.Type != "/docs/errors/"+c.code || !strings.HasPrefix(r.problem.Detail, c.code+" ")) {
			t.Errorf("%s: problem %+v, want %s", c.name, r.problem, c.code)
		}
		if c.status == 401 && r.header.Get("WWW-Authenticate") != `Bearer realm="sqsolve"` {
			t.Errorf("%s: WWW-Authenticate %q", c.name, r.header.Get("WWW-Authenticate"))
		}
	}
}

// "Back up now" answers 202 with the job and its Location, and the job's
// audit entry was written with it (ADR 0026); the cookie path with its CSRF
// header works the same as a token.
func TestAPIBackupNowQueuesAnAuditedJob(t *testing.T) {
	e := apiServer(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := e.a.DB.Exec(`INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES ('L1', 'lib', 'Lib', 'active', 'lib', 'lib', 'resolve', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	r := e.call(t, "POST", "/api/v1/libraries/L1/backups", "cookie", "", true)
	var job struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(r.body), &job)
	if r.status != http.StatusAccepted || job.ID == "" || r.header.Get("Location") != "/api/v1/jobs/"+job.ID {
		t.Fatalf("back up now: %d %s (Location %s)", r.status, r.body, r.header.Get("Location"))
	}
	r = e.call(t, "GET", "/api/v1/audit", e.admin, "", false)
	var log struct {
		Items []audit.Entry `json:"items"`
	}
	json.Unmarshal([]byte(r.body), &log)
	found := false
	for _, en := range log.Items {
		found = found || en.Action == "backup.create" && en.Outcome == audit.Accepted && en.Details["job_id"] == job.ID && en.TargetID == "L1" && en.Actor.Name == "boss"
	}
	if r.status != 200 || !found {
		t.Fatalf("audit %d: %s", r.status, r.body)
	}
	// Maintenance needs its body: an empty one is not "enabled: false".
	if r := e.call(t, "POST", "/api/v1/libraries/L1/maintenance", e.admin, "", false); r.status != 400 || r.problem.Code != "SQS-C061" {
		t.Fatalf("empty maintenance body: %d %s", r.status, r.body)
	}
}
