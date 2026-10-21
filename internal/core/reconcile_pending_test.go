// SPDX-License-Identifier: AGPL-3.0-only

package core_test

// Follow-up of the audit fixes: a journal that reconciliation cannot settle
// is tried again at every start, and until then nothing it names is dropped
// and its library is left as it is; a database checked for adoption always
// ends closed when it is not adopted; a previous state whose database is gone
// is forgotten.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/store"
)

// pendingFlag reads whether a job still waits for reconciliation.
func (e *env) pendingFlag(id string) int {
	e.t.Helper()
	var n int
	if err := e.svc.DB.QueryRowContext(e.ctx, `SELECT reconcile_pending FROM jobs WHERE id = ?`, id).Scan(&n); err != nil {
		e.t.Fatalf("job %s: %v", id, err)
	}
	return n
}

// start runs what a start of sqsolve runs before the dispatcher.
func (e *env) start() string {
	e.t.Helper()
	e.idle()
	interrupted, err := e.svc.Jobs.MarkInterrupted(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.Join(e.svc.Reconcile(e.ctx, interrupted), "\n")
}

// A replace cut between its renames whose last rename fails at the first
// start (a superuser session holds the restored database), and a revert of
// which only the database it was bringing in is left: neither is settled, so
// the sweep keeps the restored copy, step 4 leaves both libraries restoring
// and keeps their previous-state reference, a warning event and a note say
// which databases are kept, and the jobs stay pending (also through the job
// purge). At the next start the replace completes and is settled.
func TestUndecidedSwapKeepsItsDatabases(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	a, pass := e.createActive("und_swap")
	b := e.firstBackup(a)
	e.idle()
	restored := pg.RestoreDB(a.Slug, "cut00001")
	leftover := pg.RestoreDB(a.Slug, "old00001")
	prev := pg.PrevDB(a.Slug, time.Now())
	for _, name := range []string{restored, leftover} {
		if err := e.svc.PG.CreateDatabase(ctx, name, a.OwnerRole); err != nil {
			t.Fatal(err)
		}
	}
	su, err := e.c.Connect(ctx, "postgres", e.c.SuperPass, restored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := su.Exec(ctx, `CREATE TABLE restored_marker (id int); INSERT INTO restored_marker VALUES (1), (2), (3)`); err != nil {
		t.Fatal(err)
	}
	su.Close(ctx)
	// A session of sqsolve_admin makes the rename fail, while the sweep's
	// DROP ... WITH (FORCE) could end it.
	hold, err := e.c.Connect(ctx, pg.AdminRole, e.c.AdminPass, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close(ctx)
	if err := e.svc.PG.SetAllowConnections(ctx, a.DBName, false); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.RenameDatabase(ctx, a.DBName, prev); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring', prev_db_name = ?, prev_expires_at = ?, prev_backup_id = ? WHERE id = ?`,
		prev, store.T(time.Now().Add(7*24*time.Hour)), b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("JS1", core.JobRestoreReplace, a.ID, "swap",
		fmt.Sprintf(`{"live":%q,"prev":%q,"restored":%q,"phase":"rename_restored","state_before":"active"}`, a.DBName, prev, restored))

	r, _ := e.createActive("und_revert")
	e.firstBackup(r)
	e.idle()
	oldPrev := pg.PrevDB(r.Slug, time.Now().Add(-48*time.Hour))
	newPrev := pg.PrevDB(r.Slug, time.Now())
	if err := e.svc.PG.SetAllowConnections(ctx, r.DBName, false); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.RenameDatabase(ctx, r.DBName, oldPrev); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring', prev_db_name = ? WHERE id = ?`, newPrev, r.ID); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("JR1", core.JobRestoreRevert, r.ID, "swap",
		fmt.Sprintf(`{"live":%q,"prev":%q,"restored":%q,"phase":"rename_live","state_before":"active","prev_before":%q}`, r.DBName, newPrev, oldPrev, oldPrev))

	notes := e.start()

	if e.db(restored) == nil || e.db(prev) == nil {
		t.Fatalf("a database of the undecided swap was dropped (restored kept %v, prev kept %v)\n%s", e.db(restored) != nil, e.db(prev) != nil, notes)
	}
	if e.db(leftover) != nil {
		t.Fatalf("a restore database no journal names was not swept\n%s", notes)
	}
	if got := e.lib(a.ID); got.State != core.StateRestoring || got.PrevDB != prev {
		t.Fatalf("library of the undecided swap changed: state %s prev %q\n%s", got.State, got.PrevDB, notes)
	}
	if got := e.lib(r.ID); got.State != core.StateRestoring || got.PrevDB != newPrev {
		t.Fatalf("library of the undecided revert changed: state %s prev %q\n%s", got.State, got.PrevDB, notes)
	}
	if e.db(oldPrev) == nil {
		t.Fatal("the database the revert was bringing in was dropped")
	}
	for _, l := range []*core.Library{a, r} {
		if ev, _ := e.svc.Events.Latest(ctx, "library.missing", l.ID); ev != nil {
			t.Fatalf("%s reported missing while its journal is not settled: %s", l.Slug, ev.Message)
		}
	}
	ev, _ := e.svc.Events.Latest(ctx, "library.reconcile_pending", a.ID)
	if ev == nil || ev.Severity != events.Warning || ev.JobID != "JS1" ||
		!strings.Contains(ev.Message, `Reconciliation could not settle the interrupted job "Restore by replacing" of "Test und_swap": und_swap: could not complete the swap: `) ||
		!strings.Contains(ev.Message, "The library's state and access are left as they are, and the databases its journal names are kept ("+a.DBName+", "+prev+", "+restored+")") ||
		!strings.Contains(ev.Message, "sqsolve tries again at every start") {
		t.Fatalf("warning about the undecided swap: %+v", ev)
	}
	ev, _ = e.svc.Events.Latest(ctx, "library.reconcile_pending", r.ID)
	if ev == nil || ev.Severity != events.Warning ||
		!strings.Contains(ev.Message, "und_revert: of the databases of the swap only "+oldPrev+" is on the server; nothing was changed") {
		t.Fatalf("warning about the undecided revert: %+v", ev)
	}
	for _, want := range []string{
		"und_swap: the interrupted job JS1 is not settled; its state and access are left as they are and the databases its journal names are kept (" + a.DBName + ", " + prev + ", " + restored + "); the next start tries again",
		"kept " + restored + ": an interrupted job that reconciliation has not settled names it",
		"deleted auxiliary database " + leftover,
	} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes do not say %q:\n%s", want, notes)
		}
	}
	if e.pendingFlag("JS1") != 1 || e.pendingFlag("JR1") != 1 {
		t.Fatal("the undecided jobs are not pending")
	}
	if _, err := e.svc.Jobs.Purge(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"JS1", "JR1"} {
		if _, err := e.svc.Jobs.Get(ctx, id); err != nil {
			t.Fatalf("job %s purged while pending: %v", id, err)
		}
	}

	// Next start: the session is gone, the swap completes.
	hold.Close(ctx)
	notes = e.start()
	got := e.lib(a.ID)
	if got.State != core.StateActive || got.PrevDB != prev {
		t.Fatalf("after the second start: state %s prev %q\n%s", got.State, got.PrevDB, notes)
	}
	if e.db(restored) != nil || e.db(prev) == nil {
		t.Fatalf("swap not completed\n%s", notes)
	}
	if err := e.reach(a.OwnerRole, pass, a.DBName); err != nil {
		t.Fatalf("workstation cannot connect after the swap completed: %v", err)
	}
	su2, err := e.c.Connect(ctx, "postgres", e.c.SuperPass, a.DBName)
	if err != nil {
		t.Fatal(err)
	}
	defer su2.Close(ctx)
	var n int
	if err := su2.QueryRow(ctx, `SELECT count(*) FROM restored_marker`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("the restored copy is not live: %d rows (%v)", n, err)
	}
	if e.pendingFlag("JS1") != 0 {
		t.Fatal("the settled swap is still pending")
	}
	if e.pendingFlag("JR1") != 1 || e.lib(r.ID).State != core.StateRestoring {
		t.Fatal("the undecided revert was settled or changed")
	}
	if _, err := e.svc.Jobs.Purge(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Jobs.Get(ctx, "JS1"); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("settled job not purged: %v", err)
	}
}

