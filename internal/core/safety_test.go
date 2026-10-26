// SPDX-License-Identifier: AGPL-3.0-only

package core_test

// Restore and lifecycle safety (ADR 0024, audit findings C1, H4, H6, M, M1,
// M2 and M3): every runner re-validates its preconditions, failure paths
// drop only what the run created, maintenance blocks the library roles and
// not the database, and a previous state is never dropped without a verified
// backup of its own.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// idle waits until no job is running.
func (e *env) idle() {
	for i := 0; i < 800 && e.svc.Jobs.Busy() > 0; i++ {
		time.Sleep(25 * time.Millisecond)
	}
}

func (e *env) canLogin(role string) bool {
	e.t.Helper()
	p, err := e.svc.PG.Pool()
	if err != nil {
		e.t.Fatal(err)
	}
	var ok bool
	if err := p.QueryRow(e.ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, role).Scan(&ok); err != nil {
		e.t.Fatalf("role %s: %v", role, err)
	}
	return ok
}

func (e *env) db(name string) *pg.DBInfo {
	e.t.Helper()
	d, err := e.svc.PG.Database(e.ctx, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *env) reach(role string, pass secrets.String, db string) error {
	conn, err := e.c.ConnectTCP(e.ctx, role, pass, db)
	if err == nil {
		conn.Close(e.ctx)
	}
	return err
}

// failsWith waits for j to fail and checks its error code and message.
func (e *env) failsWith(j *jobs.Job, code, text string) *jobs.Job {
	e.t.Helper()
	got := e.wait(j, jobs.Failed)
	if got.ErrorCode != code || !strings.Contains(got.ErrorDetail, text) {
		e.t.Fatalf("job %s failed with %s %q, want %s containing %q", j.Type, got.ErrorCode, got.ErrorDetail, code, text)
	}
	return got
}

func (e *env) lib(id string) *core.Library {
	e.t.Helper()
	l, err := e.svc.Library(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

// lose drops the database (and optionally the owner role) of an active
// library and reconciles, leaving it "missing".
func (e *env) lose(l *core.Library, role bool) {
	e.t.Helper()
	e.idle()
	if err := e.svc.PG.DropDatabase(e.ctx, l.DBName); err != nil {
		e.t.Fatal(err)
	}
	if role {
		if err := e.svc.PG.DropRole(e.ctx, l.OwnerRole); err != nil {
			e.t.Fatal(err)
		}
	}
	e.svc.Reconcile(e.ctx, nil)
	if got := e.lib(l.ID); got.State != core.StateMissing {
		e.t.Fatalf("state %s, want missing", got.State)
	}
}

// --- 1. Failure paths never drop a database the run did not create (C1) ---

// A stale restore.new (a Retry of an old failed job) whose slug is now used by
// another library must not drop that library's database.
func TestStaleRestoreNewKeepsOtherLibrary(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	src, _ := e.createActive("src_lib")
	b := e.firstBackup(src)
	e.idle()
	bj, err := e.svc.BackupNow(ctx, admin, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := e.svc.Backup(ctx, resultBackupID(t, e.wait(bj, jobs.Succeeded)))
	abs := mustAbs(e.svc, b)
	os.Rename(abs, abs+".away")
	_, j1, err := e.svc.RestoreAsNew(ctx, admin, b.ID, "copy_lib", "Copy one")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j1, jobs.Failed)
	os.Rename(abs+".away", abs)
	other, j2, err := e.svc.RestoreAsNew(ctx, admin, b2.ID, "copy_lib", "Copy two")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j2, jobs.Succeeded)
	e.idle()
	pass, _ := e.svc.RevealPassword(ctx, admin, other.ID, "")
	// The old job runs again with its old parameters (what Retry does).
	stale, err := e.svc.Jobs.Enqueue(ctx, core.JobRestoreNew, j1.LibraryID, restoreParams(t, j1.Params), admin)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(stale, "SQS-R050", "nothing was changed")
	if e.db("copy_lib") == nil {
		t.Fatal("the stale restore dropped the live database of another library")
	}
	if n := e.count(other.OwnerRole, pass, "copy_lib", "SM_Project"); n != 50 {
		t.Fatalf("other library has %d rows", n)
	}
	if got := e.lib(other.ID); got.State != core.StateActive {
		t.Fatalf("other library state %s", got.State)
	}
}

// A stale recreate (double click, or Retry of an old failed one) after a
// successful recreate must not drop the recreated database.
func TestStaleRecreateKeepsLiveDB(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("dbl_click")
	b := e.firstBackup(l)
	e.lose(l, true)
	abs := mustAbs(e.svc, b)
	os.Rename(abs, abs+".away")
	failed, err := e.svc.Jobs.Enqueue(ctx, core.JobRestoreRecreate, l.ID, core.RestoreParams{BackupID: b.ID}, admin)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(failed, jobs.Failed)
	os.Rename(abs+".away", abs)
	ok, err := e.svc.Jobs.Enqueue(ctx, core.JobRestoreRecreate, l.ID, core.RestoreParams{BackupID: b.ID}, admin)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(ok, jobs.Succeeded)
	stale, err := e.svc.Jobs.Enqueue(ctx, core.JobRestoreRecreate, l.ID, core.RestoreParams{BackupID: b.ID}, admin)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(stale, "SQS-R040", "only libraries with no database on the server can be recreated")
	if e.db(l.DBName) == nil {
		t.Fatal("the stale recreate dropped the live database")
	}
	if n := e.count(l.OwnerRole, pass, l.DBName, "SM_Project"); n != 50 {
		t.Fatalf("live library has %d rows", n)
	}
	if got := e.lib(l.ID); got.State != core.StateActive {
		t.Fatalf("state %s", got.State)
	}
}

// Recreate asks for the identifier and a second click returns the queued job.
func TestRecreateConfirmedAndUnique(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("one_job")
	e.firstBackup(l)
	e.lose(l, true)
	if _, err := e.svc.Recreate(ctx, admin, l.ID, "", "wrong"); err == nil || err.Error() != "SQS-R002 type the library identifier to confirm" {
		t.Fatalf("recreate without confirmation: %v", err)
	}
	j1, err := e.svc.Recreate(ctx, admin, l.ID, "", l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	j2, err := e.svc.Recreate(ctx, admin, l.ID, "", l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if j2.ID != j1.ID {
		t.Fatalf("double click queued two recreates: %s and %s", j1.ID, j2.ID)
	}
	e.wait(j1, jobs.Succeeded)
	if _, err := e.svc.Recreate(ctx, admin, l.ID, "", l.Slug); err == nil || err.Error() != "SQS-R040 only libraries with no database on the server can be recreated" {
		t.Fatalf("recreate of an active library: %v", err)
	}
	if n := e.count(l.OwnerRole, pass, l.DBName, "SM_Project"); n != 50 {
		t.Fatalf("recreated library has %d rows", n)
	}
}

// A recreate whose database reappeared before the job ran fails without
// touching it.
func TestRecreateLeavesExistingDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("came_back")
	b := e.firstBackup(l)
	e.idle()
	// The catalog says "missing" but the database is there (volume remounted).
	e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'missing' WHERE id = ?`, l.ID)
	j, err := e.svc.Jobs.Enqueue(ctx, core.JobRestoreRecreate, l.ID, core.RestoreParams{BackupID: b.ID}, admin)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(j, "SQS-R052", `a database "came_back" already exists on the server; it was left untouched`)
	if n := e.count(l.OwnerRole, pass, l.DBName, "SM_Project"); n != 50 {
		t.Fatalf("database touched: %d rows", n)
	}
}

// --- 2. A missing library whose database reappears becomes active (H4) ---

func TestMissingLibraryComesBack(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("remounted")
	e.firstBackup(l)
	e.idle()
	hba := func() string {
		b, _ := os.ReadFile(filepath.Join(e.c.ConfDir, "pg_hba.conf"))
		return string(b)
	}
	if err := e.svc.PG.RenameDatabase(ctx, l.DBName, "remounted_away"); err != nil {
		t.Fatal(err)
	}
	e.svc.Reconcile(ctx, nil)
	if got := e.lib(l.ID); got.State != core.StateMissing {
		t.Fatalf("state %s, want missing", got.State)
	}
	if strings.Contains(hba(), "# library remounted\n") {
		t.Fatal("pg_hba.conf still lists the missing library")
	}
	// The volume comes back.
	if err := e.svc.PG.RenameDatabase(ctx, "remounted_away", l.DBName); err != nil {
		t.Fatal(err)
	}
	notes := e.svc.Reconcile(ctx, nil)
	if got := e.lib(l.ID); got.State != core.StateActive {
		t.Fatalf("state %s after the database came back (notes %v)", got.State, notes)
	}
	if ev, _ := e.svc.Events.Latest(ctx, "library.found", l.ID); ev == nil {
		t.Fatal("no library.found event")
	}
	if !strings.Contains(hba(), "# library remounted\n") {
		t.Fatalf("pg_hba.conf does not list the library again:\n%s", hba())
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
		t.Fatalf("workstation cannot connect: %v", err)
	}
}

// A database with the library's name but another owner is not adopted.
func TestMissingLibraryForeignOwnerStaysMissing(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("foreign_owner")
	e.firstBackup(l)
	e.lose(l, false)
	if err := e.svc.PG.CreateDatabase(ctx, l.DBName, pg.AdminRole); err != nil {
		t.Fatal(err)
	}
	e.svc.Reconcile(ctx, nil)
	if got := e.lib(l.ID); got.State != core.StateMissing {
		t.Fatalf("state %s, want missing", got.State)
	}
}

// --- 3. Maintenance blocks the library roles, not the database (H6) ---

func TestMaintenanceBlocksRolesNotDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("maint_lib")
	b := e.firstBackup(l)
	e.idle()
	ana, err := e.svc.AddMember(ctx, admin, l.ID, "ana")
	if err != nil {
		t.Fatal(err)
	}
	anaPass, _ := e.svc.RevealPassword(ctx, admin, l.ID, ana.ID)
	bea, err := e.svc.AddMember(ctx, admin, l.ID, "bea")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeMember(ctx, admin, l.ID, bea.ID); err != nil {
		t.Fatal(err)
	}
	open := e.connect(l.OwnerRole, pass, l.DBName)
	defer open.Close(ctx)

	// Without closing sessions: the open one keeps working (PoC-09), new ones are refused.
	if _, err := e.svc.SetMaintenance(ctx, admin, l.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := open.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("open session interrupted: %v", err)
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err == nil {
		t.Fatal("owner can open a new connection in maintenance")
	}
	if err := e.reach(ana.PGRole, anaPass, l.DBName); err == nil {
		t.Fatal("member can open a new connection in maintenance")
	}
	if d := e.db(l.DBName); !d.AllowConn {
		t.Fatal("maintenance closed the database to sqsolve_admin")
	}

	// Backups, the activity check and a restore by swap keep working.
	bj, err := e.svc.BackupNow(ctx, admin, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(bj, jobs.Succeeded)
	open.Close(ctx)
	rj, err := e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, true)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(rj, jobs.Succeeded)
	if got := e.lib(l.ID); got.State != core.StateMaintenance {
		t.Fatalf("state %s after restore, want maintenance", got.State)
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err == nil {
		t.Fatal("restore reopened a library in maintenance to its roles")
	}
	if e.canLogin(l.OwnerRole) || e.canLogin(ana.PGRole) {
		t.Fatal("library roles can log in during maintenance")
	}
	vj, err := e.svc.Revert(ctx, admin, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(vj, jobs.Succeeded)
	if got := e.lib(l.ID); got.State != core.StateMaintenance || e.canLogin(l.OwnerRole) {
		t.Fatalf("revert reopened a library in maintenance: %s", got.State)
	}
	// A person added during maintenance waits for it to end too.
	cai, err := e.svc.AddMember(ctx, admin, l.ID, "cai")
	if err != nil {
		t.Fatal(err)
	}
	caiPass, _ := e.svc.RevealPassword(ctx, admin, l.ID, cai.ID)
	if err := e.reach(cai.PGRole, caiPass, l.DBName); err == nil {
		t.Fatal("a member added during maintenance can connect")
	}

	// Leaving maintenance reopens active credentials only.
	if _, err := e.svc.SetMaintenance(ctx, admin, l.ID, false, false); err != nil {
		t.Fatal(err)
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
		t.Fatalf("owner cannot connect after maintenance: %v", err)
	}
	if err := e.reach(ana.PGRole, anaPass, l.DBName); err != nil {
		t.Fatalf("member cannot connect after maintenance: %v", err)
	}
	if err := e.reach(cai.PGRole, caiPass, l.DBName); err != nil {
		t.Fatalf("member added during maintenance cannot connect after it: %v", err)
	}
	if e.canLogin(bea.PGRole) {
		t.Fatal("a revoked credential got LOGIN back when leaving maintenance")
	}

	// Closing sessions ends the library's sessions.
	open = e.connect(l.OwnerRole, pass, l.DBName)
	n, err := e.svc.SetMaintenance(ctx, admin, l.ID, true, true)
	if err != nil || n != 1 {
		t.Fatalf("terminated %d sessions: %v", n, err)
	}
	if _, err := open.Exec(ctx, `SELECT 1`); err == nil {
		t.Fatal("session survived maintenance with close sessions")
	}

	// Delete in maintenance works (its final backup needs sqsolve_admin).
	tj, err := e.svc.Trash(ctx, admin, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(tj, jobs.Succeeded)
}

// Installations that put a library in maintenance before ADR 0024 have
// datallowconn = false: reconciliation converts them.
func TestReconcileConvertsLegacyMaintenance(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("legacy_maint")
	e.firstBackup(l)
	e.idle()
	e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'maintenance' WHERE id = ?`, l.ID)
	if err := e.svc.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
		t.Fatal(err)
	}
	e.svc.Reconcile(ctx, nil)
	if d := e.db(l.DBName); !d.AllowConn {
		t.Fatal("legacy maintenance still has datallowconn = false")
	}
	if e.canLogin(l.OwnerRole) {
		t.Fatal("legacy maintenance left the owner with LOGIN")
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err == nil {
		t.Fatal("workstation can connect to a library in maintenance")
	}
	if got := e.lib(l.ID); got.State != core.StateMaintenance {
		t.Fatalf("state %s", got.State)
	}
}

// --- 4. Members keep acting as the owner after a swap or a recreate ---

func TestMemberDefaultRoleAfterSwapAndRecreate(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("theta")
	b := e.firstBackup(l)
	e.idle()
	cred, err := e.svc.AddMember(ctx, admin, l.ID, "ana")
	if err != nil {
		t.Fatal(err)
	}
	mp, _ := e.svc.RevealPassword(ctx, admin, l.ID, cred.ID)
	who := func(when string) {
		t.Helper()
		c := e.connect(cred.PGRole, mp, l.DBName)
		defer c.Close(ctx)
		var u string
		if err := c.QueryRow(ctx, `SELECT current_user`).Scan(&u); err != nil {
			t.Fatal(err)
		}
		if u != l.OwnerRole {
			t.Fatalf("%s: member acts as %s, want %s", when, u, l.OwnerRole)
		}
	}
	who("before")
	j, err := e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, true)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	who("after replace")
	j, err = e.svc.Revert(ctx, admin, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	who("after revert")
	// The database is lost but the roles survive.
	e.lose(e.lib(l.ID), false)
	j, err = e.svc.Jobs.Enqueue(ctx, core.JobRestoreRecreate, l.ID, core.RestoreParams{BackupID: b.ID}, admin)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	who("after recreate")
}

// --- 5. A failed or interrupted delete or revert never leaves the library locked out (M1) ---

// A superuser session cannot be closed by sqsolve_admin: the delete fails
// with a clear code and the library stays usable.
func TestTrashFailureLeavesLibraryUsable(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("eps")
	e.firstBackup(l)
	e.idle()
	su, err := e.c.Connect(ctx, "postgres", e.c.SuperPass, l.DBName)
	if err != nil {
		t.Fatal(err)
	}
	j, err := e.svc.Trash(ctx, admin, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(j, "SQS-L052", "still has sessions sqsolve cannot close")
	su.Close(ctx)
	if got := e.lib(l.ID); got.State != core.StateActive {
		t.Fatalf("state %s", got.State)
	}
	if !e.canLogin(l.OwnerRole) || !e.db(l.DBName).AllowConn {
		t.Fatal("failed delete left the library locked")
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
		t.Fatalf("workstation cannot connect after a failed delete: %v", err)
	}
}

// A crash during the lock step of a revert (before its first rename) leaves
// the library usable after reconciliation.
func TestReconcileRevertCrashAtLock(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("revlock")
	e.firstBackup(l)
	e.idle()
	e.svc.DB.ExecContext(ctx, `INSERT INTO jobs(id, type, library_id, state, scheduled_for, created_at, created_by_type) VALUES ('J9', 'restore.revert', ?, 'running', ?, ?, 'user')`, l.ID, store.NowT(), store.NowT())
	e.svc.DB.ExecContext(ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at) VALUES ('J9', 1, 'pre_restore_backup', 'succeeded', ?)`, store.NowT())
	e.svc.DB.ExecContext(ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at) VALUES ('J9', 2, 'lock', 'running', ?)`, store.NowT())
	e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring' WHERE id = ?`, l.ID)
	if err := e.svc.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
		t.Fatal(err)
	}
	interrupted, _ := e.svc.Jobs.MarkInterrupted(ctx)
	e.svc.Reconcile(ctx, interrupted)
	if got := e.lib(l.ID); got.State != core.StateActive {
		t.Fatalf("state %s", got.State)
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
		t.Fatalf("workstation cannot connect after reconciliation: %v", err)
	}
}

// A crash between the trash journal and the rename leaves the library usable.
func TestReconcileTrashCrashBeforeRename(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("trashcut")
	e.firstBackup(l)
	e.idle()
	e.svc.PG.SetRoleLogin(ctx, l.OwnerRole, false)
	e.svc.PG.SetAllowConnections(ctx, l.DBName, false)
	e.svc.DB.ExecContext(ctx, `INSERT INTO jobs(id, type, library_id, state, scheduled_for, created_at, created_by_type) VALUES ('J8', 'library.delete', ?, 'running', ?, ?, 'user')`, l.ID, store.NowT(), store.NowT())
	e.svc.DB.ExecContext(ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at, data) VALUES ('J8', 3, 'move', 'running', ?, ?)`, store.NowT(),
		`{"from":"trashcut","to":"trashcut__trash_20261020101010"}`)
	interrupted, _ := e.svc.Jobs.MarkInterrupted(ctx)
	e.svc.Reconcile(ctx, interrupted)
	if got := e.lib(l.ID); got.State != core.StateActive {
		t.Fatalf("state %s", got.State)
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
		t.Fatalf("workstation cannot connect after reconciliation: %v", err)
	}
}

// Stale lifecycle jobs re-validate their preconditions and touch nothing.
func TestStaleLifecycleJobsTouchNothing(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	a, _ := e.createActive("first_lib")
	e.firstBackup(a)
	e.idle()
	tj, err := e.svc.Trash(ctx, admin, a.ID, a.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(tj, jobs.Succeeded)
	trashed := e.lib(a.ID)
	// A second delete of a trashed library.
	j, _ := e.svc.Jobs.Enqueue(ctx, core.JobLibraryDelete, a.ID, nil, admin)
	e.failsWith(j, "SQS-L041", "cannot delete a library in state In the trash")
	if got := e.lib(a.ID); got.TrashDB != trashed.TrashDB || got.State != core.StateTrashed {
		t.Fatalf("stale delete rewrote the trash: %+v", got)
	}
	pj, err := e.svc.Purge(ctx, admin, a.ID, a.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(pj, jobs.Succeeded)
	// The slug is reused by another library.
	b, pass := e.createActive("first_lib")
	e.firstBackup(b)
	e.idle()
	j, _ = e.svc.Jobs.Enqueue(ctx, core.JobLibraryDelete, a.ID, nil, admin)
	e.failsWith(j, "SQS-L041", "cannot delete a library in state Purged")
	j, _ = e.svc.Jobs.Enqueue(ctx, core.JobLibraryUntrash, a.ID, nil, admin)
	e.failsWith(j, "SQS-L042", "the library is not in the trash")
	if n := e.count(b.OwnerRole, pass, b.DBName, "SM_Project"); n != 50 {
		t.Fatalf("other library touched: %d rows", n)
	}
	if got := e.lib(a.ID); got.State != core.StatePurged {
		t.Fatalf("purged library changed to %s", got.State)
	}
	// Revert and replace of a library in the wrong state.
	j, _ = e.svc.Jobs.Enqueue(ctx, core.JobRestoreRevert, a.ID, core.RestoreParams{}, admin)
	e.failsWith(j, "SQS-R051", "cannot revert a library in state Purged")
	j, _ = e.svc.Jobs.Enqueue(ctx, core.JobRestoreReplace, a.ID, core.RestoreParams{BackupID: "x"}, admin)
	e.failsWith(j, "SQS-R003", "cannot replace a library in state Purged")
	e.svc.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = 'first_lib__prev_202610190000' WHERE id = ?`, a.ID)
	if _, err := e.svc.Revert(ctx, admin, a.ID, a.Slug); err == nil || err.Error() != "SQS-R051 cannot revert a library in state Purged" {
		t.Fatalf("revert of a purged library: %v", err)
	}
}

