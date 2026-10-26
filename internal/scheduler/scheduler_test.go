// SPDX-License-Identifier: AGPL-3.0-only

package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
)

func TestValidate(t *testing.T) {
	if _, err := Validate("0 2 * * *", "America/Mexico_City"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "0 2 * *", "0 0 2 * * *", "61 * * * *", "nope"} {
		if _, err := Validate(bad, "UTC"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := Validate("0 2 * * *", "Mars/Base"); err == nil {
		t.Error("bad tz accepted")
	}
}

func TestNextInZone(t *testing.T) {
	loc, _ := time.LoadLocation("America/Mexico_City")
	ref := time.Date(2026, 10, 18, 10, 0, 0, 0, loc)
	n, err := Next("0 2 * * *", loc, ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 19, 2, 0, 0, 0, loc)
	if !n.Equal(want) {
		t.Fatalf("got %s want %s", n, want)
	}
	// With jitter, running exactly at tick+jitter yields the next day.
	j := 90 * time.Second
	n1, _ := Next("0 2 * * *", loc, ref, j)
	n2, _ := Next("0 2 * * *", loc, n1, j)
	if !n1.Equal(want.Add(j)) || !n2.Equal(want.Add(24*time.Hour+j)) {
		t.Fatalf("jitter: %s %s", n1, n2)
	}
	up, _ := Upcoming("0 */4 * * 1-5", "UTC", time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC), 3)
	// Friday 21:00: the next weekday slot is Monday 00:00.
	if len(up) != 3 || up[0] != time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("weekday cron: %v", up)
	}
}

func TestJitterStable(t *testing.T) {
	if a := JitterFor("A"); a != JitterFor(string([]byte{65})) || JitterFor("A") >= 5*time.Minute || JitterFor("") != 0 {
		t.Fatal("jitter")
	}
}

func TestTickEnqueuesAndMisfire(t *testing.T) {
	ctx := context.Background()
	db, _ := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"), "")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	ev := events.NewRecorder(db, bus, log)
	jm := jobs.NewManager(db, ev, bus, log, 1, 1)
	jm.Register(jobs.Spec{Type: "backup.create", Run: func(context.Context, *jobs.Run) error { return nil }})
	s := New(db, jm, ev, log)
	s.RegisterKind("backup", Kind{JobType: "backup.create", Jitter: true, Title: "backup"})
	base := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	if err := s.Upsert(ctx, "backup", "LIB1", "0 2 * * *", "UTC", true, time.Hour); err != nil {
		t.Fatal(err)
	}
	sc, _ := s.Get(ctx, "backup", "LIB1")
	due := *sc.NextRunAt
	// Not due yet.
	s.Tick(ctx)
	if js, _ := jm.List(ctx, jobs.Filter{}); len(js) != 0 {
		t.Fatal("enqueued too early")
	}
	// Due: one job, next moves a day ahead.
	s.Now = func() time.Time { return due.Add(time.Second) }
	s.Tick(ctx)
	js, _ := jm.List(ctx, jobs.Filter{})
	if len(js) != 1 || js[0].LibraryID != "LIB1" {
		t.Fatalf("jobs %+v", js)
	}
	sc, _ = s.Get(ctx, "backup", "LIB1")
	if !sc.NextRunAt.Equal(due.Add(24 * time.Hour)) {
		t.Fatalf("next %s", sc.NextRunAt)
	}
	// Missed by more than the window: event, no job.
	s.Now = func() time.Time { return due.Add(24*time.Hour + 3*time.Hour) }
	s.Tick(ctx)
	js, _ = jm.List(ctx, jobs.Filter{})
	if len(js) != 1 {
		t.Fatalf("missed run was enqueued: %d", len(js))
	}
	evs, _ := ev.List(ctx, events.Filter{TypePrefix: "backup.missed"})
	if len(evs) != 1 {
		t.Fatalf("missed event: %d", len(evs))
	}
}

func TestGateSkipsRunWithoutJob(t *testing.T) {
	ctx := context.Background()
	db, _ := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"), "")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	ev := events.NewRecorder(db, bus, log)
	jm := jobs.NewManager(db, ev, bus, log, 1, 1)
	jm.Register(jobs.Spec{Type: "backup.create", Run: func(context.Context, *jobs.Run) error { return nil }})
	s := New(db, jm, ev, log)
	open := false
	s.RegisterKind("backup", Kind{JobType: "backup.create", Title: "backup", Gate: func(context.Context, string) (bool, string) { return open, "no changes" }})
	base := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	if err := s.Upsert(ctx, "backup", "LIB1", "*/15 * * * *", "UTC", true, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	sc, _ := s.Get(ctx, "backup", "LIB1")
	due := *sc.NextRunAt
	// Closed gate: no job, but the schedule moves on to the next check.
	s.Now = func() time.Time { return due.Add(time.Second) }
	s.Tick(ctx)
	if js, _ := jm.List(ctx, jobs.Filter{}); len(js) != 0 {
		t.Fatalf("gated run was enqueued: %d", len(js))
	}
	sc, _ = s.Get(ctx, "backup", "LIB1")
	if !sc.NextRunAt.Equal(due.Add(15 * time.Minute)) {
		t.Fatalf("next %s, want %s", sc.NextRunAt, due.Add(15*time.Minute))
	}
	// Open gate: the next check enqueues.
	open = true
	s.Now = func() time.Time { return sc.NextRunAt.Add(time.Second) }
	s.Tick(ctx)
	if js, _ := jm.List(ctx, jobs.Filter{}); len(js) != 1 {
		t.Fatalf("open gate did not enqueue: %d", len(js))
	}
}

// An expression whose date never comes (31 February) is refused when it is
// stored, with SQS-J020, instead of failing later without a code after the
// backup policy was already saved.
func TestValidateRefusesADateThatNeverComes(t *testing.T) {
	_, err := Validate("0 0 31 2 *", "UTC")
	if !errors.Is(err, ErrInvalidCron) || err.Error() != `SQS-J020 invalid schedule expression: "0 0 31 2 *" never runs` {
		t.Fatalf("got %v", err)
	}
	if _, err := Validate("0 0 29 2 *", "UTC"); err != nil {
		t.Fatalf("29 February comes every four years: %v", err)
	}
}