// A delete cut after its rename whose move cannot be recorded at the first
// start (the catalog refuses the write) is left as it is, not reported
// missing, and stays pending; the next start records it: the library ends
// trashed with its trash database.
func TestInterruptedTrashSettlesAtTheNextStart(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("pend_trash")
	e.firstBackup(l)
	e.idle()
	trash := pg.TrashDB(l.Slug, time.Now())
	if err := e.svc.PG.SetRoleLogin(ctx, l.OwnerRole, false); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.RenameDatabase(ctx, l.DBName, trash); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("JT1", core.JobLibraryDelete, l.ID, "move", fmt.Sprintf(`{"from":%q,"to":%q}`, l.DBName, trash))
	if _, err := e.svc.DB.ExecContext(ctx, `CREATE TRIGGER refuse_trashed BEFORE UPDATE OF state ON libraries WHEN NEW.state = 'trashed'
		BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	notes := e.start()
	got := e.lib(l.ID)
	if got.State != core.StateActive || got.TrashDB != "" {
		t.Fatalf("after the first start: state %s trash %q\n%s", got.State, got.TrashDB, notes)
	}
	if e.db(trash) == nil {
		t.Fatal("the trash database is gone")
	}
	if ev, _ := e.svc.Events.Latest(ctx, "library.missing", l.ID); ev != nil {
		t.Fatalf("reported missing while its journal is not settled: %s", ev.Message)
	}
	if !strings.Contains(notes, "pend_trash: the database was moved to the trash as "+trash+", but this could not be recorded (") {
		t.Fatalf("notes do not report the failed write:\n%s", notes)
	}
	ev, _ := e.svc.Events.Latest(ctx, "library.reconcile_pending", l.ID)
	if ev == nil || ev.Severity != events.Warning || !strings.Contains(ev.Message, `the interrupted job "Move to trash" of "Test pend_trash"`) ||
		!strings.Contains(ev.Message, "disk full") || !strings.Contains(ev.Message, "The library's state and access are left as they are. sqsolve tries again at every start") {
		t.Fatalf("warning about the pending delete: %+v", ev)
	}
	if e.pendingFlag("JT1") != 1 {
		t.Fatal("the delete is not pending")
	}
	if _, err := e.svc.Jobs.Purge(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Jobs.Get(ctx, "JT1"); err != nil {
		t.Fatalf("pending delete purged: %v", err)
	}

	if _, err := e.svc.DB.ExecContext(ctx, `DROP TRIGGER refuse_trashed`); err != nil {
		t.Fatal(err)
	}
	notes = e.start()
	got = e.lib(l.ID)
	if got.State != core.StateTrashed || got.TrashDB != trash || got.PurgeAfter == nil {
		t.Fatalf("after the next start: state %s trash %q\n%s", got.State, got.TrashDB, notes)
	}
	if e.pendingFlag("JT1") != 0 {
		t.Fatal("the settled delete is still pending")
	}
}

// A database of a missing library that is found open and is not adopted (a
// check cut by a hard kill leaves it open) is closed again.
func TestMissingLibraryFoundOpenIsClosedAgain(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("open_empty")
	e.firstBackup(l)
	e.lose(l, false)
	if err := e.svc.PG.CreateDatabase(ctx, l.DBName, l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	if d := e.db(l.DBName); d == nil || !d.AllowConn {
		t.Fatalf("fixture: %+v", d)
	}
	notes := e.start()
	if got := e.lib(l.ID); got.State != core.StateMissing {
		t.Fatalf("empty database adopted: state %s", got.State)
	}
	if d := e.db(l.DBName); d == nil || d.AllowConn {
		t.Fatalf("not adopted and left open (or dropped): %+v\n%s", d, notes)
	}
}

// A check cut by a panic still closes the database it opened.
func TestAdoptionCheckClosesTheDatabaseOnAPanic(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("panic_check")
	e.firstBackup(l)
	e.idle()
	if err := e.svc.PG.RenameDatabase(ctx, l.DBName, "panic_check_away"); err != nil {
		t.Fatal(err)
	}
	e.svc.Reconcile(ctx, nil)
	if got := e.lib(l.ID); got.State != core.StateMissing {
		t.Fatalf("state %s, want missing", got.State)
	}
	if err := e.svc.PG.RenameDatabase(ctx, "panic_check_away", l.DBName); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
		t.Fatal(err)
	}
	restore := core.SetAdoptionCheckHook(func(db string) {
		if db == l.DBName {
			if d := e.db(db); d == nil || !d.AllowConn {
				t.Errorf("the hook ran before the database was opened: %+v", d)
			}
			panic("cut during the check")
		}
	})
	func() {
		defer func() {
			if p := recover(); p != "cut during the check" {
				t.Errorf("recovered %v", p)
			}
		}()
		e.svc.Reconcile(ctx, nil)
	}()
	restore()
	if d := e.db(l.DBName); d == nil || d.AllowConn {
		t.Fatalf("the database opened for the check was left open: %+v", d)
	}
	e.svc.Reconcile(ctx, nil)
	if got := e.lib(l.ID); got.State != core.StateActive {
		t.Fatalf("not adopted at the next start: %s", got.State)
	}
	if d := e.db(l.DBName); d == nil || !d.AllowConn {
		t.Fatalf("adopted database closed: %+v", d)
	}
}

// A previous-state reference whose database is gone is forgotten at every
// start (here after a revert of a missing library cut after its rename, so
// the library is adopted with the former __prev); one whose name an invalid
// database still holds, or whose database exists, is kept.
func TestGonePreviousStateIsForgotten(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	su, err := e.c.Connect(ctx, "postgres", e.c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)

	a, _ := e.createActive("gone_prev")
	e.firstBackup(a)
	e.idle()
	gone := pg.PrevDB(a.Slug, time.Now().Add(-time.Hour))
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = ?, prev_expires_at = ?, prev_backup_id = 'B0' WHERE id = ?`,
		gone, store.T(time.Now().Add(time.Hour)), a.ID); err != nil {
		t.Fatal(err)
	}

	inv, _ := e.createActive("inv_prev")
	e.firstBackup(inv)
	e.idle()
	invPrev := pg.PrevDB(inv.Slug, time.Now())
	if err := e.svc.PG.CreateDatabase(ctx, invPrev, inv.OwnerRole); err != nil {
		t.Fatal(err)
	}
	if _, err := su.Exec(ctx, `UPDATE pg_database SET datconnlimit = -2 WHERE datname = $1`, invPrev); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = ? WHERE id = ?`, invPrev, inv.ID); err != nil {
		t.Fatal(err)
	}

	// A swap that stopped halfway (SQS-R053): the original sits under its
	// previous-state name and the library is missing.
	m, pass := e.createActive("adopt_prev")
	e.firstBackup(m)
	e.idle()
	mPrev := pg.PrevDB(m.Slug, time.Now())
	if err := e.svc.PG.SetAllowConnections(ctx, m.DBName, false); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.RenameDatabase(ctx, m.DBName, mPrev); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = ? WHERE id = ?`, mPrev, m.ID); err != nil {
		t.Fatal(err)
	}
	first := e.start()
	if got := e.lib(m.ID); got.State != core.StateMissing || got.PrevDB != mPrev {
		t.Fatalf("stuck swap: state %s prev %q", got.State, got.PrevDB)
	}
	// Revert of the missing library cut after its rename.
	if err := e.svc.PG.RenameDatabase(ctx, mPrev, m.DBName); err != nil {
		t.Fatal(err)
	}
	notes := e.start()

	got := e.lib(a.ID)
	if got.PrevDB != "" || got.PrevBackupID != "" || got.PrevExpiresAt != nil {
		t.Fatalf("gone previous state kept: %+v\n%s", got, notes)
	}
	if !strings.Contains(first, "gone_prev: forgot its previous state "+gone+", which is no longer on the server") {
		t.Fatalf("notes do not report the forgotten previous state:\n%s", first)
	}
	if !strings.Contains(notes, "adopt_prev: forgot its previous state "+mPrev+", which is no longer on the server") {
		t.Fatalf("notes do not report the previous state the adoption forgot:\n%s", notes)
	}
	if got := e.lib(inv.ID); got.PrevDB != invPrev {
		t.Fatalf("a previous state an invalid database holds was forgotten")
	}
	got = e.lib(m.ID)
	if got.State != core.StateActive || got.PrevDB != "" {
		t.Fatalf("adopted library: state %s prev %q\n%s", got.State, got.PrevDB, notes)
	}
	if err := e.reach(m.OwnerRole, pass, m.DBName); err != nil {
		t.Fatalf("workstation cannot connect to the adopted library: %v", err)
	}
	if _, err := e.svc.Revert(ctx, admin, m.ID, m.Slug); err == nil || err.Error() != "SQS-R030 there is no previous state to recover" {
		t.Fatalf("revert offered for a gone previous state: %v", err)
	}
}

