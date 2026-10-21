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

	"github.com/donutinit/sqsolve/internal/jobs"
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
	s.reconcileTrash(ctx, l, map[string]string{"from": "lib", "to": "lib__trash_20261020000000"}, note)
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
