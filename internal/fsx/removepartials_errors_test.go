// SPDX-License-Identifier: AGPL-3.0-only

package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A folder without .partial files is left as it is, and a symbolic link
// named like one is not a leftover of a write: it is kept.
func TestRemovePartialsKeepsOtherEntries(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, "a.db"), nil, 0o600)
	os.Symlink(target, filepath.Join(dir, "link.partial"))
	n, err := RemovePartials(dir)
	if n != 0 || err != nil {
		t.Fatalf("removed %d: %v", n, err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 2 {
		t.Fatalf("left %v", left)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("the link target was removed")
	}
}

// A folder that cannot be listed, or a .partial file that cannot be
// removed, is reported.
func TestRemovePartialsErrors(t *testing.T) {
	notRoot(t)
	locked := filepath.Join(t.TempDir(), "locked")
	os.Mkdir(locked, 0o000)
	defer os.Chmod(locked, 0o700)
	if n, err := RemovePartials(locked); n != 0 || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unlisted: %d %v", n, err)
	}
	ro := filepath.Join(t.TempDir(), "ro")
	os.Mkdir(ro, 0o700)
	os.WriteFile(filepath.Join(ro, "a.partial"), nil, 0o600)
	os.Chmod(ro, 0o500)
	defer os.Chmod(ro, 0o700)
	if n, err := RemovePartials(ro); n != 0 || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read-only: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(ro, "a.partial")); err != nil {
		t.Fatal("the file is gone")
	}
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if _, err := RemovePartials(file); err == nil {
		t.Fatal("a file is not a folder")
	}
}