// A missing library whose own database came back is not deleted without its
// final backup; a trashed library whose database disappeared is not
// "recovered" into nothing.
func TestTrashAndUntrashCheckTheirDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("own_db_back")
	e.firstBackup(l)
	e.lose(l, false)
	if err := e.svc.PG.CreateDatabase(ctx, l.DBName, l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	j, err := e.svc.Trash(ctx, admin, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(j, "SQS-L050", `the database "own_db_back" exists again on the server`)
	if e.db(l.DBName) == nil {
		t.Fatal("database touched")
	}

	m, _ := e.createActive("trash_gone")
	e.firstBackup(m)
	e.idle()
	j, err = e.svc.Trash(ctx, admin, m.ID, m.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	m = e.lib(m.ID)
	if err := e.svc.PG.DropDatabase(ctx, m.TrashDB); err != nil {
		t.Fatal(err)
	}
	j, err = e.svc.RestoreFromTrash(ctx, admin, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(j, "SQS-L051", "the database in the trash ("+m.TrashDB+") no longer exists on the server")
	if got := e.lib(m.ID); got.State != core.StateTrashed {
		t.Fatalf("state %s", got.State)
	}
}

// --- 6. The previous state is backed up as it was when it stopped being live (M2) ---

func TestPreviousStateBackedUpAfterSwap(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("edit_window")
	b := e.firstBackup(l)
	e.idle()
	ws := e.connect(l.OwnerRole, pass, l.DBName)
	j, err := e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, false)
	if err != nil {
		t.Fatal(err)
	}
	// While the restore waits for sessions, the editor keeps working.
	for i := 0; ; i++ {
		got, _ := e.svc.Jobs.Get(ctx, j.ID)
		if got.CurrentStep == "wait_for_sessions" {
			break
		}
		if i > 1200 || got.State.Finished() {
			t.Fatalf("job did not wait for sessions: %+v", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := ws.Exec(ctx, `INSERT INTO "SM_Project"(name) SELECT 'late' || g FROM generate_series(1, 7) g`); err != nil {
		t.Fatal(err)
	}
	ws.Close(ctx)
	e.wait(j, jobs.Succeeded)
	l = e.lib(l.ID)
	pb, err := e.svc.Backup(ctx, l.PrevBackupID)
	if err != nil {
		t.Fatal(err)
	}
	if pb.SourceDB != l.PrevDB || !pb.Verified() {
		t.Fatalf("previous state not covered by its own backup: %+v (prev %s)", pb, l.PrevDB)
	}
	m, err := core.ReadManifest(strings.TrimSuffix(mustAbs(e.svc, pb), ".backup") + ".manifest.json")
	if err != nil || m.RowCounts["SM_Project"] != 57 {
		t.Fatalf("backup of the previous state misses the late edits: %+v %v", m, err)
	}
	if d := e.db(l.PrevDB); d == nil || d.AllowConn {
		t.Fatalf("previous state left open: %+v", d)
	}
}

// An expired previous state from before this change (its backup is of the
// live database) is backed up before it is dropped.
func TestPurgeExpiredBacksUpPreviousState(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("expired_prev")
	b := e.firstBackup(l)
	e.idle()
	j, err := e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, true)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	l = e.lib(l.ID)
	prev := l.PrevDB
	// Legacy row: covered only by a backup of the live database, and expired.
	e.svc.DB.ExecContext(ctx, `UPDATE libraries SET prev_backup_id = ?, prev_expires_at = ? WHERE id = ?`, b.ID, store.T(time.Now().Add(-time.Hour)), l.ID)
	marker := filepath.Join(e.svc.Cfg.BackupDir, fsx.MarkerName)
	saved, _ := os.ReadFile(marker)
	os.Remove(marker)
	pj, _ := e.svc.Jobs.Enqueue(ctx, core.JobPurgeExpired, "", nil, admin)
	e.wait(pj, jobs.Succeeded)
	if e.db(prev) == nil || e.lib(l.ID).PrevDB != prev {
		t.Fatal("a previous state without a backup of its own was dropped")
	}
	if ev, _ := e.svc.Events.Latest(ctx, "library.prev_kept", l.ID); ev == nil || !strings.Contains(ev.Message, "SQS-R055 "+prev+" has no verified backup of its own and one could not be taken now") {
		t.Fatalf("no warning for the kept previous state: %+v", ev)
	}
	os.WriteFile(marker, saved, 0o600)
	pj, _ = e.svc.Jobs.Enqueue(ctx, core.JobPurgeExpired, "", nil, admin)
	e.wait(pj, jobs.Succeeded)
	if e.db(prev) != nil {
		t.Fatal("expired previous state not dropped")
	}
	if got := e.lib(l.ID); got.PrevDB != "" {
		t.Fatalf("prev_db_name not cleared: %q", got.PrevDB)
	}
	found := false
	bs, _ := e.svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Statuses: []string{core.BackupOK}})
	for _, x := range bs {
		if x.SourceDB == prev && x.Verified() {
			found = true
		}
	}
	if !found {
		t.Fatal("no verified backup of the dropped previous state")
	}
}

// --- 7. Orphans and failed swaps keep the original tracked (M3) ---

func TestSweepOrphanPrevRequiresBackup(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("orphan_lib")
	e.firstBackup(l)
	e.idle()
	orphan := pg.PrevDB(l.Slug, time.Now().Add(-30*24*time.Hour))
	if err := e.svc.PG.CreateDatabase(ctx, orphan, l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	e.svc.PG.SetAllowConnections(ctx, orphan, false)
	marker := filepath.Join(e.svc.Cfg.BackupDir, fsx.MarkerName)
	saved, _ := os.ReadFile(marker)
	os.Remove(marker)
	pj, _ := e.svc.Jobs.Enqueue(ctx, core.JobPurgeExpired, "", nil, admin)
	e.wait(pj, jobs.Succeeded)
	if e.db(orphan) == nil {
		t.Fatal("orphan dropped without a backup")
	}
	if ev, _ := e.svc.Events.Latest(ctx, "library.prev_kept", l.ID); ev == nil {
		t.Fatal("no warning for the kept orphan")
	}
	os.WriteFile(marker, saved, 0o600)
	pj, _ = e.svc.Jobs.Enqueue(ctx, core.JobPurgeExpired, "", nil, admin)
	e.wait(pj, jobs.Succeeded)
	if e.db(orphan) != nil {
		t.Fatal("orphan with a backup not dropped")
	}
	bs, _ := e.svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Statuses: []string{core.BackupOK}})
	found := false
	for _, x := range bs {
		found = found || x.SourceDB == orphan && x.Verified()
	}
	if !found {
		t.Fatal("no verified backup of the dropped orphan")
	}
}

// The second rename of a swap fails (a superuser session holds the restored
// database): the original goes back under its name and stays tracked.
func TestSwapRenameFailurePutsOriginalBack(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("put_back")
	b := e.firstBackup(l)
	e.idle()
	ws := e.connect(l.OwnerRole, pass, l.DBName)
	j, err := e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, false)
	if err != nil {
		t.Fatal(err)
	}
	restoreDB := pg.RestoreDB(l.Slug, ulid.Short(j.ID))
	for i := 0; ; i++ {
		got, _ := e.svc.Jobs.Get(ctx, j.ID)
		if got.CurrentStep == "wait_for_sessions" {
			break
		}
		if i > 1200 || got.State.Finished() {
			t.Fatalf("job did not wait for sessions: %+v", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
	su, err := e.c.Connect(ctx, "postgres", e.c.SuperPass, restoreDB)
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)
	ws.Exec(ctx, `INSERT INTO "SM_Project"(name) VALUES ('keep me')`)
	ws.Close(ctx)
	e.failsWith(j, "SQS-L052", "the database "+restoreDB+" still has sessions sqsolve cannot close")
	got := e.lib(l.ID)
	if got.State != core.StateActive || got.PrevDB != "" {
		t.Fatalf("after the failed swap: state %s prev %q", got.State, got.PrevDB)
	}
	if n := e.count(l.OwnerRole, pass, l.DBName, "SM_Project"); n != 51 {
		t.Fatalf("original not in place: %d rows", n)
	}
}

// Both renames failed in an earlier run: the original sits under its
// previous-state name, recorded in the catalog. Reconciliation reports the
// library missing without dropping anything, and Revert puts it back.
func TestReconcileAfterStuckSwap(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("stuck_swap")
	e.firstBackup(l)
	e.idle()
	prev := pg.PrevDB(l.Slug, time.Now())
	e.svc.PG.SetAllowConnections(ctx, l.DBName, false)
	if err := e.svc.PG.RenameDatabase(ctx, l.DBName, prev); err != nil {
		t.Fatal(err)
	}
	e.svc.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = ?, prev_expires_at = ? WHERE id = ?`, prev, store.T(time.Now().Add(-time.Hour)), l.ID)
	e.svc.Reconcile(ctx, nil)
	got := e.lib(l.ID)
	if got.State != core.StateMissing || got.PrevDB != prev || e.db(prev) == nil {
		t.Fatalf("after reconcile: %+v", got)
	}
	// Expired, but the library has no live database: it is not dropped.
	pj, _ := e.svc.Jobs.Enqueue(ctx, core.JobPurgeExpired, "", nil, admin)
	e.wait(pj, jobs.Succeeded)
	if e.db(prev) == nil {
		t.Fatal("the only copy of the original was dropped")
	}
	rj, err := e.svc.Revert(ctx, admin, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(rj, jobs.Succeeded)
	got = e.lib(l.ID)
	if got.State != core.StateActive || got.PrevDB != "" {
		t.Fatalf("after revert: state %s prev %q", got.State, got.PrevDB)
	}
	if n := e.count(l.OwnerRole, pass, l.DBName, "SM_Project"); n != 50 {
		t.Fatalf("original not back: %d rows", n)
	}
}

func resultBackupID(t *testing.T, j *jobs.Job) string {
	t.Helper()
	var r struct {
		BackupID string `json:"backup_id"`
	}
	if err := json.Unmarshal(j.Result, &r); err != nil || r.BackupID == "" {
		t.Fatalf("job result %s: %v", j.Result, err)
	}
	return r.BackupID
}

func restoreParams(t *testing.T, raw []byte) core.RestoreParams {
	t.Helper()
	var p core.RestoreParams
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// --- Fix round 1 ---

// interruptedJob stores a job left running by a previous process, with one
// step carrying data as its journal.
func (e *env) interruptedJob(id, typ, libraryID, step, data string) {
	e.t.Helper()
	now := store.NowT()
	if _, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO jobs(id, type, library_id, state, scheduled_for, created_at, started_at, created_by_type) VALUES (?, ?, ?, 'running', ?, ?, ?, 'user')`,
		id, typ, libraryID, now, now, now); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at, data) VALUES (?, 1, ?, 'running', ?, ?)`, id, step, now, data); err != nil {
		e.t.Fatal(err)
	}
}

// A recreate interrupted while restoring leaves an empty database it created:
// reconciliation drops it and the library stays missing (it is not adopted as
// an active, empty library). One interrupted after its comparison is adopted.
func TestReconcileInterruptedRecreate(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("rc_empty")
	e.firstBackup(l)
	e.lose(l, false)
	if err := e.svc.PG.CreateDatabase(ctx, l.DBName, l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("J7", core.JobRestoreRecreate, l.ID, "restore", `{"created":"rc_empty","phase":"creating"}`)
	done, _ := e.createActive("rc_done")
	e.firstBackup(done)
	e.idle()
	e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'missing' WHERE id = ?`, done.ID)
	e.interruptedJob("J6", core.JobRestoreRecreate, done.ID, "compare", `{"created":"rc_done","phase":"verified"}`)
	interrupted, _ := e.svc.Jobs.MarkInterrupted(ctx)
	e.svc.Reconcile(ctx, interrupted)
	if got := e.lib(l.ID); got.State != core.StateMissing || e.db(l.DBName) != nil {
		t.Fatalf("empty database of an interrupted recreate adopted: state %s, database kept %v", got.State, e.db(l.DBName) != nil)
	}
	if ev, _ := e.svc.Events.Latest(ctx, "library.found", l.ID); ev != nil {
		t.Fatal("library.found emitted for an empty database")
	}
	if got := e.lib(done.ID); got.State != core.StateActive {
		t.Fatalf("verified recreate not adopted: %s", got.State)
	}
	j, err := e.svc.Recreate(ctx, admin, l.ID, "", l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	if n := e.count(l.OwnerRole, pass, l.DBName, "SM_Project"); n != 50 {
		t.Fatalf("recreated library has %d rows", n)
	}
}

