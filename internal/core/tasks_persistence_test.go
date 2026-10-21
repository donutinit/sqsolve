// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/store"
)

// Audit Low (task 4 item 12): the monthly export was rewritten with the
// current month only, so entries written after the last copy of a month
// (the copy runs at 04:15) never reached its file. On the first days of a
// month the previous month is exported again.
func TestAuditExportCoversTheEndOfTheMonth(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	add := func(ts time.Time, action string) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO audit_log(ts, actor_type, actor_id, actor_name, actor_ip, action, target_type, target_id, outcome, details, prev_hash, hash)
			VALUES (?, 'system', '', 'sqsolve', '', ?, 'system', '', 'ok', '{}', '', '')`, store.T(ts), action)
		if err != nil {
			t.Fatal(err)
		}
	}
	add(time.Date(2026, 9, 30, 2, 0, 0, 0, time.UTC), "early.september")
	add(time.Date(2026, 9, 30, 23, 50, 0, 0, time.UTC), "late.september")
	add(time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC), "early.october")
	s := &Service{DB: db, Audit: audit.New(db)}
	dir := t.TempDir()
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return string(b)
	}

	// The copy of 2026-10-01 04:15 rewrites September with its last entry.
	if err := s.exportAuditMonths(ctx, dir, time.Date(2026, 10, 1, 4, 15, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if sep := read("audit-202609.jsonl"); !strings.Contains(sep, "late.september") || !strings.Contains(sep, "early.september") {
		t.Fatalf("September without its last entries:\n%s", sep)
	}
	if oct := read("audit-202610.jsonl"); !strings.Contains(oct, "early.october") || strings.Contains(oct, "september") {
		t.Fatalf("October export:\n%s", oct)
	}

	// After the third day the previous month is left alone.
	os.Remove(filepath.Join(dir, "audit-202609.jsonl"))
	if err := s.exportAuditMonths(ctx, dir, time.Date(2026, 10, 4, 4, 15, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit-202609.jsonl")); !os.IsNotExist(err) {
		t.Fatal("the previous month was exported after the first three days")
	}
}

// Audit Low (task 4 item 11): housekeeping prunes the catalog rows of backups
// deleted more than a year ago.
func TestHousekeepingPrunesDeletedBackups(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	created := store.T(now.AddDate(-2, 0, 0))
	exec(`INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES ('L', 'l', 'L', 'active', 'l', 'l', 'resolve', ?, ?)`, created, created)
	for id, deleted := range map[string]time.Time{"YEAR_AND_A_DAY": now.AddDate(-1, 0, -1), "ELEVEN_MONTHS": now.AddDate(0, -11, 0)} {
		exec(`INSERT INTO backups(id, library_id, kind, status, started_at, deleted_at) VALUES (?, 'L', 'scheduled', 'deleted', ?, ?)`,
			id, created, store.T(deleted))
	}
	s := &Service{DB: db, Now: func() time.Time { return now }}
	if n, err := s.pruneDeletedBackups(ctx); err != nil || n != 1 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	var left string
	db.QueryRowContext(ctx, `SELECT group_concat(id) FROM backups`).Scan(&left)
	if left != "ELEVEN_MONTHS" {
		t.Fatalf("left %q", left)
	}
}
