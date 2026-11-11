// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"testing"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// A library restored as new gets the backup policy of the library its backup
// came from, mode included. The mode used to be left out: in activity mode
// the stored expression is the check cadence, so the new library got a cron
// schedule that backed up at every check, with changes or without.
func TestRestoreAsNewKeepsThePolicyMode(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("activity_src")
	b := e.firstBackup(l)
	e.quiesce()
	pol, err := e.svc.Policy(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	pol.Mode, pol.CheckMinutes = core.ModeActivity, 30
	if err := e.svc.SetPolicy(ctx, admin, l.ID, pol); err != nil {
		t.Fatal(err)
	}
	nl, rj, err := e.svc.RestoreAsNew(ctx, admin, b.ID, "activity_copy", "Copy")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(rj, jobs.Succeeded)
	got, err := e.svc.Policy(ctx, nl.ID)
	if err != nil || got.Mode != core.ModeActivity || got.CheckMinutes != 30 || got.Cron != core.ActivityCron(30) {
		t.Fatalf("policy of the new library %+v %v, want activity mode every 30 minutes", got, err)
	}
	sc, err := e.svc.Sched.Get(ctx, core.ScheduleBackup, nl.ID)
	if err != nil || sc == nil || !sc.Enabled || sc.Cron != core.ActivityCron(30) {
		t.Fatalf("schedule of the new library %+v %v", sc, err)
	}
}
