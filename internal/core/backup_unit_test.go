// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		sum := sha256.Sum256(content)
		m := Manifest{ManifestVersion: 1, BackupID: id, Kind: o.kind, File: filepath.Base(abs), Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
		m.Library.ID, m.Library.Slug, m.Server.DatabaseSize = l.ID, l.Slug, o.dbSize
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

// mustAbs is the checked path of b; the backups of these tests have valid
// paths.
func mustAbs(s *Service, b *Backup) string {
	p, err := s.AbsPath(b)
	if err != nil {
		panic(err)
	}
	return p
}

func onDisk(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// unmount makes the backups folder look like a disk that is not mounted:
// what it holds (the marker and libraries/) moves to disk, and the mount
// point stays as the empty folder Docker leaves in its place. mount puts it
// back.
func (e *bkEnv) unmount() (disk string, mount func()) {
	e.t.Helper()
	disk = e.t.TempDir()
	move := func(from, to string) {
		e.t.Helper()
		entries, err := os.ReadDir(from)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, en := range entries {
			if err := os.Rename(filepath.Join(from, en.Name()), filepath.Join(to, en.Name())); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	move(e.svc.Cfg.BackupDir, disk)
	return disk, func() { move(disk, e.svc.Cfg.BackupDir) }
}

// remark replaces the marker of the backups volume with m; restore puts the
// original back.
func (e *bkEnv) remark(m fsx.Marker) (restore func()) {
	e.t.Helper()
	marker := filepath.Join(e.svc.Cfg.BackupDir, fsx.MarkerName)
	saved, err := os.ReadFile(marker)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := fsx.WriteMarker(e.svc.Cfg.BackupDir, m); err != nil {
		e.t.Fatal(err)
	}
	return func() {
		e.t.Helper()
		if err := os.WriteFile(marker, saved, 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
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
	if b, _ := e.svc.Backup(e.ctx, after.ID); b.Status != BackupOK || !onDisk(mustAbs(e.svc, b)) {
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
		abs := mustAbs(e.svc, b)
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
		if onDisk(mustAbs(e.svc, b)) || onDisk(manifestPath(mustAbs(e.svc, b))) {
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

// TestRetentionKeepsTheBackupsOfALibraryWithoutItsOwnPolicy: retention
// applies only a policy of the library's own. Two libraries hold the same
// 30 backups and both show the default policy of Settings: the one that has
// it as its own policy loses exactly what that policy expires, as before;
// the one without a policy of its own (the catalog rebuild registers such a
// library, purged) keeps every backup and only its failed leftovers are
// cleaned. Once a policy is saved for it, retention applies that policy.
func TestRetentionKeepsTheBackupsOfALibraryWithoutItsOwnPolicy(t *testing.T) {
	e := newBkEnv(t)
	def := DefaultSettings("UTC").DefaultPolicy
	own := e.library("own", StatePurged)
	if _, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO backup_policies(library_id, enabled, schedule_cron, timezone, keep_last, keep_daily, keep_weekly, keep_monthly,
		min_verified, mode, check_minutes, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, own.ID, def.Enabled, def.Cron, def.Timezone,
		def.KeepLast, def.KeepDaily, def.KeepWeekly, def.KeepMonthly, def.MinVerified, def.Mode, def.CheckMinutes, store.NowT()); err != nil {
		t.Fatal(err)
	}
	rebuilt := e.library("rebuilt", StatePurged)
	now := time.Now()
	thirty := func(l *Library) []*Backup {
		var bs []*Backup
		for i := 29; i >= 0; i-- {
			bs = append(bs, e.backup(l, bkOpt{started: now.Add(-time.Duration(i)*40*24*time.Hour - time.Hour)}))
		}
		return bs
	}
	ownBs, rebuiltBs := thirty(own), thirty(rebuilt)
	failed := e.backup(rebuilt, bkOpt{started: now.Add(-40 * 24 * time.Hour), status: BackupFailed, verify: VerifyNone})

	for _, c := range []struct {
		l   *Library
		own bool
	}{{own, true}, {rebuilt, false}} {
		if got, err := e.svc.HasOwnPolicy(e.ctx, c.l.ID); got != c.own || err != nil {
			t.Fatalf("HasOwnPolicy of %s: %v %v", c.l.Slug, got, err)
		}
		if p, err := e.svc.Policy(e.ctx, c.l.ID); p != def || err != nil {
			t.Fatalf("Policy of %s: %+v %v", c.l.Slug, p, err)
		}
	}
	// What the default policy expires; the library without a policy of its
	// own lost the same 16 backups before.
	_, wantExpire, err := e.svc.RetentionPreview(e.ctx, own.ID, def)
	if err != nil || len(wantExpire) != 16 {
		t.Fatalf("preview of the policy: %d expired %v", len(wantExpire), err)
	}
	if _, expire, err := e.svc.RetentionPreview(e.ctx, rebuilt.ID, def); err != nil || len(expire) != 16 {
		t.Fatalf("preview of the default policy: %d expired %v", len(expire), err)
	}

	if n, err := e.svc.applyRetention(e.ctx, own.ID); err != nil || n != 16 {
		t.Fatalf("applyRetention with its own policy: %d %v", n, err)
	}
	expired := map[string]bool{}
	for _, id := range wantExpire {
		expired[id] = true
	}
	for i, b := range ownBs {
		want := BackupOK
		if expired[b.ID] {
			want = BackupDeleted
		}
		if got, _ := e.svc.Backup(e.ctx, b.ID); got.Status != want || onDisk(mustAbs(e.svc, b)) != (want == BackupOK) {
			t.Fatalf("backup %d of the library with its own policy: %s, want %s", i, got.Status, want)
		}
	}

	if n, err := e.svc.applyRetention(e.ctx, rebuilt.ID); err != nil || n != 0 {
		t.Fatalf("applyRetention without a policy of its own: %d %v", n, err)
	}
	for i, b := range rebuiltBs {
		abs := mustAbs(e.svc, b)
		if got, _ := e.svc.Backup(e.ctx, b.ID); got.Status != BackupOK || !onDisk(abs) || !onDisk(manifestPath(abs)) {
			t.Fatalf("backup %d of the library without a policy of its own: %+v", i, got)
		}
	}
	if evs := e.events(events.BackupExpired); len(evs) != 1 || evs[0].LibraryID != own.ID {
		t.Fatalf("expired events %+v", evs)
	}
	if onDisk(mustAbs(e.svc, failed)) || onDisk(manifestPath(mustAbs(e.svc, failed))) {
		t.Fatal("the leftovers of a failed backup were kept")
	}
	if _, err := e.svc.Backup(e.ctx, failed.ID); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("failed row older than 30 days kept: %v", err)
	}

	e.policy(rebuilt, 2, 1)
	if got, err := e.svc.HasOwnPolicy(e.ctx, rebuilt.ID); !got || err != nil {
		t.Fatalf("HasOwnPolicy after a policy was saved: %v %v", got, err)
	}
	if n, err := e.svc.applyRetention(e.ctx, rebuilt.ID); err != nil || n != 28 {
		t.Fatalf("applyRetention once a policy is saved: %d %v", n, err)
	}
	for i, b := range rebuiltBs {
		if got, _ := e.svc.Backup(e.ctx, b.ID); (got.Status == BackupOK) != (i >= 28) {
			t.Fatalf("backup %d after the policy was saved: %s", i, got.Status)
		}
	}
}

// TestApplyRetentionPreRestoreTakesNoPlace: the pre_restore backups that
// four restores leave take none of the keep_last places, so the backup just
// restored and the scheduled one stay until keep_last newer backups of other
// kinds exist.
func TestApplyRetentionPreRestoreTakesNoPlace(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("swap", StateActive)
	e.policy(l, 2, 1)
	restored := e.backup(l, bkOpt{kind: KindManual})
	regular := e.backup(l, bkOpt{})
	var pre []*Backup
	for i := 0; i < 4; i++ {
		pre = append(pre, e.backup(l, bkOpt{kind: KindPreRestore}), e.backup(l, bkOpt{kind: KindPreRestore, source: "swap__prev_202610261800"}))
	}
	all := append([]*Backup{restored, regular}, pre...)
	p, err := e.svc.Policy(e.ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	keep, expire, err := e.svc.RetentionPreview(e.ctx, l.ID, p)
	if err != nil || len(keep) != 10 || len(expire) != 0 {
		t.Fatalf("preview: keep %v expire %v %v", keep, expire, err)
	}
	if n, err := e.svc.applyRetention(e.ctx, l.ID); err != nil || n != 0 {
		t.Fatalf("applyRetention: %d %v", n, err)
	}
	for _, b := range all {
		got, _ := e.svc.Backup(e.ctx, b.ID)
		abs := mustAbs(e.svc, b)
		if got.Status != BackupOK || !onDisk(abs) || !onDisk(manifestPath(abs)) {
			t.Fatalf("%s backup %s touched: %+v", b.Kind, b.ID, got)
		}
	}
	e.backup(l, bkOpt{})
	e.backup(l, bkOpt{})
	if n, err := e.svc.applyRetention(e.ctx, l.ID); err != nil || n != 2 {
		t.Fatalf("applyRetention after two more backups: %d %v", n, err)
	}
	for _, b := range []*Backup{restored, regular} {
		if got, _ := e.svc.Backup(e.ctx, b.ID); got.Status != BackupDeleted || got.ExpiredReason != "retention" {
			t.Fatalf("%s backup not expired: %+v", b.Kind, got)
		}
	}
	for _, b := range pre {
		got, _ := e.svc.Backup(e.ctx, b.ID)
		abs := mustAbs(e.svc, b)
		if got.Status != BackupOK || !onDisk(abs) || !onDisk(manifestPath(abs)) {
			t.Fatalf("protected pre_restore backup %s touched: %+v", b.ID, got)
		}
	}
	if evs := e.events(events.BackupExpired); len(evs) != 1 || !strings.Contains(evs[0].Message, "deleted 2 backups") {
		t.Fatalf("expired event %+v", evs)
	}
}

// TestRetentionStopsWhenTheLibraryCannotBeRead: the lookup of the library
// tells the backups of the live database from those of a previous state.
// When it fails while the backups can still be read, retention stops with
// its error and deletes nothing. Taking every backup as one of the live
// database, it met the minimum of verified backups with the backup of an old
// previous state and expired the only verified backup of the live database.
func TestRetentionStopsWhenTheLibraryCannotBeRead(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("lookup", StateActive)
	e.policy(l, 1, 1)
	now := time.Now()
	live := e.backup(l, bkOpt{started: now.Add(-48 * time.Hour)})
	prev := e.backup(l, bkOpt{started: now.Add(-40 * 24 * time.Hour), kind: KindPreRestore, source: "lookup__prev_202609300000"})
	newest := e.backup(l, bkOpt{started: now, verify: VerifyNone})
	exec := func(q string) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(e.ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`ALTER TABLE libraries RENAME COLUMN db_name TO db_name_unreadable`)
	n, err := e.svc.applyRetention(e.ctx, l.ID)
	if err == nil || !strings.Contains(err.Error(), "no such column: db_name") || n != 0 {
		t.Fatalf("retention after a failed lookup of the library: %d %v", n, err)
	}
	for _, b := range []*Backup{live, prev, newest} {
		if got, _ := e.svc.Backup(e.ctx, b.ID); got.Status != BackupOK || !onDisk(mustAbs(e.svc, b)) {
			t.Fatalf("backup %s from %s deleted after a failed lookup: %+v", b.SourceDB, b.StartedAt, got)
		}
	}
	// Once the library can be read, the backup of the previous state goes.
	exec(`ALTER TABLE libraries RENAME COLUMN db_name_unreadable TO db_name`)
	if n, err := e.svc.applyRetention(e.ctx, l.ID); err != nil || n != 1 {
		t.Fatalf("applyRetention: %d %v", n, err)
	}
	if got, _ := e.svc.Backup(e.ctx, prev.ID); got.Status != BackupDeleted {
		t.Fatalf("backup of the previous state kept: %+v", got)
	}
	if got, _ := e.svc.Backup(e.ctx, live.ID); got.Status != BackupOK {
		t.Fatalf("the verified backup of the live database expired: %+v", got)
	}
}

// Retention cleans the leftovers of failed and interrupted backups. When
// they cannot be listed it stops with the error before it deletes anything,
// instead of skipping their cleanup silently; once they can, the pass runs.
func TestRetentionStopsWhenTheFailedBackupsCannotBeListed(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("leftovers", StateActive)
	e.policy(l, 1, 1)
	now := time.Now()
	old := e.backup(l, bkOpt{started: now.Add(-48 * time.Hour)})
	e.backup(l, bkOpt{started: now})
	failed := e.backup(l, bkOpt{started: now.Add(-time.Hour), status: BackupFailed, verify: VerifyNone})
	exec := func(q string) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(e.ctx, q, failed.ID); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE backups SET size_bytes = 'unreadable' WHERE id = ?`)
	n, err := e.svc.applyRetention(e.ctx, l.ID)
	if err == nil || !strings.Contains(err.Error(), `"size_bytes"`) || n != 0 {
		t.Fatalf("retention with the failed backups unreadable: %d %v", n, err)
	}
	if got, _ := e.svc.Backup(e.ctx, old.ID); got.Status != BackupOK || !onDisk(mustAbs(e.svc, old)) {
		t.Fatalf("deleted after a failed lookup: %+v", got)
	}
	if !onDisk(mustAbs(e.svc, failed)) {
		t.Fatal("the leftovers of the failed backup went")
	}
	exec(`UPDATE backups SET size_bytes = 0 WHERE id = ?`)
	if n, err := e.svc.applyRetention(e.ctx, l.ID); err != nil || n != 1 {
		t.Fatalf("applyRetention: %d %v", n, err)
	}
	if onDisk(mustAbs(e.svc, failed)) {
		t.Fatal("the leftovers of the failed backup were kept")
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
		if got, _ := e.svc.Backup(e.ctx, b.ID); got.Status != BackupOK || !onDisk(mustAbs(e.svc, b)) {
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
		if b, _ := e.svc.Backup(e.ctx, latest.ID); b.Status != BackupOK || !onDisk(mustAbs(e.svc, b)) {
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
	abs := mustAbs(e.svc, old)
	if got.Status != BackupDeleted || got.ExpiredReason != "deleted manually" || got.DeletedAt == nil || onDisk(abs) || onDisk(manifestPath(abs)) {
		t.Fatalf("not deleted: %+v", got)
	}
	// Written before the files go (accepted) and after (ok): docs 08 §9.
	var outcomes string
	e.svc.DB.QueryRowContext(e.ctx, `SELECT group_concat(outcome, ' ') FROM (SELECT outcome FROM audit_log WHERE action = 'backup.delete' AND target_id = ? ORDER BY seq)`, old.ID).Scan(&outcomes)
	if outcomes != "accepted ok" {
		t.Fatalf("audit entries: %q", outcomes)
	}
	// Deleting again is a no-op.
	if err := e.svc.DeleteBackup(e.ctx, bkActor, old.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

// DeleteBackup refuses the newest verified backup of a live library
// (SQS-B034). When the lookup of the newest verified backup fails it stops
// with that error: it does not delete, nor write the audit entry, on a
// guess. The newer row here is blocked, so the backup asked for is the
// newest usable one, and the row cannot be read either.
func TestDeleteBackupStopsWhenTheNewestVerifiedCannotBeTold(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("unsure", StateActive)
	newest := e.backup(l, bkOpt{})
	unreadable := e.backup(l, bkOpt{started: time.Now().Add(time.Minute)})
	if _, err := e.svc.DB.ExecContext(e.ctx, `UPDATE backups SET file_path = '../elsewhere.backup', size_bytes = 'unreadable' WHERE id = ?`, unreadable.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteBackup(e.ctx, bkActor, newest.ID); err == nil || !strings.Contains(err.Error(), `"size_bytes"`) {
		t.Fatalf("deleted on a lookup that failed: %v", err)
	}
	if got, _ := e.svc.Backup(e.ctx, newest.ID); got.Status != BackupOK || !onDisk(mustAbs(e.svc, newest)) {
		t.Fatalf("the newest usable backup: %+v", got)
	}
	var n int
	e.svc.DB.QueryRowContext(e.ctx, `SELECT count(*) FROM audit_log WHERE action = 'backup.delete'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d backup.delete entries", n)
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
	abs := mustAbs(e.svc, stuck)
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

// Without this installation's marker (here a marker that went away while the
// files stay visible) no file is removed and the backup is not recorded
// deleted.
func TestRemoveBackupFilesNeedsTheMarker(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("rm_marker", StateActive)
	b := e.backup(l, bkOpt{})
	if err := os.Remove(filepath.Join(e.svc.Cfg.BackupDir, fsx.MarkerName)); err != nil {
		t.Fatal(err)
	}
	err := e.svc.removeBackupFiles(e.ctx, b, "retention")
	if !errors.Is(err, fsx.ErrNoMarker) || err.Error() != "SQS-S001 the volume has no sqsolve marker" {
		t.Fatalf("without the marker: %v", err)
	}
	abs := mustAbs(e.svc, b)
	if got, _ := e.svc.Backup(e.ctx, b.ID); got.Status != BackupOK || got.DeletedAt != nil || !onDisk(abs) || !onDisk(manifestPath(abs)) {
		t.Fatalf("removed without the marker: %+v, file %v, manifest %v", got, onDisk(abs), onDisk(manifestPath(abs)))
	}
}

// F09: on a backups volume that is not this installation's (another
// installation's marker, this installation's marker of another volume, or
// a disk that is not mounted, where the mount point is an empty folder)
// retention deletes nothing and records nothing deleted, the row of an old
// failed backup included, and announces nothing. Once the volume is back it
// applies.
func TestApplyRetentionNeedsThisInstallationsVolume(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("client_x", StateActive)
	e.policy(l, 1, 1)
	now := time.Now()
	old := e.backup(l, bkOpt{started: now.Add(-48 * time.Hour)})
	newest := e.backup(l, bkOpt{started: now.Add(-time.Hour)})
	failed := e.backup(l, bkOpt{started: now.Add(-40 * 24 * time.Hour), status: BackupFailed, verify: VerifyNone})
	dir := e.svc.Cfg.BackupDir
	untouched := func(what string) {
		t.Helper()
		for _, b := range []*Backup{old, newest} {
			if got, _ := e.svc.Backup(e.ctx, b.ID); got.Status != BackupOK || got.DeletedAt != nil || got.ExpiredReason != "" {
				t.Fatalf("%s: %s recorded as %+v", what, b.ID, got)
			}
		}
		if _, err := e.svc.Backup(e.ctx, failed.ID); err != nil {
			t.Fatalf("%s: the row of the failed backup was removed: %v", what, err)
		}
		if evs := e.events(events.BackupExpired); len(evs) != 0 {
			t.Fatalf("%s: %+v", what, evs)
		}
	}

	restore := e.remark(fsx.Marker{InstanceID: "OTHER", Volume: fsx.VolumeBackups, CreatedAt: now})
	n, err := e.svc.applyRetention(e.ctx, l.ID)
	if n != 0 || err == nil || err.Error() != "SQS-S003 volume "+dir+" belongs to another installation (OTHER)" {
		t.Fatalf("another installation's volume: %d %v", n, err)
	}
	if !onDisk(mustAbs(e.svc, old)) || !onDisk(mustAbs(e.svc, failed)) {
		t.Fatal("another installation's volume: files were removed")
	}
	untouched("another installation's volume")
	restore()

	swapped := fsx.Marker{InstanceID: "UNIT", Volume: fsx.VolumeDB, CreatedAt: now}
	restore = e.remark(swapped)
	n, err = e.svc.applyRetention(e.ctx, l.ID)
	if n != 0 || err == nil || err.Error() != fsx.SwappedError(dir, fsx.VolumeBackups, &swapped).Error() || !strings.HasPrefix(err.Error(), "SQS-S019 ") {
		t.Fatalf("swapped volumes: %d %v", n, err)
	}
	untouched("swapped volumes")
	restore()

	disk, mount := e.unmount()
	n, err = e.svc.applyRetention(e.ctx, l.ID)
	if n != 0 || !errors.Is(err, fsx.ErrNoMarker) || err.Error() != "SQS-S001 the volume has no sqsolve marker" {
		t.Fatalf("unmounted volume: %d %v", n, err)
	}
	if !onDisk(filepath.Join(disk, old.FilePath)) {
		t.Fatal("unmounted volume: the file left the disk")
	}
	untouched("unmounted volume")
	mount()

	if n, err := e.svc.applyRetention(e.ctx, l.ID); n != 1 || err != nil {
		t.Fatalf("this installation's volume: %d %v", n, err)
	}
	if got, _ := e.svc.Backup(e.ctx, old.ID); got.Status != BackupDeleted || onDisk(mustAbs(e.svc, old)) {
		t.Fatalf("expired backup on this installation's volume: %+v", got)
	}
	if _, err := e.svc.Backup(e.ctx, failed.ID); !errors.Is(err, ErrBackupNotFound) || onDisk(mustAbs(e.svc, failed)) {
		t.Fatalf("failed backup on this installation's volume: %v", err)
	}
}

// F09: a delete by hand on a disk that is not mounted is refused with the
// code of the volume before its audit entry, and leaves the row and the file
// as they were; once the disk is back it deletes.
func TestDeleteBackupNeedsThisInstallationsVolume(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("client_x", StateActive)
	old := e.backup(l, bkOpt{started: time.Now().Add(-48 * time.Hour)})
	e.backup(l, bkOpt{})
	entries := func() string {
		t.Helper()
		var s string
		if err := e.svc.DB.QueryRowContext(e.ctx, `SELECT COALESCE(group_concat(outcome, ' '), '') FROM (SELECT outcome FROM audit_log WHERE action = 'backup.delete' AND target_id = ? ORDER BY seq)`, old.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	disk, mount := e.unmount()
	err := e.svc.DeleteBackup(e.ctx, bkActor, old.ID)
	if !errors.Is(err, fsx.ErrNoMarker) || err.Error() != "SQS-S001 the volume has no sqsolve marker" {
		t.Fatalf("delete on an unmounted volume: %v", err)
	}
	if got, _ := e.svc.Backup(e.ctx, old.ID); got.Status != BackupOK || got.DeletedAt != nil {
		t.Fatalf("recorded as %+v", got)
	}
	if !onDisk(filepath.Join(disk, old.FilePath)) {
		t.Fatal("the file left the disk")
	}
	if got := entries(); got != "" {
		t.Fatalf("audit entries of a refused delete: %q", got)
	}
	mount()

	if err := e.svc.DeleteBackup(e.ctx, bkActor, old.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Backup(e.ctx, old.ID); got.Status != BackupDeleted || onDisk(mustAbs(e.svc, old)) {
		t.Fatalf("after mounting: %+v", got)
	}
	if got := entries(); got != "accepted ok" {
		t.Fatalf("audit entries: %q", got)
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

// latestVerified reads the name of the library's database to put the
// backups of its previous states last: when that lookup fails it returns the
// error, never a previous-state backup as the newest verified one. A target
// that is no library row (backupsInUse passes the target of a verification
// job) has no name and is not an error.
func TestLatestVerifiedStopsWhenTheLibraryCannotBeRead(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("lv", StateActive)
	now := time.Now()
	e.backup(l, bkOpt{started: now.Add(-time.Hour)})
	e.backup(l, bkOpt{started: now, source: "lv__prev_202610201200"})
	if b, err := e.svc.LatestVerified(e.ctx, "lv"); err != nil || b != nil {
		t.Fatalf("a target that is no library id: %+v %v", b, err)
	}
	if _, err := e.svc.DB.ExecContext(e.ctx, `ALTER TABLE libraries RENAME COLUMN db_name TO db_name_gone`); err != nil {
		t.Fatal(err)
	}
	if b, err := e.svc.LatestVerified(e.ctx, l.ID); err == nil || !strings.Contains(err.Error(), "no such column: db_name") || b != nil {
		t.Fatalf("with the name of the database unreadable: %+v %v", b, err)
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

// F48: the hours without a backup before the warning and the maximum upload
// are bounded as the retention periods are, so neither the stale duration
// (hours times time.Hour) nor the upload limit in bytes (MB shifted by 20)
// can overflow; the largest values are accepted.
func TestSaveSettingsBoundsStaleHoursAndUpload(t *testing.T) {
	e := newBkEnv(t)
	const staleMsg = "SQS-C033 the hours without a backup before the warning must be between 1 and 87600"
	const uploadMsg = "SQS-C034 the maximum upload must be between 1 and 1048576 MB"
	for _, c := range []struct {
		stale, upload int
		msg           string
	}{
		{0, 20480, staleMsg}, {87601, 20480, staleMsg}, {2562048, 20480, staleMsg},
		{48, 0, uploadMsg}, {48, 1048577, uploadMsg}, {48, 1 << 43, uploadMsg},
	} {
		st := DefaultSettings("UTC")
		st.StaleHours, st.ImportMaxMB = c.stale, c.upload
		if err := e.svc.SaveSettings(e.ctx, bkActor, st); err == nil || err.Error() != c.msg {
			t.Fatalf("stale %d, upload %d: %v", c.stale, c.upload, err)
		}
	}
	st := DefaultSettings("UTC")
	st.StaleHours, st.ImportMaxMB = 87600, 1048576
	if err := e.svc.SaveSettings(e.ctx, bkActor, st); err != nil {
		t.Fatalf("the largest values were refused: %v", err)
	}
	if got := e.svc.Settings(e.ctx); got.StaleHours != 87600 || got.ImportMaxMB != 1048576 {
		t.Fatalf("read back: stale %d, upload %d", got.StaleHours, got.ImportMaxMB)
	}
}

// F48: values stored before the bounds existed are clamped when read.
func TestSettingsClampStaleHoursAndUpload(t *testing.T) {
	e := newBkEnv(t)
	st := DefaultSettings("UTC")
	st.StaleHours, st.ImportMaxMB = 3000000, 1<<44
	if err := e.svc.DB.SetSetting(e.ctx, settingsKey, st, "old"); err != nil {
		t.Fatal(err)
	}
	got := e.svc.Settings(e.ctx)
	if got.StaleHours != 87600 || got.ImportMaxMB != 1048576 {
		t.Fatalf("not clamped: stale %d, upload %d", got.StaleHours, got.ImportMaxMB)
	}
	if d := time.Duration(got.StaleHours) * time.Hour; d <= 0 {
		t.Fatalf("stale duration %v", d)
	}
	if n := int64(got.ImportMaxMB) << 20; n <= 0 {
		t.Fatalf("upload limit %d", n)
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

// PLAN 1.2.2: a deleted backup is recorded deleted only once the removal of
// its files is durable; a folder that cannot be synced leaves the row as it
// was and the deletion fails.
func TestBackupDeletedOnlyWhenItsRemovalIsDurable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	e := newBkEnv(t)
	e.svc.Audit = audit.New(e.svc.DB)
	l := e.library("client_x", StateActive)
	e.backup(l, bkOpt{started: time.Now()})
	old := e.backup(l, bkOpt{started: time.Now().Add(-48 * time.Hour)})
	dir := filepath.Dir(mustAbs(e.svc, old))
	os.Chmod(dir, 0o300) // removable, not readable: the sync cannot open it
	defer os.Chmod(dir, 0o700)
	err := e.svc.DeleteBackup(e.ctx, bkActor, old.ID)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("want the sync error, got %v", err)
	}
	if got, _ := e.svc.Backup(e.ctx, old.ID); got.Status != BackupOK {
		t.Fatalf("recorded %s before the removal was durable", got.Status)
	}
	os.Chmod(dir, 0o700)
	// Deleting again finishes it: the files are gone already.
	if err := e.svc.DeleteBackup(e.ctx, bkActor, old.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Backup(e.ctx, old.ID); got.Status != BackupDeleted {
		t.Fatalf("status %s", got.Status)
	}
}

// PLAN 1.2.2: retention removes the row of an old failed backup only once
// the removal of its leftovers is durable.
func TestFailedBackupRowGoesOnlyAfterADurableRemoval(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	e := newBkEnv(t)
	l := e.library("client_x", StateActive)
	e.policy(l, 7, 1)
	e.backup(l, bkOpt{started: time.Now()})
	failed := e.backup(l, bkOpt{started: time.Now().Add(-40 * 24 * time.Hour), status: BackupFailed})
	abs := mustAbs(e.svc, failed)
	dir := filepath.Dir(abs)
	os.Chmod(dir, 0o300) // removable, not readable: the sync cannot open it
	defer os.Chmod(dir, 0o700)
	if _, err := e.svc.applyRetention(e.ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Backup(e.ctx, failed.ID); err != nil {
		t.Fatalf("the row went before its removal was durable: %v", err)
	}
	os.Chmod(dir, 0o700)
	if _, err := e.svc.applyRetention(e.ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Backup(e.ctx, failed.ID); !errors.Is(err, ErrBackupNotFound) || onDisk(abs) {
		t.Fatalf("row %v, file on disk %v", err, onDisk(abs))
	}
}
