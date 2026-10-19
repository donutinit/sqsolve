// SPDX-License-Identifier: AGPL-3.0-or-later

package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
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

func TestRunStepsLogsResult(t *testing.T) {
	m, _ := newManager(t)
	m.Register(Spec{Type: "t.ok", Run: func(ctx context.Context, r *Run) error {
		var p struct{ N int }
		r.Params(&p)
		r.Step("uno", func() error { r.Infof("n=%d", p.N); return nil })
		r.Step("dos", func() error { r.Journal(map[string]string{"x": "y"}); r.Progress(50); return nil })
		r.SetResult(map[string]int{"n": p.N})
		return nil
	}})
	m.Register(Spec{Type: "t.fail", Run: func(ctx context.Context, r *Run) error {
		return r.Step("roto", func() error { return errors.New("SQS-B012 sin espacio") })
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
