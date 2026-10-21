// SPDX-License-Identifier: AGPL-3.0-only

package core

// A check that cannot reach PostgreSQL is not an answer: reconciliation and
// the restore runners report it and change nothing, instead of reading it as
// "the database does not exist" (final review of the audit fixes). The
// catalog-only Service has no PostgreSQL, so every check fails.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
)

// A delete cut after its journal, whose move cannot be checked, keeps the
// lock and the database name for the next start instead of undoing the lock
// over a database that may already be in the trash.
func TestReconcileTrashWaitsWhenTheCheckFails(t *testing.T) {
	s, l := catalogOnly(t)
	ctx := context.Background()
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'active' WHERE id = ?`, l.ID)
	l, _ = s.Library(ctx, l.ID)
	var notes []string
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	if s.reconcileTrash(ctx, l, map[string]string{"from": "lib", "to": "lib__trash_20261020000000"}, note) {
		t.Fatal("a delete whose move could not be checked was settled")
	}
	all := strings.Join(notes, "\n")
	if strings.Contains(all, "the lock was undone") {
		t.Fatalf("the lock was undone without knowing where the database is:\n%s", all)
	}
	if !strings.Contains(all, "lib: could not check whether the deletion moved the database to lib__trash_20261020000000 (SQS-P001 PostgreSQL is not available); nothing was changed") {
		t.Fatalf("notes do not report the failed check:\n%s", all)
	}
	if got, _ := s.Library(ctx, l.ID); got.State != StateActive || got.TrashDB != "" {
		t.Fatalf("library changed: %+v", got)
	}
}

// Revert reports a failed check of the previous state as such, not as
// "the previous-state database no longer exists" (SQS-R031).
func TestRevertReportsAFailedCheck(t *testing.T) {
	s, l := catalogOnly(t)
	ctx := context.Background()
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'active' WHERE id = ?`, l.ID)
	err := s.runRevert(ctx, &jobs.Run{Job: &jobs.Job{ID: "J1", LibraryID: l.ID}})
	if err == nil || strings.Contains(err.Error(), "SQS-R031") ||
		err.Error() != "could not check the previous-state database lib__prev_202610190000: SQS-P001 PostgreSQL is not available" {
		t.Fatalf("error %v", err)
	}
}

// A database a recreate created and could not drop after a failure is named
// in the job error, after the cause, which keeps its code.
func TestDropCreatedReportsAFailedDrop(t *testing.T) {
	s, _ := catalogOnly(t)
	cause := errors.New("SQS-R012 row counts do not match: SM_Project: 50 instead of 999")
	err := s.dropCreated(context.Background(), "lib", cause)
	want := "SQS-R012 row counts do not match: SM_Project: 50 instead of 999. The database lib this run created could not be dropped (SQS-P001 PostgreSQL is not available): check it on the server and drop it before recreating again"
	if err == nil || err.Error() != want || !errors.Is(err, cause) {
		t.Fatalf("got %v, want %q", err, want)
	}
}

// A swap whose databases cannot be checked is not settled: it is tried again
// at the next start and nothing changes.
func TestReconcileSwapWaitsWhenTheCheckFails(t *testing.T) {
	s, l := catalogOnly(t)
	ctx := context.Background()
	var notes []string
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	j := swapJournal{Live: "lib", Prev: "lib__prev_202610200000", Restored: "lib__restore_x", Phase: "rename_restored", State: "active"}
	if s.reconcileSwap(ctx, l, j, note) {
		t.Fatal("a swap that could not be checked was settled")
	}
	if all := strings.Join(notes, "\n"); all != "lib: could not check the databases of the swap (SQS-P001 PostgreSQL is not available); nothing was changed" {
		t.Fatalf("notes %q", all)
	}
	if got, _ := s.Library(ctx, l.ID); got.State != StateRestoring || got.PrevDB != "lib__prev_202610190000" {
		t.Fatalf("library changed: %+v", got)
	}
}