// A previous state that a pending journal names is never dropped, not even
// with a verified backup: the expiry sweep keeps it with SQS-R090 until the
// journal is settled.
func TestPendingJournalKeepsThePreviousStateItNames(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("guard_prev")
	e.firstBackup(l)
	e.idle()
	orphan := pg.PrevDB(l.Slug, time.Now().Add(-30*24*time.Hour))
	if err := e.svc.PG.CreateDatabase(ctx, orphan, l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.SetAllowConnections(ctx, orphan, false); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("JG1", core.JobRestoreRevert, l.ID, "swap",
		fmt.Sprintf(`{"live":%q,"prev":%q,"restored":%q,"phase":"rename_live","state_before":"active"}`, l.DBName, pg.PrevDB(l.Slug, time.Now()), orphan))
	if _, err := e.svc.Jobs.MarkInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	pj, _ := e.svc.Jobs.Enqueue(ctx, core.JobPurgeExpired, "", nil, admin)
	e.wait(pj, jobs.Succeeded)
	if e.db(orphan) == nil {
		t.Fatal("a previous state a pending journal names was dropped")
	}
	ev, _ := e.svc.Events.Latest(ctx, "library.prev_kept", l.ID)
	if ev == nil || !strings.Contains(ev.Message, "SQS-R090 "+orphan+" is named by an interrupted job that reconciliation at startup has not settled yet; it was kept") {
		t.Fatalf("warning for the kept previous state: %+v", ev)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE jobs SET reconcile_pending = 0 WHERE id = 'JG1'`); err != nil {
		t.Fatal(err)
	}
	pj, _ = e.svc.Jobs.Enqueue(ctx, core.JobPurgeExpired, "", nil, admin)
	e.wait(pj, jobs.Succeeded)
	if e.db(orphan) != nil {
		t.Fatal("orphan not dropped once the journal is settled")
	}
}

// A swap journal that stays pending while its library is usable (the cut came
// before the lock took effect and the first check failed) must not undo what
// was done with the library since when a later start settles it: the library
// put in maintenance meanwhile stays in maintenance.
func TestRetriedSwapJournalDoesNotUndoLaterWork(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("moved_on")
	e.firstBackup(l)
	e.idle()
	e.interruptedJob("JM1", core.JobRestoreReplace, l.ID, "lock",
		fmt.Sprintf(`{"live":%q,"prev":%q,"restored":%q,"phase":"lock","state_before":"active"}`, l.DBName, pg.PrevDB(l.Slug, time.Now()), pg.RestoreDB(l.Slug, "gone0001")))
	if _, err := e.svc.Jobs.MarkInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetMaintenance(ctx, admin, l.ID, true, false); err != nil {
		t.Fatal(err)
	}
	notes := e.start()
	if got := e.lib(l.ID); got.State != core.StateMaintenance {
		t.Fatalf("a journal tried again lifted maintenance: state %s\n%s", got.State, notes)
	}
	if e.canLogin(l.OwnerRole) {
		t.Fatal("a journal tried again gave the role of a library in maintenance its login back")
	}
	if e.pendingFlag("JM1") != 0 {
		t.Fatal("the journal is still pending")
	}
}

// --- Fix round 1 ---

// earlierPendingJob stores a job an earlier start marked interrupted and
// could not settle (or settled without recording it), with one step
// carrying data as its journal.
func (e *env) earlierPendingJob(id, typ, libraryID string, started time.Time, step, data string) {
	e.t.Helper()
	at := store.T(started)
	if _, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO jobs(id, type, library_id, state, scheduled_for, created_at, started_at, finished_at, created_by_type, reconcile_pending)
		VALUES (?, ?, ?, 'interrupted', ?, ?, ?, ?, 'user', 1)`, id, typ, libraryID, at, at, at, at); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at, data) VALUES (?, 1, ?, 'interrupted', ?, ?)`, id, step, at, data); err != nil {
		e.t.Fatal(err)
	}
}

// cutBetweenRenames leaves l as a replace cut between its renames: the
// original under its previous-state name (closed), a copy of it as the
// restored database, the library restoring, and a running job with the
// journal of the second rename.
func (e *env) cutBetweenRenames(l *core.Library, jobID string) (prev, restored string) {
	e.t.Helper()
	ctx := e.ctx
	su, err := e.c.Connect(ctx, "postgres", e.c.SuperPass, "postgres")
	if err != nil {
		e.t.Fatal(err)
	}
	defer su.Close(ctx)
	restored = pg.RestoreDB(l.Slug, "cut00001")
	prev = pg.PrevDB(l.Slug, time.Now())
	if _, err := su.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q TEMPLATE %q OWNER %q`, restored, l.DBName, l.OwnerRole)); err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.PG.RenameDatabase(ctx, l.DBName, prev); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring', prev_db_name = ?, prev_expires_at = ? WHERE id = ?`,
		prev, store.T(time.Now().Add(7*24*time.Hour)), l.ID); err != nil {
		e.t.Fatal(err)
	}
	e.interruptedJob(jobID, core.JobRestoreReplace, l.ID, "swap",
		fmt.Sprintf(`{"live":%q,"prev":%q,"restored":%q,"phase":"rename_restored","state_before":"active"}`, l.DBName, prev, restored))
	return prev, restored
}

