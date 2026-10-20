// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"errors"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/drp"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// TestExportFromBackup covers docs 22 §7 against a real PostgreSQL: the
// workspace job restores the archive into a temporary database, the export
// session reads that database (and refuses it, since the test tables are not
// Resolve's), and closing drops it.
func TestExportFromBackup(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("exportar")
	b := e.firstBackup(l)
	if _, _, err := e.svc.PrepareBackupExport(ctx, admin, b.ID); !errors.Is(err, core.ErrExportsDisabled) {
		t.Fatalf("prepared with the feature off: %v", err)
	}
	st := e.svc.Settings(ctx)
	st.ExperimentalExports = true
	if err := e.svc.SaveSettings(ctx, admin, st); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.OpenBackupExport(ctx, b.ID); !errors.Is(err, core.ErrExportNotReady) {
		t.Fatalf("session without workspace: %v", err)
	}
	ws, j, err := e.svc.PrepareBackupExport(ctx, admin, b.ID)
	if err != nil || ws.State != core.WorkspacePreparing || j == nil {
		t.Fatalf("prepare: %v %+v", err, ws)
	}
	// Asking again while it is live returns the same workspace, no new job.
	if ws2, _, _ := e.svc.PrepareBackupExport(ctx, admin, b.ID); ws2 == nil || ws2.ID != ws.ID {
		t.Fatalf("second prepare made another workspace: %+v", ws2)
	}
	e.wait(j, jobs.Succeeded)
	ws, _ = e.svc.BackupExportWorkspace(ctx, b.ID)
	if ws.State != core.WorkspaceReady || !ws.Live(time.Now()) {
		t.Fatalf("workspace not ready: %+v", ws)
	}
	if db, _ := e.svc.PG.Database(ctx, ws.DBName); db == nil {
		t.Fatalf("temporary database %s missing", ws.DBName)
	}
	// The session reaches the temporary database: it is the schema check
	// of the exporter that refuses the test tables, with its code.
	if _, _, err := e.svc.OpenBackupExport(ctx, b.ID); !errors.Is(err, drp.ErrSchema) {
		t.Fatalf("session on the workspace: %v", err)
	}
	if err := e.svc.CloseBackupExport(ctx, admin, b.ID); err != nil {
		t.Fatal(err)
	}
	if db, _ := e.svc.PG.Database(ctx, ws.DBName); db != nil {
		t.Fatalf("temporary database %s still there after close", ws.DBName)
	}
	if ws, _ = e.svc.BackupExportWorkspace(ctx, b.ID); ws.State != core.WorkspaceClosed {
		t.Fatalf("workspace not closed: %+v", ws)
	}
	// Expired workspaces are purged by housekeeping.
	ws, j, _ = e.svc.PrepareBackupExport(ctx, admin, b.ID)
	e.wait(j, jobs.Succeeded)
	e.svc.DB.ExecContext(ctx, `UPDATE export_workspaces SET expires_at = ? WHERE id = ?`, "2000-01-01T00:00:00Z", ws.ID)
	if n := e.svc.PurgeExportWorkspaces(ctx); n != 1 {
		t.Fatalf("purged %d", n)
	}
	if db, _ := e.svc.PG.Database(ctx, ws.DBName); db != nil {
		t.Fatal("expired workspace database still there")
	}
}
