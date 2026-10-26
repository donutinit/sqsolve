// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// A verification queued without a backup (a schedule names only its library,
// a system job only its target) tests the newest verified backup; a library
// without backups is not an error. A backup that is no longer available is
// refused with SQS-B021 and keeps its status.
func TestVerifyPicksTheNewestVerifiedBackup(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("vfy_pick")
	b := e.firstBackup(l)
	e.idle()
	j, err := e.svc.Jobs.Enqueue(ctx, core.JobBackupVerify, l.ID, core.VerifyParams{}, admin)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	if got := e.backupNow(b.ID); got.VerifyStatus != core.VerifyL2OK {
		t.Fatalf("the newest verified backup was not tested: %s %s", got.VerifyStatus, got.VerifyDetail)
	}
	j, err = e.svc.Jobs.Enqueue(ctx, core.JobBackupVerify, "", core.VerifyParams{Target: l.ID}, admin)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)

	empty, cj, err := e.svc.CreateLibrary(ctx, admin, core.CreateInput{DisplayName: "Empty", Slug: "vfy_empty"})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(cj, jobs.Succeeded)
	j, err = e.svc.Jobs.Enqueue(ctx, core.JobBackupVerify, empty.ID, core.VerifyParams{}, admin)
	if err != nil {
		t.Fatal(err)
	}
	j = e.wait(j, jobs.Succeeded)
	if !strings.Contains(e.jobLogs(j), "info: The library has no backups to verify") {
		t.Fatalf("logs: %s", e.jobLogs(j))
	}

	e.svc.DB.ExecContext(ctx, `UPDATE backups SET status = 'failed' WHERE id = ?`, b.ID)
	j, err = e.svc.Jobs.Enqueue(ctx, core.JobBackupVerify, l.ID, core.VerifyParams{BackupID: b.ID}, admin)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(j, "SQS-B021", "SQS-B021 the backup is not available (failed)")
	if got := e.backupNow(b.ID); got.VerifyStatus != core.VerifyL2OK {
		t.Fatalf("a refused verification changed the verdict: %s", got.VerifyStatus)
	}
}
