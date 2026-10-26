// SPDX-License-Identifier: AGPL-3.0-only

package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// SealFile moves a finished file to its final name with its content and
// mode, replacing an older file there.
func TestSealFileRenamesIntoPlace(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "dump.partial"), filepath.Join(dir, "dump")
	os.WriteFile(src, []byte("new dump"), 0o640)
	os.WriteFile(dst, []byte("old dump"), 0o600)
	if err := SealFile(src, dst); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	st, _ := os.Stat(dst)
	if string(b) != "new dump" || st.Mode().Perm() != 0o640 {
		t.Fatalf("%q %04o", b, st.Mode().Perm())
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("the source is still there")
	}
}

// A source that does not exist is an error and creates nothing.
func TestSealFileMissingSource(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dump")
	if err := SealFile(filepath.Join(dir, "nothing.partial"), dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("a destination was created")
	}
}

// A destination that cannot be replaced (a folder with files) is an error
// and the source stays where it was, so the caller can clean it up.
func TestSealFileRenameError(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "dump.partial"), filepath.Join(dir, "dump")
	os.WriteFile(src, []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(dst, "inside"), 0o700)
	err := SealFile(src, dst)
	var le *os.LinkError
	if !errors.As(err, &le) || le.Op != "rename" {
		t.Fatalf("want the rename error, got %v", err)
	}
	if b, _ := os.ReadFile(src); string(b) != "x" {
		t.Fatal("the source was lost")
	}
}