// The drop after an interrupted recreate needs every condition: a journaled
// database with tables (pg_restore had committed) or of another owner is kept,
// and one that cannot be inspected is kept and the library is not adopted.
func TestReconcileInterruptedRecreateKeepsWhatItCannotProve(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	full, _ := e.createActive("rc_full")
	e.firstBackup(full)
	// Empty but owned by another role: not the recreate's, kept.
	foreign, _ := e.createActive("rc_foreign")
	e.firstBackup(foreign)
	e.lose(foreign, false)
	if err := e.svc.PG.CreateDatabase(ctx, foreign.DBName, pg.AdminRole); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("J4", core.JobRestoreRecreate, foreign.ID, "restore", `{"created":"rc_foreign","phase":"creating"}`)
	// Empty and its own, but it cannot be inspected: kept and left missing.
	closed, _ := e.createActive("rc_closed")
	e.firstBackup(closed)
	e.lose(closed, false)
	if err := e.svc.PG.CreateDatabase(ctx, closed.DBName, closed.OwnerRole); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.SetAllowConnections(ctx, closed.DBName, false); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("J3", core.JobRestoreRecreate, closed.ID, "restore", `{"created":"rc_closed","phase":"creating"}`)
	// Restored (it has tables) but cut before the comparison: kept and adopted.
	// Marked last, because lose() above reconciles too.
	e.idle()
	e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'missing' WHERE id = ?`, full.ID)
	e.interruptedJob("J5", core.JobRestoreRecreate, full.ID, "restore", `{"created":"rc_full","phase":"creating"}`)
	interrupted, _ := e.svc.Jobs.MarkInterrupted(ctx)
	notes := strings.Join(e.svc.Reconcile(ctx, interrupted), "\n")
	if got := e.lib(full.ID); got.State != core.StateActive || e.db(full.DBName) == nil {
		t.Fatalf("restored database of an interrupted recreate: state %s", got.State)
	}
	if got := e.lib(foreign.ID); got.State != core.StateMissing || e.db(foreign.DBName) == nil {
		t.Fatalf("database of another owner: state %s, kept %v", got.State, e.db(foreign.DBName) != nil)
	}
	if got := e.lib(closed.ID); got.State != core.StateMissing || e.db(closed.DBName) == nil {
		t.Fatalf("database that could not be inspected: state %s, kept %v", got.State, e.db(closed.DBName) != nil)
	}
	if ev, _ := e.svc.Events.Latest(ctx, "library.found", closed.ID); ev != nil {
		t.Fatal("library.found for a database the recreate check could not inspect")
	}
	if !strings.Contains(notes, "rc_closed: could not inspect rc_closed, left by an interrupted recreate") ||
		!strings.Contains(notes, "rc_closed: the interrupted job J3 is not settled; its state and access are left as they are; the next start tries again") {
		t.Fatalf("notes do not report the undecided check:\n%s", notes)
	}
	if e.pendingFlag("J3") != 1 || e.pendingFlag("J4") != 0 || e.pendingFlag("J5") != 0 {
		t.Fatal("only the undecided recreate stays pending")
	}
	// It is checked again at the next start: once it can be inspected, the
	// empty database the recreate created is dropped and the job is settled.
	if err := e.svc.PG.SetAllowConnections(ctx, closed.DBName, true); err != nil {
		t.Fatal(err)
	}
	notes = e.start()
	if got := e.lib(closed.ID); got.State != core.StateMissing || e.db(closed.DBName) != nil {
		t.Fatalf("at the next start: state %s, database kept %v\n%s", got.State, e.db(closed.DBName) != nil, notes)
	}
	if e.pendingFlag("J3") != 0 {
		t.Fatal("the recreate settled at the next start is still pending")
	}
}

// The journals that make a recreate and a delete recoverable are required: if
// one cannot be written the step stops before the database is created or moved.
func TestRecreateAndMoveNeedTheirJournal(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("rj_lib")
	e.firstBackup(l)
	e.lose(l, true)
	m, pass := e.createActive("mj_lib")
	e.firstBackup(m)
	e.idle()
	if _, err := e.svc.DB.ExecContext(ctx, `CREATE TRIGGER refuse_journals BEFORE UPDATE ON job_steps WHEN NEW.data LIKE '%"phase":"creating"%' OR NEW.data LIKE '%"to":"%'
		BEGIN SELECT RAISE(ABORT, 'journal refused'); END`); err != nil {
		t.Fatal(err)
	}
	j, err := e.svc.Recreate(ctx, admin, l.ID, "", l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(j, "SQS-R058", "the job journal could not be written, so the database was not created: ")
	if got := e.lib(l.ID); got.State != core.StateMissing || e.db(l.DBName) != nil {
		t.Fatalf("recreate without its journal: state %s, database %v", got.State, e.db(l.DBName) != nil)
	}
	tj, err := e.svc.Trash(ctx, admin, m.ID, m.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(tj, "SQS-L055", "the job journal could not be written, so the database was not moved to the trash: ")
	if got := e.lib(m.ID); got.State != core.StateActive {
		t.Fatalf("delete without its journal: state %s", got.State)
	}
	if err := e.reach(m.OwnerRole, pass, m.DBName); err != nil {
		t.Fatalf("library locked after the refused move: %v", err)
	}
}

// CREATE DATABASE failing because the name exists (42P04) never drops that
// database: a provisioning row whose database name belongs to another library
// reaches the create step and must leave it intact, dropping only the role it
// created.
func TestRestoreNewNeverDropsADatabaseItDidNotCreate(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	victim, pass := e.createActive("victim_lib")
	b := e.firstBackup(victim)
	e.idle()
	now := store.NowT()
	if _, err := e.svc.DB.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES ('LX', 'intruder', 'Intruder', 'provisioning', 'victim_lib', 'intruder', 'restore', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	j, err := e.svc.Jobs.Enqueue(ctx, core.JobRestoreNew, "LX", core.RestoreParams{BackupID: b.ID, Slug: "intruder", DisplayName: "Intruder"}, admin)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(j, "SQS-R052", `a database "victim_lib" already exists on the server; it was left untouched`)
	if n := e.count(victim.OwnerRole, pass, "victim_lib", "SM_Project"); n != 50 {
		t.Fatalf("the other library's database was touched: %d rows", n)
	}
	if ok, _ := e.svc.PG.RoleExists(ctx, "intruder"); ok {
		t.Fatal("the role this run created was not dropped")
	}
	if got := e.lib("LX"); got.State != core.StatePurged {
		t.Fatalf("failed restore row state %s", got.State)
	}
}

// ADR 0024: roles NOLOGIN first, then the database opened. If a role cannot
// be set NOLOGIN (here one created by the superuser, which sqsolve_admin may
// not alter), a library in maintenance keeps its database closed.
func TestMaintenanceKeepsDatabaseClosedIfARoleCannotBeLocked(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("half_maint")
	e.firstBackup(l)
	e.idle()
	su, err := e.c.Connect(ctx, "postgres", e.c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := su.Exec(ctx, `CREATE ROLE half_maint__zed LOGIN`); err != nil {
		t.Fatal(err)
	}
	su.Close(ctx)
	if _, err := e.svc.DB.ExecContext(ctx, `INSERT INTO library_credentials(id, library_id, kind, pg_role, person_label, password_enc, created_at) VALUES ('CZ', ?, 'member', 'half_maint__zed', 'zed', '', ?)`,
		l.ID, store.NowT()); err != nil {
		t.Fatal(err)
	}
	// Putting it in maintenance fails and leaves it active and usable.
	const kept = "SQS-L054 not every library role of half_maint could be set NOLOGIN, so the connections of its database were left as they were"
	if _, err := e.svc.SetMaintenance(ctx, admin, l.ID, true, false); err == nil || !strings.HasPrefix(err.Error(), kept) {
		t.Fatalf("maintenance with a role still able to log in: %v", err)
	}
	if got := e.lib(l.ID); got.State != core.StateActive {
		t.Fatalf("state %s", got.State)
	}
	if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
		t.Fatalf("library unusable after the failed maintenance: %v", err)
	}
	// Legacy maintenance (database closed) is not opened while a role keeps LOGIN.
	e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'maintenance' WHERE id = ?`, l.ID)
	if err := e.svc.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
		t.Fatal(err)
	}
	notes := e.svc.Reconcile(ctx, nil)
	if e.db(l.DBName).AllowConn {
		t.Fatalf("database opened in maintenance while a library role can still log in (notes %v)", notes)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "half_maint: could not apply its access: "+kept) {
		t.Fatalf("reconciliation did not report it: %v", notes)
	}
}

