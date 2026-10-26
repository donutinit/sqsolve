// SPDX-License-Identifier: AGPL-3.0-only

package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
)

// syncBuffer is a log destination safe for the scheduler and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// loggedScheduler is newScheduler with a log the test can read.
func loggedScheduler(t *testing.T) (*Scheduler, *jobs.Manager, *events.Recorder, *store.DB, *syncBuffer) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	bus := events.NewBus()
	ev := events.NewRecorder(db, bus, log)
	jm := jobs.NewManager(db, ev, bus, log, 1, 1)
	jm.Register(jobs.Spec{Type: "backup.create", Run: func(context.Context, *jobs.Run) error { return nil }})
	jm.Register(jobs.Spec{Type: "backup.verify", Run: func(context.Context, *jobs.Run) error { return nil }})
	return New(db, jm, ev, log), jm, ev, db, logs
}

// insertSchedule stores a schedule row as is, for rows Upsert would refuse.
func insertSchedule(t *testing.T, db *store.DB, id, kind, target, cron, tz string, next time.Time) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO schedules(id, kind, target_id, cron, timezone, enabled, misfire_window_minutes, next_run_at, created_at) VALUES (?, ?, ?, ?, ?, 1, 60, ?, ?)`,
		id, kind, target, cron, tz, store.T(next), store.NowT()); err != nil {
		t.Fatal(err)
	}
}

func queued(t *testing.T, jm *jobs.Manager) []*jobs.Job {
	t.Helper()
	js, err := jm.List(context.Background(), jobs.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return js
}

// A due run of a kind without jitter enqueues a job for no library, with
// the target in its parameters, created by the schedule; the schedule
// records the run, the job and its next run.
func TestDueRunEnqueuesAScheduledJob(t *testing.T) {
	ctx := context.Background()
	s, jm, _, _, _ := loggedScheduler(t)
	s.RegisterKind("verify", Kind{JobType: "backup.verify", Title: "verification"})
	base := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	if err := s.Upsert(ctx, "verify", "LIB1", "0 2 * * *", "UTC", true, time.Hour); err != nil {
		t.Fatal(err)
	}
	at := base.Add(time.Hour + 30*time.Minute) // late, but within the window
	s.Now = func() time.Time { return at }
	s.Tick(ctx)
	js := queued(t, jm)
	if len(js) != 1 {
		t.Fatalf("%d jobs", len(js))
	}
	sc, _ := s.Get(ctx, "verify", "LIB1")
	j := js[0]
	var params map[string]string
	json.Unmarshal(j.Params, &params)
	if j.Type != "backup.verify" || j.LibraryID != "" || params["target"] != "LIB1" || params["kind"] != "scheduled" ||
		j.ScheduleID != sc.ID || j.CreatedBy.Type != "schedule" || j.CreatedBy.ID != sc.ID {
		t.Fatalf("job %+v, params %v", j, params)
	}
	if sc.LastJobID != j.ID || sc.LastRunAt == nil || !sc.LastRunAt.Equal(at) || !sc.NextRunAt.Equal(time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("schedule %+v", sc)
	}
}

// A run missed by more than its window is not enqueued: an error event
// says which run, in the schedule's time zone, and the schedule moves on to
// its next run keeping the last job.
func TestMissedRunIsReported(t *testing.T) {
	ctx := context.Background()
	s, jm, ev, _, _ := loggedScheduler(t)
	s.RegisterKind("verify", Kind{JobType: "backup.verify", Title: "verification"})
	mx, _ := time.LoadLocation("America/Mexico_City")
	base := time.Date(2026, 10, 19, 1, 0, 0, 0, mx)
	s.Now = func() time.Time { return base }
	if err := s.Upsert(ctx, "verify", "LIB1", "0 2 * * *", "America/Mexico_City", true, time.Hour); err != nil {
		t.Fatal(err)
	}
	at := base.Add(3 * time.Hour) // 04:00, two hours after the 02:00 run
	s.Now = func() time.Time { return at }
	s.Tick(ctx)
	if js := queued(t, jm); len(js) != 0 {
		t.Fatalf("a missed run was enqueued: %+v", js)
	}
	evs, _ := ev.List(ctx, events.Filter{TypePrefix: events.BackupMissed})
	if len(evs) != 1 {
		t.Fatalf("%d events", len(evs))
	}
	e := evs[0]
	if e.Severity != events.Error || e.LibraryID != "LIB1" || e.Message != "Missed the scheduled verification run from 2026-10-19 02:00 (the server was down)" || e.Data["kind"] != "verify" {
		t.Fatalf("event %+v", e)
	}
	sc, _ := s.Get(ctx, "verify", "LIB1")
	if sc.LastJobID != "" || !sc.LastRunAt.Equal(at) || !sc.NextRunAt.Equal(time.Date(2026, 10, 20, 2, 0, 0, 0, mx)) {
		t.Fatalf("schedule %+v, next %s", sc, sc.NextRunAt)
	}
}

// Disabling the schedules of a target stops all of its runs and only its
// own; enabling them again resumes them.
func TestSetEnabledTogglesEverySchedulesOfATarget(t *testing.T) {
	ctx := context.Background()
	s, jm, _, db, _ := loggedScheduler(t)
	s.RegisterKind("backup", Kind{JobType: "backup.create", Jitter: true, Title: "backup"})
	s.RegisterKind("verify", Kind{JobType: "backup.verify", Jitter: true, Title: "verification"})
	base := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	for _, k := range []struct{ kind, target string }{{"backup", "LIB1"}, {"verify", "LIB1"}, {"backup", "LIB2"}} {
		if err := s.Upsert(ctx, k.kind, k.target, "0 2 * * *", "UTC", true, 6*time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetEnabled(ctx, "LIB1", false); err != nil {
		t.Fatal(err)
	}
	all, _ := s.List(ctx)
	for _, sc := range all {
		if sc.Enabled != (sc.TargetID == "LIB2") {
			t.Fatalf("%s %s enabled %v", sc.Kind, sc.TargetID, sc.Enabled)
		}
	}
	s.Now = func() time.Time { return base.Add(2 * time.Hour) } // after every jitter
	s.Tick(ctx)
	if js := queued(t, jm); len(js) != 1 || js[0].LibraryID != "LIB2" {
		t.Fatalf("jobs while LIB1 is disabled: %+v", js)
	}
	if err := s.SetEnabled(ctx, "LIB1", true); err != nil {
		t.Fatal(err)
	}
	s.Tick(ctx)
	js := queued(t, jm)
	types := map[string]bool{}
	for _, j := range js {
		if j.LibraryID == "LIB1" {
			types[j.Type] = true
		}
	}
	if len(js) != 3 || !types["backup.create"] || !types["backup.verify"] {
		t.Fatalf("jobs after enabling LIB1: %+v", js)
	}
	db.Close()
	if err := s.SetEnabled(ctx, "LIB1", false); err == nil || err.Error() != "sql: database is closed" {
		t.Fatalf("closed database: %v", err)
	}
}

// Delete removes only the schedule of that kind and target; Get then
// reports none and List keeps the others in kind and target order.
func TestDeleteRemovesOneSchedule(t *testing.T) {
	ctx := context.Background()
	s, _, _, db, _ := loggedScheduler(t)
	s.RegisterKind("backup", Kind{JobType: "backup.create", Title: "backup"})
	s.RegisterKind("verify", Kind{JobType: "backup.verify", Title: "verification"})
	for _, k := range []struct{ kind, target string }{{"verify", "LIB1"}, {"backup", "LIB2"}, {"backup", "LIB1"}} {
		if err := s.Upsert(ctx, k.kind, k.target, "0 2 * * *", "UTC", true, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete(ctx, "backup", "LIB1"); err != nil {
		t.Fatal(err)
	}
	if sc, err := s.Get(ctx, "backup", "LIB1"); sc != nil || err != nil {
		t.Fatalf("deleted schedule %+v: %v", sc, err)
	}
	all, err := s.List(ctx)
	if err != nil || len(all) != 2 || all[0].Kind+" "+all[0].TargetID != "backup LIB2" || all[1].Kind+" "+all[1].TargetID != "verify LIB1" {
		t.Fatalf("left %+v: %v", all, err)
	}
	db.Close()
	if err := s.Delete(ctx, "verify", "LIB1"); err == nil || err.Error() != "sql: database is closed" {
		t.Fatalf("closed database delete: %v", err)
	}
	if _, err := s.List(ctx); err == nil || err.Error() != "sql: database is closed" {
		t.Fatalf("closed database list: %v", err)
	}
}

// A row that cannot be read fails List with the cause, and Tick skips it
// and still fires the healthy schedules.
func TestUnreadableRowIsSkippedByTick(t *testing.T) {
	ctx := context.Background()
	s, jm, _, db, _ := loggedScheduler(t)
	s.RegisterKind("verify", Kind{JobType: "backup.verify", Title: "verification"})
	base := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	if err := s.Upsert(ctx, "verify", "LIB1", "0 2 * * *", "UTC", true, time.Hour); err != nil {
		t.Fatal(err)
	}
	insertSchedule(t, db, "BROKEN", "verify", "LIB2", "0 2 * * *", "UTC", base)
	db.Exec(`UPDATE schedules SET misfire_window_minutes = 'lots' WHERE id = 'BROKEN'`)
	if _, err := s.List(ctx); err == nil || !strings.Contains(err.Error(), `name "misfire_window_minutes"`) {
		t.Fatalf("list: %v", err)
	}
	s.Now = func() time.Time { return base.Add(time.Hour + time.Second) }
	s.Tick(ctx)
	if js := queued(t, jm); len(js) != 1 {
		t.Fatalf("%d jobs, want the healthy one", len(js))
	}
}

// Schedules that cannot be computed are logged and left as they are, with
// no job: a kind this process does not know, a time zone that does not
// exist, an expression whose date never comes.
func TestInvalidSchedulesAreLoggedAndKept(t *testing.T) {
	ctx := context.Background()
	s, jm, _, db, logs := loggedScheduler(t)
	s.RegisterKind("verify", Kind{JobType: "backup.verify", Title: "verification"})
	due := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	insertSchedule(t, db, "UNKNOWNKIND", "pigeon", "LIB1", "0 2 * * *", "UTC", due)
	insertSchedule(t, db, "BADZONE", "verify", "LIB2", "0 2 * * *", "Mars/Base", due)
	insertSchedule(t, db, "NEVER", "verify", "LIB3", "0 0 31 2 *", "UTC", due)
	s.Now = func() time.Time { return due.Add(time.Minute) }
	s.Tick(ctx)
	if js := queued(t, jm); len(js) != 0 {
		t.Fatalf("jobs %+v", js)
	}
	all, _ := s.List(ctx)
	for _, sc := range all {
		if !sc.NextRunAt.Equal(due) || sc.LastRunAt != nil {
			t.Errorf("%s moved: next %s, last %v", sc.ID, sc.NextRunAt, sc.LastRunAt)
		}
	}
	out := logs.String()
	for _, want := range []string{
		`msg="invalid schedule" id=UNKNOWNKIND kind=pigeon`,
		`msg="invalid schedule" id=BADZONE kind=verify`,
		`msg=schedule id=NEVER err=`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

// A run whose job cannot be enqueued stays due, so the next tick tries
// again; the cause is logged.
func TestEnqueueFailureKeepsTheRunDue(t *testing.T) {
	ctx := context.Background()
	s, jm, _, _, logs := loggedScheduler(t)
	s.RegisterKind("vacuum", Kind{JobType: "system.vacuum", Title: "vacuum"})
	base := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	if err := s.Upsert(ctx, "vacuum", "", "0 2 * * *", "UTC", true, time.Hour); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return base.Add(time.Hour + time.Second) }
	s.Tick(ctx)
	s.Tick(ctx)
	if js := queued(t, jm); len(js) != 0 {
		t.Fatalf("jobs %+v", js)
	}
	sc, _ := s.Get(ctx, "vacuum", "")
	if !sc.NextRunAt.Equal(base.Add(time.Hour)) || sc.LastRunAt != nil {
		t.Fatalf("schedule moved: %+v", sc)
	}
	if n := strings.Count(logs.String(), `msg="could not enqueue" schedule=`+sc.ID+` err="SQS-J001 unknown job type: system.vacuum"`); n != 2 {
		t.Fatalf("%d enqueue errors logged, want one per tick:\n%s", n, logs.String())
	}
}

// When the schedule cannot be moved to its next run, the failure is logged
// both after a run and after a panic; the job of the run is kept.
func TestScheduleUpdateFailureIsLogged(t *testing.T) {
	ctx := context.Background()
	s, jm, _, db, logs := loggedScheduler(t)
	s.RegisterKind("verify", Kind{JobType: "backup.verify", Title: "verification"})
	s.RegisterKind("backup", Kind{JobType: "backup.create", Jitter: true, Title: "backup", Gate: func(context.Context, string) (bool, string) { panic("bad gate") }})
	base := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	for _, kind := range []string{"verify", "backup"} {
		if err := s.Upsert(ctx, kind, "LIB1", "0 2 * * *", "UTC", true, 6*time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER schedules_stuck BEFORE UPDATE ON schedules BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return base.Add(2 * time.Hour) }
	s.Tick(ctx)
	if js := queued(t, jm); len(js) != 1 || js[0].Type != "backup.verify" {
		t.Fatalf("jobs %+v", js)
	}
	if n := strings.Count(logs.String(), `msg="could not move the schedule to its next run"`); n != 2 {
		t.Fatalf("%d update failures logged, want 2:\n%s", n, logs.String())
	}
}

// After a panic, a kind with jitter moves to its next run shifted by the
// target's jitter, like a normal run.
func TestPanicMovesToTheJitteredNextRun(t *testing.T) {
	ctx := context.Background()
	s, _, ev, _, _ := loggedScheduler(t)
	s.RegisterKind("backup", Kind{JobType: "backup.create", Jitter: true, Title: "backup", Gate: func(context.Context, string) (bool, string) { panic("bad gate") }})
	base := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	if err := s.Upsert(ctx, "backup", "LIB1", "0 2 * * *", "UTC", true, 6*time.Hour); err != nil {
		t.Fatal(err)
	}
	sc, _ := s.Get(ctx, "backup", "LIB1")
	s.Now = func() time.Time { return base.Add(2 * time.Hour) }
	s.Tick(ctx)
	got, _ := s.Get(ctx, "backup", "LIB1")
	if want := sc.NextRunAt.Add(24 * time.Hour); !got.NextRunAt.Equal(want) {
		t.Fatalf("next %s, want %s", got.NextRunAt, want)
	}
	evs, _ := ev.List(ctx, events.Filter{TypePrefix: events.SystemInternalError})
	want := "Internal error in the scheduler (schedule " + sc.ID + ", backup LIB1) (it keeps running): bad gate"
	if len(evs) != 1 || evs[0].Message != want || evs[0].Data["loop"] != "scheduler" {
		t.Fatalf("events %+v, want %q", evs, want)
	}
}

// A tick that cannot read the schedules logs the cause and does nothing.
func TestTickOnAClosedDatabase(t *testing.T) {
	s, _, _, db, logs := loggedScheduler(t)
	db.Close()
	s.Tick(context.Background())
	if !strings.Contains(logs.String(), `msg=scheduler err="sql: database is closed"`) {
		t.Fatalf("log %s", logs.String())
	}
}
