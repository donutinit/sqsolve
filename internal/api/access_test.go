// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
)

// Each token scope reaches its routes and nothing else (docs 10 §2): a token
// without the scope gets 403 with SQS-A030 before the handler runs, and a
// token with it gets whatever the handler answers.
func TestAPITokenScopes(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedBackup(t, "B1", "L1", core.BackupOK, time.Now())
	os.MkdirAll(filepath.Join(e.a.Cfg.BackupDir, "libraries"), 0o700)
	os.WriteFile(filepath.Join(e.a.Cfg.BackupDir, "libraries", "B1.backup"), []byte("dump"), 0o600)
	tokens := map[string]string{"read": e.read, "backup": e.token(t, "backup"), "restore": e.token(t, "restore"), "export": e.token(t, "export"), "admin": e.admin}
	all := []string{"read", "backup", "restore", "export", "admin"}
	for _, c := range []struct {
		method, path, body string
		allowed            []string
	}{
		{"GET", "/api/v1/libraries/L1", "", all},
		{"GET", "/api/v1/backups", "", all},
		{"GET", "/api/v1/jobs", "", all},
		{"GET", "/api/v1/events", "", all},
		{"GET", "/api/v1/system/status", "", all},
		{"GET", "/api/v1/libraries/L1/projects", "", all},
		{"POST", "/api/v1/libraries", `{"display_name":"New","slug":"new_lib"}`, []string{"admin"}},
		{"PATCH", "/api/v1/libraries/L1", `{"notes":"n"}`, []string{"admin"}},
		{"DELETE", "/api/v1/libraries/L1", `{"confirm":"not the slug"}`, []string{"admin"}},
		{"POST", "/api/v1/libraries/L1/maintenance", `{"enabled":false}`, []string{"admin"}},
		{"POST", "/api/v1/libraries/L1/connection/reveal", "", []string{"admin"}},
		{"PUT", "/api/v1/libraries/L1/backup-policy", `{"cron":"0 3 * * *","timezone":"UTC","keep_last":1,"min_verified":1}`, []string{"admin"}},
		{"POST", "/api/v1/libraries/L1/backups", "", []string{"backup", "admin"}},
		{"POST", "/api/v1/backups/B1/verify", "", []string{"backup", "admin"}},
		{"GET", "/api/v1/backups/B1/download", "", []string{"restore", "admin"}},
		{"POST", "/api/v1/backups/B1/restore", `{"mode":"new","slug":"copy","display_name":"Copy"}`, []string{"restore", "admin"}},
		{"GET", "/api/v1/libraries/L1/projects/" + someUUID + "/export", "", []string{"export", "admin"}},
		{"POST", "/api/v1/backups/B1/export", "", []string{"export", "admin"}},
		{"DELETE", "/api/v1/backups/B1/export", "", []string{"export", "admin"}},
		{"GET", "/api/v1/audit", "", []string{"admin"}},
		{"POST", "/api/v1/jobs/NOPE/cancel", "", []string{"admin"}},
	} {
		for _, scope := range all {
			r := e.call(t, c.method, c.path, tokens[scope], c.body, false)
			allowed := false
			for _, s := range c.allowed {
				allowed = allowed || s == scope
			}
			switch {
			case allowed && (r.status == 401 || r.status == 403):
				t.Errorf("%s %s with %s: %d %s", c.method, c.path, scope, r.status, r.body)
			case !allowed:
				wantProblem(t, c.method+" "+c.path+" with "+scope, r, 403, a030)
			}
		}
	}
}

// A panel session carries its role: an operator backs up and reads the
// audit log but cannot edit a library; a viewer only reads.
func TestAPISessionRoles(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	operator, viewer := e.as(t, "olga", auth.RoleOperator), e.as(t, "vera", auth.RoleViewer)
	for _, c := range []struct {
		who                *apiEnv
		name, method, path string
		body               string
		status             int
	}{
		{operator, "operator backs up", "POST", "/api/v1/libraries/L1/backups", "", 202},
		{operator, "operator reads the audit log", "GET", "/api/v1/audit", "", 200},
		{operator, "operator edits a library", "PATCH", "/api/v1/libraries/L1", `{"notes":"x"}`, 403},
		{operator, "operator reveals a password", "POST", "/api/v1/libraries/L1/connection/reveal", "", 403},
		{viewer, "viewer lists libraries", "GET", "/api/v1/libraries", "", 200},
		{viewer, "viewer backs up", "POST", "/api/v1/libraries/L1/backups", "", 403},
		{viewer, "viewer reads the audit log", "GET", "/api/v1/audit", "", 403},
	} {
		r := c.who.call(t, c.method, c.path, "cookie", c.body, true)
		if c.status == 403 {
			wantProblem(t, c.name, r, 403, a030)
		} else if r.status != c.status {
			t.Errorf("%s: %d %s", c.name, r.status, r.body)
		}
	}
}

// With a session cookie every method but GET and HEAD needs that session's
// own X-CSRF-Token: the token of another session is refused like a missing
// one. A cookie that is no session is 401 with SQS-A004.
func TestAPISessionCSRF(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	other := e.as(t, "olga", auth.RoleOperator)
	mixed := *e
	mixed.csrfTok = other.csrfTok
	wantProblem(t, "another session's token", mixed.call(t, "POST", "/api/v1/libraries/L1/backups", "cookie", "", true), 401, "SQS-A031 missing X-CSRF-Token header")
	for _, c := range [][2]string{{"PATCH", "/api/v1/libraries/L1"}, {"PUT", "/api/v1/libraries/L1/backup-policy"},
		{"DELETE", "/api/v1/libraries/L1"}, {"POST", "/api/v1/libraries/L1/maintenance"}} {
		wantProblem(t, c[0]+" without the header", e.call(t, c[0], c[1], "cookie", `{}`, false), 401, "SQS-A031 missing X-CSRF-Token header")
	}
	req, _ := http.NewRequest("HEAD", e.srv.URL+"/api/v1/libraries", nil)
	req.AddCookie(&http.Cookie{Name: "sqsolve_session", Value: e.cookie})
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("HEAD without the header: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	stale := *e
	stale.cookie = "not-a-session"
	r := stale.call(t, "GET", "/api/v1/libraries", "cookie", "", false)
	wantProblem(t, "unknown session", r, 401, "SQS-A004 invalid or expired session")
	if r.header.Get("WWW-Authenticate") != `Bearer realm="sqsolve"` {
		t.Errorf("WWW-Authenticate %q", r.header.Get("WWW-Authenticate"))
	}
}

// When the installation requires a second factor for administrators, an
// administrator's session without one gets 403 with SQS-A051; other roles
// and API tokens are not affected (docs 10 §1).
func TestAPIAdminSessionNeedsSecondFactor(t *testing.T) {
	e := apiServer(t)
	operator := e.as(t, "olga", auth.RoleOperator)
	ctx := context.Background()
	st := e.a.Svc.Settings(ctx)
	st.TOTPRequiredAdmins = true
	if err := e.a.Svc.SaveSettings(ctx, audit.System, st); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, "administrator session", e.call(t, "GET", "/api/v1/libraries", "cookie", "", false), 403,
		"SQS-A051 the second factor is required for administrators on this installation: enable it in the panel before continuing")
	if r := e.call(t, "GET", "/api/v1/libraries", e.admin, "", false); r.status != 200 {
		t.Errorf("administrator token: %d %s", r.status, r.body)
	}
	if r := operator.call(t, "GET", "/api/v1/libraries", "cookie", "", false); r.status != 200 {
		t.Errorf("operator session: %d %s", r.status, r.body)
	}
}
