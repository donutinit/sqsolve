// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// bkEnv is a Service backed by a real SQLite control plane and real
// temporary volumes, without PostgreSQL or a job dispatcher: enough for the
// catalog, retention and deletion code.
type bkEnv struct {
	t   *testing.T
	ctx context.Context
	svc *Service
}

var bkActor = audit.Actor{Type: "user", ID: "U1", Name: "tester"}

func newBkEnv(t *testing.T) *bkEnv {
	t.Helper()
	ctx := context.Background()
	app, backups, pgdir := t.TempDir(), t.TempDir(), t.TempDir()
	db, err := store.Open(ctx, filepath.Join(app, "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ev := events.NewRecorder(db, events.NewBus(), log)
	cfg := &config.Config{AppDir: app, BackupDir: backups, PGDir: pgdir, Location: time.UTC}
	if err := fsx.WriteMarker(backups, fsx.Marker{InstanceID: "UNIT", Volume: "backups", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Cfg: cfg, DB: db, Events: ev, Audit: audit.New(db), Log: log, InstanceID: "UNIT"}
	return &bkEnv{t: t, ctx: ctx, svc: svc}
}

// library inserts a library row in the given state, created a year ago.
func (e *bkEnv) library(slug string, st State) *Library {
	e.t.Helper()
	id := ulid.New()
	created := store.T(time.Now().Add(-365 * 24 * time.Hour))
	_, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'resolve', ?, ?)`, id, slug, "Library "+slug, string(st), slug, slug, created, created)
	if err != nil {
		e.t.Fatal(err)
	}
	l, err := e.svc.Library(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

type bkOpt struct {
	started time.Time
	verify  string
	status  string
	kind    string
	pinned  bool
	mark    string
	dbSize  int64
	noFiles bool
	source  string // source_db; the library's database when empty
}

// backup writes a backup file and its manifest on the volume and inserts the
// catalog row. Rows are inserted in call order.
func (e *bkEnv) backup(l *Library, o bkOpt) *Backup {
	e.t.Helper()
	if o.started.IsZero() {
		o.started = time.Now()
	}
	if o.verify == "" {
		o.verify = VerifyL1OK
	}
	if o.status == "" {
		o.status = BackupOK
	}
	if o.kind == "" {
		o.kind = KindScheduled
	}
	if o.source == "" {
		o.source = l.DBName
	}
	id := ulid.New()
	rel := filepath.Join("libraries", l.Slug, o.started.UTC().Format("2006"), o.started.UTC().Format("01"),
		l.Slug+"_"+o.started.UTC().Format("20060102T150405Z")+"_"+id+".backup")
	abs := filepath.Join(e.svc.Cfg.BackupDir, rel)
	content := []byte("PGDMP fake archive " + id)
	if !o.noFiles {
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(abs, content, 0o600); err != nil {
			e.t.Fatal(err)
		}
		var m Manifest
		m.BackupID, m.Size, m.Server.DatabaseSize = id, int64(len(content)), o.dbSize
		mb, _ := json.Marshal(m)
		if err := os.WriteFile(manifestPath(abs), mb, 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
	_, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO backups(id, library_id, kind, status, verify_status, file_path, size_bytes, source_db, started_at, finished_at, pinned, activity_mark)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, l.ID, o.kind, o.status, o.verify, rel, len(content), o.source,
		store.T(o.started), store.T(o.started), o.pinned, o.mark)
	if err != nil {
		e.t.Fatal(err)
	}
	b, err := e.svc.Backup(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func (e *bkEnv) policy(l *Library, keepLast, minVerified int) {
	e.t.Helper()
	_, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO backup_policies(library_id, enabled, schedule_cron, timezone, keep_last, keep_daily, keep_weekly, keep_monthly, min_verified, updated_at)
		VALUES (?, 1, '0 2 * * *', 'UTC', ?, 0, 0, 0, ?, ?)`, l.ID, keepLast, minVerified, store.NowT())
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *bkEnv) events(typ string) []events.Event {
	e.t.Helper()
	evs, err := e.svc.Events.List(e.ctx, events.Filter{TypePrefix: typ, Limit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []events.Event
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func onDisk(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestRetainKeepsNewestByInsertionAfterClockJump: after the clock went back
// three days, the backup just taken has the oldest-looking timestamp of the
// recent ones. It must still be kept: the newest N are counted by insertion
// order (audit R8).
func TestRetainKeepsNewestByInsertionAfterClockJump(t *testing.T) {
	real := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	in := items(60*24, time.Hour, real) // B0000 is the newest, B1439 the oldest
	for i := range in {
		in[i].Seq = int64(len(in) - i)
	}
	jumped := real.Add(-3*24*time.Hour - time.Hour)
	in = append(in, RetentionItem{ID: "NEW", Seq: int64(len(in) + 1), Started: jumped, Kind: KindScheduled, Verified: true})
	keep, _ := Retain(in, RetentionPolicy{KeepLast: 10, KeepDaily: 14, KeepWeekly: 8, KeepMonthly: 12, MinVerified: 1}, jumped)
	if why, ok := keep["NEW"]; !ok || !strings.Contains(why, "most recent") {
		t.Fatalf("the backup taken after a backward clock jump was not kept as one of the newest: %q %v", why, ok)
	}
	// Exactly KeepLast items carry the "most recent" reason.
	n := 0
	for _, why := range keep {
		if strings.Contains(why, "most recent") {
			n++
		}
	}
	if n != 10 {
		t.Fatalf("%d items kept as most recent, want 10", n)
	}
}

// TestRetainMinVerifiedByInsertionOrder: the minimum of verified backups is
// filled with the newest verified ones by insertion order too.
func TestRetainMinVerifiedByInsertionOrder(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	in := []RetentionItem{
		{ID: "future", Seq: 1, Started: now.Add(48 * time.Hour), Kind: KindScheduled, Verified: true},
		{ID: "newest", Seq: 2, Started: now, Kind: KindScheduled, Verified: true},
		{ID: "unverified", Seq: 3, Started: now.Add(time.Minute), Kind: KindScheduled},
	}
	keep, expire := Retain(in, RetentionPolicy{KeepLast: 1, MinVerified: 1}, now)
	if _, ok := keep["unverified"]; !ok {
		t.Fatalf("newest by insertion not kept: %v", keep)
	}
	if keep["newest"] != "minimum of verified backups" {
		t.Fatalf("minimum of verified filled with the wrong backup: %v", keep)
	}
	if len(expire) != 1 || expire[0] != "future" {
		t.Fatalf("expire %v", expire)
	}
}

func TestRetainPinnedReasonInEnglish(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	in := []RetentionItem{
		{ID: "new", Seq: 2, Started: now, Kind: KindScheduled, Verified: true},
		{ID: "old", Seq: 1, Started: now.Add(-400 * 24 * time.Hour), Kind: KindScheduled, Pinned: true},
	}
	keep, _ := Retain(in, RetentionPolicy{KeepLast: 1, MinVerified: 1}, now)
	if keep["old"] != "pinned" {
		t.Fatalf("pinned reason %q", keep["old"])
	}
}

// TestApplyRetentionUsesInsertionOrder: the catalog feeds Retain with the
// insertion order, so a clock that went back cannot expire the backup just
// taken.
func TestApplyRetentionUsesInsertionOrder(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("clock", StateActive)
	e.policy(l, 1, 1)
	now := time.Now()
	before := e.backup(l, bkOpt{started: now})                     // taken before the jump
	after := e.backup(l, bkOpt{started: now.Add(-72 * time.Hour)}) // taken after the clock went back 3 days
	n, err := e.svc.applyRetention(e.ctx, l.ID)
	if err != nil || n != 1 {
		t.Fatalf("applyRetention: %d %v", n, err)
	}
	if b, _ := e.svc.Backup(e.ctx, after.ID); b.Status != BackupOK || !onDisk(e.svc.AbsPath(b)) {
		t.Fatalf("the backup just taken was expired: %+v", b)
	}
	if b, _ := e.svc.Backup(e.ctx, before.ID); b.Status != BackupDeleted {
		t.Fatalf("older insertion kept: %+v", b)
	}
}

// TestApplyRetentionDeletesExpired covers the delete loop: files and
// manifests go, rows stay as deleted with the reason, an event is emitted,
// and leftovers of failed backups are cleaned.
func TestApplyRetentionDeletesExpired(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("ret", StateActive)
	e.policy(l, 2, 1)
	now := time.Now()
	var bs []*Backup
	for i := 3; i >= 0; i-- {
		bs = append(bs, e.backup(l, bkOpt{started: now.Add(-time.Duration(i) * time.Hour)}))
	}
	failedOld := e.backup(l, bkOpt{started: now.Add(-40 * 24 * time.Hour), status: BackupFailed, verify: VerifyNone})
	failedNew := e.backup(l, bkOpt{started: now.Add(-time.Hour), status: BackupInterrupted, verify: VerifyNone})
	n, err := e.svc.applyRetention(e.ctx, l.ID)
	if err != nil || n != 2 {
		t.Fatalf("applyRetention: %d %v", n, err)
	}
	for i, b := range bs {
		got, _ := e.svc.Backup(e.ctx, b.ID)
		abs := e.svc.AbsPath(b)
		if i < 2 {
			if got.Status != BackupDeleted || got.ExpiredReason != "retention" || got.DeletedAt == nil || onDisk(abs) || onDisk(manifestPath(abs)) {
				t.Fatalf("expired backup %d not removed: %+v", i, got)
			}
		} else if got.Status != BackupOK || !onDisk(abs) || !onDisk(manifestPath(abs)) {
			t.Fatalf("kept backup %d touched: %+v", i, got)
		}
	}
	if evs := e.events(events.BackupExpired); len(evs) != 1 || !strings.Contains(evs[0].Message, "deleted 2 backups") {
		t.Fatalf("expired event %+v", evs)
	}
	for _, b := range []*Backup{failedOld, failedNew} {
		if onDisk(e.svc.AbsPath(b)) || onDisk(manifestPath(e.svc.AbsPath(b))) {
			t.Fatalf("leftovers of %s remain", b.Status)
		}
	}
	if _, err := e.svc.Backup(e.ctx, failedOld.ID); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("failed row older than 30 days kept: %v", err)
	}
	if _, err := e.svc.Backup(e.ctx, failedNew.ID); err != nil {
		t.Fatalf("recent failed row deleted: %v", err)
	}
}

// TestRetentionSkipsBackupsInUse: a backup a queued or running restore,
// verification or export is about to read is never deleted under it.
func TestRetentionSkipsBackupsInUse(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("inuse", StateActive)
	e.policy(l, 1, 1)
	now := time.Now()
	restoreSrc := e.backup(l, bkOpt{started: now.Add(-5 * time.Hour)})
	verifySrc := e.backup(l, bkOpt{started: now.Add(-4 * time.Hour)})
	exportSrc := e.backup(l, bkOpt{started: now.Add(-3 * time.Hour)})
	doneSrc := e.backup(l, bkOpt{started: now.Add(-2 * time.Hour)})
	e.backup(l, bkOpt{started: now})
	job := func(typ, state string, params any) {
		_, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO jobs(id, type, library_id, params, state, scheduled_for, created_at, created_by_type) VALUES (?, ?, ?, ?, ?, ?, ?, 'user')`,
			ulid.New(), typ, l.ID, store.JSON(params), state, store.NowT(), store.NowT())
		if err != nil {
			t.Fatal(err)
		}
	}
	job(JobRestoreNew, "queued", RestoreParams{BackupID: restoreSrc.ID, Slug: "copy"})
	job(JobBackupVerify, "running", VerifyParams{BackupID: verifySrc.ID})
	job(JobRestoreReplace, "succeeded", RestoreParams{BackupID: doneSrc.ID})
	if _, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO export_workspaces(id, backup_id, db_name, job_id, state, created_at, expires_at) VALUES (?, ?, 'sqsolve_export_x', '', 'preparing', ?, ?)`,
		ulid.New(), exportSrc.ID, store.NowT(), store.T(now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.applyRetention(e.ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []*Backup{restoreSrc, verifySrc, exportSrc} {
		if got, _ := e.svc.Backup(e.ctx, b.ID); got.Status != BackupOK || !onDisk(e.svc.AbsPath(b)) {
			t.Fatalf("backup in use was deleted: %+v", got)
		}
	}
	if got, _ := e.svc.Backup(e.ctx, doneSrc.ID); got.Status != BackupDeleted || n != 1 {
		t.Fatalf("backup of a finished job kept: %+v (deleted %d)", got, n)
	}
}

// TestDeleteBackupGuard: the newest verified backup cannot be deleted by
// hand while the library can still be restored or recreated, including a
// missing library (audit Low).
func TestDeleteBackupGuard(t *testing.T) {
	e := newBkEnv(t)
	for _, st := range []State{StateActive, StateMaintenance, StateMissing, StateTrashed, StateRestoring, StateInitExpired} {
		l := e.library("g_"+string(st), st)
		older := e.backup(l, bkOpt{started: time.Now().Add(-time.Hour)})
		latest := e.backup(l, bkOpt{})
		err := e.svc.DeleteBackup(e.ctx, bkActor, latest.ID)
		if err == nil || !strings.Contains(err.Error(), "most recent verified backup of the library; it cannot be deleted manually") {
			t.Fatalf("%s: latest verified deleted: %v", st, err)
		}
		if b, _ := e.svc.Backup(e.ctx, latest.ID); b.Status != BackupOK || !onDisk(e.svc.AbsPath(b)) {
			t.Fatalf("%s: refused delete touched the backup: %+v", st, b)
		}
		if err := e.svc.DeleteBackup(e.ctx, bkActor, older.ID); err != nil {
			t.Fatalf("%s: older backup: %v", st, err)
		}
	}
	// A purged library can no longer be restored in place: its backups are
	// the user's to clean up.
	l := e.library("g_purged", StatePurged)
	latest := e.backup(l, bkOpt{})
	if err := e.svc.DeleteBackup(e.ctx, bkActor, latest.ID); err != nil {
		t.Fatalf("purged library: %v", err)
	}
}

func TestDeleteBackup(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("deleting", StateActive)
	old := e.backup(l, bkOpt{started: time.Now().Add(-time.Hour)})
	pinned := e.backup(l, bkOpt{started: time.Now().Add(-30 * time.Minute), pinned: true})
	e.backup(l, bkOpt{})
	if err := e.svc.DeleteBackup(e.ctx, bkActor, pinned.ID); err == nil || !strings.Contains(err.Error(), "unpin the backup before deleting it") {
		t.Fatalf("pinned deleted: %v", err)
	}
	if err := e.svc.DeleteBackup(e.ctx, bkActor, "NOPE"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("unknown backup: %v", err)
	}
	if err := e.svc.DeleteBackup(e.ctx, bkActor, old.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Backup(e.ctx, old.ID)
	abs := e.svc.AbsPath(old)
	if got.Status != BackupDeleted || got.ExpiredReason != "deleted manually" || got.DeletedAt == nil || onDisk(abs) || onDisk(manifestPath(abs)) {
		t.Fatalf("not deleted: %+v", got)
	}
	var n int
	e.svc.DB.QueryRowContext(e.ctx, `SELECT count(*) FROM audit_log WHERE action = 'backup.delete' AND target_id = ?`, old.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("%d audit entries", n)
	}
	// Deleting again is a no-op.
	if err := e.svc.DeleteBackup(e.ctx, bkActor, old.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestRemoveBackupFiles(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("rm", StateActive)
	// Files already gone are not an error.
	gone := e.backup(l, bkOpt{noFiles: true})
	if err := e.svc.removeBackupFiles(e.ctx, gone, "retention"); err != nil {
		t.Fatal(err)
	}
	if b, _ := e.svc.Backup(e.ctx, gone.ID); b.Status != BackupDeleted || b.ExpiredReason != "retention" {
		t.Fatalf("row not marked: %+v", b)
	}
	// A file that cannot be removed keeps the row as it was.
	stuck := e.backup(l, bkOpt{noFiles: true})
	abs := e.svc.AbsPath(stuck)
	if err := os.MkdirAll(filepath.Join(abs, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.removeBackupFiles(e.ctx, stuck, "retention"); err == nil {
		t.Fatal("removal of a non-empty directory reported success")
	}
	if b, _ := e.svc.Backup(e.ctx, stuck.ID); b.Status != BackupOK {
		t.Fatalf("row marked deleted although the file remains: %+v", b)
	}
}

// TestLatestVerifiedPrefersLiveDatabase: a backup taken of a previous state
// (`<slug>__prev_*`, just before it is dropped) is an older state of the
// library: it never hides the newest backup of the live database, and only
// counts when the library has nothing else.
func TestLatestVerifiedPrefersLiveDatabase(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("live", StateActive)
	now := time.Now()
	liveB := e.backup(l, bkOpt{started: now.Add(-2 * time.Hour)})
	e.backup(l, bkOpt{started: now.Add(-time.Hour), verify: VerifyNone})
	e.backup(l, bkOpt{started: now, source: "live__prev_202610201200"})
	if b, err := e.svc.LatestVerified(e.ctx, l.ID); err != nil || b == nil || b.ID != liveB.ID {
		t.Fatalf("latest verified %+v %v, want the live database backup %s", b, err, liveB.ID)
	}
	// An imported backup (source_db is the file name) is not an auxiliary
	// database: the newest one still wins.
	imported := e.backup(l, bkOpt{started: now.Add(time.Minute), kind: KindImported, source: "external.backup"})
	if b, _ := e.svc.LatestVerified(e.ctx, l.ID); b == nil || b.ID != imported.ID {
		t.Fatalf("imported backup demoted: %+v", b)
	}
	// Only a previous-state backup: it is still the latest verified.
	only := e.library("onlyprev", StateMissing)
	prevB := e.backup(only, bkOpt{source: "onlyprev__prev_202610201200"})
	if b, _ := e.svc.LatestVerified(e.ctx, only.ID); b == nil || b.ID != prevB.ID {
		t.Fatalf("fallback %+v", b)
	}
	// Retention meets min_verified with the live database first.
	r := e.library("retprev", StateActive)
	e.policy(r, 1, 1)
	keptLive := e.backup(r, bkOpt{started: now.Add(-3 * time.Hour)})
	e.backup(r, bkOpt{started: now.Add(-2 * time.Hour), verify: VerifyFailed})
	e.backup(r, bkOpt{started: now, source: "retprev__prev_202610201200"})
	if _, err := e.svc.applyRetention(e.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := e.svc.Backup(e.ctx, keptLive.ID); b.Status != BackupOK {
		t.Fatalf("the only verified backup of the live database was expired: %+v", b)
	}
}

// TestCheckStaleIgnoresFutureTimestamps: a verified backup dated in the
// future (the clock went back) says nothing about how old the protection
// really is, so it never silences the stale warning (audit Low, R8).
func TestCheckStaleIgnoresFutureTimestamps(t *testing.T) {
	e := newBkEnv(t)
	future := e.library("future", StateActive)
	e.backup(future, bkOpt{started: time.Now().Add(72 * time.Hour)})
	fresh := e.library("fresh", StateActive)
	e.backup(fresh, bkOpt{started: time.Now().Add(72 * time.Hour)})
	e.backup(fresh, bkOpt{started: time.Now().Add(-time.Hour)})
	if err := e.svc.CheckStale(e.ctx); err != nil {
		t.Fatal(err)
	}
	evs := e.events(events.BackupStale)
	if len(evs) != 1 || evs[0].LibraryID != future.ID {
		t.Fatalf("stale events %+v", evs)
	}
	if !strings.Contains(evs[0].Message, "clock") {
		t.Fatalf("the warning does not mention the clock: %s", evs[0].Message)
	}
}

func TestSaveSettingsCapsRetentionDays(t *testing.T) {
	e := newBkEnv(t)
	for _, c := range []struct{ trash, prev int }{{999999, 7}, {30, 3651}, {0, 7}, {30, -1}} {
		st := DefaultSettings("UTC")
		st.TrashDays, st.PrevDays = c.trash, c.prev
		err := e.svc.SaveSettings(e.ctx, bkActor, st)
		if err == nil || err.Error() != "SQS-C032 retention periods must be between 1 and 3650 days" {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	st := DefaultSettings("UTC")
	st.TrashDays, st.PrevDays = MaxRetentionDays, MaxRetentionDays
	if err := e.svc.SaveSettings(e.ctx, bkActor, st); err != nil {
		t.Fatalf("3650 days refused: %v", err)
	}
}

// TestSettingsClampRetentionDays: values stored before the cap existed are
// clamped when read, so no caller can overflow time.Duration (audit M4).
func TestSettingsClampRetentionDays(t *testing.T) {
	e := newBkEnv(t)
	st := DefaultSettings("UTC")
	st.TrashDays, st.PrevDays = 999999, 2000000
	if err := e.svc.DB.SetSetting(e.ctx, settingsKey, st, "old"); err != nil {
		t.Fatal(err)
	}
	got := e.svc.Settings(e.ctx)
	if got.TrashDays != MaxRetentionDays || got.PrevDays != MaxRetentionDays {
		t.Fatalf("not clamped: trash %d prev %d", got.TrashDays, got.PrevDays)
	}
	if d := time.Duration(got.PrevDays) * 24 * time.Hour; d <= 0 || d < 3000*24*time.Hour {
		t.Fatalf("duration %v", d)
	}
}

// TestVerifyErrorClassification: which tool failures are a verdict on a
// backup and which only mean the verification could not run (audit H5).
func TestVerifyErrorClassification(t *testing.T) {
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	exited := func(code int, stderr string) error {
		return fmt.Errorf("SQS-R010 pg_restore failed: %w", &execx.Error{Name: "pg_restore", ExitCode: code, Stderr: stderr})
	}
	cases := []struct {
		name             string
		ctx              context.Context
		err              error
		tool, restoreOut bool
	}{
		{"exit code on the archive", ctx, exited(1, "pg_restore: error: could not read from input file: end of file"), true, true},
		{"not a custom archive", ctx, errNotCustomArchive, true, true},
		{"job cancelled", cancelled, exited(1, "pg_restore: error: could not read from input file: end of file"), false, false},
		{"killed by a signal", ctx, exited(-1, ""), false, false},
		{"could not start", ctx, &execx.Error{Name: "pg_restore", Err: os.ErrNotExist}, false, false},
		{"program not found", ctx, errors.New("SQS-X002 pg_restore not found"), false, false},
		{"disk full on the server", ctx, exited(1, `pg_restore: error: COPY failed for table "x": ERROR:  could not extend file "base/1/2": No space left on device`), true, false},
		{"server gone", ctx, exited(1, "pg_restore: error: connection to server on socket \"/tmp/.s.PGSQL.5432\" failed"), true, false},
		{"session terminated", ctx, exited(1, "FATAL:  terminating connection due to administrator command"), true, false},
		{"lock table full", ctx, exited(1, "pg_restore: error: could not execute query: ERROR:  out of shared memory\nHINT:  You might need to increase \"max_locks_per_transaction\"."), true, false},
		{"server I/O error", ctx, exited(1, `pg_restore: error: COPY failed for table "x": ERROR:  could not read block 7 in file "base/16384/16390": Input/output error`), true, false},
		{"server cannot write", ctx, exited(1, `pg_restore: error: could not execute query: ERROR:  could not write to file "base/pgsql_tmp/pgsql_tmp123.0": No such device`), true, false},
		{"read-only server", ctx, exited(1, "pg_restore: error: could not execute query: ERROR:  cannot execute CREATE TABLE in a read-only transaction"), true, false},
		{"temp file limit", ctx, exited(1, "pg_restore: error: could not execute query: ERROR:  temporary file size exceeds temp_file_limit (1024kB)"), true, false},
		{"bad data in the archive", ctx, exited(1, `pg_restore: error: COPY failed for table "x": ERROR:  invalid input syntax for type integer`), true, true},
	}
	for _, c := range cases {
		if got := toolVerdict(c.ctx, c.err); got != c.tool {
			t.Errorf("%s: toolVerdict %v", c.name, got)
		}
		if got := restoreVerdict(c.ctx, c.err); got != c.restoreOut {
			t.Errorf("%s: restoreVerdict %v", c.name, got)
		}
	}
	// A cause wrapped twice keeps one code.
	err := notRunError(notRunError(errors.New("SQS-B022 not enough space")))
	if err.Error() != "SQS-B070 the verification could not run: SQS-B022 not enough space" {
		t.Fatalf("wrapped twice: %v", err)
	}
	if err := (&verifyFailedError{"the manifest is missing"}); err.Error() != "SQS-B020 verification failed: the manifest is missing" {
		t.Fatalf("verdict message: %v", err)
	}
}

// TestReadArchiveFully: the full read of an import says whether the archive
// is damaged (a verdict) or whether it could not be read at all.
func TestReadArchiveFully(t *testing.T) {
	e := newBkEnv(t)
	e.svc.Exec = execx.NewRunner("")
	if _, err := e.svc.Exec.Path("pg_restore"); err != nil {
		t.Skip("pg_restore is not installed")
	}
	bad := filepath.Join(t.TempDir(), "bad.backup")
	if err := os.WriteFile(bad, []byte("not an archive at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := e.svc.readArchiveFully(e.ctx, bad)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-B072 the archive is incomplete or damaged: pg_restore exited with code 1: ") {
		t.Fatalf("damaged archive: %v", err)
	}
	cctx, cancel := context.WithCancel(e.ctx)
	cancel()
	err = e.svc.readArchiveFully(cctx, bad)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-B073 the archive could not be read: ") || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}

// TestCheckRestoreSpace: one helper decides whether the database volume can
// take a restored copy of a backup (ops M3).
func TestCheckRestoreSpace(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("space", StateActive)
	small := e.backup(l, bkOpt{dbSize: 1 << 20})
	if err := e.svc.checkRestoreSpace(small, "the restore"); err != nil {
		t.Fatalf("1 MiB refused: %v", err)
	}
	huge := e.backup(l, bkOpt{dbSize: 1 << 60})
	err := e.svc.checkRestoreSpace(huge, "the restore")
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-B022 not enough space on the database volume for the restore: 1.2 EB needed, ") ||
		!strings.HasSuffix(err.Error(), " free") {
		t.Fatalf("huge database accepted or wrong message: %v", err)
	}
	// Without a manifest the archive size decides (four times, as before).
	noManifest := e.backup(l, bkOpt{noFiles: true})
	noManifest.SizeBytes = 1 << 60
	if err := e.svc.checkRestoreSpace(noManifest, "the test restore"); err == nil || !strings.Contains(err.Error(), "for the test restore: 4.0 EB needed") {
		t.Fatalf("no manifest: %v", err)
	}
}
