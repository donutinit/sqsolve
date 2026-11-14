// SPDX-License-Identifier: AGPL-3.0-only

package pg_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// TestCreateLibraryRoleReportsAFailedMembership: when sqsolve_admin cannot
// be given the new role (the superuser renamed it under its open session),
// the creation fails with SQS-P021 and the role is not left behind.
func TestCreateLibraryRoleReportsAFailedMembership(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	su := superuser(t, c, "postgres")
	v, err := pg.ScramVerifier(secrets.GeneratePassword(24))
	if err != nil {
		t.Fatal(err)
	}
	opts := pg.RoleOptions{Verifier: v, Login: true, ConnLimit: -1}
	if _, err := su.Exec(ctx, `ALTER ROLE sqsolve_admin RENAME TO sqsolve_admin_away`); err != nil {
		t.Fatal(err)
	}
	err = cl.CreateLibraryRole(ctx, "half_lib", opts)
	// Renamed back before any check that can stop the test.
	if _, rerr := su.Exec(ctx, `ALTER ROLE sqsolve_admin_away RENAME TO sqsolve_admin`); rerr != nil {
		t.Fatal(rerr)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-P021 membership of half_lib: ") || !pg.IsCode(err, "42704") || strings.Contains(err.Error(), v) {
		t.Fatalf("membership that fails: %s", hidden(fmt.Sprint(err), v))
	}
	var n int
	if err := su.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname = 'half_lib'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d roles half_lib left behind: %v", n, err)
	}
	if err := cl.CreateLibraryRole(ctx, "half_lib", opts); err != nil {
		t.Fatalf("once sqsolve_admin is back: %s", hidden(err.Error(), v))
	}
}

// TestReloadConfReportsFalse: a pg_reload_conf that answers false is an
// error (SQS-P030). A function of that name ahead of pg_catalog in the search
// path of sqsolve_admin answers false.
func TestReloadConfReportsFalse(t *testing.T) {
	ctx := context.Background()
	c, cl := setup(t)
	su := superuser(t, c, "postgres")
	for _, q := range []string{
		`CREATE SCHEMA shadow`,
		`CREATE FUNCTION shadow.pg_reload_conf() RETURNS boolean LANGUAGE sql AS 'SELECT false'`,
		`GRANT USAGE ON SCHEMA shadow TO sqsolve_admin`,
		`ALTER ROLE sqsolve_admin SET search_path = shadow, pg_catalog`,
	} {
		if _, err := su.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	const reset = `ALTER ROLE sqsolve_admin RESET search_path`
	defer su.Exec(ctx, reset)
	// The search path of a role applies to its new sessions.
	if err := cl.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cl.ReloadConf(ctx); err == nil || err.Error() != "SQS-P030 pg_reload_conf returned false" {
		t.Fatalf("reload that answers false: %v", err)
	}
	if _, err := su.Exec(ctx, reset); err != nil {
		t.Fatal(err)
	}
	if err := cl.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cl.ReloadConf(ctx); err != nil {
		t.Fatalf("reload with the search path reset: %v", err)
	}
}
