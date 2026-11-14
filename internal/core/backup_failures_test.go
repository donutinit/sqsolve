// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// subtest runs f as a subtest of t whose failures the helpers of e report,
// so that they never stop the parent test from the goroutine of the subtest.
func (e *env) subtest(t *testing.T, name string, f func(t *testing.T)) {
	t.Run(name, func(st *testing.T) {
		e.t = st
		defer func() { e.t = t }()
		f(st)
	})
}

// TestBackupFailuresCarryTheirCode: a backup that fails in prepare or in
// dump ends with the code of what failed, is recorded as failed, leaves no
// file, .partial or manifest behind, is reported once and does not change
// the newest verified backup of the library.
func TestBackupFailuresCarryTheirCode(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("fail_lib")
	e.firstBackup(l)
	e.quiesce()

	latest := func(t *testing.T) *core.Backup {
		t.Helper()
		lv, err := e.svc.LatestVerified(ctx, l.ID)
		if err != nil || lv == nil {
			t.Fatalf("latest verified backup: %+v %v", lv, err)
		}
		return lv
	}
	// fails runs a manual backup that must fail, checks what it leaves and
	// returns its finished job.
	fails := func(t *testing.T) *jobs.Job {
		t.Helper()
		reported, verified := len(e.eventsOf(events.BackupFailed, l.ID)), latest(t).ID
		j, err := e.svc.BackupNow(ctx, admin, l.ID)
		if err != nil {
			t.Fatal(err)
		}
		g := e.wait(j, jobs.Failed)
		rows, err := e.svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Statuses: []string{core.BackupFailed}})
		if err != nil {
			t.Fatal(err)
		}
		var row *core.Backup
		for _, b := range rows {
			if b.JobID == j.ID {
				if row != nil {
					t.Fatalf("two failed backups for job %s", j.ID)
				}
				row = b
			}
		}
		if row == nil || row.Error != g.ErrorDetail {
			t.Fatalf("failed backup of job %s: %+v, job error %q", j.ID, row, g.ErrorDetail)
		}
		abs := mustAbs(e.svc, row)
		for _, p := range []string{abs, abs + ".partial", strings.TrimSuffix(abs, ".backup") + ".manifest.json"} {
			if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s is left behind: %v", p, err)
			}
		}
		if n := len(e.eventsOf(events.BackupFailed, l.ID)); n != reported+1 {
			t.Fatalf("%d backup.failed events, want %d", n, reported+1)
		}
		if got := latest(t).ID; got != verified {
			t.Fatalf("newest verified backup changed from %s to %s", verified, got)
		}
		return g
	}

	e.subtest(t, "row count", func(t *testing.T) {
		restore := core.SetFault("backup.count", errors.New("count refused"))
		defer restore()
		g := fails(t)
		if g.ErrorCode != "SQS-B011" || g.ErrorDetail != "SQS-B011 row count: count refused" {
			t.Fatalf("job error %s: %q", g.ErrorCode, g.ErrorDetail)
		}
		restore()
		e.quiesce()
	})

	e.subtest(t, "pg_dump", func(t *testing.T) {
		dir := t.TempDir()
		script := "#!/bin/sh\necho 'pg_dump: error: simulated failure' >&2\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, "pg_dump"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		saved := e.svc.Exec
		e.svc.Exec = execx.NewRunner(dir)
		defer func() { e.svc.Exec = saved }()
		g := fails(t)
		if g.ErrorCode != "SQS-B010" || g.ErrorDetail != "SQS-B010 pg_dump failed: pg_dump exited with code 1: pg_dump: error: simulated failure" {
			t.Fatalf("job error %s: %q", g.ErrorCode, g.ErrorDetail)
		}
		e.quiesce()
		e.svc.Exec = saved
		// The failure left nothing in the way of the next backup.
		j, err := e.svc.BackupNow(ctx, admin, l.ID)
		if err != nil {
			t.Fatal(err)
		}
		e.wait(j, jobs.Succeeded)
		e.quiesce()
	})

	e.subtest(t, "no space", func(t *testing.T) {
		lv := latest(t)
		// The estimate is the size of the newest verified backup times 3/2,
		// in int64: 1<<60 gives 1.5 EB, a larger power of two overflows.
		if _, err := e.svc.DB.ExecContext(ctx, `UPDATE backups SET size_bytes = ? WHERE id = ?`, int64(1)<<60, lv.ID); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := e.svc.DB.ExecContext(ctx, `UPDATE backups SET size_bytes = ? WHERE id = ?`, lv.SizeBytes, lv.ID); err != nil {
				t.Error(err)
			}
		}()
		g := fails(t)
		if g.ErrorCode != "SQS-B012" ||
			!strings.HasPrefix(g.ErrorDetail, "SQS-B012 not enough space on the backups volume: 1.5 EB estimated, ") ||
			!strings.HasSuffix(g.ErrorDetail, " free") {
			t.Fatalf("job error %s: %q", g.ErrorCode, g.ErrorDetail)
		}
		e.quiesce()
	})

	// Last: it drops the database of the library.
	e.subtest(t, "database gone", func(t *testing.T) {
		if err := e.svc.PG.DropDatabase(ctx, l.DBName); err != nil {
			t.Fatal(err)
		}
		g := fails(t)
		if g.ErrorCode != "SQS-B002" || g.ErrorDetail != "SQS-B002 database fail_lib does not exist" {
			t.Fatalf("job error %s: %q", g.ErrorCode, g.ErrorDetail)
		}
		e.quiesce()
		// The backup of a closed database (a previous state) checks it too.
		_, err := e.svc.BackupClosedDB(ctx, e.lib(l.ID), "fail_lib__prev_209901010000")
		if err == nil || err.Error() != "SQS-B002 database fail_lib__prev_209901010000 does not exist" {
			t.Fatalf("backup of a closed database that does not exist: %v", err)
		}
	})
}
