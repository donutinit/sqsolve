// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

// catalogOnly is a Service with the control plane but no PostgreSQL: enough
// for what the swap and the previous-state rules record in the catalog.
func catalogOnly(t *testing.T) (*Service, *Library) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Service{Cfg: &config.Config{AppDir: dir, PGDir: dir, Location: time.UTC}, DB: db, Log: log,
		PG: pg.New(filepath.Join(dir, "nosocket"), 1, secrets.String{}, secrets.String{}), Events: events.NewRecorder(db, events.NewBus(), log)}
	now := store.NowT()
	if _, err := db.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, prev_db_name, prev_backup_id, created_at, updated_at)
		VALUES ('L1', 'lib', 'Lib', 'restoring', 'lib', 'lib', 'resolve', 'lib__prev_202610190000', 'B0', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	l, err := s.Library(ctx, "L1")
	if err != nil {
		t.Fatal(err)
	}
	return s, l
}

// The prev_db_name clear only applies if the row still names the database it
// read: a revert that swapped in between keeps its new previous state.
func TestClearPrevIsConditional(t *testing.T) {
	s, l := catalogOnly(t)
	ctx := context.Background()
	s.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = 'lib__prev_202610200000' WHERE id = ?`, l.ID)
	if s.clearPrev(ctx, l.ID, "lib__prev_202610190000") {
		t.Fatal("cleared a previous state that changed meanwhile")
	}
	if got, _ := s.Library(ctx, l.ID); got.PrevDB != "lib__prev_202610200000" {
		t.Fatalf("prev %q", got.PrevDB)
	}
	if !s.clearPrev(ctx, l.ID, "lib__prev_202610200000") {
		t.Fatal("did not clear the current previous state")
	}
	if got, _ := s.Library(ctx, l.ID); got.PrevDB != "" || got.PrevBackupID != "" || got.PrevExpiresAt != nil {
		t.Fatalf("not cleared: %+v", got)
	}
}

// recordPrev writes the new previous state before the rename; unrecordPrev
// puts the old one back only while the row still names the new one.
func TestRecordAndUnrecordPrev(t *testing.T) {
	s, l := catalogOnly(t)
	ctx := context.Background()
	j := newSwapJournal(l, "lib__prev_202610201200", "lib__restore_x")
	if err := s.recordPrev(ctx, l.ID, j.Prev, "B1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Library(ctx, l.ID); got.PrevDB != j.Prev || got.PrevBackupID != "B1" || got.PrevExpiresAt == nil {
		t.Fatalf("not recorded: %+v", got)
	}
	s.unrecordPrev(ctx, l.ID, j)
	if got, _ := s.Library(ctx, l.ID); got.PrevDB != "lib__prev_202610190000" || got.PrevBackupID != "B0" || got.PrevExpiresAt != nil {
		t.Fatalf("not put back: %+v", got)
	}
	// Someone else changed it: left alone.
	s.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = 'other' WHERE id = ?`, l.ID)
	s.unrecordPrev(ctx, l.ID, j)
	if got, _ := s.Library(ctx, l.ID); got.PrevDB != "other" {
		t.Fatalf("overwrote a newer value: %q", got.PrevDB)
	}
}

// A swap whose put-back failed reports the library missing, keeps the
// original tracked and says where it is (SQS-R053).
func TestSwapStuckReportsWhereTheOriginalIs(t *testing.T) {
	s, l := catalogOnly(t)
	ctx := context.Background()
	r := &jobs.Run{Job: &jobs.Job{ID: "J1"}}
	err := s.swapStuck(ctx, r, l, "lib__prev_202610190000", errors.New("rename failed"))
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-R053 the swap stopped halfway") || !strings.Contains(err.Error(), "it is kept as lib__prev_202610190000 and Revert puts it back") {
		t.Fatalf("error %v", err)
	}
	got, _ := s.Library(ctx, l.ID)
	if got.State != StateMissing || got.PrevDB != "lib__prev_202610190000" {
		t.Fatalf("after a stuck swap: %+v", got)
	}
	ev, _ := s.Events.Latest(ctx, events.RestoreFailed, l.ID)
	if ev == nil || ev.Severity != events.Critical || !strings.Contains(ev.Message, "SQS-R053") {
		t.Fatalf("event %+v", ev)
	}
}

// A completed swap whose reopening failed is reported as such (SQS-R054).
func TestReopenFailedIsReported(t *testing.T) {
	s, l := catalogOnly(t)
	ctx := context.Background()
	err := s.reopenFailed(ctx, &jobs.Run{Job: &jobs.Job{ID: "J1"}}, l, errors.New("grant failed"))
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-R054 the databases were swapped but reopening the library failed: grant failed") {
		t.Fatalf("error %v", err)
	}
	if ev, _ := s.Events.Latest(ctx, events.RestoreFailed, l.ID); ev == nil || ev.Severity != events.Critical {
		t.Fatalf("event %+v", ev)
	}
}

// Only a previous state of the library itself can be dropped (SQS-R056).
func TestDropPrevRefusesOtherDatabases(t *testing.T) {
	s, l := catalogOnly(t)
	for _, name := range []string{"lib", "other__prev_202610190000", "lib__trash_20261019000000"} {
		err := s.dropPrevDB(context.Background(), nil, l, name, "")
		if err == nil || err.Error() != "SQS-R056 "+name+" is not a previous state of lib; it was not dropped" {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