// A lock is never taken without its journal: if the journal cannot be written
// (a trigger refuses it here), the operation stops before locking and the
// library is unchanged.
func TestLockNeedsItsJournal(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("journal_lib")
	b := e.firstBackup(l)
	e.idle()
	rj, err := e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, true)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(rj, jobs.Succeeded)
	before := e.lib(l.ID)
	if _, err := e.svc.DB.ExecContext(ctx, `CREATE TRIGGER refuse_lock_journal BEFORE UPDATE ON job_steps WHEN NEW.data LIKE '%"phase":"lock"%'
		BEGIN SELECT RAISE(ABORT, 'journal refused'); END`); err != nil {
		t.Fatal(err)
	}
	usable := func(what string) {
		t.Helper()
		got := e.lib(l.ID)
		if got.State != core.StateActive || got.PrevDB != before.PrevDB || !e.canLogin(l.OwnerRole) || !e.db(l.DBName).AllowConn {
			t.Fatalf("%s changed the library without its journal: %+v", what, got)
		}
		if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
			t.Fatalf("%s: workstation cannot connect: %v", what, err)
		}
	}
	vj, err := e.svc.Revert(ctx, admin, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.failsWith(vj, "SQS-R057", "the job journal could not be written, so nothing was locked: "); !strings.Contains(got.ErrorDetail, "journal refused") {
		t.Fatalf("cause lost: %s", got.ErrorDetail)
	}
	usable("revert")
	rj, err = e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, true)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(rj, "SQS-R057", "the job journal could not be written, so nothing was locked")
	usable("replace")
	tj, err := e.svc.Trash(ctx, admin, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(tj, "SQS-L053", "the job journal could not be written, so nothing was locked")
	usable("delete")
}

