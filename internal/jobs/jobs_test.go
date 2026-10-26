// SPDX-License-Identifier: AGPL-3.0-only

package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/store"
)

func newManager(t *testing.T) (*Manager, *store.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "j.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	m := NewManager(db, events.NewRecorder(db, bus, log), bus, log, 2, 1)
	return m, db
}

func waitState(t *testing.T, m *Manager, id string, want State) *Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := m.Get(context.Background(), id)
		if err == nil && j.State == want {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	j, _ := m.Get(context.Background(), id)
	t.Fatalf("job %s: want %s, have %s (%s)", id, want, j.State, j.ErrorDetail)
	return nil
}

// waitEvents waits until n events of the type are stored: finish writes the job's state
// before it emits its event, so a test that saw the state may not see the event yet.
func waitEvents(t *testing.T, m *Manager, typ string, n int) []events.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs, err := m.ev.List(context.Background(), events.Filter{TypePrefix: typ})
		if err == nil && len(evs) >= n || time.Now().After(deadline) {
			return evs
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunStepsLogsResult(t *testing.T) {
	m, _ := newManager(t)
	m.Register(Spec{Type: "t.ok", Run: func(ctx context.Context, r *Run) error {
		var p struct{ N int }
		r.Params(&p)
		r.Step("one", func() error { r.Infof("n=%d", p.N); return nil })
		r.Step("two", func() error { r.Journal(map[string]string{"x": "y"}); r.Progress(50); return nil })
		r.SetResult(map[string]int{"n": p.N})
		return nil
	}})
	m.Register(Spec{Type: "t.fail", Run: func(ctx context.Context, r *Run) error {
		return r.Step("broken", func() error { return errors.New("SQS-B012 no space") })
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	j, err := m.Enqueue(ctx, "t.ok", "", map[string]int{"N": 7}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	j = waitState(t, m, j.ID, Succeeded)
	if j.Progress != 100 || string(j.Result) != `{"n":7}` {
		t.Fatalf("%+v", j)
	}
	steps, _ := m.Steps(ctx, j.ID)
	if len(steps) != 2 || steps[1].State != "succeeded" || string(steps[1].Data) != `{"x":"y"}` {
		t.Fatalf("steps %+v", steps)
	}
	logs, _ := m.Logs(ctx, j.ID, 0)
	if len(logs) != 1 || logs[0].Message != "n=7" {
		t.Fatalf("logs %+v", logs)
	}
	f, _ := m.Enqueue(ctx, "t.fail", "", nil, audit.System)
	f = waitState(t, m, f.ID, Failed)
	if f.ErrorCode != "SQS-B012" {
		t.Fatalf("code %q", f.ErrorCode)
	}
}

func TestWriteLockSerializes(t *testing.T) {
	m, _ := newManager(t)
	var concurrent, max int32
	m.Register(Spec{Type: "t.w", Lock: LockWrite, Run: func(ctx context.Context, r *Run) error {
		c := atomic.AddInt32(&concurrent, 1)
		for {
			old := atomic.LoadInt32(&max)
			if c <= old || atomic.CompareAndSwapInt32(&max, old, c) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		return nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	a, _ := m.Enqueue(ctx, "t.w", "LIB1", nil, audit.System)
	b, _ := m.Enqueue(ctx, "t.w", "LIB1", nil, audit.System)
	waitState(t, m, a.ID, Succeeded)
	waitState(t, m, b.ID, Succeeded)
	if max != 1 {
		t.Fatalf("write lock allowed %d concurrent jobs", max)
	}
}

func TestCancelAndInterrupt(t *testing.T) {
	m, db := newManager(t)
	started := make(chan struct{})
	m.Register(Spec{Type: "t.slow", Cancellable: true, Run: func(ctx context.Context, r *Run) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	m.Register(Spec{Type: "t.noop", Run: func(ctx context.Context, r *Run) error { return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	j, _ := m.Enqueue(ctx, "t.slow", "", nil, audit.System)
	<-started
	if err := m.Cancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, j.ID, Cancelled)

	// A job left "running" by a dead process becomes interrupted.
	db.ExecContext(ctx, `INSERT INTO jobs(id, type, state, scheduled_for, created_at, created_by_type) VALUES ('X1', 't.noop', 'running', ?, ?, 'system')`, store.NowT(), store.NowT())
	got, err := m.MarkInterrupted(ctx)
	if err != nil || len(got) != 1 || got[0].ID != "X1" {
		t.Fatalf("%v %v", got, err)
	}
	x, _ := m.Get(ctx, "X1")
	if x.State != Interrupted {
		t.Fatal(x.State)
	}
	// Unique returns the queued job instead of a new one.
	m.stop.Store(true)
	u1, _ := m.Enqueue(ctx, "t.noop", "L", nil, audit.System, Unique())
	u2, _ := m.Enqueue(ctx, "t.noop", "L", nil, audit.System, Unique())
	if u1.ID != u2.ID {
		t.Fatal("unique not respected")
	}
}

// PLAN P0.2: a step runs only once its row is stored, and a journal must
// land on the row of its step.
func TestStepAndJournalAreRequired(t *testing.T) {
	m, db := newManager(t)
	var ran atomic.Bool
	results := make(chan [2]error, 1)
	m.Register(Spec{Type: "t.step", Run: func(ctx context.Context, r *Run) error {
		return r.Step("guarded", func() error { ran.Store(true); return nil })
	}})
	m.Register(Spec{Type: "t.journal", Run: func(ctx context.Context, r *Run) error {
		// Before any step there is no row to carry the journal.
		before := r.Journal(map[string]string{"phase": "early"})
		var inside error
		r.Step("one", func() error {
			if _, err := db.ExecContext(ctx, `DELETE FROM job_steps WHERE job_id = ?`, r.Job.ID); err != nil {
				return err
			}
			inside = r.Journal(map[string]string{"phase": "lost"})
			return nil
		})
		results <- [2]error{before, inside}
		return nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER refuse_step BEFORE INSERT ON job_steps WHEN NEW.name = 'guarded' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	m.Start(ctx)
	j, _ := m.Enqueue(ctx, "t.step", "", nil, audit.System)
	j = waitState(t, m, j.ID, Failed)
	if ran.Load() {
		t.Fatal("the step ran although its row could not be stored")
	}
	if j.ErrorCode != "SQS-J040" || !strings.HasPrefix(j.ErrorDetail, "SQS-J040 the job step could not be recorded, so it was not run: ") || !strings.Contains(j.ErrorDetail, "disk I/O error") {
		t.Fatalf("%s: %s", j.ErrorCode, j.ErrorDetail)
	}
	m.Enqueue(ctx, "t.journal", "", nil, audit.System)
	got := <-results
	for i, err := range got {
		if err == nil || err.Error() != "SQS-J041 the job journal was not written: its step is not recorded" {
			t.Errorf("journal %d: %v", i, err)
		}
	}
	// A failing update is returned too.
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER refuse_journal BEFORE UPDATE OF data ON job_steps BEGIN SELECT RAISE(ABORT, 'journal refused'); END`); err != nil {
		t.Fatal(err)
	}
	r := &Run{Job: &Job{ID: "JX"}, m: m, ctx: ctx, step: 1}
	db.ExecContext(ctx, `INSERT INTO jobs(id, type, state, priority, scheduled_for, created_at, created_by_type) VALUES ('JX', 't.journal', 'running', 50, ?, ?, 'system')`, store.NowT(), store.NowT())
	db.ExecContext(ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at) VALUES ('JX', 1, 'x', 'running', ?)`, store.NowT())
	if err := r.Journal(map[string]string{"a": "b"}); !errors.Is(err, ErrJournal) || !strings.HasPrefix(err.Error(), "SQS-J041 the job journal was not written: ") || !strings.Contains(err.Error(), "journal refused") {
		t.Fatalf("refused update: %v", err)
	}
}

// PLAN P0.4: a job of a Reconcile type that stops after an irreversible
// step (ErrUnsettled) fails and stays pending reconciliation, so its journal
// is kept for the next start; other types just fail.
func TestUnsettledFailureStaysPending(t *testing.T) {
	m, db := newManager(t)
	m.Register(Spec{Type: "t.recon", Reconcile: true, Run: func(ctx context.Context, r *Run) error {
		return fmt.Errorf("SQS-L047 the database was dropped, but the catalog could not be updated: disk I/O error. %w", ErrUnsettled)
	}})
	m.Register(Spec{Type: "t.plain", Run: func(ctx context.Context, r *Run) error {
		return fmt.Errorf("SQS-L047 x. %w", ErrUnsettled)
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	pending := func(id string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT reconcile_pending FROM jobs WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	j, _ := m.Enqueue(ctx, "t.recon", "L1", nil, audit.System)
	j = waitState(t, m, j.ID, Failed)
	if j.ErrorCode != "SQS-L047" || !strings.HasSuffix(j.ErrorDetail, "reconciliation finishes it at the next start of sqsolve") || pending(j.ID) != 1 {
		t.Fatalf("%s %q pending %d", j.ErrorCode, j.ErrorDetail, pending(j.ID))
	}
	if list, _ := m.PendingReconciliation(ctx); len(list) != 1 || list[0].ID != j.ID {
		t.Fatalf("pending %+v", list)
	}
	if n, _ := m.Purge(ctx, time.Now().Add(time.Hour)); n != 0 {
		t.Fatal("the purge deleted a job pending reconciliation")
	}
	p, _ := m.Enqueue(ctx, "t.plain", "L1", nil, audit.System)
	p = waitState(t, m, p.ID, Failed)
	if pending(p.ID) != 0 {
		t.Fatal("a type without reconciliation was left pending")
	}
}

// ADR 0028: the same request returns the queued job, one with different
// parameters is refused with SQS-J005 (no job, no audit entry), and
// UniqueAny keeps the first request whatever its parameters.
func TestUniqueJobIdentity(t *testing.T) {
	m, db := newManager(t)
	log := audit.New(db)
	m.SetAudit(log)
	m.Register(Spec{Type: "t.u", Run: func(ctx context.Context, r *Run) error { return nil }})
	ctx := context.Background()
	type params struct {
		BackupID string `json:"backup_id"`
		Kind     string `json:"kind"`
	}
	first, err := m.Enqueue(ctx, "t.u", "L1", params{BackupID: "B1", Kind: "k"}, audit.System, Unique(), Audited("t.u", "library", "L1", nil))
	if err != nil {
		t.Fatal(err)
	}
	// The same parameters, written in another order: the queued job.
	same, err := m.Enqueue(ctx, "t.u", "L1", map[string]string{"kind": "k", "backup_id": "B1"}, audit.System, Unique(), Audited("t.u", "library", "L1", nil))
	if err != nil || same.ID != first.ID {
		t.Fatalf("same request: %v %v", same, err)
	}
	_, err = m.Enqueue(ctx, "t.u", "L1", params{BackupID: "B2", Kind: "k"}, audit.System, Unique(), Audited("t.u", "library", "L1", nil))
	if !errors.Is(err, ErrConflict) || err.Error() != "SQS-J005 another job of this type is already queued or running for this library with different parameters; wait for it or cancel it (job "+first.ID+")" {
		t.Fatalf("different parameters: %v", err)
	}
	// Another library is another job.
	if other, err := m.Enqueue(ctx, "t.u", "L2", params{BackupID: "B2"}, audit.System, Unique()); err != nil || other.ID == first.ID {
		t.Fatalf("another library: %v %v", other, err)
	}
	// UniqueAny: the first request wins.
	if anyJob, err := m.Enqueue(ctx, "t.u", "L1", params{BackupID: "B3"}, audit.System, UniqueAny()); err != nil || anyJob.ID != first.ID {
		t.Fatalf("UniqueAny: %v %v", anyJob, err)
	}
	if list, _ := m.List(ctx, Filter{LibraryID: "L1"}); len(list) != 1 {
		t.Fatalf("%d jobs queued for L1", len(list))
	}
	if entries, _ := log.List(ctx, 0, 10, ""); len(entries) != 1 || entries[0].Details["job_id"] != first.ID {
		t.Fatalf("audit entries %+v", entries)
	}
}
