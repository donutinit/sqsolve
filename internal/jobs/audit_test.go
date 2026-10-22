// SPDX-License-Identifier: AGPL-3.0-only

package jobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/store"
)

func auditedManager(t *testing.T) (*Manager, *store.DB, *audit.Log) {
	t.Helper()
	m, db := newManager(t)
	l := audit.New(db)
	m.SetAudit(l)
	return m, db, l
}

// entries returns the audit entries of action, oldest first, once there
// are n of them.
func entries(t *testing.T, l *audit.Log, action string, n int) []audit.Entry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		list, err := l.List(context.Background(), 0, 100, "")
		if err != nil {
			t.Fatal(err)
		}
		var out []audit.Entry
		for i := len(list) - 1; i >= 0; i-- {
			if list[i].Action == action {
				out = append(out, list[i])
			}
		}
		if len(out) >= n || time.Now().After(deadline) {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func jobRows(t *testing.T, db *store.DB) int {
	t.Helper()
	var n int
	db.QueryRow(`SELECT count(*) FROM jobs`).Scan(&n)
	return n
}

// Audit AUD-004: the entry of an action that queues a job was written before
// the job, as ok, and its failure did not stop the job. Now the entry
// (accepted, with the job's id) and the job are one transaction, and the
// result of the job follows when it ends.
func TestAuditedJobEntryAndResult(t *testing.T) {
	m, _, l := auditedManager(t)
	m.Register(Spec{Type: "t.ok", Run: func(context.Context, *Run) error { return nil }})
	m.Register(Spec{Type: "t.fail", Run: func(context.Context, *Run) error { return errors.New("SQS-B012 no space") }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	ok, err := m.Enqueue(ctx, "t.ok", "L1", nil, by, Audited("library.delete", "library", "L1", map[string]any{"slug": "boda"}))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, ok.ID, Succeeded)
	got := entries(t, l, "library.delete", 2)
	if len(got) != 2 || got[0].Outcome != audit.Accepted || got[0].Details["job_id"] != ok.ID || got[0].Details["slug"] != "boda" ||
		got[1].Outcome != audit.OK || got[1].Details["job_id"] != ok.ID || got[1].Actor != by || got[1].TargetID != "L1" {
		t.Fatalf("entries %+v", got)
	}
	bad, err := m.Enqueue(ctx, "t.fail", "L1", nil, by, Audited("library.purge", "library", "L1", nil))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, bad.ID, Failed)
	got = entries(t, l, "library.purge", 2)
	if len(got) != 2 || got[1].Outcome != audit.Failed || got[1].Details["state"] != "failed" || got[1].Details["error_code"] != "SQS-B012" {
		t.Fatalf("entries %+v", got)
	}
	// A job queued without Audited writes nothing.
	plain, _ := m.Enqueue(ctx, "t.ok", "", nil, audit.System)
	waitState(t, m, plain.ID, Succeeded)
	if list, _ := l.List(ctx, 0, 100, ""); len(list) != 4 {
		t.Fatalf("%d entries", len(list))
	}
}

func TestAuditedJobNotQueuedWithoutItsEntry(t *testing.T) {
	m, db, _ := auditedManager(t)
	m.Register(Spec{Type: "t.ok", Run: func(context.Context, *Run) error { return nil }})
	if _, err := db.Exec(`CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := m.Enqueue(context.Background(), "t.ok", "L1", nil, audit.System, Audited("library.delete", "library", "L1", nil))
	if !errors.Is(err, audit.ErrUnavailable) || !strings.HasPrefix(err.Error(), "SQS-X040 the audit log could not be written, so the action was not done: ") {
		t.Fatalf("want SQS-X040, got %v", err)
	}
	if n := jobRows(t, db); n != 0 {
		t.Fatalf("%d jobs queued without their entry", n)
	}
}

// A job a crash left running gets its result (interrupted) at the next
// start.
func TestAuditedJobInterruptedByARestart(t *testing.T) {
	m, db, l := auditedManager(t)
	ctx := context.Background()
	m.Register(Spec{Type: "t.swap", Reconcile: true, Run: func(context.Context, *Run) error { return nil }})
	if _, err := db.ExecContext(ctx, `INSERT INTO jobs(id, type, library_id, state, scheduled_for, created_at, created_by_type, created_by_id, created_by_name, audit)
		VALUES ('X1', 't.swap', 'L1', 'running', ?, ?, 'user', 'u1', 'diego', '{"action":"restore.replace","target_type":"library","target_id":"L1"}')`, store.NowT(), store.NowT()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MarkInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got := entries(t, l, "restore.replace", 1)
	if len(got) != 1 || got[0].Outcome != audit.Failed || got[0].Details["state"] != "interrupted" || got[0].Actor.Name != "diego" || got[0].Details["job_id"] != "X1" {
		t.Fatalf("entries %+v", got)
	}
}

// Unique looks for the pending job in the same transaction that queues the
// new one: concurrent requests queue one job.
func TestUniqueUnderConcurrency(t *testing.T) {
	m, db, _ := auditedManager(t)
	m.Register(Spec{Type: "t.ok", Run: func(context.Context, *Run) error { return nil }})
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			if _, err := m.Enqueue(context.Background(), "t.ok", "L1", nil, audit.System, Unique(), Audited("restore.recreate", "library", "L1", nil)); err != nil {
				t.Error(err)
			}
		}()
	}
	close(gate)
	wg.Wait()
	if n := jobRows(t, db); n != 1 {
		t.Fatalf("%d jobs queued", n)
	}
}

// An Audited job cancelled while still queued gets its result too.
func TestAuditedJobCancelledWhileQueued(t *testing.T) {
	m, _, l := auditedManager(t)
	ctx := context.Background()
	m.Register(Spec{Type: "t.ok", Run: func(context.Context, *Run) error { return nil }})
	j, err := m.Enqueue(ctx, "t.ok", "L1", nil, audit.System, Audited("backup.create", "library", "L1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	got := entries(t, l, "backup.create", 2)
	if len(got) != 2 || got[1].Outcome != audit.Failed || got[1].Details["state"] != "cancelled" || got[1].Details["job_id"] != j.ID {
		t.Fatalf("entries %+v", got)
	}
}

// When the final state of a job cannot be recorded, its row stays running
// and the next start marks it interrupted: no result is written before
// then, so the log never holds two results for one job.
func TestAuditedJobResultFollowsTheRecordedState(t *testing.T) {
	m, db, l := auditedManager(t)
	m.finishWaits = []time.Duration{time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Register(Spec{Type: "t.ok", Run: func(context.Context, *Run) error { return nil }})
	installTrigger(t, db, `CREATE TRIGGER never_done BEFORE UPDATE OF state ON jobs WHEN NEW.state IN ('succeeded', 'failed') BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	m.Start(ctx)
	j, err := m.Enqueue(ctx, "t.ok", "L1", nil, audit.System, Audited("backup.create", "library", "L1", nil))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, _ := m.Get(ctx, j.ID)
		if g.State == Running && m.Busy() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %s busy %d", g.State, m.Busy())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := entries(t, l, "backup.create", 1); len(got) != 1 || got[0].Outcome != audit.Accepted {
		t.Fatalf("a result was written for a job still recorded as running: %+v", got)
	}
}
