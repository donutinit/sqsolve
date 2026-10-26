// SPDX-License-Identifier: AGPL-3.0-only

package pg_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/pgconf"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// TestConnectionLimitsAreEnforced: the connection limit of a role and of a
// database are what PostgreSQL applies to workstations.
func TestConnectionLimitsAreEnforced(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	pass := libraryRole(t, c, cl, "lim_lib")
	if err := cl.SetRoleConnLimit(ctx, "lim_lib", 1); err != nil {
		t.Fatal(err)
	}
	first, err := c.ConnectTCP(ctx, "lim_lib", pass, "lim_lib")
	if err != nil {
		t.Fatal(err)
	}
	if conn, err := c.ConnectTCP(ctx, "lim_lib", pass, "lim_lib"); err == nil || !strings.Contains(err.Error(), `too many connections for role "lim_lib"`) {
		if conn != nil {
			conn.Close(ctx)
		}
		t.Fatalf("second connection with a role limit of 1: %v", err)
	}
	first.Close(ctx)
	if err := cl.SetRoleConnLimit(ctx, "lim_lib", -1); err != nil {
		t.Fatal(err)
	}
	if err := cl.SetDatabaseConnLimit(ctx, "lim_lib", 3); err != nil {
		t.Fatal(err)
	}
	if d, err := cl.Database(ctx, "lim_lib"); err != nil || d == nil || d.ConnLimit != 3 {
		t.Fatalf("database limit: %+v %v", d, err)
	}
	if err := cl.SetDatabaseConnLimit(ctx, "no_such_db", 3); !pg.IsCode(err, "3D000") {
		t.Fatalf("limit of a database that does not exist: %v", err)
	}
}

