// SPDX-License-Identifier: AGPL-3.0-only

package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	if err := WriteFileAtomic(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "one" {
		t.Fatal(string(b))
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm %o", st.Mode().Perm())
	}
}

func TestWriteFileAtomicCrashKeepsOld(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a")
	WriteFileAtomic(p, []byte("old"), 0o600)
	for _, step := range []string{"write", "rename"} {
		failpoint = func(s string) error {
			if s == step {
				return errors.New("crash")
			}
			return nil
		}
		if err := WriteFileAtomic(p, []byte("new"), 0o600); err == nil {
			t.Fatal("expected failure")
		}
		if b, _ := os.ReadFile(p); string(b) != "old" {
			t.Fatalf("after crash at %s: %q", step, b)
		}
	}
	failpoint = nil
}

func TestMarker(t *testing.T) {
	dir := t.TempDir()
	if err := CheckMarker(dir, "A"); !errors.Is(err, ErrNoMarker) {
		t.Fatal(err)
	}
	WriteMarker(dir, Marker{InstanceID: "A", Volume: "backups", CreatedAt: time.Now()})
	if err := CheckMarker(dir, "A"); err != nil {
		t.Fatal(err)
	}
	if err := CheckMarker(dir, "B"); err == nil {
		t.Fatal("other instance accepted")
	}
}

func TestWithin(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "x")
	os.WriteFile(in, nil, 0o600)
	outside := t.TempDir()
	link := filepath.Join(dir, "link")
	os.Symlink(outside, link)
	if ok, _ := Within(dir, in); !ok {
		t.Fatal("inside not detected")
	}
	if ok, _ := Within(dir, link); ok {
		t.Fatal("symlink escape not detected")
	}
}

func TestEmptyAndStatfs(t *testing.T) {
	dir := t.TempDir()
	if e, _ := IsEmptyDir(dir); !e {
		t.Fatal("should be empty")
	}
	os.WriteFile(filepath.Join(dir, "f"), nil, 0o600)
	if e, _ := IsEmptyDir(dir); e {
		t.Fatal("should not be empty")
	}
	s, err := Statfs(dir)
	if err != nil || s.Total == 0 {
		t.Fatal(err)
	}
}