// PLAN P0.2: a replacement whose step row or journal cannot be written stops
// before PostgreSQL changes, or puts the original back when the journal of
// the second rename is refused; the library stays usable and unchanged.
func TestReplaceNeedsItsStepsAndJournals(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("rjn_lib")
	b := e.firstBackup(l)
	e.idle()
	before := e.lib(l.ID)
	unchanged := func(what string) {
		t.Helper()
		e.idle()
		got := e.lib(l.ID)
		if got.State != core.StateActive || got.PrevDB != before.PrevDB || e.db(l.DBName) == nil || !e.db(l.DBName).AllowConn {
			t.Fatalf("%s changed the library: %+v", what, got)
		}
		if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
			t.Fatalf("%s: workstation cannot connect: %v", what, err)
		}
		dbs, err := e.svc.PG.Databases(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range dbs {
			if strings.HasPrefix(d.Name, l.Slug+"__") {
				t.Fatalf("%s left %s on the server", what, d.Name)
			}
		}
	}
	trigger := func(sql string) func() {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(ctx, sql); err != nil {
			t.Fatal(err)
		}
		return func() {
			if _, err := e.svc.DB.ExecContext(ctx, `DROP TRIGGER fault`); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The row of the restore step is refused: nothing is created.
	drop := trigger(`CREATE TRIGGER fault BEFORE INSERT ON job_steps WHEN NEW.name = 'restore' BEGIN SELECT RAISE(ABORT, 'step refused'); END`)
	j, err := e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, true)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(j, "SQS-J040", "the job step could not be recorded, so it was not run: ")
	unchanged("a refused step row")
	drop()
	// The journal of the copy being restored is refused: nothing is created.
	drop = trigger(`CREATE TRIGGER fault BEFORE UPDATE OF data ON job_steps WHEN NEW.data LIKE '%"phase":"restoring"%' BEGIN SELECT RAISE(ABORT, 'journal refused'); END`)
	j, _ = e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, true)
	e.failsWith(j, "SQS-R059", "the job journal could not be written, so the backup was not restored: ")
	unchanged("a refused restoring journal")
	drop()
	// The journal of the second rename is refused: the original is put back.
	drop = trigger(`CREATE TRIGGER fault BEFORE UPDATE OF data ON job_steps WHEN NEW.data LIKE '%"phase":"rename_restored"%' BEGIN SELECT RAISE(ABORT, 'journal refused'); END`)
	j, _ = e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, true)
	e.failsWith(j, "SQS-R059", "the job journal could not be written, so the backup was not restored: ")
	unchanged("a refused rename journal")
	drop()
	// With the journals back the same replacement works.
	j, _ = e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, true)
	e.wait(j, jobs.Succeeded)
}

