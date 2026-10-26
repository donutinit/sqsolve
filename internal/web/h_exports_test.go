// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/core"
)

// The experimental export without a reachable database: the library tab
// and its downloads say why with their codes (feature off, library not
// live, PostgreSQL down), and a viewer cannot download.
func TestLibraryExportRefusals(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	fxViewer(t, a)
	active := fxLibrary(t, a, "apolo", core.StateActive)
	trashed := fxLibrary(t, a, "zeus", core.StateTrashed)
	boss, _ := jobsLogin(t, srv, "boss", jobsAdminPass)
	if st, _, body := boss.do("GET", "/libraries/"+active.ID+"/export/albums/v1", nil); st != http.StatusConflict ||
		!strings.Contains(body, "SQS-E002 experimental export is disabled; an administrator can enable it in Settings") {
		t.Fatalf("album with the feature off: %d", st)
	}
	st := a.Svc.Settings(ctx)
	st.ExperimentalExports = true
	if err := a.Svc.SaveSettings(ctx, audit.System, st); err != nil {
		t.Fatal(err)
	}
	if page := boss.page("/libraries/" + active.ID + "/export"); !strings.Contains(page, "SQS-E005 the library is not available for export: failed to connect to `user=sqsolve_admin database=apolo`: ") {
		t.Fatal("the export tab without PostgreSQL does not say why")
	}
	if page := boss.page("/libraries/" + trashed.ID + "/export"); !strings.Contains(page, "SQS-E005 the library is not available for export: it is in state In the trash") {
		t.Fatal("the export tab of a library in the trash does not say why")
	}
	for _, kind := range []string{"projects", "stills", "albums"} {
		if st, _, body := boss.do("GET", "/libraries/"+trashed.ID+"/export/"+kind+"/x", nil); st != http.StatusServiceUnavailable ||
			!strings.Contains(body, "SQS-E005 the library is not available for export: it is in state In the trash") {
			t.Errorf("%s download from a library in the trash: %d", kind, st)
		}
	}
	eve, _ := jobsLogin(t, srv, "eve", fxViewerPass)
	for _, kind := range []string{"projects", "stills", "albums"} {
		if st, _, body := eve.do("GET", "/libraries/"+active.ID+"/export/"+kind+"/x", nil); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
			t.Errorf("viewer %s download: %d", kind, st)
		}
	}
}

