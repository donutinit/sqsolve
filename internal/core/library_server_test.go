// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

// A session ended from the panel must belong to the library: the pid of
// another library's workstation is refused (SQS-L016) and that session goes
// on; the library's own is ended, with its audit entry, and not without it.
func TestTerminateSessionEndsOnlyTheLibrarysOwn(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("ses_one")
	m, mpass := e.createActive("ses_two")
	e.idle()
	mine := e.connect(l.OwnerRole, pass, l.DBName)
	defer mine.Close(ctx)
	theirs := e.connect(m.OwnerRole, mpass, m.DBName)
	defer theirs.Close(ctx)
	ss, err := e.svc.Sessions(ctx, l.ID)
	if err != nil || len(ss) != 1 || ss[0].User != l.OwnerRole {
		t.Fatalf("sessions of the library: %+v %v", ss, err)
	}
	err = e.svc.TerminateSession(ctx, admin, l.ID, int32(theirs.PgConn().PID()))
	if err == nil || err.Error() != "SQS-L016 the session no longer exists or does not belong to this library" {
		t.Fatalf("terminating another library's session: %v", err)
	}
	if err := theirs.Ping(ctx); err != nil {
		t.Fatalf("the other library's session was ended: %v", err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	pid := int32(mine.PgConn().PID())
	if err := e.svc.TerminateSession(ctx, admin, l.ID, pid); err == nil || !strings.HasPrefix(err.Error(), "SQS-X040 ") {
		t.Fatalf("terminating without the audit entry: %v", err)
	}
	if err := mine.Ping(ctx); err != nil {
		t.Fatalf("a session was ended without its audit entry: %v", err)
	}
	e.svc.DB.ExecContext(ctx, `DROP TRIGGER audit_down`)
	if err := e.svc.TerminateSession(ctx, admin, l.ID, pid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for mine.Ping(ctx) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the session survived")
		}
		time.Sleep(50 * time.Millisecond)
	}
	entries, _ := e.svc.Audit.List(ctx, 0, 20, l.ID)
	found := false
	for _, x := range entries {
		found = found || x.Action == "library.session_terminate" && x.Outcome == audit.OK
	}
	if !found {
		t.Fatalf("no audit entry for the termination: %+v", entries)
	}
	// A library in the trash has no sessions to show.
	e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'trashed' WHERE id = ?`, m.ID)
	if ss, err := e.svc.Sessions(ctx, m.ID); err != nil || ss != nil {
		t.Fatalf("sessions of a trashed library: %+v %v", ss, err)
	}
}

// During the initialization window, a database Resolve created under another
// name is reported once (the name typed in Resolve must be the identifier),
// and the library keeps waiting for the right one.
func TestMisnamedDatabaseIsReportedOnce(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, j, err := e.svc.CreateLibrary(ctx, admin, core.CreateInput{DisplayName: "Typo", Slug: "typo_lib"})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	pass, err := e.svc.RevealPassword(ctx, admin, l.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	conn := e.connect(l.OwnerRole, pass, "postgres")
	if _, err := conn.Exec(ctx, "CREATE DATABASE typo_lib_2026"); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	for i := 0; i < 3; i++ {
		e.svc.WatchInit(ctx)
	}
	if got := e.svc.MisnamedDatabases(ctx, e.lib(l.ID)); len(got) != 1 || got[0] != "typo_lib_2026" {
		t.Fatalf("misnamed databases %v", got)
	}
	evs := e.eventsOf(events.LibraryExtraDatabase, l.ID)
	if len(evs) != 1 || evs[0].Severity != events.Warning ||
		evs[0].Message != `Resolve created the database "typo_lib_2026", but the library must be named exactly "typo_lib". Create it again in Resolve with that name` {
		t.Fatalf("events %+v", evs)
	}
	if got := e.lib(l.ID); got.State != core.StateAwaitingInit {
		t.Fatalf("state %s", got.State)
	}
}

// A change of a live library's structure (a workstation with another Resolve
// version upgraded it) is reported once and the new fingerprint recorded.
func TestFingerprintAllReportsASchemaChange(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("fp_lib")
	e.idle()
	before := e.lib(l.ID).Fingerprint
	e.svc.FingerprintAll(ctx)
	if n := len(e.eventsOf(events.LibrarySchemaChanged, l.ID)); n != 0 {
		t.Fatalf("%d schema events without a change", n)
	}
	conn := e.connect(l.OwnerRole, pass, l.DBName)
	if _, err := conn.Exec(ctx, `ALTER TABLE "SM_Project" ADD COLUMN "Upgraded" int`); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	e.svc.FingerprintAll(ctx)
	e.svc.FingerprintAll(ctx)
	evs := e.eventsOf(events.LibrarySchemaChanged, l.ID)
	if len(evs) != 1 || evs[0].Severity != events.Warning ||
		evs[0].Message != `The structure of "Test fp_lib" changed (2 tables). A workstation with another Resolve version probably upgraded it` {
		t.Fatalf("schema events %+v", evs)
	}
	if after := e.lib(l.ID).Fingerprint; after == before || !strings.HasPrefix(after, "sha256:") {
		t.Fatalf("fingerprint %q, before %q", after, before)
	}
}

// With the precreate strategy sqsolve creates the empty database itself: the
// library is active at once, its database belongs to its role, PUBLIC cannot
// connect, the role cannot create databases and the backup policy is set. An
// unknown strategy is refused.
func TestPrecreatedLibrary(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	if _, _, err := e.svc.CreateLibrary(ctx, admin, core.CreateInput{DisplayName: "X", Slug: "strat_x", Strategy: "clone"}); err == nil || err.Error() != "SQS-L007 unknown creation strategy" {
		t.Fatalf("unknown strategy: %v", err)
	}
	l, j, err := e.svc.CreateLibrary(ctx, admin, core.CreateInput{DisplayName: "Early", Slug: "early_lib", Strategy: core.StrategyPrecreate})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	if got := e.lib(l.ID); got.State != core.StateActive {
		t.Fatalf("state %s", got.State)
	}
	if d := e.db("early_lib"); d == nil || d.Owner != "early_lib" {
		t.Fatalf("database %+v", d)
	}
	p, _ := e.svc.PG.Pool()
	var createdb, public bool
	p.QueryRow(ctx, `SELECT rolcreatedb FROM pg_roles WHERE rolname = 'early_lib'`).Scan(&createdb)
	p.QueryRow(ctx, `SELECT has_database_privilege('public', 'early_lib', 'CONNECT')`).Scan(&public)
	if createdb || public {
		t.Fatalf("CREATEDB %v, PUBLIC can connect %v", createdb, public)
	}
	if sc, err := e.svc.Sched.Get(ctx, core.ScheduleBackup, l.ID); err != nil || sc == nil || !sc.Enabled {
		t.Fatalf("backup schedule %+v %v", sc, err)
	}
	pass, _ := e.svc.RevealPassword(ctx, admin, l.ID, "")
	if err := e.reach("early_lib", pass, "early_lib"); err != nil {
		t.Fatalf("the workstation cannot connect: %v", err)
	}
}

// A create job re-checks the server when it runs: a database or a role with
// the library's name that appeared after the request fails it with its code,
// the library ends purged with the cause in its notes, and only what the run
// created is removed. A job for a library that is no longer being prepared
// touches nothing.
func TestLibraryCreateRechecksTheServer(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	if err := e.svc.PG.CreateDatabase(ctx, "taken_db", pg.AdminRole); err != nil {
		t.Fatal(err)
	}
	v, _ := pg.ScramVerifier(secrets.GeneratePassword(24))
	if err := e.svc.PG.CreateLibraryRole(ctx, "taken_role", pg.RoleOptions{Verifier: v, Login: true, ConnLimit: -1}); err != nil {
		t.Fatal(err)
	}
	now := store.NowT()
	for _, row := range [][2]string{{"LDB", "taken_db"}, {"LROLE", "taken_role"}} {
		if _, err := e.svc.DB.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, notes, created_at, updated_at)
			VALUES (?, ?, ?, 'provisioning', ?, ?, 'resolve', '', ?, ?)`, row[0], row[1], row[1], row[1], row[1], now, now); err != nil {
			t.Fatal(err)
		}
	}
	j, _ := e.svc.Jobs.Enqueue(ctx, core.JobLibraryCreate, "LDB", nil, admin)
	e.failsWith(j, "SQS-L005", `a database "taken_db" already exists on the server`)
	j, _ = e.svc.Jobs.Enqueue(ctx, core.JobLibraryCreate, "LROLE", nil, admin)
	e.failsWith(j, "SQS-L006", `a role "taken_role" already exists on the server`)
	for id, code := range map[string]string{"LDB": "SQS-L005", "LROLE": "SQS-L006"} {
		if got := e.lib(id); got.State != core.StatePurged || !strings.Contains(got.Notes, "[creation failed] "+code) {
			t.Fatalf("%s after the failed create: %s %q", id, got.State, got.Notes)
		}
	}
	if e.db("taken_db") == nil {
		t.Fatal("the existing database was dropped")
	}
	if ok, _ := e.svc.PG.RoleExists(ctx, "taken_role"); !ok {
		t.Fatal("the existing role was dropped")
	}
	l, _ := e.createActive("done_lib")
	e.idle()
	j, _ = e.svc.Jobs.Enqueue(ctx, core.JobLibraryCreate, l.ID, nil, admin)
	e.failsWith(j, "SQS-L010", "the library is not being prepared (Active)")
	if got := e.lib(l.ID); got.State != core.StateActive || e.db(l.DBName) == nil {
		t.Fatalf("a stale create touched an active library: %s", got.State)
	}
}