// A swap settled at a start whose settlement could not be recorded is tried
// again at the next start, by when its library moved on: put in maintenance,
// or moved to the trash. The journal is settled without changing anything:
// maintenance is not lifted, and the trashed library does not get its
// pre-restore data back under its name.
func TestSettledButUnrecordedSwapIsNotReplayed(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	m, pass := e.createActive("unrec_maint")
	e.firstBackup(m)
	e.idle()
	mPrev, _ := e.cutBetweenRenames(m, "JU1")
	tr, _ := e.createActive("unrec_trash")
	e.firstBackup(tr)
	e.idle()
	tPrev, _ := e.cutBetweenRenames(tr, "JU2")
	if _, err := e.svc.DB.ExecContext(ctx, `CREATE TRIGGER refuse_settle BEFORE UPDATE OF reconcile_pending ON jobs WHEN NEW.reconcile_pending = 0
		BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	notes := e.start()
	for _, l := range []struct {
		lib  *core.Library
		prev string
	}{{m, mPrev}, {tr, tPrev}} {
		if got := e.lib(l.lib.ID); got.State != core.StateActive || got.PrevDB != l.prev {
			t.Fatalf("%s: swap not completed: state %s prev %q\n%s", l.lib.Slug, got.State, got.PrevDB, notes)
		}
	}
	for _, id := range []string{"JU1", "JU2"} {
		if !strings.Contains(notes, "job "+id+" was settled, but this could not be recorded (") || e.pendingFlag(id) != 1 {
			t.Fatalf("job %s: the failed record is not reported or the job is not pending\n%s", id, notes)
		}
	}
	if !strings.Contains(notes, "disk full") {
		t.Fatalf("notes do not carry the cause:\n%s", notes)
	}
	// The library moves on.
	if _, err := e.svc.SetMaintenance(ctx, admin, m.ID, true, false); err != nil {
		t.Fatal(err)
	}
	tj, err := e.svc.Trash(ctx, admin, tr.ID, tr.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(tj, jobs.Succeeded)
	trash := e.lib(tr.ID).TrashDB
	if _, err := e.svc.DB.ExecContext(ctx, `DROP TRIGGER refuse_settle`); err != nil {
		t.Fatal(err)
	}

	notes = e.start()
	got := e.lib(m.ID)
	if got.State != core.StateMaintenance || got.PrevDB != mPrev {
		t.Fatalf("a replayed journal changed the library in maintenance: state %s prev %q\n%s", got.State, got.PrevDB, notes)
	}
	if e.canLogin(m.OwnerRole) {
		t.Fatal("a replayed journal gave the role of a library in maintenance its login back")
	}
	if err := e.reach(m.OwnerRole, pass, m.DBName); err == nil {
		t.Fatal("a workstation can connect to a library in maintenance")
	}
	got = e.lib(tr.ID)
	if got.State != core.StateTrashed || got.TrashDB != trash || e.db(trash) == nil {
		t.Fatalf("a replayed journal changed the trashed library: state %s trash %q\n%s", got.State, got.TrashDB, notes)
	}
	if e.db(tr.DBName) != nil || e.db(tPrev) == nil {
		t.Fatalf("a replayed journal put the previous state of a trashed library back under its name\n%s", notes)
	}
	// Maintenance is not a job: the state says the library moved on. The
	// delete is a newer job that succeeded.
	for _, want := range []string{
		"unrec_maint: the library moved on since the interrupted job JU1 (it is ",
		"unrec_trash: the newer job " + tj.ID + " of the library supersedes the interrupted job JU2; nothing was changed",
	} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes do not say %q:\n%s", want, notes)
		}
	}
	if e.pendingFlag("JU1") != 0 || e.pendingFlag("JU2") != 0 {
		t.Fatal("the journals of libraries that moved on are still pending")
	}
}

// Two pending journals on one library: the older one (a swap an earlier
// start settled without recording it, when the library was in maintenance)
// is superseded by the newer one (a replace cut during its lock now) and
// changes nothing; the newer one gives the library its state back.
func TestOlderPendingJournalIsSupersededByANewerOne(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("two_pend")
	e.firstBackup(l)
	e.idle()
	p1 := pg.PrevDB(l.Slug, time.Now().Add(-48*time.Hour))
	if err := e.svc.PG.CreateDatabase(ctx, p1, l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.SetAllowConnections(ctx, p1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = ? WHERE id = ?`, p1, l.ID); err != nil {
		t.Fatal(err)
	}
	e.earlierPendingJob("JP1", core.JobRestoreReplace, l.ID, time.Now().Add(-48*time.Hour), "swap",
		fmt.Sprintf(`{"live":%q,"prev":%q,"restored":%q,"phase":"swapped","state_before":"maintenance"}`, l.DBName, p1, pg.RestoreDB(l.Slug, "old00001")))
	if err := e.svc.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring' WHERE id = ?`, l.ID); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("JP2", core.JobRestoreReplace, l.ID, "lock",
		fmt.Sprintf(`{"live":%q,"prev":%q,"restored":%q,"phase":"lock","state_before":"active"}`, l.DBName, pg.PrevDB(l.Slug, time.Now()), pg.RestoreDB(l.Slug, "new00001")))
	notes := e.start()
	got := e.lib(l.ID)
	if got.State != core.StateActive || got.PrevDB != p1 {
		t.Fatalf("after the start: state %s prev %q\n%s", got.State, got.PrevDB, notes)
	}
	if !e.canLogin(l.OwnerRole) {
		t.Fatal("the older journal locked the roles")
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
		t.Fatalf("workstation cannot connect: %v", err)
	}
	if !strings.Contains(notes, "two_pend: the newer job JP2 of the library supersedes the interrupted job JP1; nothing was changed") {
		t.Fatalf("notes do not report the superseded journal:\n%s", notes)
	}
	if e.pendingFlag("JP1") != 0 || e.pendingFlag("JP2") != 0 {
		t.Fatal("a journal is still pending")
	}
}

