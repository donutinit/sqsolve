// SPDX-License-Identifier: AGPL-3.0-only

// Package scheduler turns cron schedules into jobs, with a fixed per-target
// offset and a policy for runs missed while the server was down.
package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"time"

	"github.com/adhocore/gronx"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// Schedule is one stored schedule.
type Schedule struct {
	ID            string
	Kind          string
	TargetID      string
	Cron          string
	Timezone      string
	Enabled       bool
	MisfireWindow time.Duration
	NextRunAt     *time.Time
	LastRunAt     *time.Time
	LastJobID     string
}

// Kind maps a schedule kind to a job type and whether it gets a jitter.
type Kind struct {
	JobType string
	Jitter  bool
	Title   string
	// Gate, when set, is asked before enqueueing a due run. A false answer
	// skips this run without a job and moves on to the next one; the reason
	// is only logged.
	Gate func(ctx context.Context, target string) (run bool, reason string)
}

// ErrInvalidCron is returned for bad expressions.
var ErrInvalidCron = errors.New("SQS-J020 invalid schedule expression")

// Validate checks a 5-field cron expression and a time zone.
func Validate(cron, tz string) (*time.Location, error) {
	if len(strings.Fields(cron)) != 5 || !gronx.IsValid(cron) {
		return nil, fmt.Errorf("%w: %q (use 5 fields: minute hour day month day-of-week)", ErrInvalidCron, cron)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("%w: zona horaria %q", ErrInvalidCron, tz)
	}
	return loc, nil
}

// Next returns the first run strictly after ref, shifted by jitter.
func Next(cron string, loc *time.Location, ref time.Time, jitter time.Duration) (time.Time, error) {
	t, err := gronx.NextTickAfter(cron, ref.Add(-jitter).In(loc), false)
	if err != nil {
		return time.Time{}, err
	}
	return t.Add(jitter), nil
}

// Upcoming lists the next n runs after ref (for previews).
func Upcoming(cron, tz string, ref time.Time, n int) ([]time.Time, error) {
	loc, err := Validate(cron, tz)
	if err != nil {
		return nil, err
	}
	var out []time.Time
	for i := 0; i < n; i++ {
		t, err := Next(cron, loc, ref, 0)
		if err != nil {
			return out, err
		}
		out = append(out, t)
		ref = t
	}
	return out, nil
}

// JitterFor is a stable 0 to 5 minute offset derived from the target.
func JitterFor(target string) time.Duration {
	if target == "" {
		return 0
	}
	h := fnv.New32a()
	h.Write([]byte(target))
	return time.Duration(h.Sum32()%300) * time.Second
}

// Scheduler enqueues due jobs.
type Scheduler struct {
	db    *store.DB
	jobs  *jobs.Manager
	ev    *events.Recorder
	log   *slog.Logger
	kinds map[string]Kind
	Now   func() time.Time
}

// New creates a scheduler.
func New(db *store.DB, jm *jobs.Manager, ev *events.Recorder, log *slog.Logger) *Scheduler {
	return &Scheduler{db: db, jobs: jm, ev: ev, log: log, kinds: map[string]Kind{}, Now: time.Now}
}

// RegisterKind maps a schedule kind to a job type.
func (s *Scheduler) RegisterKind(kind string, k Kind) { s.kinds[kind] = k }

// Upsert creates or updates the schedule of (kind, target).
func (s *Scheduler) Upsert(ctx context.Context, kind, target, cron, tz string, enabled bool, misfire time.Duration) error {
	k, ok := s.kinds[kind]
	if !ok {
		return fmt.Errorf("SQS-J021 unknown schedule kind: %s", kind)
	}
	loc, err := Validate(cron, tz)
	if err != nil {
		return err
	}
	var jitter time.Duration
	if k.Jitter {
		jitter = JitterFor(target)
	}
	next, err := Next(cron, loc, s.Now(), jitter)
	if err != nil {
		return err
	}
	if misfire <= 0 {
		misfire = 12 * time.Hour
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO schedules(id, kind, target_id, cron, timezone, enabled, misfire_window_minutes, next_run_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, target_id) DO UPDATE SET cron = excluded.cron, timezone = excluded.timezone, enabled = excluded.enabled,
		misfire_window_minutes = excluded.misfire_window_minutes, next_run_at = excluded.next_run_at`,
		ulid.New(), kind, target, cron, tz, enabled, int(misfire/time.Minute), store.T(next), store.NowT())
	return err
}

// Delete removes the schedule of (kind, target).
func (s *Scheduler) Delete(ctx context.Context, kind, target string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE kind = ? AND target_id = ?`, kind, target)
	return err
}