// PLAN P0.3: adding a person needs its audit entry: without it no role is
// created (SQS-X040); when only the result entry fails, the person is added
// and the answer is SQS-X041.
func TestMemberAddNeedsItsAuditEntry(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("mem_audit")
	e.idle()
	exec := func(q string) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	role := l.Slug + "__ana"
	exec(`CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	if _, err := e.svc.AddMember(ctx, admin, l.ID, "ana"); !errors.Is(err, audit.ErrUnavailable) {
		t.Fatalf("without the entry: %v", err)
	}
	if ok, _ := e.svc.PG.RoleExists(ctx, role); ok {
		t.Fatal("the role was created without its entry")
	}
	exec(`DROP TRIGGER audit_down`)
	exec(`CREATE TRIGGER result_down BEFORE INSERT ON audit_log WHEN NEW.outcome = 'ok' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	c, err := e.svc.AddMember(ctx, admin, l.ID, "ana")
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-X041 ") || c == nil {
		t.Fatalf("with a failed result entry: %v %v", c, err)
	}
	if ok, _ := e.svc.PG.RoleExists(ctx, role); !ok {
		t.Fatal("the role is missing")
	}
}

// trashed creates an active library with a backup and moves it to the trash.
func (e *env) trashed(slug string) *core.Library {
	e.t.Helper()
	l, _ := e.createActive(slug)
	e.firstBackup(l)
	e.idle()
	tj, err := e.svc.Trash(e.ctx, admin, l.ID, l.Slug)
	if err != nil {
		e.t.Fatal(err)
	}
	e.wait(tj, jobs.Succeeded)
	return e.lib(l.ID)
}

