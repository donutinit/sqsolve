// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pendingIndexMigration leaves a database whose migration 5 (the backups
// status index, which can run again) is pending, with a setting written
// before.
func pendingIndexMigration(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(ctx, "before", "migrating", "test"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP INDEX backups_status`, `DELETE FROM schema_migrations WHERE version = 5`} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}

// Before a pending migration runs on an existing database, a private copy of
// the database as it was is written, named after the first pending version
// and the time; the migration then runs and is recorded.
func TestMigrationTakesAPriorCopy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path, bk := filepath.Join(dir, "sqsolve.db"), filepath.Join(dir, "state-backups")
	pendingIndexMigration(t, path)
	at := time.Date(2026, 10, 25, 8, 30, 0, 0, time.UTC)
	now = func() time.Time { return at }
	defer func() { now = time.Now }()
	db, err := Open(ctx, path, bk)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var applied int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 5`).Scan(&applied)
	if applied != 1 {
		t.Fatal("the pending migration was not recorded")
	}
	cp := filepath.Join(bk, "sqsolve-before-0005-20261025T083000Z.db")
	st, err := os.Stat(cp)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("copy %v: %v", st, err)
	}
	if st, _ := os.Stat(bk); st.Mode().Perm() != 0o700 {
		t.Fatalf("copies folder %04o, want 0700", st.Mode().Perm())
	}
	old, err := sql.Open("sqlite", "file:"+cp+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var value string
	var index int
	if err := old.QueryRow(`SELECT value FROM settings WHERE key = 'before'`).Scan(&value); err != nil || value != `"migrating"` {
		t.Fatalf("the copy lost the data: %q %v", value, err)
	}
	old.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'backups_status'`).Scan(&index)
	if index != 0 {
		t.Fatal("the copy was taken after the migration")
	}
}

// A new database has nothing to keep: no copy is written even with a copies
// folder.
func TestNewDatabaseTakesNoCopy(t *testing.T) {
	dir := t.TempDir()
	bk := filepath.Join(dir, "state-backups")
	db, err := Open(context.Background(), filepath.Join(dir, "sqsolve.db"), bk)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := os.Stat(bk); !os.IsNotExist(err) {
		t.Fatalf("a new database wrote a copy: %v", err)
	}
}

// Without a copies folder the migration runs without a copy.
func TestMigrationWithoutCopiesFolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sqsolve.db")
	pendingIndexMigration(t, path)
	db, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "sqsolve-before-*")); len(m) != 0 {
		t.Fatalf("copies %v", m)
	}
}

// A migration does not run when its copy cannot be written: a copies folder
// that cannot be created, or one where the copy cannot be written, stop the
// start with SQS-X013 and leave the migration pending.
func TestMigrationStopsWhenTheCopyFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "sqsolve.db")
	pendingIndexMigration(t, path)
	file := filepath.Join(dir, "a-file")
	os.WriteFile(file, nil, 0o600)
	readOnly := filepath.Join(dir, "read-only")
	os.Mkdir(readOnly, 0o500)
	defer os.Chmod(readOnly, 0o700)
	for _, tc := range []struct{ name, bk, cause string }{
		{"folder under a file", filepath.Join(file, "bk"), "not a directory"},
		{"read-only folder", readOnly, "SQS-X030 could not copy the database to " + readOnly + "/sqsolve-before-0005-"},
	} {
		_, err := Open(ctx, path, tc.bk)
		if err == nil || !strings.HasPrefix(err.Error(), "SQS-X013 could not copy the database before migrating: ") || !strings.Contains(err.Error(), tc.cause) {
			t.Fatalf("%s: want SQS-X013 with %q, got %v", tc.name, tc.cause, err)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(readOnly, "*")); len(m) != 0 {
		t.Fatalf("left behind %v", m)
	}
	db, err := Open(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var applied int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 5`).Scan(&applied)
	if applied != 1 {
		t.Fatal("the migration did not run once the copy could be written")
	}
}

// A database migrated by a newer sqsolve is refused with SQS-X012, which
// names the version and what to do, and nothing is changed.
func TestNewerDatabaseIsRefused(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sqsolve.db")
	db, err := Open(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (9999, '9999_future.sql', 'x', ?)`, NowT()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = Open(ctx, path, filepath.Join(filepath.Dir(path), "bk"))
	want := "SQS-X012 the database has migration 9999, newer than this sqsolve version; use the version it was created with or restore the previous copy"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %s", err, want)
	}
}

// A migration that fails to apply is rolled back whole: its statements and
// its row in schema_migrations, and the error names the migration.
func TestFailedMigrationIsRolledBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sqsolve.db")
	pendingIndexMigration(t, path)
	db, err := Open(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	// The migration runs, but recording it fails.
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 5`); err != nil {
		t.Fatal(err)
	}
	db.ExecContext(ctx, `DROP INDEX backups_status`)
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER no_record BEFORE INSERT ON schema_migrations BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(ctx, path, ""); err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("want the recording error, got %v", err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var index int
	raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'backups_status'`).Scan(&index)
	if index != 0 {
		t.Fatal("the statements of the failed migration were kept")
	}
	// A migration whose statements fail names itself.
	raw.Exec(`DROP TRIGGER no_record`)
	raw.Exec(`CREATE TABLE backups_status_blocker (x INTEGER)`)
	raw.Exec(`CREATE INDEX backups_status ON backups_status_blocker(x)`)
	if _, err := Open(ctx, path, ""); err == nil || !strings.HasPrefix(err.Error(), "migration 0005_backups_status_index.sql: ") {
		t.Fatalf("want the migration name, got %v", err)
	}
}

// A legacy checksum that cannot be upgraded stops the start with the cause.
func TestLegacyChecksumUpgradeFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sqsolve.db")
	db, err := Open(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	all, _ := loadMigrations()
	db.ExecContext(ctx, `UPDATE schema_migrations SET checksum = ? WHERE version = ?`, all[0].legacy, all[0].version)
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER no_upgrade BEFORE UPDATE ON schema_migrations BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(ctx, path, ""); err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("want the update error, got %v", err)
	}
}

// A path that is not a database file is refused when it is opened: a folder
// with SQS-X010 and a file of another kind with the SQLite cause.
func TestOpenRefusesWhatIsNotADatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	_, err := Open(ctx, dir, "")
	if err == nil || !strings.HasPrefix(err.Error(), fmt.Sprintf("SQS-X010 could not open %s: ", dir)) {
		t.Fatalf("a folder: %v", err)
	}
	junk := filepath.Join(dir, "junk.db")
	os.WriteFile(junk, []byte(strings.Repeat("not a database ", 100)), 0o600)
	if _, err := Open(ctx, junk, ""); err == nil || !strings.Contains(err.Error(), "file is not a database") {
		t.Fatalf("a text file: %v", err)
	}
}

// An existing database created with mode 0644 by an earlier version is
// made private when it is opened.
func TestOpenMakesAnOldDatabasePrivate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sqsolve.db")
	db, err := Open(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	os.Chmod(path, 0o644)
	db, err = Open(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("%04o, want 0600", st.Mode().Perm())
	}
}
