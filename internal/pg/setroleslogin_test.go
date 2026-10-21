// SPDX-License-Identifier: AGPL-3.0-only

package pg_test

import (
	"context"
	"slices"
	"testing"

	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// TestSetRolesLogin: only existing roles whose state differs are changed, and
// the changed ones are reported (ADR 0024).
func TestSetRolesLogin(t *testing.T) {
	ctx := context.Background()
	_, cl := setup(t)
	for _, r := range []string{"lib_a", "lib_b"} {
		v, err := pg.ScramVerifier(secrets.GeneratePassword(24))
		if err != nil {
			t.Fatal(err)
		}
		if err := cl.CreateLibraryRole(ctx, r, pg.RoleOptions{Verifier: v, Login: r == "lib_a", ConnLimit: -1}); err != nil {
			t.Fatal(err)
		}
	}
	canLogin := func(r string) bool {
		p, _ := cl.Pool()
		var ok bool
		if err := p.QueryRow(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, r).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	changed, err := cl.SetRolesLogin(ctx, []string{"lib_a", "lib_b", "lib_missing"}, false)
	if err != nil || !slices.Equal(changed, []string{"lib_a"}) {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	if canLogin("lib_a") || canLogin("lib_b") {
		t.Fatal("roles can still log in")
	}
	changed, err = cl.SetRolesLogin(ctx, []string{"lib_a", "lib_b"}, true)
	if err != nil || !slices.Equal(changed, []string{"lib_a", "lib_b"}) {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	if !canLogin("lib_a") || !canLogin("lib_b") {
		t.Fatal("roles cannot log in")
	}
	if changed, err := cl.SetRolesLogin(ctx, nil, true); err != nil || changed != nil {
		t.Fatalf("empty set: %v %v", changed, err)
	}
}