// TestMemberActsAsTheOwner: a member granted the owner role, with the owner
// as its role inside the library database, creates objects that belong to
// the owner (docs 07 §6).
func TestMemberActsAsTheOwner(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	libraryRole(t, c, cl, "team_lib")
	mpass := secrets.GeneratePassword(24)
	v, _ := pg.ScramVerifier(mpass)
	if err := cl.CreateLibraryRole(ctx, "team_lib__ana", pg.RoleOptions{Verifier: v, Login: true, ConnLimit: -1}); err != nil {
		t.Fatal(err)
	}
	if err := cl.GrantRole(ctx, "team_lib", "team_lib__ana"); err != nil {
		t.Fatal(err)
	}
	if err := cl.SetRoleDefaultRole(ctx, "team_lib__ana", "team_lib", "team_lib"); err != nil {
		t.Fatal(err)
	}
	if err := cl.SecureDatabase(ctx, "team_lib", []string{"team_lib", "team_lib__ana"}); err != nil {
		t.Fatal(err)
	}
	writeHBA(t, c, cl, pgconf.HBALibrary{DB: "team_lib", Roles: []string{"team_lib", "team_lib__ana"}})
	conn, err := c.ConnectTCP(ctx, "team_lib__ana", mpass, "team_lib")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var user string
	if _, err := conn.Exec(ctx, `CREATE TABLE made_by_ana (id int)`); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT tableowner FROM pg_tables WHERE tablename = 'made_by_ana'`).Scan(&user); err != nil || user != "team_lib" {
		t.Fatalf("table owned by %q: %v", user, err)
	}
	// Granting a role that does not exist fails with PostgreSQL's code.
	if err := cl.GrantRole(ctx, "no_such_role", "team_lib__ana"); !pg.IsCode(err, "42704") {
		t.Fatalf("grant of a missing role: %v", err)
	}
	// A role name in the list is checked after PUBLIC lost access.
	if err := cl.SecureDatabase(ctx, "team_lib", []string{"BAD NAME"}); !errors.Is(err, pg.ErrInvalidSlug) {
		t.Fatalf("SecureDatabase with an invalid role: %v", err)
	}
	if err := cl.SecureDatabase(ctx, "no_such_db", nil); !pg.IsCode(err, "3D000") {
		t.Fatalf("SecureDatabase of a missing database: %v", err)
	}
}

// TestDatabasesOwnedBy lists only the databases of the role, sorted: what
// the initialization watch uses to find a database Resolve created under
// another name.
func TestDatabasesOwnedBy(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	libraryRole(t, c, cl, "own_lib")
	libraryRole(t, c, cl, "other_lib")
	if err := cl.CreateDatabase(ctx, "own_lib_typo", "own_lib"); err != nil {
		t.Fatal(err)
	}
	got, err := cl.DatabasesOwnedBy(ctx, "own_lib")
	if err != nil || !slices.Equal(got, []string{"own_lib", "own_lib_typo"}) {
		t.Fatalf("owned by own_lib: %v %v", got, err)
	}
	if got, err := cl.DatabasesOwnedBy(ctx, "nobody"); err != nil || len(got) != 0 {
		t.Fatalf("owned by a missing role: %v %v", got, err)
	}
}

// TestTerminateSessionChecksDatabaseAndRole: a single session is ended only
// when it is on that database and belongs to one of the given roles, so a
// pid typed in the panel cannot end another library's session.
func TestTerminateSessionChecksDatabaseAndRole(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	pass := libraryRole(t, c, cl, "ses_lib")
	ws, err := c.ConnectTCP(ctx, "ses_lib", pass, "ses_lib")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close(ctx)
	pid := int32(ws.PgConn().PID())
	for name, call := range map[string]struct {
		db    string
		pid   int32
		roles []string
	}{
		"another database": {"postgres", pid, []string{"ses_lib"}},
		"another role":     {"ses_lib", pid, []string{"other_role"}},
		"no such session":  {"ses_lib", 1, []string{"ses_lib"}},
	} {
		if ok, err := cl.TerminateSession(ctx, call.db, call.pid, call.roles); err != nil || ok {
			t.Fatalf("%s: terminated %v, %v", name, ok, err)
		}
	}
	if err := ws.Ping(ctx); err != nil {
		t.Fatalf("a refused termination ended the session: %v", err)
	}
	if ok, err := cl.TerminateSession(ctx, "ses_lib", pid, []string{"ses_lib"}); err != nil || !ok {
		t.Fatalf("own session: %v %v", ok, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for ws.Ping(ctx) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the session survived its termination")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestActivityMarks: the mark of a database changes when its rows change and
// only then (ADR 0022); the mark read on a connection to the database is the
// same as the one read from the catalog, and an unknown database has none.
func TestActivityMarks(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	pass := libraryRole(t, c, cl, "act_lib")
	before, err := cl.ActivityMark(ctx, "act_lib")
	if err != nil || before == "" {
		t.Fatalf("mark of a new database: %q %v", before, err)
	}
	if m, err := cl.ActivityMark(ctx, "no_such_db"); err != nil || m != "" {
		t.Fatalf("mark of a missing database: %q %v", m, err)
	}
	ws, err := c.ConnectTCP(ctx, "act_lib", pass, "act_lib")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Exec(ctx, `CREATE TABLE t (id int); INSERT INTO t SELECT generate_series(1, 10)`); err != nil {
		t.Fatal(err)
	}
	// Statistics reach pg_stat_database when the backend ends.
	ws.Close(ctx)
	// Wait until the mark changed and stays the same: the backend may flush
	// its statistics in more than one go.
	var after string
	deadline := time.Now().Add(10 * time.Second)
	for {
		m1, err := cl.ActivityMark(ctx, "act_lib")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		m2, _ := cl.ActivityMark(ctx, "act_lib")
		if m1 != before && m1 == m2 {
			after = m2
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the mark did not settle on a new value after writes: %q, %q", m1, m2)
		}
	}
	conn, err := cl.ConnectDB(ctx, "act_lib")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	own, err := pg.ActivityMarkFrom(ctx, conn)
	if err != nil || own != after {
		t.Fatalf("mark read on the database: %q %v, catalog %q", own, err, after)
	}
	// Reading does not move it.
	if again, _ := cl.ActivityMark(ctx, "act_lib"); again != after {
		t.Fatalf("mark moved without writes: %q then %q", after, again)
	}
	all, err := cl.ActivityMarks(ctx)
	if err != nil || all["act_lib"] != after || all["postgres"] == "" {
		t.Fatalf("all marks: %v %v", all, err)
	}
	conn.Close(ctx)
	if _, err := pg.ActivityMarkFrom(ctx, conn); err == nil {
		t.Fatal("mark read on a closed connection")
	}
}

// TestPingFollowsTheServer: Ping reports a stopped server as SQS-P001 and
// recovers by itself once the server is back, without a new Open.
func TestPingFollowsTheServer(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	if err := cl.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	c.Stop()
	err := cl.Ping(ctx)
	if !errors.Is(err, pg.ErrUnavailable) || !strings.HasPrefix(err.Error(), "SQS-P001 PostgreSQL is not available: ") {
		t.Fatalf("ping of a stopped server: %v", err)
	}
	if _, err := cl.Server(ctx); err == nil {
		t.Fatal("server information from a stopped server")
	}
	if _, err := cl.Databases(ctx); err == nil {
		t.Fatal("database list from a stopped server")
	}
	c.Restart(t)
	deadline := time.Now().Add(10 * time.Second)
	for cl.Ping(ctx) != nil {
		if time.Now().After(deadline) {
			t.Fatalf("ping after the restart: %v", cl.Ping(ctx))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestWrongPasswordsAreUnavailable: a superuser or sqsolve_admin password
// that does not match the cluster is reported as SQS-P001, and Bootstrap
// does nothing with it.
func TestWrongPasswordsAreUnavailable(t *testing.T) {
	ctx := context.Background()
	c, _ := setup(t)
	wrongSuper := pg.New(c.SocketDir, c.Port, c.AdminPass, secrets.New("not the superuser password"))
	if err := wrongSuper.Bootstrap(ctx); !errors.Is(err, pg.ErrUnavailable) || !strings.Contains(err.Error(), "password authentication failed") {
		t.Fatalf("bootstrap with a wrong superuser password: %v", err)
	}
	wrongAdmin := pg.New(c.SocketDir, c.Port, secrets.New("not the admin password"), c.SuperPass)
	err := wrongAdmin.Open(ctx)
	if !errors.Is(err, pg.ErrUnavailable) || !strings.Contains(err.Error(), "password authentication failed") {
		t.Fatalf("open with a wrong admin password: %v", err)
	}
	if _, err := wrongAdmin.Pool(); !errors.Is(err, pg.ErrUnavailable) {
		t.Fatal("a refused Open left a pool")
	}
}

// TestToolsConnectWithTheAdminCredential: pg_dump and pg_restore get
// sqsolve_admin's password through a temporary PGPASSFILE and never in their
// arguments; the arguments and the credential are enough to connect.
func TestToolsConnectWithTheAdminCredential(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	args := cl.ConnArgs("postgres")
	for _, a := range args {
		if strings.Contains(a, c.AdminPass.Reveal()) {
			t.Fatalf("the password is in the arguments: %v", args)
		}
	}
	run := execx.NewRunner(c.BinDir)
	res, err := run.Run(ctx, execx.Cmd{Name: "pg_dump", Args: append(args, "--schema-only"), Credential: cl.AdminCredential()})
	if err != nil || !strings.Contains(res.Stdout, "PostgreSQL database dump") {
		t.Fatalf("pg_dump with the admin credential: %v %s", err, res.Stderr)
	}
	// Without the credential the tool does not get in (--no-password).
	res, err = run.Run(ctx, execx.Cmd{Name: "pg_dump", Args: append(cl.ConnArgs("postgres"), "--schema-only")})
	if err == nil || !strings.Contains(res.Stderr, "no password supplied") {
		t.Fatalf("pg_dump without the credential: %v %s", err, res.Stderr)
	}
}

// TestFailedStatementsKeepTheVerifierSecret: a password change that
// PostgreSQL refuses returns its error without the SCRAM verifier.
func TestFailedStatementsKeepTheVerifierSecret(t *testing.T) {
	ctx := context.Background()
	_, cl := setup(t)
	v, _ := pg.ScramVerifier(secrets.GeneratePassword(24))
	err := cl.SetRolePassword(ctx, "ghost_role", v)
	if err == nil || !strings.Contains(err.Error(), `role "ghost_role" does not exist`) || strings.Contains(err.Error(), v) {
		t.Fatalf("password of a missing role: %v", err)
	}
}

// TestSchemaReadsOnABrokenConnection: the fingerprint, the table list and the
// counts report a failed query instead of an empty result, and a database
// without tables has no fingerprint (the initialization watch waits).
func TestSchemaReadsOnABrokenConnection(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	libraryRole(t, c, cl, "empty_lib")
	conn, err := cl.ConnectDB(ctx, "empty_lib")
	if err != nil {
		t.Fatal(err)
	}
	if fp, err := pg.SchemaFingerprint(ctx, conn); err != nil || fp != (pg.Fingerprint{}) {
		t.Fatalf("fingerprint of an empty database: %+v %v", fp, err)
	}
	if _, err := pg.CountRows(ctx, conn, []string{"missing_table"}, true); err == nil || !strings.HasPrefix(err.Error(), "count of missing_table: ") || !pg.IsCode(err, "42P01") {
		t.Fatalf("count of a missing table: %v", err)
	}
	if got, err := pg.CountRows(ctx, conn, []string{"missing_table"}, false); err == nil {
		t.Fatalf("estimate of a missing table: %v", got)
	}
	conn.Close(ctx)
	if _, err := pg.SchemaFingerprint(ctx, conn); err == nil {
		t.Fatal("fingerprint on a closed connection")
	}
	if _, _, err := pg.PublicTables(ctx, conn); err == nil {
		t.Fatal("table list on a closed connection")
	}
}
