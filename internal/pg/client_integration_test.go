// SPDX-License-Identifier: AGPL-3.0-only

package pg_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/pgconf"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/testpg"
)

// superuser opens a superuser connection the test closes at the end.
func superuser(t *testing.T, c *testpg.Cluster, db string) *pgx.Conn {
	t.Helper()
	conn, err := c.Connect(context.Background(), "postgres", c.SuperPass, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

// libraryRole creates a login role with its own database, reachable over TCP.
func libraryRole(t *testing.T, c *testpg.Cluster, cl *pg.Client, name string) secrets.String {
	t.Helper()
	ctx := context.Background()
	pass := secrets.GeneratePassword(24)
	v, err := pg.ScramVerifier(pass)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.CreateLibraryRole(ctx, name, pg.RoleOptions{Verifier: v, Login: true, ConnLimit: -1}); err != nil {
		t.Fatal(err)
	}
	if err := cl.CreateDatabase(ctx, name, name); err != nil {
		t.Fatal(err)
	}
	writeHBA(t, c, cl, pgconf.HBALibrary{DB: name, Roles: []string{name}})
	return pass
}

// TestDatabasesWhileDropping: listing databases while another session creates
// and drops them never fails (audit M7: pg_database_size() is NULL for a
// database whose files are already gone). The churn is a small fixed number
// of cycles so it does not load the integration tests of other packages that
// run at the same time; TestDatabaseSemantics covers the same cases
// deterministically.
func TestDatabasesWhileDropping(t *testing.T) {
	if testing.Short() {
		t.Skip("database churn; TestDatabaseSemantics covers the cases deterministically")
	}
	const cycles = 8
	ctx := context.Background()
	_, cl := setup(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p, _ := cl.Pool()
		for i := 0; i < cycles; i++ {
			p.Exec(ctx, "CREATE DATABASE churn")
			p.Exec(ctx, "DROP DATABASE IF EXISTS churn")
		}
	}()
	errs := map[string]int{}
	calls := 0
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
		}
		calls++
		if _, err := cl.Databases(ctx); err != nil {
			errs[err.Error()]++
		}
	}
	if len(errs) > 0 {
		t.Fatalf("%d calls, errors: %v", calls, errs)
	}
}

