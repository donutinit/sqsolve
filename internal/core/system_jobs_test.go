// SPDX-License-Identifier: AGPL-3.0-only

package core

// The system jobs of docs 11 §5 and §11 that only touch the control plane and
// the backups volume, run by a real job queue without PostgreSQL: the control
// plane copy, the catalog check and rebuild, housekeeping, retention, the
// weekly verification and the system schedules.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/scheduler"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// jobsEnv is newBkEnv with a running job queue and the scheduler. Its
// PostgreSQL client has no server. The clock is base plus offset.
type jobsEnv struct {
	*bkEnv
	base   time.Time
	offset atomic.Int64
}

func newJobsEnv(t *testing.T, base time.Time) *jobsEnv {
	t.Helper()
	e := &jobsEnv{bkEnv: newBkEnv(t), base: base}
	ctx, cancel := context.WithCancel(context.Background())
	s := e.svc
	s.Now = func() time.Time { return e.base.Add(time.Duration(e.offset.Load())) }
	s.PG = pg.New(filepath.Join(t.TempDir(), "nosocket"), 1, secrets.String{}, secrets.String{})
	s.Jobs = jobs.NewManager(s.DB, s.Events, events.NewBus(), s.Log, 2, 1)
	s.Sched = scheduler.New(s.DB, s.Jobs, s.Events, s.Log)
	s.Box, _ = secrets.NewBox(secrets.RandomBytes(32))
	s.Register()
	s.Jobs.Start(ctx)
	t.Cleanup(func() {
		s.Jobs.Shutdown(10 * time.Second)
		cancel()
	})
	e.ctx = ctx
	return e
}

// run enqueues a job and waits for it to end in state want.
func (e *jobsEnv) run(typ, libraryID string, params any, want jobs.State) *jobs.Job {
	e.t.Helper()
	j, err := e.svc.Jobs.Enqueue(e.ctx, typ, libraryID, params, bkActor)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.waitJob(j.ID, want)
}

