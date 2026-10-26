// SPDX-License-Identifier: AGPL-3.0-only

package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// runUntil starts Run and returns a function that cancels it and waits
// until it returns.
func runUntil(t *testing.T, s *Scheduler) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after its context ended")
		}
	}
}

// Run checks the schedules as soon as it starts, and returns when its
// context ends.
func TestRunTicksAtStartAndStops(t *testing.T) {
	ctx := context.Background()
	s, jm, _, _ := newScheduler(t)
	s.RegisterKind("verify", Kind{JobType: "backup.verify", Title: "verification"})
	base := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	var clock atomic.Value
	clock.Store(base)
	s.Now = func() time.Time { return clock.Load().(time.Time) }
	if err := s.Upsert(ctx, "verify", "LIB1", "0 2 * * *", "UTC", true, time.Hour); err != nil {
		t.Fatal(err)
	}
	clock.Store(base.Add(time.Hour + time.Second))
	stop := runUntil(t, s)
	deadline := time.Now().Add(5 * time.Second)
	for {
		js, _ := jm.List(ctx, jobs.Filter{})
		if len(js) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the first tick did not enqueue: %d jobs", len(js))
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
}

// A panic in a whole tick (here the clock) does not stop Run: it is
// reported as a critical event and the loop goes on until its context ends.
func TestRunSurvivesAPanickingTick(t *testing.T) {
	ctx := context.Background()
	s, _, ev, _ := newScheduler(t)
	s.Now = func() time.Time { panic("clock broke") }
	stop := runUntil(t, s)
	var evs []events.Event
	deadline := time.Now().Add(5 * time.Second)
	for len(evs) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the panic was not reported")
		}
		time.Sleep(10 * time.Millisecond)
		evs, _ = ev.List(ctx, events.Filter{TypePrefix: events.SystemInternalError})
	}
	stop()
	e := evs[0]
	if len(evs) != 1 || e.Severity != events.Critical || e.Message != "Internal error in the scheduler (it keeps running): clock broke" || e.Data["loop"] != "scheduler" {
		t.Fatalf("events %+v", evs)
	}
}
