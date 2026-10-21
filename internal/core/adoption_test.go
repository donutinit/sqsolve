// SPDX-License-Identifier: AGPL-3.0-only

package core_test

// Final review of the audit fixes: reconciliation adopts a database only
// after looking at it, a library left restoring gets back the state it had,
// a recreation keeps the connection limit, a revocation that cannot be
// recorded is reported and a backup cut by a stop or a cancel is not a
// failure.

import (
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
)

// A missing library whose name is held again by an invalid database (a DROP
// DATABASE cut halfway) or by an empty one (a recreate whose pg_restore and
// drop both failed) stays missing: nothing is adopted without looking at it,
// and nothing is dropped. One with tables is adopted, and so is an empty one
// when the library never had tables.
func TestMissingLibraryAdoptsOnlyACheckedDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	su, err := e.c.Connect(ctx, "postgres", e.c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)

	inv, _ := e.createActive("adopt_invalid")
	e.firstBackup(inv)
	e.lose(inv, false)
	if err := e.svc.PG.CreateDatabase(ctx, inv.DBName, inv.OwnerRole); err != nil {
		t.Fatal(err)
	}
	if _, err := su.Exec(ctx, `UPDATE pg_database SET datconnlimit = -2 WHERE datname = $1`, inv.DBName); err != nil {
		t.Fatal(err)
	}

	empty, _ := e.createActive("adopt_empty")
	e.firstBackup(empty)
	e.lose(empty, false)
	if err := e.svc.PG.CreateDatabase(ctx, empty.DBName, empty.OwnerRole); err != nil {
		t.Fatal(err)
	}

	full, pass := e.createActive("adopt_full")
	e.firstBackup(full)
	e.idle()
	if err := e.svc.PG.RenameDatabase(ctx, full.DBName, "adopt_full_away"); err != nil {
		t.Fatal(err)
	}
	e.svc.Reconcile(ctx, nil)
	if got := e.lib(full.ID); got.State != core.StateMissing {
		t.Fatalf("state %s, want missing", got.State)
	}
	if err := e.svc.PG.RenameDatabase(ctx, "adopt_full_away", full.DBName); err != nil {
		t.Fatal(err)
	}

	// A library that never had tables (precreated, not used by Resolve yet).
	never, _ := e.createActive("adopt_never")
	e.firstBackup(never)
	e.lose(never, false)
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET schema_table_count = 0 WHERE id = ?`, never.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.CreateDatabase(ctx, never.DBName, never.OwnerRole); err != nil {
		t.Fatal(err)
	}

	notes := strings.Join(e.svc.Reconcile(ctx, nil), "\n")

	if got := e.lib(inv.ID); got.State != core.StateMissing {
		t.Fatalf("invalid database adopted: state %s", got.State)
	}
	if taken, err := e.svc.PG.DatabaseNameTaken(ctx, inv.DBName); err != nil || !taken {
		t.Fatalf("the invalid database was dropped (%v)", err)
	}
	if !strings.Contains(notes, "adopt_invalid: a database with its name was left invalid by an interrupted DROP DATABASE") {
		t.Fatalf("notes do not report the invalid database:\n%s", notes)
	}

	if got := e.lib(empty.ID); got.State != core.StateMissing {
		t.Fatalf("empty database adopted: state %s", got.State)
	}
	if e.db(empty.DBName) == nil {
		t.Fatal("the empty database was dropped")
	}
	if ev, _ := e.svc.Events.Latest(ctx, "library.found", empty.ID); ev != nil {
		t.Fatal("library.found for an empty database")
	}
	ev, _ := e.svc.Events.Latest(ctx, "library.missing", empty.ID)
	if ev == nil || ev.Severity != events.Warning || !strings.Contains(ev.Message, `A database named "adopt_empty" is on the server again, but "Test adopt_empty" stays missing: it has no tables, while the library had 2`) {
		t.Fatalf("no warning about the database that was not adopted: %+v", ev)
	}
	if !strings.Contains(notes, "adopt_empty: a database with its name is on the server again but was not adopted: it has no tables, while the library had 2; it stays missing") {
		t.Fatalf("notes do not report the empty database:\n%s", notes)
	}
	// Recreate refuses the name and leaves the database alone.
	j, err := e.svc.Recreate(ctx, admin, empty.ID, "", empty.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(j, "SQS-R052", `a database "adopt_empty" already exists on the server; it was left untouched`)
	if e.db(empty.DBName) == nil {
		t.Fatal("the recreate dropped the empty database")
	}

	if got := e.lib(full.ID); got.State != core.StateActive {
		t.Fatalf("database with tables not adopted: state %s\n%s", got.State, notes)
	}
	if ev, _ := e.svc.Events.Latest(ctx, "library.found", full.ID); ev == nil {
		t.Fatal("no library.found event")
	}
	if err := e.reach(full.OwnerRole, pass, full.DBName); err != nil {
		t.Fatalf("workstation cannot connect: %v", err)
	}

	if got := e.lib(never.ID); got.State != core.StateActive {
		t.Fatalf("empty database of a library without tables not adopted: state %s\n%s", got.State, notes)
	}
}

// A recreation gives the roles it recreates the library's connection limit,
// which lives on the roles (docs 07 §8), not "unlimited" until the next start.
func TestRecreateKeepsTheConnectionLimit(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("rc_limit")
	e.firstBackup(l)
	e.idle()
	if err := e.svc.UpdateLibrary(ctx, admin, l.ID, core.UpdateInput{DisplayName: l.DisplayName, ConnLimit: 3}); err != nil {
		t.Fatal(err)
	}
	e.lose(l, true)
	j, err := e.svc.Recreate(ctx, admin, l.ID, "", l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	p, err := e.svc.PG.Pool()
	if err != nil {
		t.Fatal(err)
	}
	var limit int
	if err := p.QueryRow(ctx, `SELECT rolconnlimit FROM pg_roles WHERE rolname = $1`, l.OwnerRole).Scan(&limit); err != nil {
		t.Fatal(err)
	}
	if limit != 3 {
		t.Fatalf("recreated role has connection limit %d, want 3", limit)
	}
}

// ADR 0024 gives LOGIN back to every credential that is not revoked, so a
// revocation the catalog could not record would come undone at the next
// start: it is reported.
func TestRevokeMemberReportsAnUnrecordedRevocation(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("revoke_lib")
	e.firstBackup(l)
	e.idle()
	c, err := e.svc.AddMember(ctx, admin, l.ID, "ana")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `CREATE TRIGGER refuse_revoke BEFORE UPDATE OF revoked_at ON library_credentials
		BEGIN SELECT RAISE(ABORT, 'revocation refused'); END`); err != nil {
		t.Fatal(err)
	}
	err = e.svc.RevokeMember(ctx, admin, l.ID, c.ID)
	want := "SQS-L080 the role " + c.PGRole + " was set NOLOGIN, but its revocation could not be recorded, so the next start would give it its login back; revoke it again: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "revocation refused") {
		t.Fatalf("got %v, want an error starting with %q", err, want)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `DROP TRIGGER refuse_revoke`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeMember(ctx, admin, l.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	e.svc.Reconcile(ctx, nil)
	if e.canLogin(c.PGRole) {
		t.Fatal("the revoked member got its login back")
	}
}

// insertJournal stores a finished restore job of a library with one step
// carrying a swap journal.
func (e *env) insertJournal(id, typ, libraryID, journal string) {
	e.t.Helper()
	now := store.NowT()
	if _, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO jobs(id, type, library_id, state, scheduled_for, created_at, finished_at, created_by_type) VALUES (?, ?, ?, 'failed', ?, ?, ?, 'user')`,
		id, typ, libraryID, now, now, now); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at, data) VALUES (?, 1, 'lock', 'failed', ?, ?)`, id, now, journal); err != nil {
		e.t.Fatal(err)
	}
}

// A library left "restoring" gets back the state its swap journal recorded
// (maintenance stays closed to its roles), and becomes missing when its
// database is not on the server, instead of always becoming active.
func TestReconcileRestoringLibraryKeepsItsState(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	m, pass := e.createActive("rs_maint")
	e.firstBackup(m)
	e.idle()
	if _, err := e.svc.SetMaintenance(ctx, admin, m.ID, true, false); err != nil {
		t.Fatal(err)
	}
	gone, _ := e.createActive("rs_gone")
	e.firstBackup(gone)
	e.idle()
	if err := e.svc.PG.DropDatabase(ctx, gone.DBName); err != nil {
		t.Fatal(err)
	}
	for _, l := range []*core.Library{m, gone} {
		if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring' WHERE id = ?`, l.ID); err != nil {
			t.Fatal(err)
		}
	}
	e.insertJournal("JRS1", core.JobRestoreReplace, m.ID, `{"live":"rs_maint","prev":"rs_maint__prev_202610200000","restored":"rs_maint__restore_x","phase":"lock","state_before":"maintenance"}`)
	notes := strings.Join(e.svc.Reconcile(ctx, nil), "\n")
	if got := e.lib(m.ID); got.State != core.StateMaintenance {
		t.Fatalf("library in maintenance before the restore is %s after reconciliation\n%s", got.State, notes)
	}
	if e.canLogin(m.OwnerRole) {
		t.Fatal("the role of a library in maintenance got its login back")
	}
	if err := e.reach(m.OwnerRole, pass, m.DBName); err == nil {
		t.Fatal("a workstation can connect to a library in maintenance")
	}
	if got := e.lib(gone.ID); got.State != core.StateMissing {
		t.Fatalf("library without a database is %s after reconciliation\n%s", got.State, notes)
	}
	if ev, _ := e.svc.Events.Latest(ctx, "library.missing", gone.ID); ev == nil || ev.Severity != events.Critical {
		t.Fatalf("no library.missing event: %+v", ev)
	}
}

