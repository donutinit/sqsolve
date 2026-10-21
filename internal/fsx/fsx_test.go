// SPDX-License-Identifier: AGPL-3.0-only

package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// Review of task 4: the volume field was never checked, so swapped mounts of
// the same installation passed. CheckMarker guards the backups volume.
func TestMarkerOfAnotherVolume(t *testing.T) {
	dir := t.TempDir()
	WriteMarker(dir, Marker{InstanceID: "A", Volume: VolumeDB, CreatedAt: time.Now()})
	err := CheckMarker(dir, "A")
	if err == nil || !strings.Contains(err.Error(), "SQS-S019") || !strings.Contains(err.Error(), "db volume") {
		t.Fatalf("a db marker accepted as the backups volume: %v", err)
	}
	if err := CheckVolume(dir, "A", VolumeDB); err != nil {
		t.Fatal(err)
	}
	if err := CheckVolume(dir, "B", VolumeDB); err == nil || !strings.Contains(err.Error(), "SQS-S003") {
		t.Fatalf("another installation: %v", err)
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

func readPair(t *testing.T, a, b string) string {
	t.Helper()
	x, _ := os.ReadFile(a)
	y, _ := os.ReadFile(b)
	return string(x) + "+" + string(y)
}

func TestWritePair(t *testing.T) {
	dir := t.TempDir()
	key, crt := filepath.Join(dir, "k"), filepath.Join(dir, "c")
	if err := WritePair(key, []byte("k1"), 0o600, crt, []byte("c1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readPair(t, key, crt); got != "k1+c1" {
		t.Fatal(got)
	}
	for path, want := range map[string]os.FileMode{key: 0o600, crt: 0o644} {
		if st, _ := os.Stat(path); st.Mode().Perm() != want {
			t.Errorf("%s: %04o, want %04o", path, st.Mode().Perm(), want)
		}
	}
	if changed, err := RecoverPair(key, crt); changed || err != nil {
		t.Fatalf("nothing to recover after a complete write: %v %v", changed, err)
	}
}

// A crash between the two renames used to leave a new key next to the old
// certificate (audit finding, task 4 item 6): RecoverPair finishes the
// write; a crash before the renames keeps the old pair.
func TestWritePairCrash(t *testing.T) {
	defer func() { failpoint = nil }()
	for _, tc := range []struct{ step, want string }{{"pair-first", "k1+c1"}, {"pair-second", "k2+c2"}} {
		dir := t.TempDir()
		key, crt := filepath.Join(dir, "k"), filepath.Join(dir, "c")
		if err := WritePair(key, []byte("k1"), 0o600, crt, []byte("c1"), 0o644); err != nil {
			t.Fatal(err)
		}
		failpoint = func(s string) error {
			if s == tc.step {
				return errors.New("crash")
			}
			return nil
		}
		if err := WritePair(key, []byte("k2"), 0o600, crt, []byte("c2"), 0o644); err == nil {
			t.Fatal("expected the simulated crash")
		}
		failpoint = nil
		changed, err := RecoverPair(key, crt)
		if err != nil || !changed {
			t.Fatalf("%s: recover: %v %v", tc.step, changed, err)
		}
		if got := readPair(t, key, crt); got != tc.want {
			t.Errorf("after a crash at %s: %s, want %s", tc.step, got, tc.want)
		}
		if m, _ := filepath.Glob(filepath.Join(dir, "*.partial")); len(m) != 0 {
			t.Errorf("leftovers after recovering: %v", m)
		}
	}
}

func TestRemovePartials(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.db.partial", "b.db", "c.jsonl.partial"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o600)
	}
	os.Mkdir(filepath.Join(dir, "sub.partial"), 0o700)
	n, err := RemovePartials(dir)
	if err != nil || n != 2 {
		t.Fatalf("removed %d: %v", n, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 2 {
		t.Fatalf("left %v", left)
	}
	if n, err := RemovePartials(filepath.Join(dir, "missing")); n != 0 || err != nil {
		t.Fatalf("a missing folder is not an error: %d %v", n, err)
	}
}
