// SPDX-License-Identifier: AGPL-3.0-only

package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Statfs reports the capacity of a real filesystem, whose used share is
// between 0 and 100 percent; a missing folder is an error. An empty Space
// (no capacity known) is 0 % used rather than a division by zero.
func TestStatfsAndUsedPercent(t *testing.T) {
	s, err := Statfs(t.TempDir())
	if err != nil || s.Total == 0 || s.Free > s.Total {
		t.Fatalf("%+v %v", s, err)
	}
	if p := s.UsedPercent(); p < 0 || p > 100 {
		t.Fatalf("used %f%%", p)
	}
	if p := (Space{Total: 200, Free: 50}).UsedPercent(); p != 75 {
		t.Fatalf("used %f%%, want 75", p)
	}
	if p := (Space{}).UsedPercent(); p != 0 {
		t.Fatalf("unknown capacity used %f%%", p)
	}
	if _, err := Statfs(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing folder: %v", err)
	}
}

// IsEmptyDir reports a folder that cannot be opened as an error, not as
// empty.
func TestIsEmptyDirErrors(t *testing.T) {
	if _, err := IsEmptyDir(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	notRoot(t)
	dir := filepath.Join(t.TempDir(), "locked")
	os.Mkdir(dir, 0o000)
	defer os.Chmod(dir, 0o700)
	if empty, err := IsEmptyDir(dir); empty || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unreadable: %v %v", empty, err)
	}
}

// Within compares resolved paths component by component: the folder itself
// and a name that only starts with two dots are inside, a sibling whose
// name extends the folder's is not, and paths that do not exist are errors.
func TestWithinEdges(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "vol")
	os.Mkdir(dir, 0o700)
	sibling := filepath.Join(base, "volume2")
	os.Mkdir(sibling, 0o700)
	dots := filepath.Join(dir, "..data")
	os.WriteFile(dots, nil, 0o600)
	for _, tc := range []struct {
		path string
		want bool
	}{{dir, true}, {dots, true}, {sibling, false}, {base, false}} {
		got, err := Within(dir, tc.path)
		if err != nil || got != tc.want {
			t.Errorf("Within(%s, %s) = %v, %v; want %v", dir, tc.path, got, err, tc.want)
		}
	}
	if _, err := Within(filepath.Join(base, "missing"), dots); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing folder: %v", err)
	}
	if _, err := Within(dir, filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing path: %v", err)
	}
	// A relative folder and an absolute path cannot be compared: not inside.
	if got, err := Within(".", dots); got || err != nil {
		t.Errorf("relative against absolute: %v %v", got, err)
	}
}
