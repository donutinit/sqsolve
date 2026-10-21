// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/store"
)

// TestSyncHBARetriesAfterFailedReload: a network rule whose validation or
// reload failed is applied by the next sync, even though the file on disk is
// already the right one, in the same process and after a restart.
func TestSyncHBARetriesAfterFailedReload(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("delta")
	e.firstBackup(l)
	rejected := func() bool {
		conn, err := e.c.ConnectTCP(ctx, l.OwnerRole, pass, l.DBName)
		if err == nil {
			conn.Close(ctx)
		}
		return err != nil
	}

	// Validation and reload cannot reach PostgreSQL: the file is written but
	// not in force, and the change is not reported as applied.
	e.svc.PG.Close()
	if err := e.svc.SetNetworkRules(ctx, admin, l.ID, []string{"10.0.0.0/8"}); err == nil {
		t.Fatal("rules reported as applied without PostgreSQL")
	}
	if err := e.svc.PG.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if rejected() {
		t.Fatal("precondition: the rule must not be in force yet")
	}
	// After a restart (a new service, as at startup) the sync loads it.
	fresh := &core.Service{Cfg: e.svc.Cfg, DB: e.svc.DB, PG: e.svc.PG, Box: e.svc.Box, Log: e.svc.Log}
	if err := fresh.SyncHBA(ctx); err != nil {
		t.Fatal(err)
	}
	if !rejected() {
		t.Fatal("127.0.0.1 accepted with only 10.0.0.0/8 allowed after a restart")
	}

	// In the same process: the next identical change retries the reload.
	e.svc.PG.Close()
	if err := e.svc.SetNetworkRules(ctx, admin, l.ID, nil); err == nil {
		t.Fatal("rules reported as applied without PostgreSQL")
	}
	if err := e.svc.PG.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetNetworkRules(ctx, admin, l.ID, nil); err != nil {
		t.Fatal(err)
	}
	if rejected() {
		t.Fatal("removing the rule did not reach PostgreSQL")
	}
}

const failCredentialUpdate = `CREATE TRIGGER fail_update BEFORE UPDATE ON library_credentials BEGIN SELECT RAISE(ABORT, 'injected failure'); END`

// TestRotatePasswordStoreFailure: when the new password cannot be saved, the
// previous one is put back in PostgreSQL, so the panel never shows a password
// that does not work.
func TestRotatePasswordStoreFailure(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("rot")
	e.firstBackup(l)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}

	exec(failCredentialUpdate)
	err := e.svc.RotatePassword(ctx, admin, l.ID, "")
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-L070 the new password of "+l.OwnerRole+" could not be saved; the previous password is still in force: ") ||
		!strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("rotation with SQLite failing: %v", err)
	}
	shown, err := e.svc.RevealPassword(ctx, admin, l.ID, "")
	if err != nil || shown.Reveal() != pass.Reveal() {
		t.Fatalf("the panel shows another password: %v", err)
	}
	e.connect(l.OwnerRole, pass, l.DBName).Close(ctx)

	// When the previous password cannot be put back either, the error says so.
	exec(`DROP TRIGGER fail_update`)
	exec(`UPDATE library_credentials SET password_enc = 'unreadable' WHERE pg_role = ?`, l.OwnerRole)
	exec(failCredentialUpdate)
	err = e.svc.RotatePassword(ctx, admin, l.ID, "")
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-L071 the new password of "+l.OwnerRole+" could not be saved and the previous one could not be put back") ||
		!strings.Contains(err.Error(), "rotate the password again") || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("rotation without a way back: %v", err)
	}

	// Rotating again once SQLite works repairs the credential.
	exec(`DROP TRIGGER fail_update`)
	if err := e.svc.RotatePassword(ctx, admin, l.ID, ""); err != nil {
		t.Fatal(err)
	}
	np, err := e.svc.RevealPassword(ctx, admin, l.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	e.connect(l.OwnerRole, np, l.DBName).Close(ctx)
}

