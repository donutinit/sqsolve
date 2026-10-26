// SPDX-License-Identifier: AGPL-3.0-only

package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func notRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
}

// The mode is exactly the one asked for, whatever the umask, and a write
// over an existing file replaces its content and its mode.
func TestWriteFileAtomicSetsTheExactMode(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := WriteFileAtomic(p, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o644 {
		t.Fatalf("%04o despite the umask, want 0644", st.Mode().Perm())
	}
	if err := WriteFileAtomic(p, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	st, _ := os.Stat(p)
	if string(b) != "second" || st.Mode().Perm() != 0o600 {
		t.Fatalf("%q %04o", b, st.Mode().Perm())
	}
}

// A .partial file left by a crash is reused and does not end up in the
// result: its old content and mode are replaced.
func TestWriteFileAtomicOverALeftoverPartial(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.json")
	os.WriteFile(p+".partial", []byte("a much longer leftover from a crash"), 0o666)
	if err := WriteFileAtomic(p, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	st, _ := os.Stat(p)
	if string(b) != "new" || st.Mode().Perm() != 0o600 {
		t.Fatalf("%q %04o", b, st.Mode().Perm())
	}
	if _, err := os.Stat(p + ".partial"); !os.IsNotExist(err) {
		t.Fatal("the .partial file is still there")
	}
}

// A write into a folder that does not exist or cannot be written fails
// without leaving anything behind.
func TestWriteFileAtomicFolderErrors(t *testing.T) {
	notRoot(t)
	dir := t.TempDir()
	if err := WriteFileAtomic(filepath.Join(dir, "missing", "a"), []byte("x"), 0o600); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing folder: %v", err)
	}
	ro := filepath.Join(dir, "ro")
	os.Mkdir(ro, 0o500)
	defer os.Chmod(ro, 0o700)
	if err := WriteFileAtomic(filepath.Join(ro, "a"), []byte("x"), 0o600); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read-only folder: %v", err)
	}
	if left, _ := os.ReadDir(ro); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
}

// When the new file cannot take the name (a folder with that name is in
// the way) the error is returned, the .partial file is removed and the
// folder is untouched.
func TestWriteFileAtomicRenameError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a")
	os.MkdirAll(filepath.Join(p, "inside"), 0o700)
	err := WriteFileAtomic(p, []byte("x"), 0o600)
	var le *os.LinkError
	if !errors.As(err, &le) || le.Op != "rename" {
		t.Fatalf("want the rename error, got %v", err)
	}
	if _, err := os.Stat(p + ".partial"); !os.IsNotExist(err) {
		t.Fatal("the .partial file was left behind")
	}
	if _, err := os.Stat(filepath.Join(p, "inside")); err != nil {
		t.Fatal("the folder in the way was touched")
	}
}
