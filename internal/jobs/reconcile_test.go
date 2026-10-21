// SPDX-License-Identifier: AGPL-3.0-only

package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/store"
)

// An interrupted job of a type that reconciliation settles from its journal
// stays pending until Reconciled is called: it is listed at every start and
// the job purge keeps it. Other interrupted jobs are not pending.
func TestReconcilePendingUntilSettled(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	noop := func(context.Context, *Run) error { return nil }
	m.Register(Spec{Type: "t.swap", Reconcile: true, Run: noop})
	m.Register(Spec{Type: "t.noop", Run: noop})
	for _, j := range []struct{ id, typ string }{{"X2", "t.swap"}, {"X1", "t.swap"}, {"X3", "t.noop"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO jobs(id, type, state, scheduled_for, created_at, created_by_type) VALUES (?, ?, 'running', ?, ?, 'system')`,
			j.id, j.typ, store.NowT(), store.NowT()); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.MarkInterrupted(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("interrupted %d jobs: %v", len(got), err)
	}
	pending, err := m.PendingReconciliation(ctx)
	if err != nil || len(pending) != 2 || pending[0].ID != "X1" || pending[1].ID != "X2" {
		t.Fatalf("pending %+v: %v", pending, err)
	}
	if pending[0].State != Interrupted || pending[0].Type != "t.swap" {
		t.Fatalf("pending job %+v", pending[0])
	}
	// A later start finds them pending again.
	if again, err := m.MarkInterrupted(ctx); err != nil || len(again) != 0 {
		t.Fatalf("second start interrupted %d jobs: %v", len(again), err)
	}
	if pending, _ := m.PendingReconciliation(ctx); len(pending) != 2 {
		t.Fatalf("pending after a second start: %+v", pending)
	}
	// The purge keeps what is pending.
	n, err := m.Purge(ctx, time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("purged %d: %v", n, err)
	}
	for _, id := range []string{"X1", "X2"} {
		if _, err := m.Get(ctx, id); err != nil {
			t.Fatalf("%s purged while pending: %v", id, err)
		}
	}
	if err := m.Reconciled(ctx, "X1"); err != nil {
		t.Fatal(err)
	}
	if pending, _ := m.PendingReconciliation(ctx); len(pending) != 1 || pending[0].ID != "X2" {
		t.Fatalf("pending after settling X1: %+v", pending)
	}
	if n, err := m.Purge(ctx, time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("purged %d after settling: %v", n, err)
	}
	if _, err := m.Get(ctx, "X1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("settled job not purged: %v", err)
	}
}

// A failed write of the settlement is reported, so the job is checked again
// at the next start instead of silently staying pending.
func TestReconciledReportsAFailedWrite(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	m.Register(Spec{Type: "t.swap", Reconcile: true, Run: func(context.Context, *Run) error { return nil }})
	db.ExecContext(ctx, `INSERT INTO jobs(id, type, state, scheduled_for, created_at, created_by_type) VALUES ('X1', 't.swap', 'running', ?, ?, 'system')`, store.NowT(), store.NowT())
	m.MarkInterrupted(ctx)
	installTrigger(t, db, `CREATE TRIGGER refuse_settle BEFORE UPDATE OF reconcile_pending ON jobs BEGIN SELECT RAISE(ABORT, 'disk full'); END`)
	if err := m.Reconciled(ctx, "X1"); err == nil {
		t.Fatal("a failed write was not reported")
	}
	if pending, _ := m.PendingReconciliation(ctx); len(pending) != 1 {
		t.Fatalf("pending %+v", pending)
	}
}

// A newer job counts only when it is of one of the types, on the same
// library, started after the job and succeeded or is pending itself.
func TestNewerJob(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	add := func(id, typ, lib, state string, pending int, started time.Time) {
		t.Helper()
		at := store.T(started)
		if _, err := db.ExecContext(ctx, `INSERT INTO jobs(id, type, library_id, state, scheduled_for, created_at, started_at, created_by_type, reconcile_pending)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'user', ?)`, id, typ, lib, state, at, at, at, pending); err != nil {
			t.Fatal(err)
		}
	}
	add("A1", "t.swap", "L1", "interrupted", 1, base)
	add("A0", "t.swap", "L1", "succeeded", 0, base.Add(-time.Minute)) // started before
	add("B1", "t.swap", "L1", "failed", 0, base.Add(time.Minute))     // failed
	add("B2", "t.other", "L1", "succeeded", 0, base.Add(time.Minute)) // other type
	add("B3", "t.swap", "L2", "succeeded", 0, base.Add(time.Minute))  // other library
	j, _ := m.Get(ctx, "A1")
	if id, err := m.NewerJob(ctx, j, "t.swap", "t.trash"); err != nil || id != "" {
		t.Fatalf("newer job %q: %v", id, err)
	}
	// A delete enqueued before A1 but started after it (lower priority).
	add("A00", "t.trash", "L1", "succeeded", 0, base.Add(2*time.Minute))
	if id, err := m.NewerJob(ctx, j, "t.swap", "t.trash"); err != nil || id != "A00" {
		t.Fatalf("newer job %q: %v", id, err)
	}
	add("C1", "t.swap", "L1", "interrupted", 1, base.Add(3*time.Minute))
	if id, err := m.NewerJob(ctx, j, "t.swap", "t.trash"); err != nil || id != "C1" {
		t.Fatalf("newer job %q: %v", id, err)
	}
}
