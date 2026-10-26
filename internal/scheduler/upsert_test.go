// SPDX-License-Identifier: AGPL-3.0-only

package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Upsert refuses a kind nobody registered (SQS-J021) and an invalid
// expression (SQS-J020) without storing anything.
func TestUpsertRefusesUnknownKindAndBadCron(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newScheduler(t)
	s.RegisterKind("backup", Kind{JobType: "backup.create", Title: "backup"})
	err := s.Upsert(ctx, "nope", "LIB1", "0 2 * * *", "UTC", true, time.Hour)
	if err == nil || err.Error() != "SQS-J021 unknown schedule kind: nope" {
		t.Fatalf("unknown kind: %v", err)
	}
	err = s.Upsert(ctx, "backup", "LIB1", "0 2 * *", "UTC", true, time.Hour)
	if !errors.Is(err, ErrInvalidCron) || err.Error() != `SQS-J020 invalid schedule expression: "0 2 * *" (use 5 fields: minute hour day month day-of-week)` {
		t.Fatalf("bad cron: %v", err)
	}
	if err := s.Upsert(ctx, "backup", "LIB1", "0 2 * * *", "Mars/Base", true, time.Hour); err == nil || err.Error() != `SQS-J020 invalid schedule expression: time zone "Mars/Base"` {
		t.Fatalf("bad time zone: %v", err)
	}
	if all, err := s.List(ctx); err != nil || len(all) != 0 {
		t.Fatalf("stored %+v: %v", all, err)
	}
}

// The first run is computed in the schedule's time zone, shifted by the
// target's jitter for kinds that have one; a misfire window of zero or less
// means 12 hours.
func TestUpsertComputesTheFirstRun(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newScheduler(t)
	s.RegisterKind("backup", Kind{JobType: "backup.create", Jitter: true, Title: "backup"})
	s.RegisterKind("verify", Kind{JobType: "backup.verify", Title: "verification"})
	mx, _ := time.LoadLocation("America/Mexico_City")
	s.Now = func() time.Time { return time.Date(2026, 10, 20, 10, 0, 0, 0, mx) }
	if err := s.Upsert(ctx, "backup", "LIB1", "0 2 * * *", "America/Mexico_City", true, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, "verify", "LIB1", "0 2 * * *", "America/Mexico_City", false, -time.Minute); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get(ctx, "backup", "LIB1")
	want := time.Date(2026, 10, 21, 2, 0, 0, 0, mx).Add(JitterFor("LIB1"))
	if JitterFor("LIB1") == 0 || !b.NextRunAt.Equal(want) || b.MisfireWindow != 12*time.Hour || !b.Enabled || b.Timezone != "America/Mexico_City" || b.LastRunAt != nil {
		t.Fatalf("backup schedule %+v, next %s, want %s", b, b.NextRunAt, want)
	}
	v, _ := s.Get(ctx, "verify", "LIB1")
	if !v.NextRunAt.Equal(time.Date(2026, 10, 21, 2, 0, 0, 0, mx)) || v.MisfireWindow != 12*time.Hour || v.Enabled {
		t.Fatalf("verify schedule %+v, next %s", v, v.NextRunAt)
	}
}

// Upsert on an existing (kind, target) changes that schedule in place: same
// ID, new expression, zone, state, window and next run.
func TestUpsertUpdatesInPlace(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newScheduler(t)
	s.RegisterKind("verify", Kind{JobType: "backup.verify", Title: "verification"})
	s.Now = func() time.Time { return time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC) }
	if err := s.Upsert(ctx, "verify", "LIB1", "0 2 * * *", "UTC", true, time.Hour); err != nil {
		t.Fatal(err)
	}
	first, _ := s.Get(ctx, "verify", "LIB1")
	if err := s.Upsert(ctx, "verify", "LIB1", "30 4 * * 0", "Europe/Madrid", false, 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	all, _ := s.List(ctx)
	if len(all) != 1 {
		t.Fatalf("%d schedules, want 1", len(all))
	}
	got := all[0]
	madrid, _ := time.LoadLocation("Europe/Madrid")
	if got.ID != first.ID || got.Cron != "30 4 * * 0" || got.Timezone != "Europe/Madrid" || got.Enabled || got.MisfireWindow != 3*time.Hour ||
		!got.NextRunAt.Equal(time.Date(2026, 10, 25, 4, 30, 0, 0, madrid)) {
		t.Fatalf("updated %+v, next %s", got, got.NextRunAt)
	}
}

// Previews list the next runs in order; an invalid expression gives
// SQS-J020 and a date that comes rarely is still found.
func TestUpcomingPreview(t *testing.T) {
	ref := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	got, err := Upcoming("0 0 29 2 *", "UTC", ref, 2)
	if err != nil || len(got) != 2 || !got[0].Equal(time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)) || !got[1].Equal(time.Date(2032, 2, 29, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("leap days %v: %v", got, err)
	}
	if got, err := Upcoming("nope", "UTC", ref, 3); !errors.Is(err, ErrInvalidCron) || got != nil {
		t.Fatalf("invalid: %v %v", got, err)
	}
	if got, err := Upcoming("0 0 31 2 *", "UTC", ref, 3); err == nil || len(got) != 0 {
		t.Fatalf("a date that never comes: %v %v", got, err)
	}
}
