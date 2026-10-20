// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"testing"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// TestBackupByActivity covers ADR 0022 against a real PostgreSQL: a backup
// records its activity mark, the gate skips while nothing changed, a write
// reopens it, and an unchanged library never counts as stale.
func TestBackupByActivity(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("actividad")
	b := e.firstBackup(l)
	if b.ActivityMark == "" {
		t.Fatalf("backup without activity mark: %+v", b)
	}
	pol, _ := e.svc.Policy(ctx, l.ID)
	pol.Mode, pol.CheckMinutes = core.ModeActivity, 15
	if err := e.svc.SetPolicy(ctx, admin, l.ID, pol); err != nil {
		t.Fatal(err)
	}
	if pol, _ = e.svc.Policy(ctx, l.ID); !pol.Adaptive() || pol.Cron != "*/15 * * * *" {
		t.Fatalf("policy not stored: %+v", pol)
	}
	// Nothing changed since the first backup: the gate is closed and the
	// library is still protected however old the backup is.
	if run, why := e.svc.BackupGate(ctx, l.ID); run {
		t.Fatalf("gate open without changes: %s", why)
	}
	if pol, _ = e.svc.Policy(ctx, l.ID); pol.LastCheckAt == nil || !pol.LastCheckSame {
		t.Fatalf("check not recorded: %+v", pol)
	}
	if e.svc.UnchangedSince(ctx, l) == nil {
		t.Fatal("unchanged library reported as changed")
	}
	// A write reopens the gate.
	w := e.connect(l.OwnerRole, pass, l.DBName)
	w.Exec(ctx, `INSERT INTO "SM_Project"(name) VALUES ('nuevo')`)
	w.Exec(ctx, `SELECT pg_stat_force_next_flush()`)
	w.Exec(ctx, `SELECT 1`)
	w.Close(ctx)
	if run, _ := e.svc.BackupGate(ctx, l.ID); !run {
		t.Fatal("gate closed after a write")
	}
	if pol, _ = e.svc.Policy(ctx, l.ID); pol.LastCheckSame {
		t.Fatal("check recorded as unchanged after a write")
	}
	// The next backup takes a new mark and closes the gate again.
	j, err := e.svc.BackupNow(ctx, admin, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	bs, _ := e.svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Statuses: []string{core.BackupOK}})
	if len(bs) < 2 || bs[0].ActivityMark == "" || bs[0].ActivityMark == b.ActivityMark {
		t.Fatalf("second backup mark: %+v", bs)
	}
	if run, why := e.svc.BackupGate(ctx, l.ID); run {
		t.Fatalf("gate open right after a backup: %s", why)
	}
}