// PLAN P0.4: a purge whose catalog writes fail after the database or the
// roles were dropped fails, stays pending reconciliation, and the next start
// finishes it: the library is purged, its roles are gone and its backups are
// kept. A lookup that fails stops the purge before the roles are dropped.
func TestPurgeFaultsAreNeverSuccess(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	exec := func(q string) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	finished := func(l *core.Library, notes string) {
		t.Helper()
		got := e.lib(l.ID)
		if got.State != core.StatePurged || got.TrashDB != "" {
			t.Fatalf("%s after the next start: %s %q\n%s", l.Slug, got.State, got.TrashDB, notes)
		}
		if ok, _ := e.svc.PG.RoleExists(ctx, l.OwnerRole); ok {
			t.Fatalf("%s: the role survived", l.Slug)
		}
		if bs, _ := e.svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Statuses: []string{core.BackupOK}}); len(bs) == 0 {
			t.Fatalf("%s: the backups were lost", l.Slug)
		}
		var n int
		e.svc.DB.QueryRowContext(ctx, `SELECT count(*) FROM schedules WHERE target_id = ?`, l.ID).Scan(&n)
		if n != 0 {
			t.Fatalf("%s: %d schedules left", l.Slug, n)
		}
	}
	faults := []struct {
		name, trigger, code string
	}{
		{"credentials", `CREATE TRIGGER fault BEFORE UPDATE OF revoked_at ON library_credentials BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`, "SQS-L047"},
		{"state", `CREATE TRIGGER fault BEFORE UPDATE OF state ON libraries WHEN NEW.state = 'purged' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`, "SQS-L047"},
		{"schedules", `CREATE TRIGGER fault BEFORE DELETE ON schedules BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`, "SQS-L047"},
	}
	for i, f := range faults {
		l := e.trashed(fmt.Sprintf("pf_lib%d", i))
		exec(f.trigger)
		j, err := e.svc.Purge(ctx, admin, l.ID, l.Slug)
		if err != nil {
			t.Fatal(err)
		}
		got := e.failsWith(j, f.code, "reconciliation finishes it at the next start of sqsolve")
		if e.pendingFlag(got.ID) != 1 {
			t.Fatalf("%s: the failed purge is not pending reconciliation", f.name)
		}
		exec(`DROP TRIGGER fault`)
		notes := e.start()
		finished(l, notes)
		if e.pendingFlag(got.ID) != 0 {
			t.Fatalf("%s: still pending after the start\n%s", f.name, notes)
		}
	}
	// A role lookup that fails stops the purge before any role is dropped.
	l := e.trashed("pf_lookup")
	restore := core.SetFault("purge.role_lookup", errors.New("lookup refused"))
	j, _ := e.svc.Purge(ctx, admin, l.ID, l.Slug)
	e.failsWith(j, "SQS-L047", "lookup refused")
	if ok, _ := e.svc.PG.RoleExists(ctx, l.OwnerRole); !ok {
		t.Fatal("the role was dropped although the lookup failed")
	}
	restore()
	restore = core.SetFault("purge.owned_lookup", errors.New("lookup refused"))
	j, _ = e.svc.Purge(ctx, admin, l.ID, l.Slug)
	e.failsWith(j, "SQS-L047", "lookup refused")
	if ok, _ := e.svc.PG.RoleExists(ctx, l.OwnerRole); !ok {
		t.Fatal("the role was dropped although the lookup of its databases failed")
	}
	restore()
	// The purge can run again and finishes.
	j, _ = e.svc.Purge(ctx, admin, l.ID, l.Slug)
	e.wait(j, jobs.Succeeded)
	finished(l, "")
}

