// SPDX-License-Identifier: AGPL-3.0-only

package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// noServer is a client whose socket folder has no server.
func noServer(t *testing.T) *pg.Client {
	t.Helper()
	return pg.New(t.TempDir(), 5432, secrets.New("admin password"), secrets.New("superuser password"))
}

// TestCatalogWithoutTheServer: while PostgreSQL has not been reached (or
// after Close), every catalog read and every change fails with SQS-P001
// instead of using a pool that is not there.
func TestCatalogWithoutTheServer(t *testing.T) {
	ctx := context.Background()
	cl := noServer(t)
	const want = "SQS-P001 PostgreSQL is not available"
	calls := map[string]error{}
	add := func(name string, err error) { calls[name] = err }
	_, err := cl.Pool()
	add("Pool", err)
	_, err = cl.Server(ctx)
	add("Server", err)
	_, err = cl.Databases(ctx)
	add("Databases", err)
	_, err = cl.Database(ctx, "lib")
	add("Database", err)
	_, err = cl.DatabaseNameTaken(ctx, "lib")
	add("DatabaseNameTaken", err)
	_, err = cl.RoleExists(ctx, "lib")
	add("RoleExists", err)
	_, err = cl.DatabasesOwnedBy(ctx, "lib")
	add("DatabasesOwnedBy", err)
	_, err = cl.Sessions(ctx, "lib")
	add("Sessions", err)
	_, err = cl.TerminateSession(ctx, "lib", 42, []string{"lib"})
	add("TerminateSession", err)
	_, err = cl.TerminateAll(ctx, "lib")
	add("TerminateAll", err)
	_, err = cl.SetRolesLogin(ctx, []string{"lib"}, false)
	add("SetRolesLogin", err)
	_, err = cl.ActivityMark(ctx, "lib")
	add("ActivityMark", err)
	add("ReloadConf", cl.ReloadConf(ctx))
	_, err = cl.HBAErrors(ctx)
	add("HBAErrors", err)
	add("Ping", cl.Ping(ctx))
	add("CreateLibraryRole", cl.CreateLibraryRole(ctx, "lib", pg.RoleOptions{Verifier: "SCRAM-SHA-256$4096:x", Login: true, ConnLimit: -1}))
	add("SetRolePassword", cl.SetRolePassword(ctx, "lib", "SCRAM-SHA-256$4096:x"))
	add("SetRoleCreateDB", cl.SetRoleCreateDB(ctx, "lib", false))
	add("SetRoleLogin", cl.SetRoleLogin(ctx, "lib", false))
	add("SetRoleConnLimit", cl.SetRoleConnLimit(ctx, "lib", 3))
	add("SetRoleDefaultRole", cl.SetRoleDefaultRole(ctx, "lib__ana", "lib", "lib"))
	add("GrantRole", cl.GrantRole(ctx, "lib", "lib__ana"))
	add("DropRole", cl.DropRole(ctx, "lib"))
	add("CreateDatabase", cl.CreateDatabase(ctx, "lib", "lib"))
	add("SecureDatabase", cl.SecureDatabase(ctx, "lib", []string{"lib"}))
	add("SetAllowConnections", cl.SetAllowConnections(ctx, "lib", false))
	add("SetDatabaseConnLimit", cl.SetDatabaseConnLimit(ctx, "lib", 2))
	add("RenameDatabase", cl.RenameDatabase(ctx, "lib", "lib__prev_202610250000"))
	add("DropDatabase", cl.DropDatabase(ctx, "lib"))
	for name, err := range calls {
		// SetRolePassword rebuilds its error from the text to redact the
		// verifier, so there only the message carries the code.
		if err == nil || !strings.HasPrefix(err.Error(), want) || name != "SetRolePassword" && !errors.Is(err, pg.ErrUnavailable) {
			t.Errorf("%s without a server: %v", name, err)
		}
	}
	// Nothing to change is not an error, with or without a server.
	if changed, err := cl.SetRolesLogin(ctx, nil, true); err != nil || changed != nil {
		t.Errorf("SetRolesLogin of no roles: %v %v", changed, err)
	}
}

