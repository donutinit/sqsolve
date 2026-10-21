// SPDX-License-Identifier: AGPL-3.0-only

package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/store"
)

func failJob(t *testing.T, db *store.DB, id string, state State) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `UPDATE jobs SET state = ?, finished_at = ? WHERE id = ?`, string(state), store.NowT(), id); err != nil {
		t.Fatal(err)
	}
}

func TestRetryPolicy(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	noop := func(context.Context, *Run) error { return nil }
	m.Register(Spec{Type: "t.backup", Retryable: true, Action: "backup.create", Run: noop})
	m.Register(Spec{Type: "t.replace", Action: "restore.replace", Run: noop})
	op := audit.Actor{Type: "user", ID: "U2", Name: "op"}

	// Idempotent types are queued again with the same parameters and the
	// next attempt number.
	b, _ := m.Enqueue(ctx, "t.backup", "LIB1", map[string]string{"kind": "manual"}, audit.System)
	failJob(t, db, b.ID, Failed)
	nb, err := m.Retry(ctx, b.ID, op)
	if err != nil {
		t.Fatal(err)
	}
	if nb.ID == b.ID || nb.Type != "t.backup" || nb.LibraryID != "LIB1" || string(nb.Params) != `{"kind":"manual"}` || nb.Attempt != 2 || nb.CreatedBy.Name != "op" || nb.State != Queued {
		t.Fatalf("retried job %+v", nb)
	}
	if nb.Retryable {
		t.Fatal("a queued job is not retryable")
	}
	// A destructive type is refused with its code, and nothing is queued.
	r, _ := m.Enqueue(ctx, "t.replace", "LIB1", map[string]string{"backup_id": "B1"}, audit.System)
	failJob(t, db, r.ID, Cancelled)
	if g, _ := m.Get(ctx, r.ID); g.Retryable {
		t.Fatal("a destructive job is shown as retryable")
	}
	if g, _ := m.Get(ctx, b.ID); !g.Retryable {
		t.Fatal("a failed idempotent job is not shown as retryable")
	}
	before, _ := m.List(ctx, Filter{})
	_, err = m.Retry(ctx, r.ID, op)
	if !errors.Is(err, ErrNotRetryable) || !strings.HasPrefix(err.Error(), "SQS-J030 ") || !strings.Contains(err.Error(), "start the operation again") {
		t.Fatalf("retry of a destructive job: %v", err)
	}
	after, _ := m.List(ctx, Filter{})
	if len(after) != len(before) {
		t.Fatal("a refused retry queued a job")
	}
	// Parameters that do not decode are an error, not an empty retry.
	c, _ := m.Enqueue(ctx, "t.backup", "LIB2", nil, audit.System)
	failJob(t, db, c.ID, Interrupted)
	db.ExecContext(ctx, `UPDATE jobs SET params = '{bad' WHERE id = ?`, c.ID)
	if _, err := m.Retry(ctx, c.ID, op); err == nil || !strings.HasPrefix(err.Error(), "SQS-J031 the stored parameters of job "+c.ID+" are not valid: ") {
		t.Fatalf("retry with broken params: %v", err)
	}
	// The permission comes from the type; unknown types need an empty action.
	if m.Action("t.replace") != "restore.replace" || m.Action("t.backup") != "backup.create" || m.Action("nope") != "" {
		t.Fatal("actions per type")
	}
}

