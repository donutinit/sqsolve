// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"errors"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/compat"
)

func fullSchema(t *testing.T) (*Model, schema) {
	t.Helper()
	m, err := DefaultModel()
	if err != nil {
		t.Fatal(err)
	}
	s := schema{}
	for table, cols := range m.Tables {
		s[table] = map[string]family{}
		for c, typ := range cols {
			s[table][c] = familyOf(typ)
		}
	}
	return m, s
}

func TestCheckContract(t *testing.T) {
	m, s := fullSchema(t)
	if err := checkContract(m, s); err != nil {
		t.Fatalf("full schema rejected: %v", err)
	}
	delete(s, "SM_Project_FolderMap") // guard tables are optional
	if err := checkContract(m, s); err != nil {
		t.Fatalf("missing guard table rejected: %v", err)
	}
	s["SM_Project"]["ProjectName"] = famInt
	err := checkContract(m, s)
	if !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), "SM_Project.ProjectName es entero y se esperaba texto") {
		t.Errorf("type mismatch: %v", err)
	}
	s["SM_Project"]["ProjectName"] = famText
	delete(s["SM_Project"], "ProjectVersion")
	if err := checkContract(m, s); !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), "SM_Project.ProjectVersion") {
		t.Errorf("missing column: %v", err)
	}
	delete(s, "Gallery::GyStill")
	if err := checkContract(m, s); !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), "tabla Gallery::GyStill") || !strings.Contains(err.Error(), "SQS-E001") {
		t.Errorf("missing table: %v", err)
	}
	for table := range s {
		delete(s, table)
	}
	if err := checkContract(m, s); err == nil || !strings.Contains(err.Error(), " más") {
		t.Errorf("many missing tables should be summarized: %v", err)
	}
}

func TestValidID(t *testing.T) {
	id, ok := ValidID("ABCDEF01-2345-4789-ABCD-EF0123456789")
	if !ok || id != "abcdef01-2345-4789-abcd-ef0123456789" {
		t.Errorf("%q %v", id, ok)
	}
	for _, bad := range []string{"", "x", "abcdef01-2345-4789-abcd-ef012345678", "abcdef01-2345-4789-abcd-ef0123456789'", "abcdef012345478 9abcdef0123456789"} {
		if _, ok := ValidID(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNames(t *testing.T) {
	if got := SafeName(`a/b\c:d*e?f"g<h>i|j`, "x"); got != "a_b_c_d_e_f_g_h_i_j" {
		t.Errorf("SafeName = %q", got)
	}
	if got := SafeName("  ", "x"); got != "x" {
		t.Errorf("blank = %q", got)
	}
	if got := SafeName(".oculto. ", "x"); got != "oculto" {
		t.Errorf("dots = %q", got)
	}
	if got := SafeName("a\x00b\tc", "x"); got != "abc" {
		t.Errorf("control chars = %q", got)
	}
	if got := SafeName(strings.Repeat("é", 100), "x"); len(got) != 120 {
		t.Errorf("long name = %d bytes", len(got))
	}
	if got := StillFileName("1.1", "4f0ccc3c-e806-4fa0-9742-5f10131c7b61"); got != "1.1-4f0ccc3c.drx" {
		t.Errorf("StillFileName = %q", got)
	}
	if got := StillFileName("", "4f0ccc3c-e806-4fa0-9742-5f10131c7b61"); got != "still-4f0ccc3c.drx" {
		t.Errorf("StillFileName without label = %q", got)
	}
}

func TestHeader(t *testing.T) {
	if got := (&Session{}).Header(17); got != "" {
		t.Errorf("unknown fingerprint header = %q", got)
	}
	s := &Session{KnownOK: true, Known: compat.Known{AppVersion: "21.1.1.0010", ProjectVersion: 17}}
	if got := s.Header(0); got != `<!--DbAppVer="21.1.1.0010" DbPrjVer="17"-->` {
		t.Errorf("registry version header = %q", got)
	}
	if got := s.Header(15); got != `<!--DbAppVer="21.1.1.0010" DbPrjVer="15"-->` {
		t.Errorf("explicit version header = %q", got)
	}
}