// When the jobs pending reconciliation cannot be read, nothing they may name
// is touched: every __restore database is kept, a library left restoring
// stays restoring, no previous state is forgotten, and a warning event says
// so. Once they can be read, the next start settles them.
func TestUnreadablePendingJobsKeepEverything(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("unread_jobs")
	e.firstBackup(l)
	e.idle()
	leftover := pg.RestoreDB(l.Slug, "left0001")
	if err := e.svc.PG.CreateDatabase(ctx, leftover, l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	gone := pg.PrevDB(l.Slug, time.Now().Add(-time.Hour))
	if err := e.svc.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring', prev_db_name = ? WHERE id = ?`, gone, l.ID); err != nil {
		t.Fatal(err)
	}
	e.earlierPendingJob("JB1", core.JobRestoreReplace, l.ID, time.Now().Add(-time.Hour), "lock",
		fmt.Sprintf(`{"live":%q,"prev":%q,"restored":%q,"phase":"lock","state_before":"active"}`, l.DBName, pg.PrevDB(l.Slug, time.Now()), leftover))
	// A value the job row cannot be read with, as a damaged row would.
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE jobs SET priority = 'unreadable' WHERE id = 'JB1'`); err != nil {
		t.Fatal(err)
	}
	notes := e.start()
	if e.db(leftover) == nil {
		t.Fatalf("a __restore database was dropped while the pending jobs could not be read\n%s", notes)
	}
	if got := e.lib(l.ID); got.State != core.StateRestoring || got.PrevDB != gone {
		t.Fatalf("library changed while the pending jobs could not be read: state %s prev %q\n%s", got.State, got.PrevDB, notes)
	}
	for _, want := range []string{
		"could not read the interrupted jobs earlier starts did not settle (",
		"kept " + leftover + ": the interrupted jobs earlier starts did not settle could not be read, and one may name it",
		"unread_jobs was left restoring and the interrupted jobs earlier starts did not settle could not be read; it stays restoring until the next start",
	} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes do not say %q:\n%s", want, notes)
		}
	}
	ev, _ := e.svc.Events.Latest(ctx, "library.reconcile_pending", "")
	if ev == nil || ev.Severity != events.Warning ||
		!strings.Contains(ev.Message, "Reconciliation could not read the interrupted jobs earlier starts did not settle (") ||
		!strings.Contains(ev.Message, "every __restore database is kept, libraries left restoring stay restoring and no previous state is forgotten") {
		t.Fatalf("warning about the unreadable jobs: %+v", ev)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE jobs SET priority = 100 WHERE id = 'JB1'`); err != nil {
		t.Fatal(err)
	}
	notes = e.start()
	got := e.lib(l.ID)
	if got.State != core.StateActive || got.PrevDB != "" {
		t.Fatalf("at the next start: state %s prev %q\n%s", got.State, got.PrevDB, notes)
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
		t.Fatalf("workstation cannot connect: %v", err)
	}
	if e.db(leftover) != nil || e.pendingFlag("JB1") != 0 {
		t.Fatalf("not settled at the next start\n%s", notes)
	}
}