// installTrigger makes SQLite reject an UPDATE of the jobs table, the way a
// full disk or an I/O error would.
func installTrigger(t *testing.T, db *store.DB, sql string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchSurvivesFailedStartUpdate(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	m.Register(Spec{Type: "t.h", Heavy: true, Lock: LockWrite, Run: func(context.Context, *Run) error { return nil }})
	j, _ := m.Enqueue(ctx, "t.h", "LIB1", nil, audit.System)
	installTrigger(t, db, `CREATE TRIGGER no_start BEFORE UPDATE OF state ON jobs WHEN NEW.state = 'running' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	m.dispatch(ctx) // panicked with a nil result before
	if g, _ := m.Get(ctx, j.ID); g.State != Queued {
		t.Fatalf("state %s", g.State)
	}
	if m.Busy() != 0 || len(m.heavy) != 0 || len(m.locks.m) != 0 || len(m.running) != 0 {
		t.Fatalf("leaked: busy %d heavy %d locks %d running %d", m.Busy(), len(m.heavy), len(m.locks.m), len(m.running))
	}
	// Once the database accepts the update again, the job runs.
	db.ExecContext(ctx, `DROP TRIGGER no_start`)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.Start(cctx)
	waitState(t, m, j.ID, Succeeded)
}

func TestDispatcherRecoversFromPanic(t *testing.T) {
	m, _ := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Register(Spec{Type: "t.ok", Run: func(context.Context, *Run) error { return nil }})
	calls := 0
	m.hookDispatch = func() {
		calls++
		if calls == 1 {
			panic("bad row")
		}
	}
	m.Start(ctx)
	j, _ := m.Enqueue(ctx, "t.ok", "", nil, audit.System)
	waitState(t, m, j.ID, Succeeded)
	evs, _ := m.ev.List(ctx, events.Filter{TypePrefix: events.SystemInternalError})
	if len(evs) != 1 || evs[0].Severity != events.Critical || !strings.Contains(evs[0].Message, "job dispatcher") || !strings.Contains(evs[0].Message, "bad row") {
		t.Fatalf("panic event %+v", evs)
	}
}

func TestFinishRetriesTheFinalUpdate(t *testing.T) {
	m, db := newManager(t)
	m.finishWaits = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Register(Spec{Type: "t.ok", Run: func(context.Context, *Run) error { return nil }})
	// RAISE(FAIL) keeps the counter change made before it: the update fails once.
	installTrigger(t, db, `CREATE TABLE fail_once(n INTEGER)`)
	installTrigger(t, db, `INSERT INTO fail_once VALUES (1)`)
	installTrigger(t, db, `CREATE TRIGGER fail_succeeded BEFORE UPDATE OF state ON jobs WHEN NEW.state = 'succeeded' AND (SELECT n FROM fail_once) > 0
		BEGIN UPDATE fail_once SET n = n - 1; SELECT RAISE(FAIL, 'database is locked'); END`)
	m.Start(ctx)
	j, _ := m.Enqueue(ctx, "t.ok", "", nil, audit.System)
	waitState(t, m, j.ID, Succeeded)
	var left int
	if err := db.QueryRowContext(ctx, `SELECT n FROM fail_once`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("the final update never failed: %d %v", left, err)
	}
}

func TestUniqueIgnoresGhostRunningJob(t *testing.T) {
	m, db := newManager(t)
	m.finishWaits = []time.Duration{time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Register(Spec{Type: "t.sched", Run: func(context.Context, *Run) error { return nil }})
	installTrigger(t, db, `CREATE TRIGGER never_done BEFORE UPDATE OF state ON jobs WHEN NEW.state IN ('succeeded', 'failed') BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	m.Start(ctx)
	first, _ := m.Enqueue(ctx, "t.sched", "LIB1", nil, audit.System, Unique())
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, _ := m.Get(ctx, first.ID)
		if g.State == Running && m.Busy() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %s busy %d", g.State, m.Busy())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The row says "running" but nothing runs it: the next run still gets a job.
	m.stop.Store(true)
	next, err := m.Enqueue(ctx, "t.sched", "LIB1", nil, audit.System, Unique())
	if err != nil || next.ID == first.ID || next.State != Queued {
		t.Fatalf("unique returned the ghost: %+v %v", next, err)
	}
	// A real queued job is still shared.
	again, _ := m.Enqueue(ctx, "t.sched", "LIB1", nil, audit.System, Unique())
	if again.ID != next.ID {
		t.Fatal("unique no longer shares the queued job")
	}
}

func TestShutdownRecordsInterrupted(t *testing.T) {
	m, _ := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	m.Register(Spec{Type: "t.long", Cancellable: true, Run: func(ctx context.Context, r *Run) error {
		return r.Step("dump", func() error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}})
	m.Start(ctx)
	j, _ := m.Enqueue(ctx, "t.long", "LIB1", nil, audit.System)
	<-started
	m.Shutdown(5 * time.Second)
	g, _ := m.Get(ctx, j.ID)
	if g.State != Interrupted || g.ErrorCode != "SQS-J010" || !strings.Contains(g.ErrorDetail, "stopped while the job was running") {
		t.Fatalf("after shutdown: %s %s %s", g.State, g.ErrorCode, g.ErrorDetail)
	}
	steps, _ := m.Steps(ctx, j.ID)
	if len(steps) != 1 || steps[0].State != "interrupted" {
		t.Fatalf("steps %+v", steps)
	}
	evs, _ := m.ev.List(ctx, events.Filter{TypePrefix: events.JobInterrupted})
	if len(evs) != 1 || evs[0].JobID != j.ID || evs[0].Data["job_type"] != "t.long" {
		t.Fatalf("interrupted event %+v", evs)
	}
}

func TestUserCancelStaysCancelled(t *testing.T) {
	m, _ := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	m.Register(Spec{Type: "t.long", Cancellable: true, Run: func(ctx context.Context, r *Run) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	m.Start(ctx)
	j, _ := m.Enqueue(ctx, "t.long", "", nil, audit.System)
	<-started
	if err := m.Cancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, j.ID, Cancelled)
}

func TestBlockedJobsDoNotStarveTheQueue(t *testing.T) {
	m, _ := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	m.Register(Spec{Type: "t.heavy", Heavy: true, Priority: 90, Run: func(context.Context, *Run) error { <-release; return nil }})
	m.Register(Spec{Type: "t.light", Priority: 10, Run: func(context.Context, *Run) error { return nil }})
	m.Start(ctx)
	defer func() { close(release); m.Shutdown(5 * time.Second) }()
	first, _ := m.Enqueue(ctx, "t.heavy", "", nil, audit.System)
	waitState(t, m, first.ID, Running)
	// More blocked heavy jobs than one page of the queue, all ahead of the light one.
	for i := 0; i < dispatchPage+5; i++ {
		if _, err := m.Enqueue(ctx, "t.heavy", "", nil, audit.System); err != nil {
			t.Fatal(err)
		}
	}
	light, _ := m.Enqueue(ctx, "t.light", "", nil, audit.System)
	waitState(t, m, light.ID, Succeeded)
}

func TestPanicInJobIsReportedInEnglish(t *testing.T) {
	m, _ := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Register(Spec{Type: "t.panic", Run: func(context.Context, *Run) error { panic("oops") }})
	m.Start(ctx)
	j, _ := m.Enqueue(ctx, "t.panic", "", nil, audit.System)
	j = waitState(t, m, j.ID, Failed)
	if j.ErrorCode != "SQS-X999" || j.ErrorDetail != "SQS-X999 internal error: oops" {
		t.Fatalf("%s %q", j.ErrorCode, j.ErrorDetail)
	}
	evs := waitEvents(t, m, events.JobFailed, 1)
	if len(evs) != 1 || evs[0].Data["job_type"] != "t.panic" {
		t.Fatalf("failed event %+v", evs)
	}
}