// TestDatabaseSemantics: Database(name) reads one row; an invalid database
// (a DROP DATABASE that did not finish) does not count as existing for the
// library state, but is listed marked Invalid (so reconciliation can drop it)
// and keeps its name taken; a database without files reports size 0 instead
// of failing the whole list.
func TestDatabaseSemantics(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	su := superuser(t, c, "postgres")
	for _, db := range []string{"valid_db", "half_dropped", "no_files"} {
		if _, err := su.Exec(ctx, "CREATE DATABASE "+db+" OWNER sqsolve_admin"); err != nil {
			t.Fatal(err)
		}
	}

	d, err := cl.Database(ctx, "valid_db")
	if err != nil || d == nil || d.Name != "valid_db" || d.Owner != pg.AdminRole || d.SizeBytes <= 0 || !d.AllowConn {
		t.Fatalf("valid database: %+v %v", d, err)
	}
	if d, err := cl.Database(ctx, "does_not_exist"); err != nil || d != nil {
		t.Fatalf("missing database: %+v %v", d, err)
	}
	if d, err := cl.Database(ctx, "template1"); err != nil || d != nil {
		t.Fatalf("template: %+v %v", d, err)
	}

	// What an interrupted DROP DATABASE leaves behind (PostgreSQL 16+).
	if _, err := su.Exec(ctx, `UPDATE pg_database SET datconnlimit = -2 WHERE datname = 'half_dropped'`); err != nil {
		t.Fatal(err)
	}
	if d, err := cl.Database(ctx, "half_dropped"); err != nil || d != nil {
		t.Fatalf("invalid database reported as existing: %+v %v", d, err)
	}
	for name, want := range map[string]bool{"half_dropped": true, "valid_db": true, "template1": true, "does_not_exist": false} {
		if taken, err := cl.DatabaseNameTaken(ctx, name); err != nil || taken != want {
			t.Errorf("name %s taken: %v %v", name, taken, err)
		}
	}

	// A database whose directory is gone, as in the middle of a drop.
	var oid uint32
	if err := su.QueryRow(ctx, `SELECT oid FROM pg_database WHERE datname = 'no_files'`).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	if _, err := su.Exec(ctx, "CHECKPOINT"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(c.DataDir, "base", fmt.Sprint(oid))
	moved := filepath.Join(c.Base, "moved_db")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	all, err := cl.Databases(ctx)
	os.Rename(moved, dir)
	if err != nil {
		t.Fatalf("databases with one directory missing: %v", err)
	}
	names := map[string]pg.DBInfo{}
	for _, d := range all {
		names[d.Name] = d
	}
	if d, ok := names["half_dropped"]; !ok || !d.Invalid || d.SizeBytes != 0 {
		t.Errorf("invalid database: %+v listed %v", d, ok)
	}
	if d, ok := names["no_files"]; !ok || d.SizeBytes != 0 || d.Invalid {
		t.Errorf("database without files: %+v %v", d, ok)
	}
	if d, ok := names["valid_db"]; !ok || d.Invalid || d.SizeBytes <= 0 {
		t.Errorf("valid database: %+v %v", d, ok)
	}
	// An invalid database can still be dropped.
	if err := cl.DropDatabase(ctx, "half_dropped"); err != nil {
		t.Fatal(err)
	}
}

// TestTerminateAllSkipsSuperusers: a superuser session in a library database
// does not abort TerminateAll (sqsolve_admin may not end it); it is reported.
func TestTerminateAllSkipsSuperusers(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	pass := libraryRole(t, c, cl, "lib_t")
	superuser(t, c, "lib_t")
	ws, err := c.ConnectTCP(ctx, "lib_t", pass, "lib_t")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close(ctx)

	n, err := cl.TerminateAll(ctx, "lib_t")
	if err != nil || n != 1 {
		t.Fatalf("TerminateAll: %d %v", n, err)
	}
	if err := ws.Ping(ctx); err == nil {
		t.Fatal("the library session survived")
	}
	// The superuser session is left alone and reported.
	ws2, err := c.ConnectTCP(ctx, "lib_t", pass, "lib_t")
	if err != nil {
		t.Fatal(err)
	}
	defer ws2.Close(ctx)
	r, err := cl.TerminateAllSessions(ctx, "lib_t")
	if err != nil || r != (pg.TerminateResult{Terminated: 1, Superuser: 1}) {
		t.Fatalf("TerminateAllSessions: %+v %v", r, err)
	}
	// The terminated backend exits asynchronously.
	deadline := time.Now().Add(5 * time.Second)
	for {
		sessions, err := cl.Sessions(ctx, "lib_t")
		if err == nil && len(sessions) == 1 && sessions[0].User == "postgres" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sessions left: %+v %v", sessions, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAdminReservedConnections: workstations cannot take the last slots;
// sqsolve_admin still connects to back up (audit: no reserved slot).
func TestAdminReservedConnections(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	p, _ := cl.Pool()
	var reserved string
	var member bool
	if err := p.QueryRow(ctx, `SELECT current_setting('reserved_connections'), pg_has_role($1, 'pg_use_reserved_connections', 'USAGE')`, pg.AdminRole).Scan(&reserved, &member); err != nil {
		t.Fatal(err)
	}
	if reserved != "3" || !member {
		t.Fatalf("reserved_connections=%s, %s uses them: %v", reserved, pg.AdminRole, member)
	}

	pass := libraryRole(t, c, cl, "busy_lib")
	var conns []*pgx.Conn
	defer func() {
		for _, conn := range conns {
			conn.Close(ctx)
		}
	}()
	var lastErr error
	for i := 0; i < 60; i++ {
		conn, err := c.ConnectTCP(ctx, "busy_lib", pass, "busy_lib")
		if err != nil {
			lastErr = err
			break
		}
		conns = append(conns, conn)
	}
	if lastErr == nil || !strings.Contains(lastErr.Error(), "pg_use_reserved_connections") {
		t.Fatalf("after %d workstation connections: %v", len(conns), lastErr)
	}
	admin, err := cl.ConnectDB(ctx, "busy_lib")
	if err != nil {
		t.Fatalf("sqsolve_admin without a free slot: %v", err)
	}
	admin.Close(ctx)
}

// TestAdminLockTimeout: admin queries and DDL fail with a clear error when
// another session holds a lock, instead of waiting forever.
func TestAdminLockTimeout(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	cl.LockTimeout = 300 * time.Millisecond
	if err := cl.Open(ctx); err != nil {
		t.Fatal(err)
	}
	su := superuser(t, c, "postgres")
	if _, err := su.Exec(ctx, "CREATE DATABASE locked OWNER sqsolve_admin"); err != nil {
		t.Fatal(err)
	}

	// A count behind an ACCESS EXCLUSIVE lock (Resolve altering a table).
	holder := superuser(t, c, "locked")
	if _, err := holder.Exec(ctx, `CREATE TABLE t (id int)`); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `BEGIN; LOCK TABLE t IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	conn, err := cl.ConnectDB(ctx, "locked")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	start := time.Now()
	_, err = pg.CountRows(ctx, conn, []string{"t"}, true)
	if !errors.Is(err, pg.ErrLockTimeout) || !strings.Contains(err.Error(), "SQS-P050") || !pg.IsCode(err, "55P03") {
		t.Fatalf("count behind a lock: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("waited %s", time.Since(start))
	}
	holder.Exec(ctx, "ROLLBACK")
	holder.Close(ctx)

	// ALTER and DROP DATABASE behind another transaction that changed the database.
	if _, err := su.Exec(ctx, "BEGIN; ALTER DATABASE locked CONNECTION LIMIT 7"); err != nil {
		t.Fatal(err)
	}
	if err := cl.SetAllowConnections(ctx, "locked", false); !errors.Is(err, pg.ErrLockTimeout) || !strings.Contains(err.Error(), "SQS-P050") {
		t.Fatalf("ALTER DATABASE behind a lock: %v", err)
	}
	start = time.Now()
	if err := cl.DropDatabase(ctx, "locked"); !errors.Is(err, pg.ErrLockTimeout) || !strings.Contains(err.Error(), "SQS-P050") {
		t.Fatalf("DROP DATABASE behind a lock: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("DROP DATABASE waited %s", time.Since(start))
	}
	su.Exec(ctx, "ROLLBACK")
	if err := cl.DropDatabase(ctx, "locked"); err != nil {
		t.Fatalf("drop once the lock is gone: %v", err)
	}
}

// TestAdminStatementTimeout: a statement that runs too long is cancelled and
// reported with its own code.
func TestAdminStatementTimeout(t *testing.T) {
	ctx := context.Background()
	_, cl := setup(t)
	cl.StatementTimeout = 300 * time.Millisecond
	if err := cl.Open(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := cl.Pool()
	_, err := p.Exec(ctx, "SELECT pg_sleep(3)")
	err = pg.WrapTimeout(err)
	if !errors.Is(err, pg.ErrStatementTimeout) || !strings.Contains(err.Error(), "SQS-P051") || !pg.IsCode(err, "57014") {
		t.Fatalf("long statement: %v", err)
	}
	// Defaults apply when the fields are not set.
	_, def := setup(t)
	dp, _ := def.Pool()
	var lock, stmt string
	if err := dp.QueryRow(ctx, `SELECT current_setting('lock_timeout'), current_setting('statement_timeout')`).Scan(&lock, &stmt); err != nil {
		t.Fatal(err)
	}
	if lock != "1min" || stmt != "30min" {
		t.Fatalf("default timeouts: lock_timeout=%s statement_timeout=%s", lock, stmt)
	}
}

// TestCreateLibraryRoleIsAtomic: if granting the new role to sqsolve_admin
// fails, the role is not left behind to block a retry.
func TestCreateLibraryRoleIsAtomic(t *testing.T) {
	ctx := context.Background()
	_, cl := setup(t)
	i, err := cl.ExecTx(ctx, `CREATE ROLE half_made NOLOGIN`, `GRANT does_not_exist TO sqsolve_admin`)
	if err == nil || i != 1 {
		t.Fatalf("the second statement did not fail: %d %v", i, err)
	}
	if ok, err := cl.RoleExists(ctx, "half_made"); err != nil || ok {
		t.Fatalf("role left behind: %v %v", ok, err)
	}
	// The public function: an existing role is refused with its code and
	// nothing else changes.
	v, _ := pg.ScramVerifier(secrets.GeneratePassword(24))
	if err := cl.CreateLibraryRole(ctx, "once", pg.RoleOptions{Verifier: v, Login: true, ConnLimit: -1}); err != nil {
		t.Fatal(err)
	}
	err = cl.CreateLibraryRole(ctx, "once", pg.RoleOptions{Verifier: v, Login: true, ConnLimit: -1})
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-P020 creating role once") || strings.Contains(err.Error(), v) {
		t.Fatalf("duplicate role: %v", err)
	}
	p, _ := cl.Pool()
	var inherit, set bool
	if err := p.QueryRow(ctx, `SELECT m.inherit_option, m.set_option FROM pg_auth_members m
		JOIN pg_roles r ON r.oid = m.roleid JOIN pg_roles u ON u.oid = m.member
		WHERE r.rolname = 'once' AND u.rolname = $1 AND m.inherit_option`, pg.AdminRole).Scan(&inherit, &set); err != nil || !inherit || !set {
		t.Fatalf("membership of sqsolve_admin: %v %v %v", inherit, set, err)
	}
}
