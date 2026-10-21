// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"
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
	db.ExecContext(ctx, `UPDATE schema_migrations SET checksum = 'other' WHERE version = ?`, all[0].version)
	db.Close()
	if _, err := Open(ctx, path, ""); err == nil || !strings.Contains(err.Error(), "SQS-X011") {
		t.Fatalf("a different migration must be refused: %v", err)
	}
}

func TestErrNotFoundIsEnglish(t *testing.T) {
	if ErrNotFound.Error() != "not found" {
		t.Fatalf("%q", ErrNotFound.Error())
	}
}

// failingMigration leaves a database whose migration 1 is pending again and
// fails every time, like a release with a broken migration.
func failingMigration(t *testing.T, path string) {
	t.Helper()
	db, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 1`); err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func copies(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "sqsolve-before-*.db"))
	return m
}

// Audit M5: a migration that fails on every start used to write a full copy
// of the database on every start (a restart loop fills the app volume).
func TestMigrationCopyNotRepeated(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path, bk := filepath.Join(dir, "sqsolve.db"), filepath.Join(dir, "state-backups")
	failingMigration(t, path)
	defer func() { now = time.Now }()
	for i := 0; i < 3; i++ {
		// Restarts a minute apart, as Docker does with a failing service.
		at := time.Now().Add(time.Duration(i) * time.Minute)
		now = func() time.Time { return at }
		if _, err := Open(ctx, path, bk); err == nil {
			t.Fatal("the broken migration was applied")
		}
	}
	if got := copies(t, bk); len(got) != 1 || !strings.Contains(got[0], "sqsolve-before-0001-") {
		t.Fatalf("want one copy for the pending version, got %v", got)
	}
}

func TestMigrationCopiesKeepNewestFive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path, bk := filepath.Join(dir, "sqsolve.db"), filepath.Join(dir, "state-backups")
	os.MkdirAll(bk, 0o700)
	for i := 1; i <= 6; i++ {
		os.WriteFile(filepath.Join(bk, fmt.Sprintf("sqsolve-before-0000-2025010%dT000000Z.db", i)), []byte("x"), 0o600)
	}
	os.WriteFile(filepath.Join(bk, "sqsolve-replaced-20250101T000000Z.db"), []byte("x"), 0o600)
	failingMigration(t, path)
	Open(ctx, path, bk)
	got := copies(t, bk)
	if len(got) != 5 {
		t.Fatalf("want the newest 5 copies, got %v", got)
	}
	if !strings.Contains(strings.Join(got, " "), "sqsolve-before-0001-") || strings.Contains(strings.Join(got, " "), "20250101") || strings.Contains(strings.Join(got, " "), "20250102") {
		t.Fatalf("the oldest copies must go first: %v", got)
	}
	if _, err := os.Stat(filepath.Join(bk, "sqsolve-replaced-20250101T000000Z.db")); err != nil {
		t.Fatal("pruning touched a file that is not a migration copy")
	}
}

// Audit Low: BackupTo ignored the fsync error and did not sync the folder.
func TestBackupToSealsTheCopy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(ctx, filepath.Join(dir, "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dst := filepath.Join(dir, "copy.db")
	if err := db.BackupTo(ctx, dst); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dst); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("copy: %v %v", st, err)
	}
	if _, err := os.Stat(dst + ".partial"); !os.IsNotExist(err) {
		t.Fatal("the .partial file was left behind")
	}
	err = db.BackupTo(ctx, filepath.Join(dir, "missing", "copy.db"))
	if err == nil || !strings.Contains(err.Error(), "SQS-X030 could not copy the database to") {
		t.Fatalf("want SQS-X030 for a copy that cannot be written, got %v", err)
	}
	if err := db.BackupTo(ctx, filepath.Join(dir, "a\x00b")); err == nil || !strings.Contains(err.Error(), "SQS-X030 could not copy the database to") || !strings.Contains(err.Error(), "invalid path") {
		t.Fatalf("want SQS-X030 for an invalid path, got %v", err)
	}
	// A quote is a valid file name now that the path is a parameter.
	if err := db.BackupTo(ctx, filepath.Join(dir, "it's.db")); err != nil {
		t.Fatal(err)
	}
}

// Audit Low: a new sqsolve.db (and its -wal and -shm) was 0644 until the next
// start.
func TestNewDatabaseIsPrivate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sqsolve.db")
	db, err := Open(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(ctx, "x", 1, "test"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s: %04o, want 0600", filepath.Base(p), st.Mode().Perm())
		}
	}
}

func TestOpenReportsAFileItCannotCreate(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing", "sqsolve.db"), "")
	if err == nil || !strings.Contains(err.Error(), "SQS-X031 could not create") {
		t.Fatalf("want SQS-X031, got %v", err)
	}
}

func TestBackupsStatusIndex(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var plan string
	rows, err := db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT id FROM backups WHERE status = 'deleted' AND deleted_at < ?`, NowT())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		rows.Scan(&id, &parent, &unused, &detail)
		plan += detail + "\n"
	}
	rows.Close()
	if !strings.Contains(plan, "backups_status") {
		t.Fatalf("the status filter does not use the index:\n%s", plan)
	}
}

// Audit Low: rows of deleted backups were kept forever.
func TestPruneDeletedBackups(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	old, recent := T(now.AddDate(0, 0, -400)), T(now.AddDate(0, 0, -10))
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES ('L', 'l', 'L', 'active', 'l', 'l', 'resolve', ?, ?)`, old, old)
	for _, b := range []struct{ id, status, deleted string }{
		{"OLD", "deleted", old}, {"RECENT", "deleted", recent}, {"OK", "ok", ""}, {"WORKSPACE", "deleted", old},
	} {
		var del any
		if b.deleted != "" {
			del = b.deleted
		}
		mustExec(`INSERT INTO backups(id, library_id, kind, status, started_at, deleted_at) VALUES (?, 'L', 'scheduled', ?, ?, ?)`, b.id, b.status, old, del)
	}
	mustExec(`INSERT INTO export_workspaces(id, backup_id, db_name, state, created_at, expires_at) VALUES ('W', 'WORKSPACE', 'x', 'closed', ?, ?)`, old, old)
	n, err := db.PruneDeletedBackups(ctx, now.AddDate(0, 0, -365))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	var left []string
	rows, _ := db.QueryContext(ctx, `SELECT id FROM backups ORDER BY id`)
	for rows.Next() {
		var id string
		rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if strings.Join(left, ",") != "OK,RECENT,WORKSPACE" {
		t.Fatalf("left %v", left)
	}
}