// A previous state is never forgotten when the server cannot say whether its
// database exists.
func TestForgetGonePrevKeepsItOnALookupError(t *testing.T) {
	s, l := catalogOnly(t)
	ctx := context.Background()
	var notes []string
	s.forgetGonePrev(ctx, l, func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) })
	if got, _ := s.Library(ctx, l.ID); got.PrevDB != "lib__prev_202610190000" || got.PrevBackupID != "B0" {
		t.Fatalf("previous state forgotten on a lookup error: %+v", got)
	}
	if len(notes) != 0 {
		t.Fatalf("notes %q", notes)
	}
}

// A delete cut during the lock of a library in its initialization window,
// whose database cannot be checked to open it again, is not settled.
func TestReconcileTrashWaitsWhenTheWindowCheckFails(t *testing.T) {
	s, l := catalogOnly(t)
	ctx := context.Background()
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'awaiting_init' WHERE id = ?`, l.ID)
	l, _ = s.Library(ctx, l.ID)
	var notes []string
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	if s.reconcileTrash(ctx, l, nil, note) {
		t.Fatal("settled although the window database could not be checked")
	}
	all := strings.Join(notes, "\n")
	if all != "lib: could not check its database to accept connections again: SQS-P001 PostgreSQL is not available" {
		t.Fatalf("notes %q", all)
	}
}

// A previous state is kept when the jobs pending reconciliation, or the
// journal of one of them, cannot be read (SQS-R091), and when a pending
// journal names it (SQS-R090).
func TestDropPrevKeepsItWhenPendingJobsCannotBeRead(t *testing.T) {
	s, l := catalogOnly(t)
	ctx := context.Background()
	s.Jobs = jobs.NewManager(s.DB, s.Events, events.NewBus(), s.Log, 1, 1)
	name := "lib__prev_202610190000"
	now := store.NowT()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO jobs(id, type, library_id, state, priority, scheduled_for, created_at, started_at, created_by_type, reconcile_pending)
		VALUES ('JX', 'restore.revert', 'L1', 'interrupted', 'unreadable', ?, ?, ?, 'user', 1)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	err := s.dropPrevDB(ctx, nil, l, name, "")
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-R091 could not tell whether an interrupted job pending reconciliation names "+name+", so it was kept: ") {
		t.Fatalf("unreadable jobs: %v", err)
	}
	s.DB.ExecContext(ctx, `UPDATE jobs SET priority = 100 WHERE id = 'JX'`)
	s.DB.ExecContext(ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at) VALUES ('JX', 'unreadable', 'x', 'failed', ?)`, now)
	err = s.dropPrevDB(ctx, nil, l, name, "")
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-R091 could not tell whether an interrupted job pending reconciliation names "+name+", so it was kept: job JX: ") {
		t.Fatalf("unreadable journal: %v", err)
	}
	s.DB.ExecContext(ctx, `DELETE FROM job_steps WHERE job_id = 'JX'`)
	s.DB.ExecContext(ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at, data) VALUES ('JX', 1, 'swap', 'interrupted', ?, ?)`, now,
		`{"live":"lib","prev":"lib__prev_202610210000","restored":"`+name+`","phase":"rename_live","state_before":"active"}`)
	err = s.dropPrevDB(ctx, nil, l, name, "")
	if err == nil || err.Error() != "SQS-R090 "+name+" is named by an interrupted job that reconciliation at startup has not settled yet; it was kept" {
		t.Fatalf("named by a pending journal: %v", err)
	}
	// Settled: the guard lets it through to the next check.
	if err := s.Jobs.Reconciled(ctx, "JX"); err != nil {
		t.Fatal(err)
	}
	if err := s.dropPrevDB(ctx, nil, l, name, ""); err == nil || err.Error() != "SQS-P001 PostgreSQL is not available" {
		t.Fatalf("after settling: %v", err)
	}
}
