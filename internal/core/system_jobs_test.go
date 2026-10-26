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
	e := newJobsEnv(t, time.Date(2026, 10, 2, 4, 15, 0, 0, time.UTC))
	ctx := e.ctx
	sys := filepath.Join(e.svc.Cfg.BackupDir, "_system")
	if err := os.MkdirAll(sys, 0o700); err != nil {
		t.Fatal(err)
	}
	for d := 1; d <= 30; d++ {
		os.WriteFile(filepath.Join(sys, fmt.Sprintf("sqsolve-state-202609%02dT041500Z.db", d)), []byte("old copy"), 0o600)
	}
	os.WriteFile(filepath.Join(sys, "sqsolve-state-20260831T041500Z.db"), []byte("oldest copy"), 0o600)
	e.library("cp_lib", StateActive)
	if err := e.svc.Audit.Record(ctx, bkActor, "test.entry", "system", "", audit.OK, nil); err != nil {
		t.Fatal(err)
	}
	j := e.run(JobControlBackup, "", nil, jobs.Succeeded)
	name := "sqsolve-state-20261002T041500Z.db"
	if !strings.Contains(e.logs(j), "Control plane copy at "+name) {
		t.Fatalf("logs: %s", e.logs(j))
	}
	cp, err := sql.Open("sqlite", "file:"+filepath.Join(sys, name)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var slug string
	if err := cp.QueryRow(`SELECT slug FROM libraries`).Scan(&slug); err != nil || slug != "cp_lib" {
		t.Fatalf("the copy does not hold the catalog: %q %v", slug, err)
	}
	copies, _ := filepath.Glob(filepath.Join(sys, "sqsolve-state-*.db"))
	if len(copies) != 30 || onDisk(filepath.Join(sys, "sqsolve-state-20260831T041500Z.db")) || onDisk(filepath.Join(sys, "sqsolve-state-20260901T041500Z.db")) {
		t.Fatalf("%d copies kept: %v", len(copies), copies)
	}
	for _, month := range []string{"202609", "202610"} {
		b, err := os.ReadFile(filepath.Join(sys, "audit-"+month+".jsonl"))
		if err != nil {
			t.Fatalf("no audit export for %s: %v", month, err)
		}
		if month == "202610" && !strings.Contains(string(b), "test.entry") {
			t.Fatalf("the export of the month misses its entries:\n%s", b)
		}
	}
	e.offset.Store(int64(24 * time.Hour))
	e.failsWithoutMarker(JobControlBackup)
	if onDisk(filepath.Join(sys, "sqsolve-state-20261003T041500Z.db")) {
		t.Fatal("a copy was written to an unmarked volume")
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
// zone; a schedule the administrator changed is kept at the next start.
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