// A journal that cannot be read may name any __restore database: they are
// all kept, and its library is left as it is.
func TestUnreadableJournalKeepsEveryRestoreDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("unread_step")
	e.firstBackup(l)
	e.idle()
	other := pg.RestoreDB(l.Slug, "free0001")
	if err := e.svc.PG.CreateDatabase(ctx, other, l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring' WHERE id = ?`, l.ID); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("JJ1", core.JobRestoreReplace, l.ID, "lock",
		fmt.Sprintf(`{"live":%q,"prev":%q,"restored":%q,"phase":"lock","state_before":"active"}`, l.DBName, pg.PrevDB(l.Slug, time.Now()), pg.RestoreDB(l.Slug, "cut00001")))
	if _, err := e.svc.DB.ExecContext(ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at) VALUES ('JJ1', 'unreadable', 'x', 'failed', ?)`, store.NowT()); err != nil {
		t.Fatal(err)
	}
	notes := e.start()
	if e.db(other) == nil {
		t.Fatalf("a __restore database was dropped while a pending journal could not be read\n%s", notes)
	}
	if !strings.Contains(notes, "kept "+other+": the journal of an interrupted job that reconciliation has not settled could not be read, and it may name it") {
		t.Fatalf("notes do not report the kept database:\n%s", notes)
	}
	if got := e.lib(l.ID); got.State != core.StateRestoring {
		t.Fatalf("library of an unreadable journal changed: %s", got.State)
	}
	ev, _ := e.svc.Events.Latest(ctx, "library.reconcile_pending", l.ID)
	if ev == nil || !strings.Contains(ev.Message, "job JJ1: could not read its journal (") {
		t.Fatalf("warning about the unreadable journal: %+v", ev)
	}
	if e.pendingFlag("JJ1") != 1 {
		t.Fatal("the job with an unreadable journal is not pending")
	}
}

// The anomalous case (only the previous database is left) whose put-back is
// refused is not settled: the next start puts it back.
func TestAnomalousPutBackThatFailsIsPending(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("anom_fail")
	e.firstBackup(l)
	e.idle()
	prev := pg.PrevDB(l.Slug, time.Now())
	if err := e.svc.PG.RenameDatabase(ctx, l.DBName, prev); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring', prev_db_name = ? WHERE id = ?`, prev, l.ID); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("JA1", core.JobRestoreReplace, l.ID, "swap",
		fmt.Sprintf(`{"live":%q,"prev":%q,"restored":%q,"phase":"rename_restored","state_before":"active"}`, l.DBName, prev, pg.RestoreDB(l.Slug, "gone0001")))
	// A session of sqsolve_admin makes the rename fail.
	hold, err := e.c.Connect(ctx, pg.AdminRole, e.c.AdminPass, prev)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close(ctx)
	notes := e.start()
	if !strings.Contains(notes, "anom_fail: anomalous state; could not put the previous database "+prev+" back under its name: ") ||
		strings.Contains(notes, "the previous database was put back under its name") {
		t.Fatalf("notes do not report the refused put-back:\n%s", notes)
	}
	if got := e.lib(l.ID); got.State != core.StateRestoring || got.PrevDB != prev || e.db(prev) == nil {
		t.Fatalf("after the refused put-back: state %s prev %q", got.State, got.PrevDB)
	}
	if e.pendingFlag("JA1") != 1 {
		t.Fatal("the refused put-back was settled")
	}
	hold.Close(ctx)
	notes = e.start()
	if got := e.lib(l.ID); got.State != core.StateActive || got.PrevDB != "" {
		t.Fatalf("at the next start: state %s prev %q\n%s", got.State, got.PrevDB, notes)
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
		t.Fatalf("workstation cannot connect: %v", err)
	}
	if e.pendingFlag("JA1") != 0 {
		t.Fatal("still pending after the put-back")
	}
}