func (e *jobsEnv) waitJob(id string, want jobs.State) *jobs.Job {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		j, err := e.svc.Jobs.Get(e.ctx, id)
		if err == nil && j.State.Finished() {
			if j.State != want {
				e.t.Fatalf("job %s ended %s (%s %s), want %s", j.Type, j.State, j.ErrorCode, j.ErrorDetail, want)
			}
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("job %s did not end", id)
	return nil
}

// logs joins the log lines of a job.
func (e *jobsEnv) logs(j *jobs.Job) string {
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

// unmark removes the marker of the backups volume, as an unmounted disk
// would leave it; the returned function puts it back.
func (e *jobsEnv) unmark() (restore func()) {
	e.t.Helper()
	marker := filepath.Join(e.svc.Cfg.BackupDir, fsx.MarkerName)
	saved, err := os.ReadFile(marker)
	if err != nil {
		e.t.Fatal(err)
	}
	os.Remove(marker)
	return func() { os.WriteFile(marker, saved, 0o600) }
}

// failsWithoutMarker checks that a job refuses an unmarked backups volume
// with SQS-S001.
func (e *jobsEnv) failsWithoutMarker(typ string) {
	e.t.Helper()
	restore := e.unmark()
	defer restore()
	j := e.run(typ, "", nil, jobs.Failed)
	if j.ErrorCode != "SQS-S001" || j.ErrorDetail != "SQS-S001 the volume has no sqsolve marker" {
		e.t.Fatalf("%s on an unmarked volume: %s %q", typ, j.ErrorCode, j.ErrorDetail)
	}
}

// The control plane copy lands in _system as a database that opens, with the
// audit log of the month (and of the previous one on the first days), and
// only the newest 30 copies are kept. An unmarked backups volume gets
// nothing.
func TestControlPlaneBackupKeepsTheNewestCopies(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	ctx := e.ctx
	sys := filepath.Join(e.svc.Cfg.BackupDir, "_system")
	if err := os.MkdirAll(sys, 0o700); err != nil {
		t.Fatal(err)
	}
	e.library("cp_lib", StateActive)
	if err := e.svc.Audit.Record(ctx, bkActor, "test.entry", "system", "", audit.OK, nil); err != nil {
		t.Fatal(err)
	}
	// The audit log stamps its entries with the real clock, so the job runs on
	// the second day of the month that entry was written in, whatever month
	// the test runs in: the copy is of that day and the export covers that
	// month and the one before.
	var ts string
	if err := e.svc.DB.QueryRowContext(ctx, `SELECT ts FROM audit_log WHERE action = 'test.entry'`).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	written := store.ParseT(ts)
	day := time.Date(written.Year(), written.Month(), 2, 4, 15, 0, 0, time.UTC)
	e.offset.Store(int64(day.Sub(e.base)))
	stateFile := func(t time.Time) string {
		return filepath.Join(sys, "sqsolve-state-"+t.Format("20060102T150405Z")+".db")
	}
	for d := 1; d <= 30; d++ {
		os.WriteFile(stateFile(day.AddDate(0, 0, -d)), []byte("old copy"), 0o600)
	}
	os.WriteFile(stateFile(day.AddDate(0, 0, -31)), []byte("oldest copy"), 0o600)
	j := e.run(JobControlBackup, "", nil, jobs.Succeeded)
	name := filepath.Base(stateFile(day))
	if !strings.Contains(e.logs(j), "Control plane copy at "+name) {
		t.Fatalf("logs: %s", e.logs(j))
	}
	cp, err := sql.Open("sqlite", "file:"+stateFile(day)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var slug string
	if err := cp.QueryRow(`SELECT slug FROM libraries`).Scan(&slug); err != nil || slug != "cp_lib" {
		t.Fatalf("the copy does not hold the catalog: %q %v", slug, err)
	}
	copies, _ := filepath.Glob(filepath.Join(sys, "sqsolve-state-*.db"))
	if len(copies) != 30 || onDisk(stateFile(day.AddDate(0, 0, -31))) || onDisk(stateFile(day.AddDate(0, 0, -30))) {
		t.Fatalf("%d copies kept: %v", len(copies), copies)
	}
	for _, month := range []time.Time{day.AddDate(0, -1, 0), day} {
		b, err := os.ReadFile(filepath.Join(sys, "audit-"+month.Format("200601")+".jsonl"))
		if err != nil {
			t.Fatalf("no audit export for %s: %v", month.Format("2006-01"), err)
		}
		if month.Equal(day) && !strings.Contains(string(b), "test.entry") {
			t.Fatalf("the export of the month misses its entries:\n%s", b)
		}
	}
	e.offset.Add(int64(24 * time.Hour))
	e.failsWithoutMarker(JobControlBackup)
	if onDisk(stateFile(day.AddDate(0, 0, 1))) {
		t.Fatal("a copy was written to an unmarked volume")
	}
}

// An import names a file of the import folder. A name that is not there says
// so, and one that leads out of the folder through a symlink is refused;
// neither reads a file or adds a backup.
func TestImportRefusesMissingAndOutsideFiles(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	l := e.library("imp_lib", StateActive)
	j := e.run(JobBackupImport, l.ID, ImportParams{Path: "ghost.backup"}, jobs.Failed)
	if j.ErrorCode != "SQS-B055" || j.ErrorDetail != "SQS-B055 there is no file named ghost.backup in the import folder" {
		t.Fatalf("missing file: %s %q", j.ErrorCode, j.ErrorDetail)
	}
	if err := os.MkdirAll(e.svc.ImportDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.backup")
	if err := os.WriteFile(outside, []byte("not a file of the import folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.svc.ImportDir(), "link.backup")); err != nil {
		t.Fatal(err)
	}
	j = e.run(JobBackupImport, l.ID, ImportParams{Path: "link.backup", DeleteSource: true}, jobs.Failed)
	if j.ErrorCode != "SQS-B051" || j.ErrorDetail != "SQS-B051 the file is outside the import folder" {
		t.Fatalf("symlink out of the folder: %s %q", j.ErrorCode, j.ErrorDetail)
	}
	if !onDisk(outside) {
		t.Fatal("the file outside the import folder was removed")
	}
	if bs, err := e.svc.Backups(e.ctx, BackupFilter{LibraryID: l.ID}); err != nil || len(bs) != 0 {
		t.Fatalf("backups after two refused imports: %+v %v", bs, err)
	}
}

// The catalog check marks a backup whose file is gone as failed, so no
// restore picks it, and counts files the catalog does not know. On an
// unmarked volume (a disk not mounted) it marks nothing.
func TestCatalogCheckFlagsMissingFiles(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	ctx := e.ctx
	l := e.library("cat_lib", StateActive)
	kept := e.backup(l, bkOpt{started: time.Now().Add(-2 * time.Hour)})
	gone := e.backup(l, bkOpt{started: time.Now().Add(-time.Hour)})
	stray := filepath.Join(e.svc.Cfg.BackupDir, "libraries", "cat_lib", "stray.backup")
	os.WriteFile(stray, []byte("PGDMP"), 0o600)

	os.Rename(mustAbs(e.svc, gone), mustAbs(e.svc, gone)+".away")
	restore := e.unmark()
	j := e.run(JobCatalogCheck, "", nil, jobs.Failed)
	restore()
	if j.ErrorCode != "SQS-S001" {
		t.Fatalf("check on an unmarked volume: %s %s", j.ErrorCode, j.ErrorDetail)
	}
	if b, _ := e.svc.Backup(ctx, gone.ID); b.VerifyStatus != VerifyL1OK {
		t.Fatalf("an unmarked volume marked a backup: %+v", b)
	}

	j = e.run(JobCatalogCheck, "", nil, jobs.Succeeded)
	if !strings.Contains(e.logs(j), "2 backups checked; 1 missing; 1 orphaned") {
		t.Fatalf("logs: %s", e.logs(j))
	}
	if b, _ := e.svc.Backup(ctx, gone.ID); b.VerifyStatus != VerifyFailed || b.VerifyDetail != "The file does not exist on the volume" {
		t.Fatalf("missing file: %+v", b)
	}
	if b, _ := e.svc.Backup(ctx, kept.ID); b.VerifyStatus != VerifyL1OK {
		t.Fatalf("a backup with its file was marked: %+v", b)
	}
	if b, _ := e.svc.LatestVerified(ctx, l.ID); b == nil || b.ID != kept.ID {
		t.Fatalf("latest verified %+v, want the backup that has its file", b)
	}
	evs := e.events("backup.catalog_mismatch")
	if len(evs) != 1 || evs[0].Severity != events.Warning || evs[0].Message != "Catalog and files do not match: 1 files are missing and 1 files are not in the catalog" {
		t.Fatalf("mismatch events %+v", evs)
	}
}

// Pending 3.7 item 1: the catalog check counts the backups whose path could
// not be checked, reports them, and gives no verdict on them: such a backup
// is not marked missing, nobody looked at it.
func TestCatalogCheckCountsUncheckedPaths(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	l := e.library("cat_lib", StateActive)
	e.backup(l, bkOpt{started: time.Now().Add(-anotherYear)})
	stuck := e.backup(l, bkOpt{})
	defer e.uncheckable(stuck)()
	j := e.run(JobCatalogCheck, "", nil, jobs.Succeeded)
	if !strings.Contains(e.logs(j), "2 backups checked; 0 missing; 0 orphaned; 0 with a path that is not valid; 1 with a path that could not be checked") {
		t.Fatalf("logs: %s", e.logs(j))
	}
	if b, _ := e.svc.Backup(e.ctx, stuck.ID); b.VerifyStatus != VerifyL1OK {
		t.Fatalf("a backup nobody could look at got a verdict: %+v", b)
	}
	if evs := e.events(events.BackupPathUnchecked); len(evs) != 1 || evs[0].Severity != events.Error {
		t.Fatalf("path_unchecked events %+v", evs)
	}
	if evs := e.events("backup.catalog_mismatch"); len(evs) != 0 {
		t.Fatalf("mismatch events %+v", evs)
	}
}

// Pending 3.7 item 1: a purge needs a final backup whose path can be
// checked; one that cannot be read does not let it drop the database.
func TestPurgeNeedsAFinalBackupWhosePathCanBeChecked(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	tr := e.library("trashed_x", StateTrashed)
	if _, err := e.svc.DB.ExecContext(e.ctx, `UPDATE libraries SET trash_db_name = 'trashed_x__trash_202610250000' WHERE id = ?`, tr.ID); err != nil {
		t.Fatal(err)
	}
	final := e.backup(tr, bkOpt{kind: KindPreDelete})
	defer e.uncheckable(final)()
	j := e.run(JobLibraryPurge, tr.ID, nil, jobs.Failed)
	if j.ErrorCode != "SQS-L045" || j.ErrorDetail != "SQS-L045 there is no verified final backup; not purging" {
		t.Fatalf("purge with a final backup that cannot be checked: %s %q", j.ErrorCode, j.ErrorDetail)
	}
	if got, err := e.svc.Library(e.ctx, tr.ID); err != nil || got.State != StateTrashed {
		t.Fatalf("library after the refused purge: %+v %v", got, err)
	}
}

// With the backups disk not mounted (an empty folder at the mount point) the
// path of the final backup still checks, but sqsolve cannot see the file: the
// purge stops at check_backup with the volume error, before its journal and
// before any drop.
func TestPurgeStopsWhenTheBackupsVolumeIsNotMounted(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	tr := e.library("trashed_x", StateTrashed)
	if _, err := e.svc.DB.ExecContext(e.ctx, `UPDATE libraries SET trash_db_name = 'trashed_x__trash_202610250000' WHERE id = ?`, tr.ID); err != nil {
		t.Fatal(err)
	}
	e.backup(tr, bkOpt{kind: KindPreDelete})
	_, mount := e.unmount()
	defer mount()
	j := e.run(JobLibraryPurge, tr.ID, nil, jobs.Failed)
	if j.ErrorCode != "SQS-S001" || j.ErrorDetail != "SQS-S001 the volume has no sqsolve marker" {
		t.Fatalf("purge on an unmounted backups volume: %s %q", j.ErrorCode, j.ErrorDetail)
	}
	steps, err := e.svc.Jobs.Steps(e.ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Name != "check_backup" || steps[0].State != "failed" || string(steps[0].Data) != "{}" {
		t.Fatalf("steps of the refused purge: %+v", steps)
	}
	if got, err := e.svc.Library(e.ctx, tr.ID); err != nil || got.State != StateTrashed || got.TrashDB != "trashed_x__trash_202610250000" {
		t.Fatalf("library after the refused purge: %+v %v", got, err)
	}
}

// writeManifest stores a manifest at path.
func writeManifest(t *testing.T, path string, m Manifest) {
	t.Helper()
	b, _ := json.Marshal(m)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The catalog rebuild skips what it cannot use and says so: a manifest that
// cannot be read, a manifest without its file and a backup already in the
// catalog. A backup whose file does not match its manifest is added marked
// as failed (never offered for a restore) under a library rebuilt as purged.
func TestCatalogRebuildKeepsWhatItCannotUse(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	ctx := e.ctx
	l := e.library("known_lib", StateActive)
	known := e.backup(l, bkOpt{})
	dir := filepath.Join(e.svc.Cfg.BackupDir, "libraries", "lost_lib", "2026", "10")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "garbage.manifest.json"), []byte("{not json"), 0o600)
	// Complete version 1 manifests (PLAN P0.1): one without its file, one
	// whose file does not match it.
	lostID := ulid.New()
	var orphan Manifest
	orphan.ManifestVersion, orphan.BackupID, orphan.Kind, orphan.File, orphan.SHA256 = 1, ulid.New(), KindScheduled, "orphan.backup", strings.Repeat("0", 64)
	orphan.Library.ID, orphan.Library.Slug = lostID, "lost_lib"
	writeManifest(t, filepath.Join(dir, "orphan.manifest.json"), orphan)
	var damaged Manifest
	damaged.ManifestVersion, damaged.BackupID, damaged.Kind, damaged.File, damaged.Size, damaged.SHA256 = 1, ulid.New(), KindScheduled, "damaged.backup", 5, strings.Repeat("ab", 32)
	damaged.Library.ID, damaged.Library.Slug, damaged.Library.DisplayName = lostID, "lost_lib", "Lost library"
	damaged.StartedAt, damaged.FinishedAt = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
	writeManifest(t, filepath.Join(dir, "damaged.manifest.json"), damaged)
	os.WriteFile(filepath.Join(dir, "damaged.backup"), []byte("PGDMP"), 0o600)

	e.failsWithoutMarker(JobCatalogRebuild)
	if _, err := e.svc.Backup(ctx, damaged.BackupID); err == nil {
		t.Fatal("an unmarked volume was read into the catalog")
	}

	j := e.run(JobCatalogRebuild, "", nil, jobs.Succeeded)
	logs := e.logs(j)
	for _, want := range []string{
		"warn: " + filepath.Join(dir, "garbage.manifest.json") + ": SQS-B040 unreadable manifest: ",
		"warn: Manifest without file: " + filepath.Join(dir, "orphan.manifest.json"),
		"warn: " + filepath.Join("libraries", "lost_lib", "2026", "10", "damaged.backup") + " failed verification: SQS-B020 verification failed: the SHA-256 checksum does not match the manifest",
		"info: Added 1 backups to the catalog",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs miss %q:\n%s", want, logs)
		}
	}
	if string(j.Result) != `{"added":1}` {
		t.Fatalf("result %s", j.Result)
	}
	b, err := e.svc.Backup(ctx, damaged.BackupID)
	if err != nil || b.VerifyStatus != VerifyFailed || b.Verified() {
		t.Fatalf("damaged backup rebuilt as %+v %v", b, err)
	}
	lib, err := e.svc.Library(ctx, lostID)
	if err != nil || lib.State != StatePurged || lib.Slug != "lost_lib" || !strings.Contains(lib.Notes, "Rebuilt from the manifests on the backups volume") {
		t.Fatalf("rebuilt library %+v %v", lib, err)
	}
	// The manifests do not record a policy: retention keeps every backup of
	// the rebuilt library until one is saved for it.
	if own, err := e.svc.HasOwnPolicy(ctx, lostID); own || err != nil {
		t.Fatalf("policy of the rebuilt library: own %v %v", own, err)
	}
	if _, err := e.svc.Backup(ctx, orphan.BackupID); err == nil {
		t.Fatal("a manifest without its file was added")
	}
	if b, _ := e.svc.Backup(ctx, known.ID); b.VerifyStatus != VerifyL1OK {
		t.Fatalf("a backup already in the catalog was touched: %+v", b)
	}
}

// Housekeeping deletes old jobs and events, reports a modified audit log as
// critical, keeps an expired export whose database cannot be dropped (the
// next run tries again) and warns about libraries without a recent backup.
func TestHousekeeping(t *testing.T) {
	now := time.Now()
	e := newJobsEnv(t, now)
	ctx := e.ctx
	old := store.T(now.Add(-100 * 24 * time.Hour))
	if _, err := e.svc.DB.ExecContext(ctx, `INSERT INTO jobs(id, type, state, scheduled_for, created_at, finished_at, created_by_type) VALUES ('OLDJOB', ?, 'succeeded', ?, ?, ?, 'system')`,
		JobRetention, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DB.ExecContext(ctx, `INSERT INTO events(id, ts, type, severity, message) VALUES ('OLDEVENT', ?, 'system.started', 'info', 'sqsolve started')`,
		store.T(now.Add(-200*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	l := e.library("stale_lib", StateActive)
	exported := e.backup(e.library("exp_lib", StateActive), bkOpt{})
	ws := ulid.New()
	if _, err := e.svc.DB.ExecContext(ctx, `INSERT INTO export_workspaces(id, backup_id, db_name, job_id, state, created_at, expires_at) VALUES (?, ?, 'sqsolve_export_x', '', 'ready', ?, ?)`,
		ws, exported.ID, old, old); err != nil {
		t.Fatal(err)
	}
	e.svc.Audit.Record(ctx, bkActor, "test.one", "system", "", audit.OK, nil)
	e.svc.Audit.Record(ctx, bkActor, "test.two", "system", "", audit.OK, nil)
	var first int64
	e.svc.DB.QueryRowContext(ctx, `SELECT min(seq) FROM audit_log`).Scan(&first)
	e.svc.DB.ExecContext(ctx, `UPDATE audit_log SET actor_name = 'mallory' WHERE seq = ?`, first)

	j := e.run(JobHousekeeping, "", nil, jobs.Succeeded)
	logs := e.logs(j)
	if !strings.Contains(logs, "Deleted 1 old jobs") || !strings.Contains(logs, "Deleted 1 old events") {
		t.Fatalf("logs: %s", logs)
	}
	if _, err := e.svc.Jobs.Get(ctx, "OLDJOB"); err == nil {
		t.Fatal("old job kept")
	}
	broken := e.events(events.AuditChainBroken)
	if len(broken) != 1 || broken[0].Severity != events.Critical ||
		broken[0].Message != fmt.Sprintf("The audit chain is broken at entry %d: someone modified the sqsolve database", first) {
		t.Fatalf("chain events %+v", broken)
	}
	var state string
	e.svc.DB.QueryRowContext(ctx, `SELECT state FROM export_workspaces WHERE id = ?`, ws).Scan(&state)
	if state != "ready" {
		t.Fatalf("an export whose database could not be dropped is %s", state)
	}
	if stale := e.events(events.BackupStale); len(stale) != 1 || stale[0].LibraryID != l.ID {
		t.Fatalf("stale events %+v", stale)
	}
}

// Retention runs over every library, purged ones too, and keeps pinned and
// newest verified backups; a library whose policy cannot be read is skipped
// with a warning and the others are still cleaned.
func TestRetentionOfEveryLibrary(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	ctx := e.ctx
	now := time.Now()
	a := e.library("ret_a", StateActive)
	e.policy(a, 1, 1)
	pinned := e.backup(a, bkOpt{started: now.Add(-72 * time.Hour), pinned: true})
	expiredA := e.backup(a, bkOpt{started: now.Add(-48 * time.Hour)})
	newestA := e.backup(a, bkOpt{started: now.Add(-time.Hour)})
	p := e.library("ret_purged", StatePurged)
	e.policy(p, 1, 1)
	expiredP := e.backup(p, bkOpt{started: now.Add(-48 * time.Hour)})
	newestP := e.backup(p, bkOpt{started: now.Add(-time.Hour)})
	broken := e.library("ret_broken", StateActive)
	if _, err := e.svc.DB.ExecContext(ctx, `INSERT INTO backup_policies(library_id, enabled, schedule_cron, timezone, keep_last, keep_daily, keep_weekly, keep_monthly, min_verified, updated_at)
		VALUES (?, 'not a boolean', '0 2 * * *', 'UTC', 1, 0, 0, 0, 1, ?)`, broken.ID, store.NowT()); err != nil {
		t.Fatal(err)
	}
	brokenOld := e.backup(broken, bkOpt{started: now.Add(-48 * time.Hour)})
	e.backup(broken, bkOpt{started: now.Add(-time.Hour)})

	j := e.run(JobRetention, "", nil, jobs.Succeeded)
	logs := e.logs(j)
	if !strings.Contains(logs, "warn: ret_broken: ") || !strings.Contains(logs, "info: Retention applied to 3 libraries; 2 backups deleted") {
		t.Fatalf("logs: %s", logs)
	}
	for _, c := range []struct {
		b    *Backup
		want string
	}{{pinned, BackupOK}, {expiredA, BackupDeleted}, {newestA, BackupOK}, {expiredP, BackupDeleted}, {newestP, BackupOK}, {brokenOld, BackupOK}} {
		if got, _ := e.svc.Backup(ctx, c.b.ID); got.Status != c.want {
			t.Errorf("%s of %s: %s, want %s", c.b.ID, c.b.LibraryID, got.Status, c.want)
		}
	}
}

// The retention job applies the default policy of Settings to no library:
// a purged library with its own policy is pruned with it, one without a
// policy of its own keeps every backup, and the job log names it. Libraries
// that have no backups at all (not active yet) are not named: retention
// left nothing of theirs alone.
func TestRetentionJobNamesLibrariesWithoutTheirOwnPolicy(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	ctx := e.ctx
	now := time.Now()
	st := DefaultSettings("UTC")
	st.DefaultPolicy.KeepLast, st.DefaultPolicy.KeepDaily, st.DefaultPolicy.KeepWeekly, st.DefaultPolicy.KeepMonthly = 1, 0, 0, 0
	if err := e.svc.DB.SetSetting(ctx, settingsKey, st, "test"); err != nil {
		t.Fatal(err)
	}
	own := e.library("own_purged", StatePurged)
	e.policy(own, 1, 1)
	ownOld := e.backup(own, bkOpt{started: now.Add(-48 * time.Hour)})
	ownNew := e.backup(own, bkOpt{started: now.Add(-time.Hour)})
	rb := e.library("rebuilt", StatePurged)
	rbOld := e.backup(rb, bkOpt{started: now.Add(-48 * time.Hour)})
	rbNew := e.backup(rb, bkOpt{started: now.Add(-time.Hour)})
	for _, st := range []State{StateProvisioning, StateAwaitingInit, StateInitExpired} {
		e.library("empty_"+string(st), st)
	}

	j := e.run(JobRetention, "", nil, jobs.Succeeded)
	want := "info: Retention applied to 1 libraries; 1 backups deleted\n" +
		"info: Kept every backup of 1 libraries without a policy of their own (save one in the Schedule tab of each to apply retention): rebuilt"
	if logs := e.logs(j); logs != want {
		t.Fatalf("logs:\n%s\nwant:\n%s", logs, want)
	}
	for _, c := range []struct {
		b    *Backup
		want string
	}{{ownOld, BackupDeleted}, {ownNew, BackupOK}, {rbOld, BackupOK}, {rbNew, BackupOK}} {
		if got, _ := e.svc.Backup(ctx, c.b.ID); got.Status != c.want {
			t.Errorf("%s of %s: %s, want %s", c.b.ID, c.b.LibraryID, got.Status, c.want)
		}
	}
}

// F09: while the backups disk is not mounted the retention job fails as a
// whole with the code of the volume: nothing is deleted or recorded deleted,
// the row of an old failed backup included, and no deletion is announced.
// Once the disk is back the next run applies it.
func TestRetentionJobFailsOnAnUnmountedVolume(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	ctx := e.ctx
	now := time.Now()
	l := e.library("ret_unmounted", StateActive)
	e.policy(l, 1, 1)
	old := e.backup(l, bkOpt{started: now.Add(-48 * time.Hour)})
	newest := e.backup(l, bkOpt{started: now.Add(-time.Hour)})
	failed := e.backup(l, bkOpt{started: now.Add(-40 * 24 * time.Hour), status: BackupFailed, verify: VerifyNone})

	disk, mount := e.unmount()
	j := e.run(JobRetention, "", nil, jobs.Failed)
	if j.ErrorCode != "SQS-S001" || j.ErrorDetail != "SQS-S001 the volume has no sqsolve marker" {
		t.Fatalf("retention on an unmounted volume: %s %q", j.ErrorCode, j.ErrorDetail)
	}
	if logs := e.logs(j); strings.Contains(logs, "Retention applied") {
		t.Fatalf("logs: %s", logs)
	}
	for _, b := range []*Backup{old, newest} {
		if got, _ := e.svc.Backup(ctx, b.ID); got.Status != BackupOK || got.DeletedAt != nil {
			t.Fatalf("%s recorded as %+v", b.ID, got)
		}
	}
	if _, err := e.svc.Backup(ctx, failed.ID); err != nil {
		t.Fatalf("the row of the failed backup was removed: %v", err)
	}
	if !onDisk(filepath.Join(disk, old.FilePath)) {
		t.Fatal("the file left the disk")
	}
	if evs := e.events(events.BackupExpired); len(evs) != 0 {
		t.Fatalf("deletions announced: %+v", evs)
	}
	if evs := e.events(events.JobFailed); len(evs) != 1 || evs[0].Message != `The job "Apply retention" failed: SQS-S001 the volume has no sqsolve marker` {
		t.Fatalf("job.failed events: %+v", evs)
	}
	mount()

	j = e.run(JobRetention, "", nil, jobs.Succeeded)
	if logs := e.logs(j); !strings.Contains(logs, "info: Retention applied to 1 libraries; 1 backups deleted") {
		t.Fatalf("logs: %s", logs)
	}
	if got, _ := e.svc.Backup(ctx, old.ID); got.Status != BackupDeleted || onDisk(mustAbs(e.svc, old)) {
		t.Fatalf("after mounting: %+v", got)
	}
	if _, err := e.svc.Backup(ctx, failed.ID); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("the row of the failed backup after mounting: %v", err)
	}
}

// The weekly verification queues one test restore of the newest verified
// backup of each live library, and none for libraries in the trash or
// without backups.
func TestVerifyAllQueuesTheNewestVerifiedBackups(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	ctx := e.ctx
	now := time.Now()
	active := e.library("va_active", StateActive)
	e.backup(active, bkOpt{started: now.Add(-48 * time.Hour)})
	newest := e.backup(active, bkOpt{started: now.Add(-time.Hour)})
	e.backup(active, bkOpt{started: now.Add(-time.Minute), verify: VerifyFailed})
	maint := e.library("va_maint", StateMaintenance)
	maintB := e.backup(maint, bkOpt{started: now.Add(-time.Hour)})
	trashed := e.library("va_trash", StateTrashed)
	e.backup(trashed, bkOpt{started: now.Add(-time.Hour)})
	e.library("va_empty", StateActive)

	e.run(JobVerifyAll, "", nil, jobs.Succeeded)
	list, err := e.svc.Jobs.List(ctx, jobs.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, j := range list {
		if j.Type != JobBackupVerify {
			continue
		}
		var p VerifyParams
		json.Unmarshal(j.Params, &p)
		got = append(got, j.LibraryID+"/"+p.BackupID)
	}
	slices.Sort(got)
	want := []string{active.ID + "/" + newest.ID, maint.ID + "/" + maintB.ID}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("verifications queued %v, want %v", got, want)
	}
}

// The system schedules are created once, enabled, in the installation's time
// zone; a schedule the administrator changed is kept at the next start. When
// TZ changes they move to the new zone at the next start, each with its own
// expression and state.
func TestEnsureSystemSchedules(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	ctx := e.ctx
	if err := e.svc.EnsureSystemSchedules(ctx); err != nil {
		t.Fatal(err)
	}
	for kind, d := range systemSchedules {
		sc, err := e.svc.Sched.Get(ctx, kind, "")
		if err != nil || sc == nil || sc.Cron != d.cron || !sc.Enabled || sc.Timezone != "UTC" {
			t.Fatalf("%s: %+v %v", kind, sc, err)
		}
	}
	if err := e.svc.Sched.Upsert(ctx, "system.vacuum", "", "0 1 * * 6", "UTC", false, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.EnsureSystemSchedules(ctx); err != nil {
		t.Fatal(err)
	}
	if sc, _ := e.svc.Sched.Get(ctx, "system.vacuum", ""); sc.Cron != "0 1 * * 6" || sc.Enabled {
		t.Fatalf("the changed schedule was reset: %+v", sc)
	}
	all, _ := e.svc.Sched.List(ctx)
	if len(all) != len(systemSchedules) {
		t.Fatalf("%d schedules after two starts", len(all))
	}
	mx, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		t.Fatal(err)
	}
	e.svc.Cfg.Location = mx
	if err := e.svc.EnsureSystemSchedules(ctx); err != nil {
		t.Fatal(err)
	}
	for kind, d := range systemSchedules {
		sc, err := e.svc.Sched.Get(ctx, kind, "")
		if err != nil || sc == nil || sc.Timezone != "America/Mexico_City" {
			t.Fatalf("%s did not follow TZ: %+v %v", kind, sc, err)
		}
		if kind == "system.vacuum" {
			if sc.Cron != "0 1 * * 6" || sc.Enabled {
				t.Fatalf("the changed schedule was reset with the zone: %+v", sc)
			}
			continue
		}
		if sc.Cron != d.cron || !sc.Enabled {
			t.Fatalf("%s: %+v", kind, sc)
		}
	}
	if sc, _ := e.svc.Sched.Get(ctx, "system.retention", ""); sc.NextRunAt == nil || sc.NextRunAt.In(mx).Format("15:04") != "04:00" {
		t.Fatalf("retention does not run at 04:00 of the new zone: %+v", sc)
	}
	if all, _ = e.svc.Sched.List(ctx); len(all) != len(systemSchedules) {
		t.Fatalf("%d schedules after a change of TZ", len(all))
	}
}

// The Resolve upgrade window needs a target version, is queued only with its
// audit entry, and with no live library ends at once.
func TestUpgradeWindowNeedsATarget(t *testing.T) {
	e := newJobsEnv(t, time.Now())
	ctx := e.ctx
	for _, target := range []string{"", "   ", strings.Repeat("1", 21)} {
		if _, err := e.svc.UpgradeWindow(ctx, bkActor, target); err == nil || err.Error() != "SQS-C040 type the target Resolve version" {
			t.Errorf("target %q: %v", target, err)
		}
	}
	j, err := e.svc.UpgradeWindow(ctx, bkActor, " 20.1 ")
	if err != nil {
		t.Fatal(err)
	}
	j = e.waitJob(j.ID, jobs.Succeeded)
	var res struct {
		Target    string            `json:"target"`
		Libraries []json.RawMessage `json:"libraries"`
	}
	if err := json.Unmarshal(j.Result, &res); err != nil || res.Target != "20.1" || len(res.Libraries) != 0 {
		t.Fatalf("result %s %v", j.Result, err)
	}
	entries, _ := e.svc.Audit.List(ctx, 0, 10, "")
	if len(entries) == 0 || entries[len(entries)-1].Action != "system.upgrade_window" || entries[len(entries)-1].Details["target"] != "20.1" {
		t.Fatalf("audit entries %+v", entries)
	}
	if _, err := e.svc.DB.Exec(`CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UpgradeWindow(ctx, bkActor, "20.2"); err == nil || !strings.HasPrefix(err.Error(), "SQS-X040 the audit log could not be written, so the action was not done") {
		t.Fatalf("window without its audit entry: %v", err)
	}
	list, _ := e.svc.Jobs.List(ctx, jobs.Filter{})
	if len(list) != 1 {
		t.Fatalf("%d jobs: a window was queued without its audit entry", len(list))
	}
}
