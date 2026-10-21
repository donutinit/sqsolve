// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"testing"

	"github.com/donutinit/sqsolve/internal/jobs"
)

// SQS-B033 and SQS-B034 mean one thing each: a pinned backup cannot be deleted
// and neither can the newest verified backup of a live library.
func TestDeleteBackupErrorCodes(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("borrado")
	b := e.firstBackup(l)

	if err := e.svc.SetPinned(ctx, admin, b.ID, true); err != nil {
		t.Fatal(err)
	}
	err := e.svc.DeleteBackup(ctx, admin, b.ID)
	if err == nil || err.Error() != "SQS-B033 unpin the backup before deleting it" {
		t.Fatalf("pinned backup: %v", err)
	}

	if err := e.svc.SetPinned(ctx, admin, b.ID, false); err != nil {
		t.Fatal(err)
	}
	err = e.svc.DeleteBackup(ctx, admin, b.ID)
	if err == nil || err.Error() != "SQS-B034 it is the most recent verified backup of the library; it cannot be deleted manually" {
		t.Fatalf("newest verified backup: %v", err)
	}

	// Once a newer verified backup exists the older one can go.
	bj, err := e.svc.BackupNow(ctx, admin, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(bj, jobs.Succeeded)
	if err := e.svc.DeleteBackup(ctx, admin, b.ID); err != nil {
		t.Fatalf("older backup should be deletable: %v", err)
	}
}
