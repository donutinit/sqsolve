// SPDX-License-Identifier: AGPL-3.0-only

package pg_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/pgconf"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/testpg"
)

func setup(t *testing.T) (*testpg.Cluster, *pg.Client) {
	t.Helper()
	c := testpg.Start(t)
	cl := pg.New(c.SocketDir, c.Port, c.AdminPass, c.SuperPass)
	ctx := context.Background()
	if err := cl.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cl.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap is not idempotent: %v", err)
	}
	if err := cl.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cl.Close)
	return c, cl
}

func writeHBA(t *testing.T, c *testpg.Cluster, cl *pg.Client, libs ...pgconf.HBALibrary) {
	t.Helper()
	content, err := pgconf.HBA(pgconf.HBAOptions{Libraries: libs, MaintenanceDB: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgconf.WriteHBA(c.ConfDir, content); err != nil {
		t.Fatal(err)
	}
	errs, err := cl.HBAErrors(context.Background())
	if err != nil || len(errs) > 0 {
		t.Fatalf("hba errors: %v %v", err, errs)
	}
	if err := cl.ReloadConf(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryRoleLifecycle(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)

	pass := secrets.GeneratePassword(24)
	v, err := pg.ScramVerifier(pass)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.CreateLibraryRole(ctx, "client_x", pg.RoleOptions{Verifier: v, CreateDB: true, Login: true, ConnLimit: -1}); err != nil {
		t.Fatal(err)
	}
	writeHBA(t, c, cl, pgconf.HBALibrary{DB: "client_x", Roles: []string{"client_x"}})

	// The role logs in over TCP with the plain password: the verifier is right.
	conn, err := c.ConnectTCP(ctx, "client_x", pass, "postgres")
	if err != nil {
		t.Fatalf("library role login: %v\n%s", err, c.Log())
	}
	// Like Resolve's "Create": the role creates its database and tables.
	if _, err := conn.Exec(ctx, `CREATE DATABASE client_x`); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	conn, err = c.ConnectTCP(ctx, "client_x", pass, "client_x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE projects (id serial primary key, name text); INSERT INTO projects(name) VALUES ('a'), ('b')`); err != nil {
		t.Fatal(err)
	}

	// Administrative roles are rejected over the network.
	if _, err := c.ConnectTCP(ctx, "sqsolve_admin", c.AdminPass, "postgres"); err == nil {
		t.Fatal("sqsolve_admin accepted over TCP")
	}
	if _, err := c.ConnectTCP(ctx, "postgres", c.SuperPass, "postgres"); err == nil {
		t.Fatal("superuser accepted over TCP")
	}

	// sqsolve_admin sees and fingerprints the library through inheritance.
	ac, err := cl.ConnectDB(ctx, "client_x")
	if err != nil {
		t.Fatal(err)
	}
	fp, err := pg.SchemaFingerprint(ctx, ac)
	if err != nil || fp.Tables != 1 || !strings.HasPrefix(fp.Hash, "sha256:") {
		t.Fatalf("fingerprint %+v %v", fp, err)
	}
	tables, _, _ := pg.PublicTables(ctx, ac)
	counts, err := pg.CountRows(ctx, ac, tables, true)
	if err != nil || counts["projects"] != 2 {
		t.Fatalf("counts %v %v", counts, err)
	}
	ac.Close(ctx)

	sessions, err := cl.Sessions(ctx, "client_x")
	if err != nil || len(sessions) != 1 || sessions[0].User != "client_x" || sessions[0].ClientAddr != "127.0.0.1" {
		t.Fatalf("sessions %+v %v", sessions, err)
	}
	if err := cl.SetRoleCreateDB(ctx, "client_x", false); err != nil {
		t.Fatal(err)
	}
	if err := cl.SecureDatabase(ctx, "client_x", []string{"client_x"}); err != nil {
		t.Fatal(err)
	}

	// Maintenance: block and terminate.
	if err := cl.SetAllowConnections(ctx, "client_x", false); err != nil {
		t.Fatal(err)
	}
	n, err := cl.TerminateAll(ctx, "client_x")
	if err != nil || n != 1 {
		t.Fatalf("terminate %d %v", n, err)
	}
	conn.Close(ctx)
	if _, err := c.ConnectTCP(ctx, "client_x", pass, "client_x"); err == nil {
		t.Fatal("connection allowed in maintenance")
	}
	// Rename as sqsolve_admin (owner through membership + CREATEDB).
	if err := cl.RenameDatabase(ctx, "client_x", "client_x__prev_202601010000"); err != nil {
		t.Fatal(err)
	}
	if err := cl.RenameDatabase(ctx, "client_x__prev_202601010000", "client_x"); err != nil {
		t.Fatal(err)
	}
	if err := cl.SetAllowConnections(ctx, "client_x", true); err != nil {
		t.Fatal(err)
	}
	// Rotation.
	pass2 := secrets.GeneratePassword(24)
	v2, _ := pg.ScramVerifier(pass2)
	if err := cl.SetRolePassword(ctx, "client_x", v2); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ConnectTCP(ctx, "client_x", pass, "client_x"); err == nil {
		t.Fatal("old password still works")
	}
	conn, err = c.ConnectTCP(ctx, "client_x", pass2, "client_x")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)

	// Another library cannot reach this one.
	other := secrets.GeneratePassword(24)
	vo, _ := pg.ScramVerifier(other)
	cl.CreateLibraryRole(ctx, "other_role", pg.RoleOptions{Verifier: vo, Login: true, ConnLimit: -1})
	writeHBA(t, c, cl, pgconf.HBALibrary{DB: "client_x", Roles: []string{"client_x"}}, pgconf.HBALibrary{DB: "other_role", Roles: []string{"other_role"}})
	if _, err := c.ConnectTCP(ctx, "other_role", other, "client_x"); err == nil {
		t.Fatal("other library role reached client_x")
	}

	// Drop everything.
	if err := cl.DropDatabase(ctx, "client_x"); err != nil {
		t.Fatal(err)
	}
	if err := cl.DropRole(ctx, "client_x"); err != nil {
		t.Fatal(err)
	}
	info, err := cl.Server(ctx)
	if err != nil || info.VersionNum < 160000 {
		t.Fatalf("server %+v %v", info, err)
	}
}

func TestHBAValidationCatchesErrors(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	os.WriteFile(filepath.Join(c.ConfDir, "pg_hba.conf"), []byte("bogus all all 10.0.0.0/99 scram-sha-256\n"), 0o600)
	errs, err := cl.HBAErrors(ctx)
	if err != nil || len(errs) == 0 {
		t.Fatalf("expected validation errors, got %v %v", errs, err)
	}
}
