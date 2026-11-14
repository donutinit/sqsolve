// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
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

// The triggers that make the audit log refuse the entries of the rewrite of
// pg_hba.conf: all of them, or only its result "ok".
const (
	hbaAuditDown  = `CREATE TRIGGER hba_audit_down BEFORE INSERT ON audit_log WHEN NEW.action = 'postgres.hba' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`
	hbaResultDown = `CREATE TRIGGER hba_result_down BEFORE INSERT ON audit_log WHEN NEW.action = 'postgres.hba' AND NEW.outcome = 'ok' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`
)

// reloaded waits up to 5 s for a connection of role to db over TCP to be
// accepted (want true) or refused (want false): PostgreSQL applies a reload
// asynchronously.
func (e *env) reloaded(role string, pass secrets.String, db string, want bool) {
	e.t.Helper()
	var err error
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if err = e.reach(role, pass, db); (err == nil) == want || time.Now().After(deadline) {
			break
		}
	}
	if (err == nil) != want {
		e.t.Fatalf("%s on %s: connection accepted %v, want %v: %v", role, db, err == nil, want, err)
	}
}

// auditOf returns the entries of action about targetID, oldest first, that
// pass keep (all of them when keep is nil), and their outcomes separated by
// spaces.
func (e *env) auditOf(targetID, action string, keep func(audit.Entry) bool) ([]audit.Entry, string) {
	e.t.Helper()
	list, err := e.svc.Audit.List(e.ctx, 0, 200, targetID)
	if err != nil {
		e.t.Fatal(err)
	}
	var es []audit.Entry
	var outcomes []string
	for i := len(list) - 1; i >= 0; i-- {
		if en := list[i]; en.Action == action && (keep == nil || keep(en)) {
			es = append(es, en)
			outcomes = append(outcomes, en.Outcome)
		}
	}
	return es, strings.Join(outcomes, " ")
}

// The rewrite of pg_hba.conf that follows a change of the
// access to the maintenance database has its own pair of entries,
// postgres.hba, after the one of the settings; when only its result cannot
// be written the answer is SQS-X041 and the rewrite is in force.
func TestAccessSettingAuditsItsHBARewrite(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("acc")
	e.firstBackup(l)
	exec := func(q string) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.reach(l.OwnerRole, pass, "postgres"); err != nil {
		t.Fatalf("precondition: the owner role reaches the postgres database: %v", err)
	}

	st := e.svc.Settings(ctx)
	st.MaintenanceDBAccess = false
	if err := e.svc.SaveSettings(ctx, admin, st); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.Audit.List(ctx, 0, 200, "runtime")
	if err != nil {
		t.Fatal(err)
	}
	var trail []string
	for i := len(list) - 1; i >= 0; i-- {
		trail = append(trail, list[i].Action+":"+list[i].Outcome)
	}
	if got := strings.Join(trail, " "); !strings.HasSuffix(got, "settings.update:ok postgres.hba:accepted postgres.hba:ok") {
		t.Fatalf("entries of the settings: %s", got)
	}
	hba, _ := e.auditOf("runtime", "postgres.hba", nil)
	for _, en := range hba {
		if en.Actor.Name != "diego" || en.TargetType != "settings" || en.Details["cause"] != "settings.update" ||
			en.Details["maintenance_db_access"] != false || en.Details["require_tls"] != false {
			t.Errorf("postgres.hba %s: %+v", en.Outcome, en)
		}
	}
	e.reloaded(l.OwnerRole, pass, "postgres", false)
	if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
		t.Fatalf("the owner role lost its own database: %v", err)
	}

	exec(hbaResultDown)
	st.MaintenanceDBAccess = true
	err = e.svc.SaveSettings(ctx, admin, st)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-X041 the action was done, but its result could not be written to the audit log: ") {
		t.Fatalf("rewrite whose result cannot be written: %v", err)
	}
	e.reloaded(l.OwnerRole, pass, "postgres", true)
	if _, got := e.auditOf("runtime", "postgres.hba", nil); got != "accepted ok accepted" {
		t.Fatalf("postgres.hba entries: %s", got)
	}
	exec(`DROP TRIGGER hba_result_down`)
}