// TestConnectingToAStoppedServer: Open, Bootstrap and the superuser
// connection report a server that does not answer as SQS-P001, and a failed
// Open leaves no pool behind.
func TestConnectingToAStoppedServer(t *testing.T) {
	ctx := context.Background()
	cl := noServer(t)
	for name, err := range map[string]error{"Open": cl.Open(ctx), "Bootstrap": cl.Bootstrap(ctx)} {
		if !errors.Is(err, pg.ErrUnavailable) || !strings.HasPrefix(err.Error(), "SQS-P001 PostgreSQL is not available: ") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := cl.Pool(); !errors.Is(err, pg.ErrUnavailable) {
		t.Fatalf("a failed Open left a pool: %v", err)
	}
	if conn, err := cl.Superuser(ctx, "postgres"); err == nil || !errors.Is(err, pg.ErrUnavailable) {
		if conn != nil {
			conn.Close(ctx)
		}
		t.Fatalf("superuser connection: %v", err)
	}
	if conn, err := cl.ConnectDB(ctx, "lib"); err == nil {
		conn.Close(ctx)
		t.Fatal("ConnectDB reached a server that is not there")
	}
}

// TestNamesAreRefusedBeforeTheServer: every call that builds a statement from
// a name refuses one that is not a valid identifier with SQS-L001, and a
// literal with a backslash with SQS-P010, before it asks for a connection:
// even with no server the error is the name's, not SQS-P001.
func TestNamesAreRefusedBeforeTheServer(t *testing.T) {
	ctx := context.Background()
	cl := noServer(t)
	const bad = `x"; DROP DATABASE postgres; --`
	invalid := `SQS-L001 invalid identifier: "x\"; DROP DATABASE postgres; --"`
	ok := pg.RoleOptions{Verifier: "SCRAM-SHA-256$4096:x", Login: true, ConnLimit: -1}
	for name, err := range map[string]error{
		"CreateLibraryRole":           cl.CreateLibraryRole(ctx, bad, ok),
		"SetRolePassword":             cl.SetRolePassword(ctx, bad, "v"),
		"SetRoleCreateDB":             cl.SetRoleCreateDB(ctx, bad, true),
		"SetRoleLogin":                cl.SetRoleLogin(ctx, bad, true),
		"SetRoleConnLimit":            cl.SetRoleConnLimit(ctx, bad, 1),
		"SetRoleDefaultRole member":   cl.SetRoleDefaultRole(ctx, bad, "lib", "lib"),
		"SetRoleDefaultRole database": cl.SetRoleDefaultRole(ctx, "lib__ana", bad, "lib"),
		"GrantRole role":              cl.GrantRole(ctx, bad, "lib__ana"),
		"GrantRole member":            cl.GrantRole(ctx, "lib", bad),
		"DropRole":                    cl.DropRole(ctx, bad),
		"CreateDatabase name":         cl.CreateDatabase(ctx, bad, "lib"),
		"CreateDatabase owner":        cl.CreateDatabase(ctx, "lib", bad),
		"SecureDatabase":              cl.SecureDatabase(ctx, bad, []string{"lib"}),
		"SetAllowConnections":         cl.SetAllowConnections(ctx, bad, true),
		"SetDatabaseConnLimit":        cl.SetDatabaseConnLimit(ctx, bad, 1),
		"RenameDatabase from":         cl.RenameDatabase(ctx, bad, "lib"),
		"RenameDatabase to":           cl.RenameDatabase(ctx, "lib", bad),
		"DropDatabase":                cl.DropDatabase(ctx, bad),
	} {
		if !errors.Is(err, pg.ErrInvalidSlug) || err.Error() != invalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if conn, err := cl.ConnectDB(ctx, bad); err == nil || err.Error() != invalid {
		if conn != nil {
			conn.Close(ctx)
		}
		t.Errorf("ConnectDB: %v", err)
	}
	const literal = "SQS-P010 literal with disallowed characters"
	for name, err := range map[string]error{
		"CreateLibraryRole verifier":  cl.CreateLibraryRole(ctx, "lib", pg.RoleOptions{Verifier: `SCRAM\x`}),
		"SetRolePassword verifier":    cl.SetRolePassword(ctx, "lib", "SCRAM\x00"),
		"SetRoleDefaultRole of owner": cl.SetRoleDefaultRole(ctx, "lib__ana", "lib", `lib\`),
	} {
		if err == nil || err.Error() != literal {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestValidatePerson: the person part of a member role (lib__ana) is 2 to 20
// lowercase letters or digits starting with a letter, so the role name stays
// a valid identifier.
func TestValidatePerson(t *testing.T) {
	for _, s := range []string{"ana", "a1", "maria2026", strings.Repeat("b", 20)} {
		if err := pg.ValidatePerson(s); err != nil {
			t.Errorf("%q refused: %v", s, err)
		}
	}
	const want = "SQS-L001 invalid identifier: a person name uses 2 to 20 lowercase letters or digits, starting with a letter"
	for _, s := range []string{"", "a", "Ana", "1ana", "ana_b", "ana b", "ana-b", "josé", strings.Repeat("b", 21)} {
		if err := pg.ValidatePerson(s); err == nil || err.Error() != want || !errors.Is(err, pg.ErrInvalidSlug) {
			t.Errorf("%q: %v", s, err)
		}
	}
}
