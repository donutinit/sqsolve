// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/scheduler"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// Reconciliation says what it did with the files of a backup that a stop
// cut: deleted, or not touched when the catalog path is not valid
// (SQS-B043). For the second one it used to add that the leftovers were
// deleted too.
func TestReconcileNotesOfAnInterruptedBackup(t *testing.T) {
	e := newBkEnv(t)
	s := e.svc
	s.PG = pg.New(filepath.Join(t.TempDir(), "nosocket"), 1, secrets.String{}, secrets.String{})
	s.Jobs = jobs.NewManager(s.DB, s.Events, events.NewBus(), s.Log, 1, 1)
	s.Sched = scheduler.New(s.DB, s.Jobs, s.Events, s.Log)
	s.Register()
	l := e.library("client_x", StateTrashed)
	cut := e.backup(l, bkOpt{status: BackupRunning})
	blocked := e.pathRow(l, "../../elsewhere/client_x.backup")
	if _, err := s.DB.ExecContext(e.ctx, `UPDATE backups SET status = 'running' WHERE id = ?`, blocked.ID); err != nil {
		t.Fatal(err)
	}

	notes := s.Reconcile(e.ctx, nil)
	about := func(id string) (out []string) {
		for _, n := range notes {
			if strings.Contains(n, id) {
				out = append(out, n)
			}
		}
		return out
	}
	if got := about(cut.ID); len(got) != 1 || got[0] != "backup "+cut.ID+" interrupted; its leftovers were deleted" {
		t.Fatalf("notes of the backup that was cut: %q", got)
	}
	if got := about(blocked.ID); len(got) != 1 || !strings.HasPrefix(got[0], "backup "+blocked.ID+" interrupted; its files were not touched: SQS-B043 ") {
		t.Fatalf("notes of the backup with a path that is not valid: %q", got)
	}
	if _, err := os.Stat(mustAbs(s, cut)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the leftovers of the backup that was cut are still there: %v", err)
	}
	for _, b := range []*Backup{cut, blocked} {
		if got, err := s.Backup(e.ctx, b.ID); err != nil || got.Status != BackupInterrupted {
			t.Fatalf("backup %s: %+v %v, want it interrupted", b.ID, got, err)
		}
	}
}
