// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
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

// A blocked backup (SQS-B045 while its path cannot be checked, SQS-B043
// while it is not valid) is not read, so it does not hold the activity gate:
// the gate compares with the newest verified backup of the live database
// that sqsolve can read, as the staleness rule does, and opens when there is
// none.
func TestBlockedBackupDoesNotHoldTheActivityGate(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("gate_blocked")
	b := e.firstBackup(l)
	e.idle()
	e.activityPolicy(l)
	closed := func(when string) {
		t.Helper()
		if run, why := e.svc.BackupGate(ctx, l.ID); run {
			t.Fatalf("%s: gate open (%q)", when, why)
		}
	}
	// UnchangedSince keeps the marks for 20 seconds: no call before the
	// write below reaches them, because no readable backup is found.
	opens := func(when, want string) {
		t.Helper()
		run, why := e.svc.BackupGate(ctx, l.ID)
		if !run || why != want {
			t.Fatalf("%s: gate run=%v %q, want open with %q", when, run, why, want)
		}
		if pol, _ := e.svc.Policy(ctx, l.ID); pol.LastCheckAt == nil || pol.LastCheckSame {
			t.Fatalf("%s: check recorded as unchanged: %+v", when, pol)
		}
		if e.svc.UnchangedSince(ctx, l) != nil {
			t.Fatalf("%s: the staleness rule counts the library unchanged", when)
		}
	}
	closed("readable backup, no changes")

	restore := e.uncheckable(b)
	opens("path that cannot be checked", "no earlier verified backup with an activity mark")
	restore()
	closed("path checked again")

	e.svc.DB.ExecContext(ctx, `UPDATE backups SET file_path = '../elsewhere/gate_blocked.backup' WHERE id = ?`, b.ID)
	opens("path that is not valid", "no earlier verified backup with an activity mark")
	e.svc.DB.ExecContext(ctx, `UPDATE backups SET file_path = ? WHERE id = ?`, b.FilePath, b.ID)
	closed("path valid again")

	// A write, then a newer verified backup with the current mark that is
	// blocked: the readable backup is older than the write, so the gate
	// opens.
	w := e.connect(l.OwnerRole, pass, l.DBName)
	w.Exec(ctx, `INSERT INTO "SM_Project"(name) VALUES ('after')`)
	w.Exec(ctx, `SELECT pg_stat_force_next_flush()`)
	w.Exec(ctx, `SELECT 1`)
	w.Close(ctx)
	cur, err := e.svc.PG.ActivityMark(ctx, l.DBName)
	if err != nil || cur == "" || cur == b.ActivityMark {
		t.Fatalf("mark after a write: %q %v", cur, err)
	}
	now := store.T(time.Now())
	if _, err := e.svc.DB.ExecContext(ctx, `INSERT INTO backups(id, library_id, kind, status, verify_status, file_path, size_bytes, source_db, started_at, finished_at, activity_mark)
		VALUES (?, ?, 'scheduled', 'ok', 'l1_ok', '../elsewhere/gate_newer.backup', 4, ?, ?, ?, ?)`, ulid.New(), l.ID, l.DBName, now, now, cur); err != nil {
		t.Fatal(err)
	}
	opens("newer backup blocked", "")
}
