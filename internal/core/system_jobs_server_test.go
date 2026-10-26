// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// jobLogs joins the log lines of a job.
func (e *env) jobLogs(j *jobs.Job) string {
	e.t.Helper()
	lines, err := e.svc.Jobs.Logs(e.ctx, j.ID, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, l := range lines {
		out = append(out, l.Level+": "+l.Message)
	}
	return strings.Join(out, "\n")
}

// unmarkBackups removes the marker of the backups volume, as an unmounted
// disk leaves it; the returned function puts it back.
func (e *env) unmarkBackups() (restore func()) {
	e.t.Helper()
	marker := filepath.Join(e.svc.Cfg.BackupDir, fsx.MarkerName)
	saved, err := os.ReadFile(marker)
	if err != nil {
		e.t.Fatal(err)
	}
	os.Remove(marker)
	return func() { os.WriteFile(marker, saved, 0o600) }
}

// The Resolve upgrade window takes a backup of every live library before the
// upgrade and restores it as a test; the result names each backup. If one
// library cannot be backed up and verified the window stops with SQS-B060
// and workstations must not be upgraded.
func TestUpgradeWindowBacksUpAndVerifiesEveryLibrary(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("upg_lib")
	e.firstBackup(l)
	e.idle()
	j, err := e.svc.UpgradeWindow(ctx, admin, "20.1")
	if err != nil {
		t.Fatal(err)
	}
	j = e.wait(j, jobs.Succeeded)
	var res struct {
		Target    string `json:"target"`
		Libraries []struct {
			Slug     string `json:"slug"`
			BackupID string `json:"backup_id"`
		} `json:"libraries"`
	}
	if err := json.Unmarshal(j.Result, &res); err != nil || res.Target != "20.1" || len(res.Libraries) != 1 || res.Libraries[0].Slug != "upg_lib" {
		t.Fatalf("result %s %v", j.Result, err)
	}
	b := e.backupNow(res.Libraries[0].BackupID)
	if b.Kind != core.KindPreUpgrade || b.VerifyStatus != core.VerifyL2OK {
		t.Fatalf("pre-upgrade backup %+v", b)
	}
	if !strings.Contains(e.jobLogs(j), "All libraries have a verified backup taken before Resolve 20.1. Workstations can now be upgraded") {
		t.Fatalf("logs: %s", e.jobLogs(j))
	}
	restore := e.unmarkBackups()
	j, err = e.svc.UpgradeWindow(ctx, admin, "20.2")
	if err != nil {
		t.Fatal(err)
	}
	got := e.failsWith(j, "SQS-B060", "SQS-B060 the window stops: upg_lib was not backed up and verified: ")
	restore()
	if !strings.Contains(got.ErrorDetail, "SQS-S001 the volume has no sqsolve marker") {
		t.Fatalf("cause lost: %s", got.ErrorDetail)
	}
}

// The weekly optimization runs vacuumdb on every live library; one that
// fails is reported and the others are still optimized.
func TestVacuumOptimizesLiveLibraries(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("vac_lib")
	gone, _ := e.createActive("vac_gone")
	e.idle()
	// The catalog still says active, the database is gone.
	if err := e.svc.PG.DropDatabase(ctx, gone.DBName); err != nil {
		t.Fatal(err)
	}
	j, err := e.svc.Jobs.Enqueue(ctx, core.JobVacuum, "", nil, admin)
	if err != nil {
		t.Fatal(err)
	}
	j = e.wait(j, jobs.Succeeded)
	logs := e.jobLogs(j)
	if !strings.Contains(logs, "info: "+l.Slug+" optimized") || !strings.Contains(logs, "warn: "+gone.Slug+": ") {
		t.Fatalf("logs: %s", logs)
	}
	if j.Progress != 100 {
		t.Fatalf("progress %d", j.Progress)
	}
}