// A change of the access to the maintenance database whose rewrite of
// pg_hba.conf failed is put in force by saving the same settings again; once
// it took effect, saving them again rewrites nothing.
func TestFailedHBARewriteIsAppliedBySavingAgain(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("hbaretry")
	if err := e.reach(l.OwnerRole, pass, "postgres"); err != nil {
		t.Fatalf("precondition: the owner role reaches the postgres database: %v", err)
	}
	// pg_hba.conf.prev is written first: the rewrite fails before the file
	// changes (SQS-P041).
	blocker := filepath.Join(e.svc.Cfg.PGConfDir(), "pg_hba.conf.prev.partial")
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	st := e.svc.Settings(ctx)
	st.MaintenanceDBAccess = false
	err := e.svc.SaveSettings(ctx, admin, st)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-P041 could not write pg_hba.conf: ") {
		t.Fatalf("rewrite that cannot write the file: %v", err)
	}
	if p := e.svc.HBAProblem(); p == nil || p.Error() != err.Error() {
		t.Errorf("problem after the failed rewrite: %v", p)
	}
	if err := e.reach(l.OwnerRole, pass, "postgres"); err != nil {
		t.Fatalf("the postgres database closed without a rewrite: %v", err)
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SaveSettings(ctx, admin, st); err != nil {
		t.Fatalf("same settings saved again: %v", err)
	}
	e.reloaded(l.OwnerRole, pass, "postgres", false)
	if err := e.reach(l.OwnerRole, pass, l.DBName); err != nil {
		t.Fatalf("the owner role lost its own database: %v", err)
	}
	if p := e.svc.HBAProblem(); p != nil {
		t.Errorf("problem after a rewrite that took effect: %v", p)
	}

	if err := e.svc.SaveSettings(ctx, admin, st); err != nil {
		t.Fatal(err)
	}
	if _, got := e.auditOf("runtime", "postgres.hba", nil); got != "accepted failed accepted ok" {
		t.Fatalf("postgres.hba entries: %s", got)
	}
}

