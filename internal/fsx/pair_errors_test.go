// SPDX-License-Identifier: AGPL-3.0-only

package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// pairDir holds a complete old pair k1+c1.
func pairDir(t *testing.T) (dir, key, crt string) {
	t.Helper()
	dir = t.TempDir()
	key, crt = filepath.Join(dir, "k"), filepath.Join(dir, "c")
	if err := WritePair(key, []byte("k1"), 0o600, crt, []byte("c1"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, key, crt
}

func partials(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*.partial"))
	return m
}

// A path whose parent is a file cannot be checked: RecoverPair reports it
// instead of saying there is nothing to do.
func TestRecoverPairReportsLstatErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	changed, err := RecoverPair(filepath.Join(file, "k"), filepath.Join(file, "c"))
	if changed || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("%v %v", changed, err)
	}
}

// When second's .partial file cannot be removed, first's is kept too: a
// first .partial without the second one would mean the pair is complete.
func TestDiscardRemovesSecondFirst(t *testing.T) {
	dir := t.TempDir()
	key, crt := filepath.Join(dir, "k"), filepath.Join(dir, "c")
	os.WriteFile(key+".partial", []byte("k2"), 0o600)
	os.MkdirAll(filepath.Join(crt+".partial", "x"), 0o700)
	if _, err := RecoverPair(key, crt); err == nil || !strings.Contains(err.Error(), "directory not empty") {
		t.Fatalf("want the removal error, got %v", err)
	}
	if _, err := os.Stat(key + ".partial"); err != nil {
		t.Fatal("first's .partial file was removed before second's")
	}
}

// A pair whose second rename is pending cannot be completed while its name
// is taken: RecoverPair reports it and keeps the .partial file for the next
// try, which completes the pair.
func TestRecoverPairRetriesTheSecondRename(t *testing.T) {
	dir := t.TempDir()
	key, crt := filepath.Join(dir, "k"), filepath.Join(dir, "c")
	os.WriteFile(key, []byte("k2"), 0o600)
	os.WriteFile(crt+".partial", []byte("c2"), 0o644)
	os.MkdirAll(filepath.Join(crt, "x"), 0o700)
	if changed, err := RecoverPair(key, crt); changed || err == nil {
		t.Fatalf("%v %v", changed, err)
	}
	if _, err := os.Stat(crt + ".partial"); err != nil {
		t.Fatal("the pending half was lost")
	}
	os.RemoveAll(crt)
	if changed, err := RecoverPair(key, crt); !changed || err != nil {
		t.Fatalf("%v %v", changed, err)
	}
	if got := readPair(t, key, crt); got != "k2+c2" {
		t.Fatal(got)
	}
}

// A pending recovery that fails stops WritePair before it writes anything.
func TestWritePairStopsOnAFailedRecovery(t *testing.T) {
	_, key, crt := pairDir(t)
	os.WriteFile(key+".partial", []byte("k2"), 0o600)
	os.MkdirAll(filepath.Join(crt+".partial", "x"), 0o700)
	if err := WritePair(key, []byte("k3"), 0o600, crt, []byte("c3"), 0o644); err == nil || !strings.Contains(err.Error(), "directory not empty") {
		t.Fatalf("got %v", err)
	}
	if got := readPair(t, key, crt); got != "k1+c1" {
		t.Fatalf("the pair is %s", got)
	}
	if b, _ := os.ReadFile(key + ".partial"); string(b) != "k2" {
		t.Fatalf("first's .partial file was rewritten: %q", b)
	}
}

// A half that cannot be written leaves the old pair in place and no
// .partial file: first in a missing folder, then second in a folder that
// cannot be written.
func TestWritePairWriteErrors(t *testing.T) {
	notRoot(t)
	dir, key, crt := pairDir(t)
	missing := filepath.Join(dir, "missing")
	if err := WritePair(filepath.Join(missing, "k"), []byte("k2"), 0o600, crt, []byte("c2"), 0o644); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first: %v", err)
	}
	ro := filepath.Join(t.TempDir(), "ro")
	os.Mkdir(ro, 0o500)
	defer os.Chmod(ro, 0o700)
	if err := WritePair(key, []byte("k2"), 0o600, filepath.Join(ro, "c"), []byte("c2"), 0o644); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("second: %v", err)
	}
	if got := readPair(t, key, crt); got != "k1+c1" {
		t.Fatalf("the pair is %s", got)
	}
	if left := partials(t, dir); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
}

// When first cannot take its name, both .partial files are discarded and
// the old pair stays.
func TestWritePairFirstRenameError(t *testing.T) {
	dir := t.TempDir()
	key, crt := filepath.Join(dir, "k"), filepath.Join(dir, "c")
	os.MkdirAll(filepath.Join(key, "x"), 0o700)
	os.WriteFile(crt, []byte("c1"), 0o644)
	err := WritePair(key, []byte("k2"), 0o600, crt, []byte("c2"), 0o644)
	var le *os.LinkError
	if !errors.As(err, &le) || le.Op != "rename" {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(crt); string(b) != "c1" {
		t.Fatalf("second is %q", b)
	}
	if left := partials(t, dir); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
}

// When second cannot take its name after first did, its .partial file is
// kept so that RecoverPair completes the pair once the name is free.
func TestWritePairSecondRenameError(t *testing.T) {
	dir := t.TempDir()
	key, crt := filepath.Join(dir, "k"), filepath.Join(dir, "c")
	os.WriteFile(key, []byte("k1"), 0o600)
	os.MkdirAll(filepath.Join(crt, "x"), 0o700)
	err := WritePair(key, []byte("k2"), 0o600, crt, []byte("c2"), 0o644)
	var le *os.LinkError
	if !errors.As(err, &le) || le.Op != "rename" {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(key); string(b) != "k2" {
		t.Fatalf("first is %q", b)
	}
	if b, _ := os.ReadFile(crt + ".partial"); string(b) != "c2" {
		t.Fatalf("second's .partial is %q", b)
	}
	os.RemoveAll(crt)
	if changed, err := RecoverPair(key, crt); !changed || err != nil {
		t.Fatalf("%v %v", changed, err)
	}
	if got := readPair(t, key, crt); got != "k2+c2" {
		t.Fatal(got)
	}
}
