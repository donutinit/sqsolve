// SPDX-License-Identifier: AGPL-3.0-only

package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
)

func newScheduler(t *testing.T) (*Scheduler, *jobs.Manager, *events.Recorder, *store.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	ev := events.NewRecorder(db, bus, log)
	jm := jobs.NewManager(db, ev, bus, log, 1, 1)
	jm.Register(jobs.Spec{Type: "backup.create", Run: func(context.Context, *jobs.Run) error { return nil }})
	jm.Register(jobs.Spec{Type: "backup.verify", Run: func(context.Context, *jobs.Run) error { return nil }})
	return New(db, jm, ev, log), jm, ev, db
}

func TestValidateSpeaksEnglish(t *testing.T) {
	_, err := Validate("0 2 * * *", "Mars/Base")
	if !errors.Is(err, ErrInvalidCron) || err.Error() != `SQS-J020 invalid schedule expression: time zone "Mars/Base"` {
		t.Fatalf("%v", err)
	}
}

func TestTickSurvivesAPanickingSchedule(t *testing.T) {
	ctx := context.Background()
	s, jm, ev, _ := newScheduler(t)
	s.RegisterKind("backup", Kind{JobType: "backup.create", Title: "backup", Gate: func(context.Context, string) (bool, string) { panic("bad policy") }})
	s.RegisterKind("verify", Kind{JobType: "backup.verify", Title: "verification"})
	base := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	for _, kind := range []string{"backup", "verify"} {
		if err := s.Upsert(ctx, kind, "LIB1", "0 2 * * *", "UTC", true, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	s.Now = func() time.Time { return base.Add(time.Hour + time.Second) }
	s.Tick(ctx) // the panic in one schedule used to stop the process
	js, _ := jm.List(ctx, jobs.Filter{})
	if len(js) != 1 || js[0].Type != "backup.verify" {
		t.Fatalf("the healthy schedule did not run: %+v", js)
	}
	evs, _ := ev.List(ctx, events.Filter{TypePrefix: events.SystemInternalError})
	if len(evs) != 1 || evs[0].Severity != events.Critical || !strings.Contains(evs[0].Message, "scheduler") || !strings.Contains(evs[0].Message, "bad policy") {
		t.Fatalf("panic event %+v", evs)
	}
	// The broken schedule moved on to its next run: the next tick does not
	// report it again.
	sc, _ := s.Get(ctx, "backup", "LIB1")
	if !sc.NextRunAt.Equal(time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("next run %s", sc.NextRunAt)
	}
	s.Tick(ctx)
	if evs, _ := ev.List(ctx, events.Filter{TypePrefix: events.SystemInternalError}); len(evs) != 1 {
		t.Fatalf("reported again: %d", len(evs))
	}
}

func TestDueRunsGoOnAfterAGhostJob(t *testing.T) {
	ctx := context.Background()
	s, jm, _, db := newScheduler(t)
	s.RegisterKind("backup", Kind{JobType: "backup.create", Jitter: true, Title: "backup"})
	base := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	if err := s.Upsert(ctx, "backup", "LIB1", "0 * * * *", "UTC", true, time.Hour); err != nil {
		t.Fatal(err)
	}
	// What a failed final update leaves behind: a row that says running and
	// that no worker of this process runs.
	if _, err := db.ExecContext(ctx, `INSERT INTO jobs(id, type, library_id, state, scheduled_for, created_at, created_by_type) VALUES ('GHOST', 'backup.create', 'LIB1', 'running', ?, ?, 'schedule')`, store.NowT(), store.NowT()); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		sc, _ := s.Get(ctx, "backup", "LIB1")
		due := *sc.NextRunAt
		s.Now = func() time.Time { return due.Add(time.Second) }
		s.Tick(ctx)
		js, _ := jm.List(ctx, jobs.Filter{States: []jobs.State{jobs.Queued}})
		if len(js) != 1 {
			t.Fatalf("run %d: %d queued jobs", i, len(js))
		}
		db.ExecContext(ctx, `UPDATE jobs SET state = 'succeeded' WHERE id = ?`, js[0].ID)
	}
}
