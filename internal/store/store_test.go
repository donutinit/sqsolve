// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenMigrateSettings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(ctx, filepath.Join(dir, "s.db"), filepath.Join(dir, "bk"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.IntegrityCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, "a", map[string]int{"x": 1}, "test"); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	ok, err := db.GetSetting(ctx, "a", &got)
	if err != nil || !ok || got["x"] != 1 {
		t.Fatalf("%v %v %v", ok, err, got)
	}
	db.Close()
	// Reopen: no pending migrations, no backup copy.
	db, err = Open(ctx, filepath.Join(dir, "s.db"), filepath.Join(dir, "bk"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := os.Stat(filepath.Join(dir, "bk")); !os.IsNotExist(err) {
		t.Fatal("backup dir should not exist without pending migrations")
	}
	if err := db.BackupTo(ctx, filepath.Join(dir, "copy.db")); err != nil {
		t.Fatal(err)
	}
}

func TestTimeFormatSorts(t *testing.T) {
	a := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	b := a.Add(100 * time.Millisecond)
	if !(T(a) < T(b)) {
		t.Fatalf("%s !< %s", T(a), T(b))
	}
	if !ParseT(T(b)).Equal(b) {
		t.Fatal("round trip")
	}
}
