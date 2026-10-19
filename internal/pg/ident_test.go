// SPDX-License-Identifier: AGPL-3.0-or-later

package pg

import (
	"strings"
	"testing"
	"time"
)

func TestValidateSlug(t *testing.T) {
	good := []string{"abc", "cliente_x", "a1_2_3", strings.Repeat("a", 40)}
	bad := []string{"", "ab", "Abc", "1abc", "_abc", "a-b", "a b", "a__b", "pg_x", "sqsolve_x", "template1",
		"postgres", "postgresx", strings.Repeat("a", 41), "abc;drop", "ñandu", "abc\x00"}
	for _, s := range good {
		if err := ValidateSlug(s); err != nil {
			t.Errorf("%q rejected: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := ValidateSlug(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestAuxNamesFit(t *testing.T) {
	slug := strings.Repeat("a", 40)
	now := time.Now()
	for _, n := range []string{RestoreDB(slug, "abcdefgh"), PrevDB(slug, now), TrashDB(slug, now), VerifyDB("abcdefgh"), MemberRole(slug, strings.Repeat("b", 20))} {
		if len(n) > 63 {
			t.Errorf("%q is %d bytes", n, len(n))
		}
		if err := ValidateIdent(n); err != nil {
			t.Errorf("%q invalid: %v", n, err)
		}
		if !IsAuxiliary(n) {
			t.Errorf("%q not auxiliary", n)
		}
	}
	if IsAuxiliary("cliente_x") {
		t.Error("slug detected as auxiliary")
	}
}

func TestIdentQuotes(t *testing.T) {
	q, err := Ident("cliente_x")
	if err != nil || q != `"cliente_x"` {
		t.Fatalf("%q %v", q, err)
	}
	if _, err := Ident(`x"; DROP DATABASE y; --`); err == nil {
		t.Fatal("injection accepted")
	}
}

func TestLiteral(t *testing.T) {
	l, err := Literal("it's")
	if err != nil || l != "'it''s'" {
		t.Fatal(l, err)
	}
	if _, err := Literal(`a\b`); err == nil {
		t.Fatal("backslash accepted")
	}
}

func FuzzValidateSlug(f *testing.F) {
	for _, s := range []string{"abc", "a__b", "pg_x", "x\"y", "café"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ValidateSlug(s) != nil {
			return
		}
		if len(s) > 40 || strings.Contains(s, "__") {
			t.Fatalf("accepted %q", s)
		}
		for _, r := range s {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
				t.Fatalf("accepted rune %q in %q", r, s)
			}
		}
		if _, err := Ident(s); err != nil {
			t.Fatalf("valid slug not an ident: %q", s)
		}
	})
}

func TestScramVerifierKnownVector(t *testing.T) {
	// Only the format is checked here; the integration test logs in with a
	// password set through a verifier, which proves the keys are right.
	v, err := scramVerifier("pencil", []byte("0123456789abcdef"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v, "SCRAM-SHA-256$4096:MDEyMzQ1Njc4OWFiY2RlZg==$") || strings.Count(v, ":") != 2 {
		t.Fatalf("bad format %s", v)
	}
}