// Export from a backup: preparing queues the temporary restore with its
// "accepted" entry (and returns the same job while it lives), closing
// drops the workspace with its entry written first, and every refusal
// carries its code.
func TestBackupExportWorkspaceForms(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	l := fxLibrary(t, a, "apolo", core.StateActive)
	good := fxBackup(t, a, l.ID, core.BackupOK, core.VerifyL1OK, time.Now().Add(-3*time.Hour))
	bad := fxBackup(t, a, l.ID, core.BackupFailed, core.VerifyNone, time.Now().Add(-2*time.Hour))
	op, tok := jobsLogin(t, srv, "op", jobsOpPass)
	prepare := func(id string) (string, string) {
		t.Helper()
		_, loc, msg := fxPost(op, "/backups/"+id+"/export/prepare", url.Values{"csrf": {tok}})
		return loc, msg
	}
	if loc, msg := prepare(good); loc != "/backups/"+good+"/export" || msg != "SQS-E002 experimental export is disabled; an administrator can enable it in Settings" {
		t.Fatalf("prepare with the feature off: %s %q", loc, msg)
	}
	st := a.Svc.Settings(ctx)
	st.ExperimentalExports = true
	a.Svc.SaveSettings(ctx, audit.System, st)
	if loc, msg := prepare(bad); loc != "/backups/"+bad+"/export" || msg != "SQS-E005 the backup is not available (it failed or was deleted)" {
		t.Fatalf("prepare a failed backup: %s %q", loc, msg)
	}
	if st, _, body := op.do("POST", "/backups/01NOSUCHBACKUP000000000000/export/prepare", url.Values{"csrf": {tok}}); st != http.StatusNotFound || !strings.Contains(body, "SQS-B001 Backup not found.") {
		t.Fatalf("prepare an unknown backup: %d", st)
	}
	fxAuditDown(t, a)
	if _, msg := prepare(good); msg != fxAuditDownErr {
		t.Fatalf("prepare without its audit entry: %q", msg)
	}
	if ws, _ := a.Svc.BackupExportWorkspace(ctx, good); ws != nil || jobCount(t, a) != 0 {
		t.Fatalf("a workspace or job was left without its audit entry: %+v", ws)
	}
	fxAuditUp(t, a)
	loc, msg := prepare(good)
	if msg != "Preparing the backup for export; come back to this page when it finishes." {
		t.Fatalf("prepare: %q", msg)
	}
	j := fxQueuedJob(t, a, loc, core.JobBackupExport, l.ID, "backup.export_prepare", "op")
	if again, _ := prepare(good); again != loc {
		t.Fatalf("a second prepare queued another job: %s, first %s", again, loc)
	}
	page := op.page("/backups/" + good + "/export")
	if !strings.Contains(page, "The backup is being restored into the temporary database.") || !strings.Contains(page, `href="/jobs/`+j.ID+`"`) ||
		!strings.Contains(page, `action="/backups/`+good+`/export/close"`) {
		t.Fatal("the export page does not show the preparation")
	}
	// A download needs the workspace ready; once ready it needs PostgreSQL.
	if st, _, body := op.do("GET", "/backups/"+good+"/export/albums/v1", nil); st != http.StatusConflict || !strings.Contains(body, "SQS-E007 the backup is not prepared for export; prepare it first") {
		t.Fatalf("download while preparing: %d", st)
	}
	ws, _ := a.Svc.BackupExportWorkspace(ctx, good)
	fxSet(t, a, "export_workspaces", ws.ID, "state = 'ready'")
	unreachable := "SQS-E005 the library is not available for export: failed to connect to `user=sqsolve_admin database=" + ws.DBName + "`: "
	if st, _, body := op.do("GET", "/backups/"+good+"/export/albums/v1", nil); st != http.StatusServiceUnavailable || !strings.Contains(body, unreachable) {
		t.Fatalf("download without PostgreSQL: %d", st)
	}
	if page := op.page("/backups/" + good + "/export"); !strings.Contains(page, unreachable) {
		t.Fatal("the export page of a ready workspace without PostgreSQL does not say why")
	}

	// Closing writes its entry before dropping the database.
	fxAuditDown(t, a)
	if _, loc, msg := fxPost(op, "/backups/"+good+"/export/close", url.Values{"csrf": {tok}}); loc != "/backups/"+good+"/export" || msg != fxAuditDownErr {
		t.Fatalf("close without its audit entry: %s %q", loc, msg)
	}
	fxAuditUp(t, a)
	_, loc, msg = fxPost(op, "/backups/"+good+"/export/close", url.Values{"csrf": {tok}})
	if loc != "/backups/"+good+"/export" || !strings.HasPrefix(msg, "SQS-P001 PostgreSQL is not available") {
		t.Fatalf("close without PostgreSQL: %s %q", loc, msg)
	}
	if es := auditEntries(t, a, "backup.export_close"); len(es) != 2 || es[1].Outcome != audit.Accepted || es[0].Outcome != audit.Failed || es[0].Details["error"] != msg {
		t.Fatalf("close audit %+v", es)
	}
	// A backup without a workspace has nothing to close.
	if _, loc, msg := fxPost(op, "/backups/"+bad+"/export/close", url.Values{"csrf": {tok}}); loc != "/backups/"+bad || msg != "The export was closed and the temporary database was deleted." {
		t.Fatalf("close without a workspace: %s %q", loc, msg)
	}
}