// SetEnabled toggles every schedule of a target.
func (s *Scheduler) SetEnabled(ctx context.Context, target string, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE schedules SET enabled = ? WHERE target_id = ?`, enabled, target)
	return err
}

const cols = `id, kind, target_id, cron, timezone, enabled, misfire_window_minutes, next_run_at, last_run_at, last_job_id`

func scan(row interface{ Scan(...any) error }) (*Schedule, error) {
	var sc Schedule
	var mins int
	var next, last sql.NullString
	if err := row.Scan(&sc.ID, &sc.Kind, &sc.TargetID, &sc.Cron, &sc.Timezone, &sc.Enabled, &mins, &next, &last, &sc.LastJobID); err != nil {
		return nil, err
	}
	sc.MisfireWindow = time.Duration(mins) * time.Minute
	sc.NextRunAt = store.NullT(next)
	sc.LastRunAt = store.NullT(last)
	return &sc, nil
}

// Get returns the schedule of (kind, target) or nil.
func (s *Scheduler) Get(ctx context.Context, kind, target string) (*Schedule, error) {
	sc, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM schedules WHERE kind = ? AND target_id = ?`, kind, target))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return sc, err
}

// List returns all schedules.
func (s *Scheduler) List(ctx context.Context) ([]*Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM schedules ORDER BY kind, target_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Schedule
	for rows.Next() {
		sc, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// Run checks due schedules until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	s.Tick(ctx)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

// Tick enqueues every due schedule once.
func (s *Scheduler) Tick(ctx context.Context) {
	now := s.Now()
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM schedules WHERE enabled = 1 AND next_run_at <= ?`, store.T(now))
	if err != nil {
		s.log.Error("programador", "err", err)
		return
	}
	var due []*Schedule
	for rows.Next() {
		if sc, err := scan(rows); err == nil {
			due = append(due, sc)
		}
	}
	rows.Close()
	for _, sc := range due {
		s.fire(ctx, sc, now)
	}
}

func (s *Scheduler) fire(ctx context.Context, sc *Schedule, now time.Time) {
	k, ok := s.kinds[sc.Kind]
	loc, err := time.LoadLocation(sc.Timezone)
	if !ok || err != nil {
		s.log.Error("invalid schedule", "id", sc.ID, "kind", sc.Kind)
		return
	}
	var jitter time.Duration
	if k.Jitter {
		jitter = JitterFor(sc.TargetID)
	}
	next, err := Next(sc.Cron, loc, now, jitter)
	if err != nil {
		s.log.Error("schedule", "id", sc.ID, "err", err)
		return
	}
	jobID := sc.LastJobID
	run, why := true, ""
	if k.Gate != nil {
		run, why = k.Gate(ctx, sc.TargetID)
	}
	if sc.NextRunAt != nil && now.Sub(*sc.NextRunAt) > sc.MisfireWindow {
		s.ev.Emit(ctx, events.Event{Type: events.BackupMissed, Severity: events.Error, LibraryID: sc.TargetID,
			Message: fmt.Sprintf("Missed the scheduled %s run from %s (the server was down)", k.Title, sc.NextRunAt.In(loc).Format("2006-01-02 15:04")),
			Data:    map[string]any{"kind": sc.Kind, "due": sc.NextRunAt}})
	} else if !run {
		s.log.Debug("schedule skipped", "kind", sc.Kind, "target", sc.TargetID, "reason", why)
	} else {
		lib := ""
		if k.Jitter {
			lib = sc.TargetID
		}
		actor := audit.Actor{Type: "schedule", ID: sc.ID, Name: "schedule"}
		j, err := s.jobs.Enqueue(ctx, k.JobType, lib, map[string]string{"target": sc.TargetID, "kind": "scheduled"}, actor, jobs.FromSchedule(sc.ID), jobs.Unique())
		if err != nil {
			s.log.Error("could not enqueue", "schedule", sc.ID, "err", err)
			return
		}
		jobID = j.ID
	}
	s.db.ExecContext(ctx, `UPDATE schedules SET next_run_at = ?, last_run_at = ?, last_job_id = ? WHERE id = ?`,
		store.T(next), store.T(now), jobID, sc.ID)
}