// TestAddMemberRollsBackOnFailure: a failure after the role was created
// removes it, so adding the person again works (SQS-L033 used to block it).
func TestAddMemberRollsBackOnFailure(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("team")
	e.firstBackup(l)
	if _, err := e.svc.DB.ExecContext(ctx, `CREATE TRIGGER fail_insert BEFORE INSERT ON library_credentials BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	role := pg.MemberRole(l.Slug, "ana")
	_, err := e.svc.AddMember(ctx, admin, l.ID, "ana")
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-L072 could not add "+role+": ") ||
		!strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("add with SQLite failing: %v", err)
	}
	if ok, err := e.svc.PG.RoleExists(ctx, role); err != nil || ok {
		t.Fatalf("role left behind: %v %v", ok, err)
	}
	if creds, _ := e.svc.Credentials(ctx, l.ID); len(creds) != 1 {
		t.Fatalf("credentials: %d", len(creds))
	}
	if _, err := e.svc.DB.ExecContext(ctx, `DROP TRIGGER fail_insert`); err != nil {
		t.Fatal(err)
	}
	c, err := e.svc.AddMember(ctx, admin, l.ID, "ana")
	if err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	mp, err := e.svc.RevealPassword(ctx, admin, l.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.connect(c.PGRole, mp, l.DBName).Close(ctx)
}

// TestReconcileWithoutPostgreSQL: when the existence of a database cannot be
// checked, reconciliation leaves the library as it is instead of treating the
// error as "does not exist".
func TestReconcileWithoutPostgreSQL(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	prov, _ := e.createActive("prov")
	e.firstBackup(prov)
	swp, _ := e.createActive("swp")
	e.firstBackup(swp)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// A creation and a swap interrupted by a crash.
	exec(`UPDATE libraries SET state = 'provisioning' WHERE id = ?`, prov.ID)
	exec(`INSERT INTO jobs(id, type, library_id, state, scheduled_for, created_at, created_by_type) VALUES ('J1', 'restore.replace', ?, 'running', ?, ?, 'user')`, swp.ID, store.NowT(), store.NowT())
	exec(`INSERT INTO job_steps(job_id, seq, name, state, started_at, data) VALUES ('J1', 1, 'swap', 'running', ?, ?)`, store.NowT(),
		`{"live":"`+swp.DBName+`","prev":"`+pg.PrevDB(swp.Slug, time.Now())+`","restored":"x","phase":"rename_restored","state_before":"active"}`)
	interrupted, err := e.svc.Jobs.MarkInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}

	e.svc.PG.Close()
	notes := e.svc.Reconcile(ctx, interrupted)
	if err := e.svc.PG.Open(ctx); err != nil {
		t.Fatal(err)
	}
	l, _ := e.svc.Library(ctx, prov.ID)
	if l.State != core.StateProvisioning {
		t.Fatalf("library %s while its database could not be checked; notes %v", l.State, notes)
	}
	if ok, err := e.svc.PG.RoleExists(ctx, prov.OwnerRole); err != nil || !ok {
		t.Fatalf("owner role dropped: %v %v", ok, err)
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, "swp: could not check the databases of the swap") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes: %v", notes)
	}
}

// TestInvalidDatabases: a database an interrupted DROP left invalid still
// holds its name and its disk space. Reconciliation drops an invalid
// auxiliary database, and a slug held by one is not available (SQS-L005).
func TestInvalidDatabases(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	su, err := e.c.Connect(ctx, "postgres", e.c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)
	for _, q := range []string{
		`CREATE DATABASE sqsolve_verify_x OWNER sqsolve_admin`,
		`CREATE DATABASE held_slug OWNER sqsolve_admin`,
		`UPDATE pg_database SET datconnlimit = -2 WHERE datname IN ('sqsolve_verify_x', 'held_slug')`,
	} {
		if _, err := su.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for name, check := range map[string]func(context.Context, string) error{
		"SlugAvailable": e.svc.SlugAvailable, "SlugAvailableIgnoringCatalog": e.svc.SlugAvailableIgnoringCatalog,
	} {
		if err := check(ctx, "held_slug"); err == nil || !strings.HasPrefix(err.Error(), `SQS-L005 a database "held_slug" already exists on the server`) {
			t.Errorf("%s with an invalid database of that name: %v", name, err)
		}
	}
	notes := e.svc.Reconcile(ctx, nil)
	if !strings.Contains(strings.Join(notes, "\n"), "deleted auxiliary database sqsolve_verify_x") {
		t.Errorf("notes: %v", notes)
	}
	var n int
	if err := su.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname = 'sqsolve_verify_x'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("invalid auxiliary database still there: %d %v", n, err)
	}
	// A database that is not sqsolve's is never dropped, even if invalid.
	if err := su.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname = 'held_slug'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("held_slug: %d %v", n, err)
	}
}