// The rewrite of pg_hba.conf that lets a new person in has
// its own pair of entries, postgres.hba, after the one of the addition. A
// rewrite that fails or whose entry is refused leaves the person added but
// unable to connect until the next rewrite; without its accepted entry the
// file is not touched, also when the addition itself could not be closed
// (SQS-X041 wins).
func TestMemberAddAuditsItsHBARewrite(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("crew")
	e.firstBackup(l)
	exec := func(q string) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	hbaPath := filepath.Join(e.svc.Cfg.PGConfDir(), "pg_hba.conf")
	readHBA := func() []byte {
		t.Helper()
		b, err := os.ReadFile(hbaPath)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ofRole := func(role string) func(audit.Entry) bool {
		return func(en audit.Entry) bool { return en.Details["role"] == role }
	}
	hbaOf := func(role string) ([]audit.Entry, string) { return e.auditOf(l.ID, "postgres.hba", ofRole(role)) }
	password := func(c *core.Credential) secrets.String {
		t.Helper()
		p, err := e.svc.RevealPassword(ctx, admin, l.ID, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	// added checks that the person's role exists and returns its password.
	added := func(c *core.Credential, err error) secrets.String {
		t.Helper()
		if c == nil {
			t.Fatalf("no credential: %v", err)
		}
		if ok, err := e.svc.PG.RoleExists(ctx, c.PGRole); err != nil || !ok {
			t.Fatalf("role %s: %v %v", c.PGRole, ok, err)
		}
		return password(c)
	}
	refused := func(c *core.Credential, pass secrets.String) {
		t.Helper()
		if err := e.reach(c.PGRole, pass, l.DBName); err == nil {
			t.Fatalf("%s connects without a rewrite of pg_hba.conf", c.PGRole)
		}
	}

	// Phase 1: the rewrite and its entries.
	ana, err := e.svc.AddMember(ctx, admin, l.ID, "ana")
	if err != nil {
		t.Fatal(err)
	}
	hba, got := hbaOf(ana.PGRole)
	if got != "accepted ok" {
		t.Fatalf("postgres.hba entries of ana: %q", got)
	}
	memberAdd, _ := e.auditOf(l.ID, "library.member_add", ofRole(ana.PGRole))
	if len(memberAdd) != 2 || memberAdd[1].Outcome != audit.OK {
		t.Fatalf("library.member_add of ana: %+v", memberAdd)
	}
	for _, en := range hba {
		if en.Actor.Name != "diego" || en.TargetType != "library" || en.TargetID != l.ID || en.Details["cause"] != "library.member_add" ||
			en.Details["role"] != ana.PGRole || en.Seq <= memberAdd[1].Seq {
			t.Errorf("postgres.hba %s: %+v", en.Outcome, en)
		}
	}
	e.reloaded(ana.PGRole, password(ana), l.DBName, true)

	// Phase 2: the rewrite fails before pg_hba.conf is touched (SQS-P041).
	blocker := filepath.Join(e.svc.Cfg.PGConfDir(), "pg_hba.conf.prev.partial")
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	bea, err := e.svc.AddMember(ctx, admin, l.ID, "bea")
	beaPass := added(bea, err)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-P041 could not write pg_hba.conf: ") {
		t.Fatalf("rewrite that cannot write the file: %v", err)
	}
	if hba, got := hbaOf(bea.PGRole); got != "accepted failed" || hba[1].Details["error"] != err.Error() {
		t.Fatalf("postgres.hba entries of bea: %q %+v", got, hba)
	}
	if _, got := e.auditOf(l.ID, "library.member_add", ofRole(bea.PGRole)); got != "accepted ok" {
		t.Fatalf("library.member_add of bea: %q", got)
	}
	refused(bea, beaPass)
	if p := e.svc.HBAProblem(); p == nil || p.Error() != err.Error() {
		t.Errorf("problem after the failed rewrite: %v", p)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SyncHBA(ctx); err != nil {
		t.Fatal(err)
	}
	e.reloaded(bea.PGRole, beaPass, l.DBName, true)
	if p := e.svc.HBAProblem(); p != nil {
		t.Errorf("problem after a rewrite that took effect: %v", p)
	}

	// Phase 3: the entry of the rewrite is refused (SQS-X040).
	exec(hbaAuditDown)
	before := readHBA()
	cai, err := e.svc.AddMember(ctx, admin, l.ID, "cai")
	caiPass := added(cai, err)
	if !errors.Is(err, audit.ErrUnavailable) || !strings.HasPrefix(err.Error(), "SQS-X040 the audit log could not be written, so the action was not done: ") {
		t.Fatalf("rewrite without its entry: %v", err)
	}
	if !bytes.Equal(readHBA(), before) {
		t.Fatal("pg_hba.conf rewritten without its entry")
	}
	if hba, _ := hbaOf(cai.PGRole); len(hba) != 0 {
		t.Fatalf("postgres.hba entries of cai: %+v", hba)
	}
	refused(cai, caiPass)
	if p := e.svc.HBAProblem(); !errors.Is(p, audit.ErrUnavailable) || p.Error() != err.Error() {
		t.Errorf("problem after the refused rewrite: %v", p)
	}
	exec(`DROP TRIGGER hba_audit_down`)
	if err := e.svc.SyncHBA(ctx); err != nil {
		t.Fatal(err)
	}
	e.reloaded(cai.PGRole, caiPass, l.DBName, true)
	if p := e.svc.HBAProblem(); p != nil {
		t.Errorf("problem after a rewrite that took effect: %v", p)
	}

	// Phase 4: only the result of the rewrite is refused (SQS-X041).
	exec(hbaResultDown)
	dan, err := e.svc.AddMember(ctx, admin, l.ID, "dan")
	danPass := added(dan, err)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-X041 the action was done, but its result could not be written to the audit log: ") {
		t.Fatalf("rewrite whose result cannot be written: %v", err)
	}
	e.reloaded(dan.PGRole, danPass, l.DBName, true)
	if _, got := hbaOf(dan.PGRole); got != "accepted" {
		t.Fatalf("postgres.hba entries of dan: %q", got)
	}
	exec(`DROP TRIGGER hba_result_down`)

	// Phase 5: the addition could not be closed and the rewrite is refused.
	exec(`CREATE TRIGGER member_and_hba_down BEFORE INSERT ON audit_log WHEN NEW.action = 'postgres.hba' OR
		(NEW.action = 'library.member_add' AND NEW.outcome = 'ok') BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	before = readHBA()
	eva, err := e.svc.AddMember(ctx, admin, l.ID, "eva")
	evaPass := added(eva, err)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-X041 the action was done, but its result could not be written to the audit log: ") {
		t.Fatalf("addition whose result cannot be written: %v", err)
	}
	if !bytes.Equal(readHBA(), before) {
		t.Fatal("pg_hba.conf rewritten without its entry")
	}
	if hba, _ := hbaOf(eva.PGRole); len(hba) != 0 {
		t.Fatalf("postgres.hba entries of eva: %+v", hba)
	}
	refused(eva, evaPass)
	exec(`DROP TRIGGER member_and_hba_down`)
	if err := e.svc.SyncHBA(ctx); err != nil {
		t.Fatal(err)
	}
	e.reloaded(eva.PGRole, evaPass, l.DBName, true)
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
