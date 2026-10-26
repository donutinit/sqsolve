// SPDX-License-Identifier: AGPL-3.0-only

package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/pgconf"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func recorder(db *store.DB) *events.Recorder {
	return events.NewRecorder(db, events.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func eventsOf(t *testing.T, rec *events.Recorder, prefix string) []events.Event {
	t.Helper()
	evs, err := rec.List(context.Background(), events.Filter{TypePrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// Audit H7: the monitor started only after the first connection, so a
// PostgreSQL that never came up raised no alert at all.
func TestMonitorAlertsWhenPostgresNeverComesUp(t *testing.T) {
	ctx := context.Background()
	rec := recorder(newDB(t))
	c := &clock{t: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	var mu sync.Mutex
	pingErr := errors.New("SQS-P001 PostgreSQL is not available: dial unix: no such file")
	m := &Monitor{Events: rec, Now: c.now, StartupGrace: 5 * time.Minute,
		Ping: func(context.Context) error { mu.Lock(); defer mu.Unlock(); return pingErr }}
	setPing := func(err error) { mu.Lock(); pingErr = err; mu.Unlock() }

	m.CheckPG(ctx)
	c.add(4 * time.Minute)
	m.CheckPG(ctx)
	if n := len(eventsOf(t, rec, "postgres.")); n != 0 {
		t.Fatalf("alerted before the grace period: %d events", n)
	}
	c.add(time.Minute)
	m.CheckPG(ctx)
	evs := eventsOf(t, rec, "postgres.")
	if len(evs) != 1 || evs[0].Type != events.PostgresDown || evs[0].Severity != events.Critical || !strings.Contains(evs[0].Message, "5 minutes") {
		t.Fatalf("want one critical postgres.down after 5 minutes, got %+v", evs)
	}
	c.add(time.Minute)
	m.CheckPG(ctx)
	if n := len(eventsOf(t, rec, "postgres.")); n != 1 {
		t.Fatalf("the alert repeated on every check: %d events", n)
	}
	if m.PostgresUp() {
		t.Fatal("PostgresUp while down")
	}

	// It comes up: the recovery is announced because the outage was.
	setPing(nil)
	m.CheckPG(ctx)
	evs = eventsOf(t, rec, "postgres.")
	if len(evs) != 2 || evs[0].Type != events.PostgresUp {
		t.Fatalf("want postgres.up after the alert, got %+v", evs)
	}
	// Later failures alert on the first failed check, as before.
	setPing(errors.New("connection refused"))
	m.CheckPG(ctx)
	evs = eventsOf(t, rec, "postgres.")
	if len(evs) != 3 || evs[0].Type != events.PostgresDown || evs[0].Severity != events.Critical {
		t.Fatalf("want an immediate postgres.down, got %+v", evs)
	}
}

// A normal start: PostgreSQL answers a few seconds after sqsolve. Nothing is
// announced.
func TestMonitorQuietStart(t *testing.T) {
	ctx := context.Background()
	rec := recorder(newDB(t))
	c := &clock{t: time.Now()}
	up := false
	m := &Monitor{Events: rec, Now: c.now, StartupGrace: 5 * time.Minute, Ping: func(context.Context) error {
		if up {
			return nil
		}
		return errors.New("not yet")
	}}
	m.CheckPG(ctx)
	c.add(3 * time.Second)
	up = true
	m.CheckPG(ctx)
	if evs := eventsOf(t, rec, "postgres."); len(evs) != 0 {
		t.Fatalf("a normal start must be quiet: %+v", evs)
	}
	if !m.PostgresUp() {
		t.Fatal("PostgresUp after a good check")
	}
}

func TestCertSeverity(t *testing.T) {
	for _, tc := range []struct {
		left time.Duration
		want events.Severity
	}{
		{40 * 24 * time.Hour, ""},
		{20 * 24 * time.Hour, events.Warning},
		{13 * 24 * time.Hour, events.Error},
		{-time.Hour, events.Error},
	} {
		if got := certSeverity(tc.left); got != tc.want {
			t.Errorf("%v left: %q, want %q", tc.left, got, tc.want)
		}
	}
}

// Audit M (certificates): the daily check renews what sqsolve generated and
// warns about the rest at Error severity in the last 14 days, the minimum
// severity of new channels.
func TestCheckCertsRenewsThenWarns(t *testing.T) {
	ctx := context.Background()
	rec := recorder(newDB(t))
	dir := t.TempDir()
	panel := filepath.Join(dir, "panel.crt")
	cp, _, _ := pgconf.SelfSigned("studio.example", []string{"localhost"}, 10*24*time.Hour)
	os.WriteFile(panel, cp, 0o644)
	renewed := 0
	m := &Monitor{Events: rec, PanelTLS: panel, RenewPanelCert: func(context.Context) error { renewed++; return nil }}
	m.checkCerts(ctx)
	if renewed != 1 {
		t.Fatalf("the renewal hook ran %d times", renewed)
	}
	evs := eventsOf(t, rec, events.TLSExpiring)
	if len(evs) != 1 || evs[0].Severity != events.Error || !strings.Contains(evs[0].Message, "Panel web") {
		t.Fatalf("want one error tls.expiring for the panel, got %+v", evs)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		server int
		dump   string
		want   events.Severity
		says   string
	}{
		{180006, "pg_dump (PostgreSQL) 18.6", "", ""},
		{180007, "pg_dump (PostgreSQL) 18.6", "", ""},
		{180004, "pg_dump (PostgreSQL) 18.6\n", events.Warning, "update the postgres image digest in your compose file"},
		{170005, "pg_dump (PostgreSQL) 18.6", events.Critical, "PostgreSQL 17"},
		{190000, "pg_dump (PostgreSQL) 18.6 (Debian 18.6-1.pgdg13+1)", events.Critical, "pg_dump 18"},
		{180006, "", "", ""},
		{0, "pg_dump (PostgreSQL) 18.6", "", ""},
	} {
		sev, msg := CompareVersions(tc.server, tc.dump)
		if sev != tc.want || !strings.Contains(msg, tc.says) {
			t.Errorf("%d vs %q: %q %q, want %q containing %q", tc.server, tc.dump, sev, msg, tc.want, tc.says)
		}
	}
}

// Task 4 item 4: the unauthenticated health reports how old the oldest last
// verified backup is, against the threshold housekeeping uses.
func TestBackupFreshness(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	lib := func(id, state string, created time.Time) {
		exec(`INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'resolve', ?, ?)`, id, strings.ToLower(id), "Secret name "+id, state, strings.ToLower(id), strings.ToLower(id), store.T(created), store.T(created))
	}
	verified := func(lib string, at time.Time) {
		// A valid catalog path: a blocked one does not count as verified.
		exec(`INSERT INTO backups(id, library_id, kind, status, verify_status, file_path, started_at) VALUES (?, ?, 'scheduled', 'ok', 'l1_ok', ?, ?)`,
			lib+store.T(at), lib, "libraries/"+strings.ToLower(lib)+"/"+lib+".backup", store.T(at))
	}
	old := now.AddDate(0, -1, 0)
	lib("FRESH", "active", old)
	verified("FRESH", now.Add(-10*time.Hour))
	lib("STALE", "maintenance", old)
	verified("STALE", now.Add(-100*time.Hour))
	lib("NEW", "active", now.Add(-time.Hour)) // no backup yet, too young to be stale
	lib("NOSCHED", "active", old)
	verified("NOSCHED", now.Add(-500*time.Hour))
	exec(`INSERT INTO backup_policies(library_id, enabled, schedule_cron, timezone, keep_last, keep_daily, keep_weekly, keep_monthly, min_verified, updated_at)
		VALUES ('NOSCHED', 0, '0 3 * * *', 'UTC', 1, 1, 1, 1, 1, ?)`, store.T(old))
	lib("TRASHED", "trashed", old)

	svc := &core.Service{Cfg: &config.Config{Location: time.UTC}, DB: db, Now: func() time.Time { return now },
		PG: pg.New(t.TempDir(), 5432, secrets.New("x"), secrets.New("y"))}
	m := &Monitor{Svc: svc, Now: func() time.Time { return now }}
	f := m.BackupFreshness(ctx)
	if f.Libraries != 3 || f.Stale != 1 || f.ThresholdHours != 48 || f.Status != StatusCritical {
		t.Fatalf("freshness %+v", f)
	}
	if f.OldestVerifiedAgeHours != 100 {
		t.Fatalf("oldest age %v, want 100", f.OldestVerifiedAgeHours)
	}
	exec(`DELETE FROM backups WHERE library_id = 'STALE'`)
	verified("STALE", now.Add(-20*time.Hour))
	if f := m.BackupFreshness(ctx); f.Status != StatusCritical {
		t.Fatal("the result is cached for a while")
	}
	m.freshAt = time.Time{}
	if f := m.BackupFreshness(ctx); f.Status != StatusOK || f.Stale != 0 || f.OldestVerifiedAgeHours != 20 {
		t.Fatalf("after a new verified backup: %+v", f)
	}
}

// Review of task 4: a probe that timed out made the catalog read fail and
// the critical result was cached for 30 s (a false 503 for every monitor).
func TestBackupFreshnessErrorsAreNotCached(t *testing.T) {
	good := newDB(t)
	broken, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "b.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	broken.Close()
	svc := &core.Service{Cfg: &config.Config{Location: time.UTC}, DB: broken,
		PG: pg.New(t.TempDir(), 5432, secrets.New("x"), secrets.New("y"))}
	m := &Monitor{Svc: svc}
	if f := m.BackupFreshness(context.Background()); f.Status != StatusCritical {
		t.Fatalf("a failed read must be critical for that request: %+v", f)
	}
	svc.DB = good
	if f := m.BackupFreshness(context.Background()); f.Status != StatusOK {
		t.Fatalf("the failed result was cached: %+v", f)
	}
	// The request's own context does not decide the result.
	m2 := &Monitor{Svc: svc}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if f := m2.BackupFreshness(ctx); f.Status != StatusOK {
		t.Fatalf("a cancelled request gave %+v", f)
	}
}

// Concurrent probes share one computation instead of queueing on a lock
// held across PostgreSQL queries.
func TestBackupFreshnessSingleFlight(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	m := &Monitor{}
	m.freshFn = func(context.Context, time.Time) (Freshness, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		return Freshness{Status: StatusOK, Libraries: 3}, nil
	}
	var wg sync.WaitGroup
	results := make(chan Freshness, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- m.BackupFreshness(context.Background()) }()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	for f := range results {
		if f.Libraries != 3 {
			t.Fatalf("a waiter got %+v", f)
		}
	}
	if calls != 1 {
		t.Fatalf("%d computations for 5 concurrent probes", calls)
	}
}

// A panic in the computation must not leave the flight set: later probes
// would wait forever.
func TestBackupFreshnessSurvivesAPanic(t *testing.T) {
	m := &Monitor{}
	m.freshFn = func(context.Context, time.Time) (Freshness, error) { panic("boom") }
	func() {
		defer func() { recover() }()
		m.BackupFreshness(context.Background())
	}()
	m.freshFn = func(context.Context, time.Time) (Freshness, error) {
		return Freshness{Status: StatusOK, Libraries: 1}, nil
	}
	got := make(chan Freshness, 1)
	go func() { got <- m.BackupFreshness(context.Background()) }()
	select {
	case f := <-got:
		if f.Status != StatusOK || f.Libraries != 1 {
			t.Fatalf("after a panic: %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a probe waits forever after a panic in the computation")
	}
}

func TestVolumeStatus(t *testing.T) {
	for pct, want := range map[float64]string{10: StatusOK, 80: StatusWarning, 89.9: StatusWarning, 90: StatusCritical, 99: StatusCritical} {
		if got := (Volume{UsedPct: pct}).Status(); got != want {
			t.Errorf("%v %%: %s, want %s", pct, got, want)
		}
	}
	if got := (Volume{Err: "no such file"}).Status(); got != StatusCritical {
		t.Errorf("a volume that cannot be read: %s", got)
	}
}

// Notifications group system events by type and a discriminator from Data:
// every volume event names its volume, so warnings about two volumes are
// not collapsed into one group (controller request for task 4).
func TestVolumeEventsNameTheVolume(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	rec := recorder(db)
	pgDir, appDir, bkDir := t.TempDir(), t.TempDir(), t.TempDir()
	used := map[string]float64{pgDir: 10, appDir: 85, bkDir: 95}
	statfs = func(dir string) (fsx.Space, error) {
		return fsx.Space{Total: 1000, Free: uint64(1000 - 10*used[dir])}, nil
	}
	defer func() { statfs = fsx.Statfs }()
	fsx.WriteMarker(bkDir, fsx.Marker{InstanceID: "I", Volume: "backups", CreatedAt: time.Now()})
	svc := &core.Service{Cfg: &config.Config{PGDir: pgDir, AppDir: appDir, BackupDir: bkDir}, InstanceID: "I"}
	m := &Monitor{Svc: svc, Events: rec}
	m.checkVolumes(ctx)
	got := map[string]events.Severity{}
	for _, e := range eventsOf(t, rec, "storage.") {
		v, _ := e.Data["volume"].(string)
		got[v] = e.Severity
	}
	if len(got) != 2 || got["app"] != events.Warning || got["backups"] != events.Error {
		t.Fatalf("volume events without their volume: %v", got)
	}
	// The backups marker disappears: storage.volume_missing names it too.
	os.Remove(filepath.Join(bkDir, fsx.MarkerName))
	m.checkVolumes(ctx)
	evs := eventsOf(t, rec, events.StorageVolumeMissing)
	if len(evs) != 1 || evs[0].Data["volume"] != "backups" {
		t.Fatalf("storage.volume_missing: %+v", evs)
	}
	for _, v := range m.Volumes() {
		if v.ID == "" {
			t.Fatalf("volume without identifier: %+v", v)
		}
	}
}

// Spanish strings in the attention list (task 4 item 16).
func TestVolumeAttentionIsEnglish(t *testing.T) {
	for _, v := range []Volume{{Name: "Backups", UsedPct: 95}, {Name: "Backups", UsedPct: 85}, {Name: "Backups", Err: "gone"}} {
		a, ok := volumeAttention(v)
		if !ok || strings.Contains(a.Message, "Volumen") || strings.Contains(a.Message, " al ") || !strings.HasPrefix(a.Message, "Volume Backups") {
			t.Errorf("%+v: %q", v, a.Message)
		}
	}
}

// Final review: every tls.expiring names its certificate, so the warnings
// about the panel and PostgreSQL certificates are not grouped as one.
func TestCertEventsNameTheCertificate(t *testing.T) {
	ctx := context.Background()
	rec := recorder(newDB(t))
	dir := t.TempDir()
	panel := filepath.Join(dir, "panel.crt")
	cp, _, _ := pgconf.SelfSigned("studio.example", []string{"localhost"}, 10*24*time.Hour)
	os.WriteFile(panel, cp, 0o644)
	cfg := &config.Config{PGDir: t.TempDir()}
	os.MkdirAll(filepath.Join(cfg.PGConfDir(), "tls"), 0o700)
	os.WriteFile(filepath.Join(cfg.PGConfDir(), "tls", "server.crt"), cp, 0o644)
	m := &Monitor{Svc: &core.Service{Cfg: cfg}, Events: rec, PanelTLS: panel,
		RenewPanelCert: func(context.Context) error { return errors.New("could not renew the panel certificate") }}
	m.checkCerts(ctx)
	got := map[string]int{}
	for _, e := range eventsOf(t, rec, events.TLSExpiring) {
		c, _ := e.Data["cert"].(string)
		got[c]++
	}
	if len(got) != 2 || got["Panel web"] != 2 || got["PostgreSQL"] != 1 {
		t.Fatalf("tls.expiring by certificate: %v", got)
	}
}

// Final review: the health endpoint and housekeeping decide staleness with
// the same rule, so a verified backup dated in the future (the clock went
// back) makes both report the library stale.
func TestBackupFreshnessAgreesWithHousekeeping(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	rec := recorder(db)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, -1, 0)
	if _, err := db.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES ('BACK', 'back', 'Back', 'active', 'back', 'back', 'resolve', ?, ?)`, store.T(old), store.T(old)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO backups(id, library_id, kind, status, verify_status, file_path, started_at) VALUES ('B1', 'BACK', 'scheduled', 'ok', 'l1_ok', 'libraries/back/B1.backup', ?)`,
		store.T(now.Add(48*time.Hour))); err != nil {
		t.Fatal(err)
	}
	svc := &core.Service{Cfg: &config.Config{Location: time.UTC}, DB: db, Events: rec, Now: func() time.Time { return now },
		PG: pg.New(t.TempDir(), 5432, secrets.New("x"), secrets.New("y"))}
	m := &Monitor{Svc: svc, Now: func() time.Time { return now }}
	if f := m.BackupFreshness(ctx); f.Stale != 1 || f.Status != StatusCritical {
		t.Fatalf("health with a backup dated in the future: %+v", f)
	}
	if err := svc.CheckStale(ctx); err != nil {
		t.Fatal(err)
	}
	if evs := eventsOf(t, rec, events.BackupStale); len(evs) != 1 {
		t.Fatalf("housekeeping: %+v", evs)
	}
}

// Final review: a database or application volume that cannot be measured is
// critical in the health endpoint and raises the volume event too.
func TestUnreadableVolumeRaisesAnEvent(t *testing.T) {
	ctx := context.Background()
	rec := recorder(newDB(t))
	pgDir, appDir, bkDir := t.TempDir(), t.TempDir(), t.TempDir()
	statfs = func(dir string) (fsx.Space, error) {
		if dir == appDir {
			return fsx.Space{}, errors.New("input/output error")
		}
		return fsx.Space{Total: 1000, Free: 900}, nil
	}
	defer func() { statfs = fsx.Statfs }()
	fsx.WriteMarker(bkDir, fsx.Marker{InstanceID: "I", Volume: "backups", CreatedAt: time.Now()})
	m := &Monitor{Svc: &core.Service{Cfg: &config.Config{PGDir: pgDir, AppDir: appDir, BackupDir: bkDir}, InstanceID: "I"}, Events: rec}
	m.checkVolumes(ctx)
	m.checkVolumes(ctx)
	evs := eventsOf(t, rec, events.StorageVolumeMissing)
	if len(evs) != 1 || evs[0].Severity != events.Critical || evs[0].Data["volume"] != "app" || evs[0].Message != "Volume Application cannot be read: input/output error" {
		t.Fatalf("events for an unreadable volume: %+v", evs)
	}
}
