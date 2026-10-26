// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/store"
)

func TestChain(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "a.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	l := New(db)
	u := Actor{Type: "user", ID: "u1", Name: "diego", IP: "192.168.1.5"}
	for i := 0; i < 5; i++ {
		if err := l.Record(ctx, u, "library.create", "library", "L1", OK, map[string]any{"slug": "x", "n": i}); err != nil {
			t.Fatal(err)
		}
	}
	broken, n, err := l.Verify(ctx)
	if err != nil || broken != 0 || n != 5 {
		t.Fatalf("verify: %d %d %v", broken, n, err)
	}
	// Tamper with entry 3.
	if _, err := db.ExecContext(ctx, `UPDATE audit_log SET outcome = 'denied' WHERE seq = 3`); err != nil {
		t.Fatal(err)
	}
	broken, _, _ = l.Verify(ctx)
	if broken != 3 {
		t.Fatalf("tampering not detected at 3, got %d", broken)
	}
	// Deleting an entry also breaks the chain.
	db.ExecContext(ctx, `UPDATE audit_log SET outcome = 'ok' WHERE seq = 3`)
	db.ExecContext(ctx, `DELETE FROM audit_log WHERE seq = 2`)
	broken, _, _ = l.Verify(ctx)
	if broken != 3 {
		t.Fatalf("deletion not detected, got %d", broken)
	}
	var buf bytes.Buffer
	n2, err := l.Export(ctx, &buf, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil || n2 != 4 || !strings.Contains(buf.String(), `"action":"library.create"`) {
		t.Fatalf("export %d %v %s", n2, err, buf.String())
	}
	list, err := l.List(ctx, 0, 2, "")
	if err != nil || len(list) != 2 || list[0].Seq != 5 {
		t.Fatalf("list %+v %v", list, err)
	}
}

func newLog(t *testing.T) (*Log, *store.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "a.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db), db
}

// breakLog makes every new entry fail, like a full or read-only disk.
func breakLog(t *testing.T, db *store.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
}

// Audit AUD-004: an entry written in the transaction of the change it
// records exists if and only if the change does, and keeps the chain.
func TestRecordTxCommitsWithTheChange(t *testing.T) {
	ctx := context.Background()
	l, db := newLog(t)
	u := Actor{Type: "user", ID: "u1", Name: "diego"}
	l.Record(ctx, u, "a.first", "x", "1", OK, nil)
	errBoom := errors.New("boom")
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		if err := l.RecordTx(ctx, tx, u, "a.rolled_back", "x", "2", Accepted, nil); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if err := db.Tx(ctx, func(tx *sql.Tx) error {
		return l.RecordTx(ctx, tx, u, "a.kept", "x", "3", Accepted, map[string]any{"job_id": "J1"})
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := l.List(ctx, 0, 10, "")
	if len(list) != 2 || list[0].Action != "a.kept" || list[0].Outcome != Accepted || list[0].Details["job_id"] != "J1" {
		t.Fatalf("entries: %+v", list)
	}
	if broken, n, err := l.Verify(ctx); broken != 0 || n != 2 || err != nil {
		t.Fatalf("verify: %d %d %v", broken, n, err)
	}
}

func TestRequireFailsClosed(t *testing.T) {
	ctx := context.Background()
	l, db := newLog(t)
	breakLog(t, db)
	err := l.Require(ctx, System, "backup.download", "backup", "B1", OK, nil)
	if !errors.Is(err, ErrUnavailable) || !strings.HasPrefix(err.Error(), "SQS-X040 the audit log could not be written, so the action was not done: ") || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("want SQS-X040, got %v", err)
	}
}

// Do writes the entry before the change and its result after; without the
// first entry the change does not run.
func TestDoRecordsBeforeAndAfter(t *testing.T) {
	ctx := context.Background()
	l, db := newLog(t)
	ran := 0
	if err := l.Do(ctx, System, "backup.delete", "backup", "B1", map[string]any{"library_id": "L"}, func() error { ran++; return nil }); err != nil {
		t.Fatal(err)
	}
	errGone := errors.New("SQS-B099 gone")
	if err := l.Do(ctx, System, "backup.delete", "backup", "B2", nil, func() error { ran++; return errGone }); !errors.Is(err, errGone) {
		t.Fatalf("the error of the change is returned: %v", err)
	}
	list, _ := l.List(ctx, 0, 10, "")
	got := []string{}
	for i := len(list) - 1; i >= 0; i-- {
		got = append(got, list[i].TargetID+":"+list[i].Outcome)
	}
	if strings.Join(got, " ") != "B1:accepted B1:ok B2:accepted B2:failed" || list[0].Details["error"] != "SQS-B099 gone" || list[2].Details["library_id"] != "L" {
		t.Fatalf("entries: %v %+v", got, list)
	}
	breakLog(t, db)
	err := l.Do(ctx, System, "backup.delete", "backup", "B3", nil, func() error { ran++; return nil })
	if !errors.Is(err, ErrUnavailable) || ran != 2 {
		t.Fatalf("the change ran without its entry: %v (ran %d)", err, ran)
	}
}

// A change that succeeded but whose result cannot be written says so: the
// caller must not report a clean success.
func TestDoReportsAnUnwrittenResult(t *testing.T) {
	ctx := context.Background()
	l, db := newLog(t)
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER results_down BEFORE INSERT ON audit_log WHEN NEW.outcome = 'ok' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	ran := false
	err := l.Do(ctx, System, "backup.delete", "backup", "B1", nil, func() error { ran = true; return nil })
	if !ran || err == nil || !strings.HasPrefix(err.Error(), "SQS-X041 the action was done, but its result could not be written to the audit log: ") {
		t.Fatalf("want SQS-X041 after the change ran, got %v (ran %v)", err, ran)
	}
	if list, _ := l.List(ctx, 0, 10, ""); len(list) != 1 || list[0].Outcome != Accepted {
		t.Fatalf("entries %+v", list)
	}
}

// PLAN P0.3: an entry RecordTx cannot write is SQS-X040, so every caller
// reports the same code.
func TestRecordTxFailureIsUnavailable(t *testing.T) {
	ctx := context.Background()
	l, db := newLog(t)
	breakLog(t, db)
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		return l.RecordTx(ctx, tx, System, "settings.update", "settings", "app", OK, nil)
	})
	if !errors.Is(err, ErrUnavailable) || !strings.HasPrefix(err.Error(), "SQS-X040 ") || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("want SQS-X040, got %v", err)
	}
}

// PLAN P0.3: a best-effort entry that cannot be written is logged.
func TestBestEffortLogsAFailure(t *testing.T) {
	ctx := context.Background()
	l, db := newLog(t)
	var buf bytes.Buffer
	l.SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	l.BestEffort(ctx, System, "auth.login", "user", "U1", OK, nil)
	if buf.Len() != 0 {
		t.Fatalf("logged a written entry: %s", buf.String())
	}
	breakLog(t, db)
	l.BestEffort(ctx, System, "auth.login", "user", "U1", OK, nil)
	if out := buf.String(); !strings.Contains(out, "could not write an audit entry") || !strings.Contains(out, "action=auth.login") || !strings.Contains(out, "disk I/O error") {
		t.Fatalf("log %q", out)
	}
}
