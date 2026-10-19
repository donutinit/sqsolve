// SPDX-License-Identifier: AGPL-3.0-or-later

package pgconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHBAOrderAndContent(t *testing.T) {
	out, err := HBA(HBAOptions{
		MaintenanceDB: true,
		Libraries: []HBALibrary{
			{DB: "zeta", Roles: []string{"zeta"}},
			{DB: "cliente_x", Roles: []string{"cliente_x__ana", "cliente_x"}, CIDRs: []string{"192.168.1.7", "10.0.0.0/8"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	reject := -1
	firstLib := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "host\tall\tpostgres,sqsolve_admin\tall\treject") {
			reject = i
		}
		if firstLib < 0 && strings.Contains(l, "scram-sha-256") && strings.HasPrefix(l, "host") {
			firstLib = i
		}
	}
	if reject < 0 || firstLib < 0 || reject > firstLib {
		t.Fatalf("admin reject must come before library rules:\n%s", out)
	}
	if !strings.HasSuffix(lines[len(lines)-1], "host\tall\tall\tall\treject") {
		t.Fatalf("last line must reject everything:\n%s", out)
	}
	for _, want := range []string{
		"host\tcliente_x\tcliente_x,cliente_x__ana\t192.168.1.7/32\tscram-sha-256",
		"host\tcliente_x\tcliente_x,cliente_x__ana\t10.0.0.0/8\tscram-sha-256",
		"host\tpostgres\tcliente_x,cliente_x__ana\t10.0.0.0/8\tscram-sha-256",
		"host\tzeta\tzeta\tall\tscram-sha-256",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+") {
		t.Error("group syntax must not be used")
	}
	if strings.Index(out, "cliente_x") > strings.Index(out, "\tzeta\t") {
		t.Error("libraries must be sorted")
	}
}

func TestHBARejectsAdminAndBadInput(t *testing.T) {
	if _, err := HBA(HBAOptions{Libraries: []HBALibrary{{DB: "a_b", Roles: []string{"sqsolve_admin"}}}}); err == nil {
		t.Fatal("admin role accepted")
	}
	if _, err := HBA(HBAOptions{Libraries: []HBALibrary{{DB: "a b", Roles: []string{"x"}}}}); err == nil {
		t.Fatal("bad db accepted")
	}
	if _, err := HBA(HBAOptions{Libraries: []HBALibrary{{DB: "abc", Roles: []string{"abc"}, CIDRs: []string{"nope"}}}}); err == nil {
		t.Fatal("bad cidr accepted")
	}
}

func TestHBATLS(t *testing.T) {
	out, _ := HBA(HBAOptions{RequireTLS: true, Libraries: []HBALibrary{{DB: "abc", Roles: []string{"abc"}}}})
	if !strings.Contains(out, "hostssl\tabc\tabc\tall") {
		t.Fatal(out)
	}
}

func TestWriteHBAKeepsPrev(t *testing.T) {
	dir := t.TempDir()
	if changed, err := WriteHBA(dir, "one\n"); err != nil || !changed {
		t.Fatal(err)
	}
	if changed, _ := WriteHBA(dir, "one\n"); changed {
		t.Fatal("same content reported as changed")
	}
	WriteHBA(dir, "two\n")
	if b, _ := os.ReadFile(filepath.Join(dir, "pg_hba.conf.prev")); string(b) != "one\n" {
		t.Fatalf("prev %q", b)
	}
	RestorePreviousHBA(dir)
	if b, _ := os.ReadFile(filepath.Join(dir, "pg_hba.conf")); string(b) != "one\n" {
		t.Fatalf("restored %q", b)
	}
}

func TestPostgresConf(t *testing.T) {
	s := PostgresConf(Params{DataDir: "/d", ConfDir: "/c", SocketDir: "/s", Port: 5432, Listen: "*", MaxConnections: 200, MemoryMB: 16384, SSL: true})
	for _, want := range []string{"hba_file                     = '/c/pg_hba.conf'", "shared_buffers               = 2048MB", "ssl                          = on", "password_encryption          = scram-sha-256"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestSelfSigned(t *testing.T) {
	c, k, err := SelfSigned("sqsolve", []string{"192.168.1.10", "nas.local"}, 24*time.Hour)
	if err != nil || len(k) == 0 {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "c.pem")
	os.WriteFile(p, c, 0o644)
	info, err := ReadCert(p)
	if err != nil || info.Subject != "sqsolve" || len(info.Fingerprint) != 95 {
		t.Fatalf("%+v %v", info, err)
	}
}
