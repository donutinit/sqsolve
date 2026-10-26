// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"strings"
	"testing"
)

// An index that no longer matches its table is reported with SQS-X014 and
// the first finding of SQLite.
func TestIntegrityCheckReportsCorruption(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE t (a INTEGER, b INTEGER)`,
		`CREATE INDEX t_a ON t(a)`,
		`INSERT INTO t VALUES (1, 10), (2, 20)`,
		// The index now claims to be on b while its entries are those of a.
		`PRAGMA writable_schema = ON`,
		`UPDATE sqlite_master SET sql = 'CREATE INDEX t_a ON t(b)' WHERE name = 't_a'`,
		`PRAGMA writable_schema = OFF`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	db = openAgain(t, db.Path)
	err := db.IntegrityCheck(ctx)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-X014 SQLite integrity: ") || !strings.Contains(err.Error(), "t_a") {
		t.Fatalf("want SQS-X014 naming the index, got %v", err)
	}
}

func openAgain(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// A check that cannot run reports why instead of passing.
func TestIntegrityCheckOnAClosedDatabase(t *testing.T) {
	db := openTemp(t)
	db.Close()
	if err := db.IntegrityCheck(context.Background()); err == nil || err.Error() != "sql: database is closed" {
		t.Fatalf("%v", err)
	}
}

// Pruning on a database that refuses the delete reports the error and keeps
// the rows.
func TestPruneDeletedBackupsReportsErrors(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER no_prune BEFORE DELETE ON backups BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	old := "2020-01-01T00:00:00.000000Z"
	db.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES ('L', 'l', 'L', 'active', 'l', 'l', 'resolve', ?, ?)`, old, old)
	if _, err := db.ExecContext(ctx, `INSERT INTO backups(id, library_id, kind, status, started_at, deleted_at) VALUES ('B', 'L', 'scheduled', 'deleted', ?, ?)`, old, old); err != nil {
		t.Fatal(err)
	}
	n, err := db.PruneDeletedBackups(ctx, ParseT(NowT()))
	if n != 0 || err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("pruned %d: %v", n, err)
	}
	var left int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM backups`).Scan(&left)
	if left != 1 {
		t.Fatalf("%d rows left", left)
	}
}
