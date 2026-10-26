// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// Fixtures shared by the panel tests that run on SQLite only (jobsPanel):
// catalog rows written directly, as the jobs that are not running here
// would have left them, and an audit log that stops accepting entries.

const (
	fxViewerPass = "colour checker 24"
	// fxAuditDownErr is what the panel says when an entry an action
	// depends on cannot be written (docs 08 §9, ADR 0026).
	fxAuditDownErr = "SQS-X040 the audit log could not be written, so the action was not done: constraint failed: disk I/O error (1811)"
	// The refusals of page() for a role without the action and for a form
	// without its token.
	fxRoleRefused = "SQS-A030 Your role does not allow this action."
	fxCSRFRefused = "SQS-A031 The form expired or did not come from this panel. Reload the page and try again."
)

// fxAuditDown makes every new audit entry fail, like a full or read-only
// disk under SQLite.
func fxAuditDown(t *testing.T, a *app.App) {
	t.Helper()
	if _, err := a.DB.Exec(`CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
}

// fxAuditUp lets the audit log accept entries again.
func fxAuditUp(t *testing.T, a *app.App) {
	t.Helper()
	if _, err := a.DB.Exec(`DROP TRIGGER IF EXISTS audit_down`); err != nil {
		t.Fatal(err)
	}
}

// fxViewer adds the viewer "eve".
func fxViewer(t *testing.T, a *app.App) {
	t.Helper()
	if _, err := a.Auth.CreateUser(context.Background(), audit.System, "eve", "", fxViewerPass, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
}

// fxLibrary writes the catalog row of a library in state st, with its owner
// credential, as if it had been created and initialized from Resolve.
func fxLibrary(t *testing.T, a *app.App, slug string, st core.State) *core.Library {
	t.Helper()
	ctx := context.Background()
	id, now := ulid.New(), store.NowT()
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'resolve', ?, ?)`, id, slug, strings.ToUpper(slug[:1])+slug[1:], string(st), slug, slug, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO library_credentials(id, library_id, kind, pg_role, password_enc, created_at) VALUES (?, ?, 'owner', ?, 'sealed', ?)`,
		ulid.New(), id, slug, now); err != nil {
		t.Fatal(err)
	}
	l, err := a.Svc.Library(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// fxSet changes columns of a catalog row directly, the way a job would.
func fxSet(t *testing.T, a *app.App, table, id, set string, args ...any) {
	t.Helper()
	if _, err := a.DB.ExecContext(context.Background(), `UPDATE `+table+` SET `+set+` WHERE id = ?`, append(args, id)...); err != nil {
		t.Fatal(err)
	}
}

// fxBackup writes the catalog row of a backup of libraryID that started at
// started, with its status and verification.
func fxBackup(t *testing.T, a *app.App, libraryID, status, verify string, started time.Time) string {
	t.Helper()
	id := ulid.New()
	if _, err := a.DB.ExecContext(context.Background(), `INSERT INTO backups(id, library_id, kind, status, verify_status, file_path, started_at, finished_at)
		VALUES (?, ?, 'manual', ?, ?, ?, ?, ?)`, id, libraryID, status, verify, "libraries/"+id+".backup", store.T(started), store.T(started.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	return id
}

// fxQueuedJob checks that loc is the page of a queued job of type typ on
// libraryID, and that its audit entry (action) was written with it as
// "accepted" by actor (jobs.Audited).
func fxQueuedJob(t *testing.T, a *app.App, loc, typ, libraryID, action, actor string) *jobs.Job {
	t.Helper()
	j, err := a.Jobs.Get(context.Background(), strings.TrimPrefix(loc, "/jobs/"))
	if err != nil || !strings.HasPrefix(loc, "/jobs/") {
		t.Fatalf("not sent to a job: %q", loc)
	}
	if j.Type != typ || j.LibraryID != libraryID || j.State != jobs.Queued || j.CreatedBy.Name != actor {
		t.Fatalf("queued job %+v", j)
	}
	found := false
	for _, e := range auditEntries(t, a, action) {
		if e.Outcome == audit.Accepted && e.Details["job_id"] == j.ID && e.Actor.Name == actor {
			found = true
		}
	}
	if !found {
		t.Fatalf("no accepted %s entry for job %s: %+v", action, j.ID, auditEntries(t, a, action))
	}
	return j
}

// fxPost posts a form and returns the status, where the panel sent the
// browser and the notice this request left ("" if none: an older notice is
// dropped first).
func fxPost(b *browser, path string, form url.Values) (int, string, string) {
	b.t.Helper()
	u, _ := url.Parse(b.base)
	b.c.Jar.SetCookies(u, []*http.Cookie{{Name: "sqsolve_flash", Value: "", Path: "/", MaxAge: -1}})
	st, loc, _ := b.do("POST", path, form)
	return st, loc, flashOf(b)
}
