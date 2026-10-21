// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/scheduler"
	"github.com/donutinit/sqsolve/internal/store"
)

// Only idempotent, non-destructive jobs are retried with their original
// parameters (docs 11 §11); every type names the permission needed to
// cancel or retry it.
func TestJobSpecsRetryPolicyAndPermissions(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "c.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	ev := events.NewRecorder(db, bus, log)
	jm := jobs.NewManager(db, ev, bus, log, 1, 1)
	s := &Service{DB: db, Jobs: jm, Sched: scheduler.New(db, jm, ev, log), Events: ev, Log: log}
	s.Register()

	retryable := map[string]bool{
		JobBackupCreate: true, JobBackupVerify: true, JobBackupExport: true, JobBackupImport: true,
		JobVerifyAll: true, JobVacuum: true, JobRetention: true, JobControlBackup: true,
		JobCatalogCheck: true, JobCatalogRebuild: true, JobPurgeExpired: true, JobHousekeeping: true,
		JobLibraryCreate: false, JobLibraryDelete: false, JobLibraryUntrash: false, JobLibraryPurge: false,
		JobRestoreNew: false, JobRestoreReplace: false, JobRestoreRevert: false, JobRestoreRecreate: false,
		JobUpgradeWindow: false,
	}
	action := map[string]auth.Action{
		JobLibraryCreate: auth.ActLibraryCreate, JobLibraryDelete: auth.ActLibraryDelete,
		JobLibraryUntrash: auth.ActLibraryDelete, JobLibraryPurge: auth.ActLibraryDelete,
		JobBackupCreate: auth.ActBackupCreate, JobBackupVerify: auth.ActBackupVerify,
		JobBackupExport: auth.ActLibraryExport, JobBackupImport: auth.ActBackupImport,
		JobRestoreNew: auth.ActRestoreNew, JobRestoreReplace: auth.ActRestoreReplace,
		JobRestoreRevert: auth.ActRestoreRevert, JobRestoreRecreate: auth.ActRestoreReplace,
	}
	types := jm.Types()
	if len(types) != len(retryable) {
		t.Fatalf("registered %d types, the policy lists %d", len(types), len(retryable))
	}
	known := map[auth.Action]bool{}
	for _, a := range auth.AllActions {
		known[a] = true
	}
	for _, typ := range types {
		spec, _ := jm.Spec(typ)
		want, listed := retryable[typ]
		if !listed || spec.Retryable != want {
			t.Errorf("%s: retryable %v, want %v (listed %v)", typ, spec.Retryable, want, listed)
		}
		if !known[auth.Action(spec.Action)] {
			t.Errorf("%s: unknown action %q", typ, spec.Action)
		}
		wantAction, ok := action[typ]
		if !ok {
			wantAction = auth.ActSettingsEdit // system jobs: administrators
		}
		if auth.Action(spec.Action) != wantAction {
			t.Errorf("%s: action %q, want %q", typ, spec.Action, wantAction)
		}
		// Destructive types are never something an operator can replay.
		if !spec.Retryable && auth.RoleAllows(auth.RoleOperator, auth.Action(spec.Action)) && typ != JobRestoreNew {
			t.Errorf("%s: an operator can act on a destructive type", typ)
		}
	}
	// Schedule titles are English words.
	word := regexp.MustCompile(`^[a-z ]+$`)
	for kind, d := range systemSchedules {
		if !word.MatchString(d.title) {
			t.Errorf("%s: title %q", kind, d.title)
		}
	}
	if systemSchedules["system.purge"].title != "purge of expired items" || systemSchedules["system.housekeeping"].title != "housekeeping" {
		t.Errorf("titles %q %q", systemSchedules["system.purge"].title, systemSchedules["system.housekeeping"].title)
	}
}