// PLAN P0.4: a recreate whose activation fails after the database was
// restored fails, stays pending reconciliation, and the next start adopts
// the restored database; a grant that fails is not ignored.
func TestRecreateFaultsAreNeverSuccess(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	exec := func(q string) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	l, pass := e.createActive("rf_lib")
	e.firstBackup(l)
	e.lose(l, true)
	exec(`CREATE TRIGGER fault BEFORE UPDATE OF state ON libraries WHEN NEW.state = 'active' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	j, err := e.svc.Recreate(ctx, admin, l.ID, "", l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	got := e.failsWith(j, "SQS-R060", "reconciliation finishes it at the next start of sqsolve")
	if e.lib(l.ID).State == core.StateActive || e.pendingFlag(got.ID) != 1 {
		t.Fatalf("state %s, pending %d", e.lib(l.ID).State, e.pendingFlag(got.ID))
	}
	exec(`DROP TRIGGER fault`)
	notes := e.start()
	if e.lib(l.ID).State != core.StateActive {
		t.Fatalf("not adopted at the next start: %s\n%s", e.lib(l.ID).State, notes)
	}
	if n := e.count(l.OwnerRole, pass, l.DBName, "SM_Project"); n != 50 {
		t.Fatalf("%d rows", n)
	}
	// A member whose grant fails: the recreate fails instead of leaving the
	// person without access.
	m, _ := e.createActive("rg_lib")
	if _, err := e.svc.AddMember(ctx, admin, m.ID, "ana"); err != nil {
		t.Fatal(err)
	}
	e.firstBackup(m)
	e.lose(m, true)
	e.svc.PG.DropRole(ctx, m.Slug+"__ana")
	restore := core.SetFault("recreate.grant", errors.New("grant refused"))
	j, _ = e.svc.Recreate(ctx, admin, m.ID, "", m.Slug)
	e.failsWith(j, "SQS-R061", "grant refused")
	if e.lib(m.ID).State == core.StateActive {
		t.Fatal("recreated without the member's grant")
	}
	restore()
	restore = core.SetFault("recreate.role_lookup", errors.New("lookup refused"))
	j, _ = e.svc.Recreate(ctx, admin, m.ID, "", m.Slug)
	e.failsWith(j, "SQS-R061", "lookup refused")
	restore()
}

// PLAN P0.3: creating a library, or restoring a backup as a new one, with
// the audit log down answers SQS-X040 and leaves nothing behind, so the same
// request works once the log is back (it got SQS-L004 for a leftover
// provisioning row).
func TestNewLibraryWithoutItsAuditEntryLeavesNothing(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	src, _ := e.createActive("src_lib")
	b := e.firstBackup(src)
	e.idle()
	exec := func(q string) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	if _, _, err := e.svc.CreateLibrary(ctx, admin, core.CreateInput{DisplayName: "Wedding", Slug: "wedding"}); !errors.Is(err, audit.ErrUnavailable) {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := e.svc.RestoreAsNew(ctx, admin, b.ID, "copy_lib", "Copy"); !errors.Is(err, audit.ErrUnavailable) {
		t.Fatalf("restore as new: %v", err)
	}
	var n int
	e.svc.DB.QueryRowContext(ctx, `SELECT count(*) FROM libraries WHERE slug IN ('wedding', 'copy_lib')`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d library rows left without their job", n)
	}
	e.svc.DB.QueryRowContext(ctx, `SELECT count(*) FROM backup_policies WHERE library_id NOT IN (SELECT id FROM libraries)`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d policies left without their library", n)
	}
	exec(`DROP TRIGGER audit_down`)
	if _, _, err := e.svc.CreateLibrary(ctx, admin, core.CreateInput{DisplayName: "Wedding", Slug: "wedding"}); err != nil {
		t.Fatalf("create again: %v", err)
	}
	if _, _, err := e.svc.RestoreAsNew(ctx, admin, b.ID, "copy_lib", "Copy"); err != nil {
		t.Fatalf("restore as new again: %v", err)
	}
}