// A completed swap whose reopening fails at reconciliation says so in a
// note.
func TestReconciledSwapReportsAFailedReopen(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("reopen_fail")
	e.firstBackup(l)
	e.idle()
	prev := pg.PrevDB(l.Slug, time.Now())
	if err := e.svc.PG.CreateDatabase(ctx, prev, l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring', prev_db_name = ? WHERE id = ?`, prev, l.ID); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("JF1", core.JobRestoreReplace, l.ID, "swap",
		fmt.Sprintf(`{"live":%q,"prev":%q,"restored":%q,"phase":"swapped","state_before":"active"}`, l.DBName, prev, pg.RestoreDB(l.Slug, "done0001")))
	if _, err := e.svc.DB.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER refuse_reopen BEFORE UPDATE OF schema_fingerprint ON libraries WHEN OLD.id = '%s'
		BEGIN SELECT RAISE(ABORT, 'catalog refused'); END`, l.ID)); err != nil {
		t.Fatal(err)
	}
	notes := e.start()
	if !strings.Contains(notes, "reopen_fail: the swap is complete, but reopening the library reported: ") || !strings.Contains(notes, "catalog refused") {
		t.Fatalf("notes do not report the failed reopening:\n%s", notes)
	}
	if e.pendingFlag("JF1") != 0 {
		t.Fatal("the completed swap is pending")
	}
}

// A replace journaled before its lock (it was restoring the copy, no rename
// was planned) whose live database is gone did not start a swap: it is
// settled, the copy is swept and the library is reported missing, instead of
// being left frozen as active.
func TestSwapJournaledBeforeTheLockWithItsDatabaseGone(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("lost_live")
	e.firstBackup(l)
	e.idle()
	restored := pg.RestoreDB(l.Slug, "lost0001")
	if err := e.svc.PG.CreateDatabase(ctx, restored, l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.DropDatabase(ctx, l.DBName); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("JL1", core.JobRestoreReplace, l.ID, "restore",
		fmt.Sprintf(`{"live":%q,"prev":"","restored":%q,"phase":"restoring","state_before":"active"}`, l.DBName, restored))
	notes := e.start()
	if e.pendingFlag("JL1") != 0 {
		t.Fatalf("a swap that never started is pending\n%s", notes)
	}
	if !strings.Contains(notes, "lost_live: the swap had not started (no previous state was planned) and "+l.DBName+" is not on the server; "+restored+" is a leftover") {
		t.Fatalf("notes do not report the swap that did not start:\n%s", notes)
	}
	if got := e.lib(l.ID); got.State != core.StateMissing {
		t.Fatalf("state %s, want missing\n%s", got.State, notes)
	}
	if ev, _ := e.svc.Events.Latest(ctx, "library.missing", l.ID); ev == nil || ev.Severity != events.Critical {
		t.Fatalf("no critical library.missing: %+v", ev)
	}
	if ev, _ := e.svc.Events.Latest(ctx, "library.reconcile_pending", l.ID); ev != nil {
		t.Fatalf("reported pending: %s", ev.Message)
	}
	if e.db(restored) != nil {
		t.Fatal("the leftover copy was not swept")
	}
}