// A backup cut by a cancel (or by a graceful stop) is marked interrupted, as
// the startup recovery marks one cut by a crash, and is not reported as a
// failed backup.
func TestBackupCutByACancelIsInterrupted(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("cut_backup")
	e.firstBackup(l)
	e.idle()
	ids := make(chan string, 1)
	restore := core.SetMarkAndSnapshotHook(func() {
		select {
		case id := <-ids:
			if err := e.svc.Jobs.Cancel(ctx, id); err != nil {
				t.Errorf("cancel: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the job ID never arrived")
		}
	})
	defer restore()
	j, err := e.svc.BackupNow(ctx, admin, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids <- j.ID
	e.wait(j, jobs.Cancelled)
	bs, err := e.svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Statuses: []string{core.BackupInterrupted, core.BackupFailed}})
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 1 || bs[0].Status != core.BackupInterrupted {
		t.Fatalf("backups of the cancelled job: %+v", bs)
	}
	if evs := e.eventsOf(events.BackupFailed, l.ID); len(evs) != 0 {
		t.Fatalf("backup.failed for a cancelled backup: %+v", evs)
	}
}

// A database under a missing library's name that does not accept
// connections (a __prev the current version closed, put back under the name
// by a revert cut after its rename, or one closed before ADR 0024) is opened
// for sqsolve_admin only for the check: with tables it is adopted with the
// library's normal access; empty, it is kept, not adopted, and closed again.
func TestMissingLibraryChecksAClosedDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	full, pass := e.createActive("closed_full")
	e.firstBackup(full)
	e.idle()
	if err := e.svc.PG.RenameDatabase(ctx, full.DBName, "closed_full_away"); err != nil {
		t.Fatal(err)
	}
	empty, _ := e.createActive("closed_empty")
	e.firstBackup(empty)
	e.lose(empty, false)
	if got := e.lib(full.ID); got.State != core.StateMissing {
		t.Fatalf("state %s, want missing", got.State)
	}
	if err := e.svc.PG.RenameDatabase(ctx, "closed_full_away", full.DBName); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.CreateDatabase(ctx, empty.DBName, empty.OwnerRole); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{full.DBName, empty.DBName} {
		if err := e.svc.PG.SetAllowConnections(ctx, name, false); err != nil {
			t.Fatal(err)
		}
	}
	notes := strings.Join(e.svc.Reconcile(ctx, nil), "\n")

	if got := e.lib(full.ID); got.State != core.StateActive {
		t.Fatalf("closed database with tables not adopted: state %s\n%s", got.State, notes)
	}
	if d := e.db(full.DBName); d == nil || !d.AllowConn {
		t.Fatalf("adopted database does not accept connections: %+v", d)
	}
	if err := e.reach(full.OwnerRole, pass, full.DBName); err != nil {
		t.Fatalf("workstation cannot connect to the adopted library: %v", err)
	}

	if got := e.lib(empty.ID); got.State != core.StateMissing {
		t.Fatalf("closed empty database adopted: state %s", got.State)
	}
	d := e.db(empty.DBName)
	if d == nil || d.AllowConn {
		t.Fatalf("closed empty database dropped or left open: %+v", d)
	}
	ev, _ := e.svc.Events.Latest(ctx, "library.missing", empty.ID)
	if ev == nil || ev.Severity != events.Warning || strings.Contains(strings.ToLower(ev.Message), "drop") ||
		!strings.Contains(ev.Message, "The database is kept and may hold data of the library") {
		t.Fatalf("warning about the closed empty database: %+v", ev)
	}
	for _, n := range strings.Split(notes, "\n") {
		if strings.HasPrefix(n, "closed_empty:") && strings.Contains(strings.ToLower(n), "drop") {
			t.Fatalf("a note advises dropping: %s", n)
		}
	}
}
