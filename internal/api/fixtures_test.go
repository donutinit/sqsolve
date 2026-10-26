// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

// Fixtures of the handler tests: catalog rows written into the SQLite of
// apiServer, an audit log that refuses every entry, and callers with other
// roles and scopes than its administrator.

// x040 is how every refused action starts its problem detail (ADR 0026).
const x040 = "SQS-X040 the audit log could not be written, so the action was not done: "

func (e *apiEnv) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.a.DB.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// seedLibrary adds a library in state st whose database and owner role are
// named after its slug.
func (e *apiEnv) seedLibrary(t *testing.T, id, slug, name string, st core.State) {
	t.Helper()
	now := store.NowT()
	e.exec(t, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'resolve', ?, ?)`, id, slug, name, string(st), slug, slug, now, now)
}

// seedBackup adds a backup of library lib whose file would be
// libraries/<id>.backup under the backups volume.
func (e *apiEnv) seedBackup(t *testing.T, id, lib, status string, started time.Time) {
	t.Helper()
	e.exec(t, `INSERT INTO backups(id, library_id, kind, status, verify_level, verify_status, file_path, size_bytes, sha256, started_at)
		VALUES (?, ?, 'manual', ?, 1, 'l1_ok', ?, 14, 'c0ffee', ?)`, id, lib, status, "libraries/"+id+".backup", store.T(started))
}

// seedCredential adds a credential of a library whose password is sealed
// with the installation's key, as core stores it.
func (e *apiEnv) seedCredential(t *testing.T, id, lib, kind, role, pass string) {
	t.Helper()
	enc, err := e.a.Box.Seal(secrets.New(pass), "library_credentials.password_enc."+id)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO library_credentials(id, library_id, kind, pg_role, password_enc, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, lib, kind, role, enc, store.NowT())
}

// auditDown makes every new audit entry fail, as on a full or read-only disk.
func (e *apiEnv) auditDown(t *testing.T) {
	t.Helper()
	e.exec(t, `CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
}

// entries returns the audit entries of a target, newest first.
func (e *apiEnv) entries(t *testing.T, target string) []audit.Entry {
	t.Helper()
	list, err := e.a.Audit.List(context.Background(), 0, 200, target)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// hasEntry reports whether the target has an entry with that action and
// outcome by the administrator of apiServer.
func (e *apiEnv) hasEntry(t *testing.T, target, action, outcome string) bool {
	t.Helper()
	for _, en := range e.entries(t, target) {
		if en.Action == action && en.Outcome == outcome && en.Actor.Name == "boss" {
			return true
		}
	}
	return false
}

// jobsOf lists the jobs of a library.
func (e *apiEnv) jobsOf(t *testing.T, lib string) []*jobs.Job {
	t.Helper()
	list, err := e.a.Jobs.List(context.Background(), jobs.Filter{LibraryID: lib})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// token creates an API token of the administrator with these scopes.
func (e *apiEnv) token(t *testing.T, scopes ...string) string {
	t.Helper()
	ctx := context.Background()
	p, err := e.a.Auth.Session(ctx, e.cookie, "127.0.0.1", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := e.a.Auth.CreateToken(ctx, p, strings.Join(scopes, "+"), scopes, 0)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// as signs in a new panel user with that role and returns an env whose
// "cookie" calls carry that user's session and CSRF token.
func (e *apiEnv) as(t *testing.T, username string, role auth.Role) *apiEnv {
	t.Helper()
	ctx := context.Background()
	if _, err := e.a.Auth.CreateUser(ctx, audit.System, username, "", "tungsten light 3200K", role); err != nil {
		t.Fatal(err)
	}
	raw, p, _, err := e.a.Auth.Login(ctx, username, "tungsten light 3200K", "127.0.0.1", "")
	if err != nil {
		t.Fatal(err)
	}
	c := *e
	c.cookie, c.csrfTok = raw, p.CSRF
	return &c
}

// enableExports turns on the experimental export (docs 22).
func (e *apiEnv) enableExports(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	st := e.a.Svc.Settings(ctx)
	st.ExperimentalExports = true
	if err := e.a.Svc.SaveSettings(ctx, audit.System, st); err != nil {
		t.Fatal(err)
	}
}

// decodeInto unmarshals a reply body or fails the test.
func decodeInto(t *testing.T, r reply, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		t.Fatalf("decode %d %q: %v", r.status, r.body, err)
	}
}

// wantProblem checks an RFC 9457 problem with its status, code and exact detail.
func wantProblem(t *testing.T, what string, r reply, status int, detail string) {
	t.Helper()
	code := codeRe.FindString(detail)
	if r.status != status || r.problem.Status != status || r.problem.Detail != detail || r.problem.Code != code ||
		!strings.HasPrefix(r.header.Get("Content-Type"), "application/problem+json") {
		t.Errorf("%s: %d %s, want %d %q", what, r.status, r.body, status, detail)
	}
}

// wantProblemPrefix is wantProblem for details that end with a cause from
// outside sqsolve (a driver or a socket error).
func wantProblemPrefix(t *testing.T, what string, r reply, status int, prefix string) {
	t.Helper()
	code := codeRe.FindString(prefix)
	if r.status != status || r.problem.Status != status || !strings.HasPrefix(r.problem.Detail, prefix) || r.problem.Code != code {
		t.Errorf("%s: %d %s, want %d %q...", what, r.status, r.body, status, prefix)
	}
}
