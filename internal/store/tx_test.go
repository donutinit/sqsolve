// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func settingExists(t *testing.T, db *DB, key string) bool {
	t.Helper()
	ok, err := db.GetSetting(context.Background(), key, new(any))
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// Tx commits what fn wrote when fn succeeds.
func TestTxCommits(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at, updated_by) VALUES ('a', '1', ?, 'test')`, NowT())
		return err
	})
	if err != nil || !settingExists(t, db, "a") {
		t.Fatalf("commit: %v", err)
	}
}

// Tx rolls back every write of fn when fn fails, and returns fn's error
// unchanged.
func TestTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	boom := errors.New("boom")
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		for _, k := range []string{"a", "b"} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at, updated_by) VALUES (?, '1', ?, 'test')`, k, NowT()); err != nil {
				return err
			}
		}
		return boom
	})
	if err != boom {
		t.Fatalf("got %v, want fn's error", err)
	}
	if settingExists(t, db, "a") || settingExists(t, db, "b") {
		t.Fatal("the writes of a failed transaction were kept")
	}
	// A statement that fails inside the transaction undoes the earlier ones.
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER no_b BEFORE INSERT ON settings WHEN NEW.key = 'b' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		for _, k := range []string{"a", "b"} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at, updated_by) VALUES (?, '1', ?, 'test')`, k, NowT()); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "disk I/O error") || settingExists(t, db, "a") {
		t.Fatalf("partial transaction: %v", err)
	}
}

// Tx does not call fn when the transaction cannot begin.
func TestTxReportsBeginErrors(t *testing.T) {
	db := openTemp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := db.Tx(ctx, func(*sql.Tx) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled: %v, fn called %v", err, called)
	}
	db.Close()
	if err := db.Tx(context.Background(), func(*sql.Tx) error { called = true; return nil }); err == nil || err.Error() != "sql: database is closed" || called {
		t.Fatalf("closed: %v, fn called %v", err, called)
	}
}
