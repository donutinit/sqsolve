// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestChecksumIgnoresComments(t *testing.T) {
	a := sqlChecksum([]byte("-- SPDX-License-Identifier: AGPL-3.0-or-later\nCREATE TABLE x (id INTEGER);\n"))
	b := sqlChecksum([]byte("-- SPDX-License-Identifier: AGPL-3.0-only\n\n  CREATE TABLE x (id INTEGER);"))
	c := sqlChecksum([]byte("CREATE TABLE x (id TEXT);"))
	if a != b {
		t.Fatal("a comment changed the checksum")
	}
	if a == c {
		t.Fatal("a statement change kept the checksum")
	}
}

func TestLegacyChecksumIsUpgraded(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sqsolve.db")
	db, err := Open(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	all, _ := loadMigrations()
	db.ExecContext(ctx, `UPDATE schema_migrations SET checksum = ? WHERE version = ?`, all[0].legacy, all[0].version)
	db.Close()
	db, err = Open(ctx, path, "")
	if err != nil {
		t.Fatalf("a database migrated with the old checksum must open: %v", err)
	}
	defer db.Close()
	var got string
	db.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE version = ?`, all[0].version).Scan(&got)
	if got != all[0].checksum {
		t.Fatal("the stored checksum was not upgraded")
	}
	db.ExecContext(ctx, `UPDATE schema_migrations SET checksum = 'otra' WHERE version = ?`, all[0].version)
	db.Close()
	if _, err := Open(ctx, path, ""); err == nil || !strings.Contains(err.Error(), "SQS-X011") {
		t.Fatalf("a different migration must be refused: %v", err)
	}
}
