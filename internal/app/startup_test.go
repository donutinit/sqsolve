// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/health"
	"github.com/donutinit/sqsolve/internal/pgconf"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/testpg"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// openApp runs init and opens the application on cfg.
func openApp(t *testing.T, ctx context.Context, cfg *config.Config) *App {
	t.Helper()
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	if cfg.Workers == 0 {
		cfg.Workers, cfg.HeavyJobs = 1, 1
	}
	if err := Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	a, err := Open(ctx, cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func hasEvent(ctx context.Context, a *App, typ string, sev events.Severity) bool {
	evs, _ := a.Events.List(ctx, events.Filter{TypePrefix: typ})
	for _, e := range evs {
		if e.Type == typ && e.Severity == sev {
			return true
		}
	}
	return false
}

// Audit H7: until PostgreSQL answered nothing watched it, so a server that
// never came up raised no alert and /healthz kept answering ok.
func TestPostgresDownIsReportedWhenItNeverComesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testConfig(t) // its socket folder has no server
	a := openApp(t, ctx, cfg)
	defer a.Close()
	a.Monitor.StartupGrace = 100 * time.Millisecond
	a.Monitor.CheckEvery = 20 * time.Millisecond
	a.Background(ctx)
	defer a.Shutdown(time.Second)
	waitFor(t, "postgres.down", 5*time.Second, func() bool { return hasEvent(ctx, a, events.PostgresDown, events.Critical) })
	if a.PGReady() {
		t.Fatal("ready without PostgreSQL")
	}
}

// Audit H3, the serve side: init leaves an unmounted backups disk unmarked
// and goes on; sqsolve refuses to write backups there and raises the
// critical storage.volume_missing as soon as it starts.
func TestUnmountedBackupsAreReportedAtStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := established(t)
	cfg.BackupDir = t.TempDir()
	cfg.Workers, cfg.HeavyJobs, cfg.Location = 1, 1, time.UTC
	mustInit(t, cfg)
	a, err := Open(ctx, cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Svc.BackupVolumeOK(); err == nil || !strings.Contains(err.Error(), "SQS-S001") {
		t.Fatalf("backups accepted on an unmarked folder: %v", err)
	}
	a.Background(ctx)
	defer a.Shutdown(time.Second)
	waitFor(t, "storage.volume_missing", 5*time.Second, func() bool { return hasEvent(ctx, a, events.StorageVolumeMissing, events.Critical) })
	if !isEmpty(t, cfg.BackupDir) {
		t.Fatal("sqsolve wrote into the unmarked folder")
	}
}

// Task 4 item 9: crash leftovers of the control plane copies are removed at
// startup, but never from a backups volume that is not this installation's.
func TestStartupRemovesStalePartials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()
	sys := filepath.Join(cfg.BackupDir, "_system", "sqsolve-state-20261020T041500Z.db.partial")
	mig := filepath.Join(stateBackupDir(cfg), "sqsolve-before-0005-20261020T041500Z.db.partial")
	keep := filepath.Join(cfg.BackupDir, "_system", "sqsolve-state-20261019T041500Z.db")
	for _, p := range []string{sys, mig, keep} {
		touch(t, p)
	}
	a.removeStalePartials()
	for _, p := range []string{sys, mig} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived", filepath.Base(p))
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("a finished copy was removed")
	}
	// A backups volume of another installation is left alone.
	fsx.WriteMarker(cfg.BackupDir, fsx.Marker{InstanceID: "OTHER", Volume: "backups", CreatedAt: time.Now()})
	touch(t, sys)
	a.removeStalePartials()
	if _, err := os.Stat(sys); err != nil {
		t.Fatal("removed a file from a volume that is not this installation's")
	}
}

// PLAN 1.3.2: a backup row whose path leaves the backups volume is reported
// at startup as a critical event and left as it is.
func TestStartupReportsInvalidBackupPaths(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()
	now := store.NowT()
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES ('L1', 'client_x', 'Client X', 'active', 'client_x', 'client_x', 'resolve', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO backups(id, library_id, kind, status, file_path, started_at) VALUES ('B1', 'L1', 'manual', 'ok', '../../etc/passwd', ?)`, now); err != nil {
		t.Fatal(err)
	}
	a.checkBackupPaths(ctx)
	evs, err := a.Events.List(ctx, events.Filter{TypePrefix: events.BackupPathInvalid})
	if err != nil || len(evs) != 1 || evs[0].Severity != events.Critical || !strings.Contains(evs[0].Message, "B1") {
		t.Fatalf("events %+v %v", evs, err)
	}
	var path, status string
	a.DB.QueryRowContext(ctx, `SELECT file_path, status FROM backups WHERE id = 'B1'`).Scan(&path, &status)
	if path != "../../etc/passwd" || status != "ok" {
		t.Fatalf("the row was rewritten: %s %s", path, status)
	}
}

func certOf(t *testing.T, a *App) *x509.Certificate {
	t.Helper()
	c, err := a.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	x, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func writeCert(t *testing.T, cfg *config.Config, cn string, validFor time.Duration) {
	t.Helper()
	cp, kp, err := pgconf.SelfSigned(cn, []string{"localhost"}, validFor)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(panelCertPath(cfg)), 0o700)
	if err := fsx.WritePair(panelKeyPath(cfg), kp, 0o600, panelCertPath(cfg), cp, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Audit M (confirmed twice): the self-generated panel certificate was never
// renewed although the documentation said so.
func TestPanelCertificateRenewal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()

	// At startup.
	writeCert(t, cfg, "sqsolve", 10*24*time.Hour)
	if err := a.LoadPanelCert(); err != nil {
		t.Fatal(err)
	}
	if left := time.Until(certOf(t, a).NotAfter); left < 365*24*time.Hour {
		t.Fatalf("the expiring self-generated certificate was not renewed (%v left)", left)
	}
	if info, _ := a.PanelCertInfo(); time.Until(info.NotAfter) < 365*24*time.Hour {
		t.Fatal("the renewed certificate was not written")
	}

	// In the daily check, swapped without a restart: the certificate in use
	// was loaded months ago and now has 20 days left.
	writeCert(t, cfg, "sqsolve", 20*24*time.Hour)
	pair, _ := tls.LoadX509KeyPair(panelCertPath(cfg), panelKeyPath(cfg))
	a.cert.Store(&pair)
	if err := a.RenewPanelCert(ctx); err != nil {
		t.Fatal(err)
	}
	if left := time.Until(certOf(t, a).NotAfter); left < 365*24*time.Hour {
		t.Fatalf("the daily check did not hot-swap a renewed certificate (%v left)", left)
	}

	// A certificate uploaded by the administrator is never replaced.
	writeCert(t, cfg, "studio.example", 10*24*time.Hour)
	before, _ := a.PanelCertInfo()
	if err := a.LoadPanelCert(); err != nil {
		t.Fatal(err)
	}
	if err := a.RenewPanelCert(ctx); err != nil {
		t.Fatal(err)
	}
	if after, _ := a.PanelCertInfo(); after.Fingerprint != before.Fingerprint || certOf(t, a).Subject.CommonName != "studio.example" {
		t.Fatal("an uploaded certificate was replaced")
	}
}

// Audit Low (task 4 item 6): key and certificate are written as a pair and a
// missing half or a mismatch is repaired at startup.
func TestPanelCertificatePair(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()
	if err := a.LoadPanelCert(); err != nil {
		t.Fatal(err)
	}
	pairOK := func() {
		t.Helper()
		if _, err := tls.LoadX509KeyPair(panelCertPath(cfg), panelKeyPath(cfg)); err != nil {
			t.Fatalf("pair on disk: %v", err)
		}
	}
	pairOK()

	// Missing key.
	os.Remove(panelKeyPath(cfg))
	if err := a.LoadPanelCert(); err != nil {
		t.Fatalf("a missing key must be repaired: %v", err)
	}
	pairOK()

	// A key of another certificate: regenerated, the old files kept aside.
	_, otherKey, _ := pgconf.SelfSigned("x", []string{"localhost"}, time.Hour)
	os.WriteFile(panelKeyPath(cfg), otherKey, 0o600)
	var logged bytes.Buffer
	a.Log = slog.New(slog.NewTextHandler(&logged, nil))
	if err := a.LoadPanelCert(); err != nil {
		t.Fatalf("a mismatch must be repaired: %v", err)
	}
	pairOK()
	if m, _ := filepath.Glob(panelCertPath(cfg) + ".replaced-*"); len(m) != 1 {
		t.Fatalf("the mismatched certificate was not kept aside: %v", m)
	}
	if !strings.Contains(logged.String(), "SQS-C083 the panel certificate and its key did not belong together") {
		t.Fatalf("the repair is not logged: %s", logged.String())
	}

	// An upload interrupted between the two renames is completed.
	cp, kp, _ := pgconf.SelfSigned("studio.example", []string{"localhost"}, 400*24*time.Hour)
	os.WriteFile(panelKeyPath(cfg), kp, 0o600)
	os.WriteFile(panelCertPath(cfg)+".partial", cp, 0o644)
	if err := a.LoadPanelCert(); err != nil {
		t.Fatal(err)
	}
	if certOf(t, a).Subject.CommonName != "studio.example" {
		t.Fatal("the interrupted upload was not completed")
	}

	// InstallPanelCert writes both halves.
	cp, kp, _ = pgconf.SelfSigned("other.example", []string{"localhost"}, 400*24*time.Hour)
	if err := a.InstallPanelCert(cp, kp); err != nil {
		t.Fatal(err)
	}
	pairOK()
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(panelCertPath(cfg)), "*.partial")); len(m) != 0 {
		t.Fatalf("leftovers: %v", m)
	}

	// A key sqsolve cannot read is a permissions problem, not a broken
	// pair: nothing is replaced.
	if os.Geteuid() != 0 {
		os.Chmod(panelKeyPath(cfg), 0)
		defer os.Chmod(panelKeyPath(cfg), 0o600)
		err := a.LoadPanelCert()
		if err == nil || !strings.Contains(err.Error(), "SQS-C020") {
			t.Fatalf("want SQS-C020 for an unreadable key, got %v", err)
		}
		if m, _ := filepath.Glob(panelCertPath(cfg) + ".replaced-*"); len(m) != 1 {
			t.Fatalf("an unreadable pair was set aside: %v", m)
		}
	}
}

// The recovery flow restores a control plane copy taken on the old server:
// its PostgreSQL identifier belongs to the old cluster and must not trip
// the guard on the new one.
func TestRecoveryForgetsTheOldCluster(t *testing.T) {
	cfg := testConfig(t)
	livePair(t, cfg)
	stagePair(t, cfg, map[string]string{pgSystemIDSetting: "7000000000000000001"})
	if err := ApplyPendingRecovery(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, _ := sql.Open("sqlite", "file:"+cfg.DBPath())
	defer raw.Close()
	var n int
	raw.QueryRow(`SELECT count(*) FROM settings WHERE key = ?`, pgSystemIDSetting).Scan(&n)
	if n != 0 {
		t.Fatal("the recovered control plane still pins the old cluster")
	}
}

// Review of task 4: a failed read of the stored identifier looked like a
// first start and pinned the current, possibly foreign, cluster for good.
func TestIdentityNotStoredWhenTheCatalogCannotBeRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()
	// A stored value that cannot be decoded: GetSetting fails.
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES (?, '{broken', ?)`, pgSystemIDSetting, store.NowT()); err != nil {
		t.Fatal(err)
	}
	if !a.applyIdentity(ctx, "7000000000000000002") {
		t.Fatal("an unreadable catalog must not hold the start")
	}
	var raw string
	a.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, pgSystemIDSetting).Scan(&raw)
	if raw != "{broken" {
		t.Fatalf("the identifier was overwritten after a failed read: %q", raw)
	}
	// Not found is a first start: stored.
	a.DB.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, pgSystemIDSetting)
	if !a.applyIdentity(ctx, "7000000000000000002") {
		t.Fatal("first start held")
	}
	var stored string
	if ok, err := a.DB.GetSetting(ctx, pgSystemIDSetting, &stored); !ok || err != nil || stored != "7000000000000000002" {
		t.Fatalf("first start not stored: %v %v %q", ok, err, stored)
	}
	// A different cluster holds.
	if a.applyIdentity(ctx, "7000000000000000003") || !strings.Contains(a.PGIdentityProblem(), "SQS-S020") {
		t.Fatal("a different cluster did not hold")
	}
}

func TestIdentityDecision(t *testing.T) {
	for _, tc := range []struct {
		stored, current, accept string
		want                    identityAction
	}{
		{"", "7", "", identityStore},
		{"7", "7", "", identityOK},
		{"7", "8", "", identityHold},
		{"7", "8", "9", identityHold},
		{"7", "8", "8", identityAccept},
		{"7", "", "", identityUnknown},
	} {
		if got := decideIdentity(tc.stored, tc.current, tc.accept); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
}

// Integration: the guard around reconciliation (task 4 item 2).
func clusterConfig(t *testing.T, c *testpg.Cluster, backups string) *config.Config {
	t.Helper()
	cfg := &config.Config{AppDir: t.TempDir(), BackupDir: backups, PGDir: c.Base, PGDataDir: c.DataDir, PGSocket: c.SocketDir,
		PGPort: c.Port, PGListen: "127.0.0.1", PGBinDir: c.BinDir, PGMaxConnections: 50, PGMemoryMB: 256,
		UID: os.Getuid(), GID: os.Getgid(), PGUID: os.Getuid(), PGGID: os.Getgid(), Workers: 1, HeavyJobs: 1, Location: time.UTC}
	if err := os.WriteFile(cfg.SuperuserPasswordFile(), []byte(c.SuperPass.Reveal()+"\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// docs/runbooks/recover-installation.md end to end: the old server's
// control plane copy knows the old cluster; the new server has a new one and
// must start without holding.
func TestRecoveryOnANewServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backups := t.TempDir()

	// The old server.
	oldCfg := clusterConfig(t, testpg.Start(t), backups)
	old := openApp(t, ctx, oldCfg)
	old.EnsureKeyCheck(ctx)
	old.Background(ctx)
	waitFor(t, "the old PostgreSQL", 20*time.Second, old.PGReady)
	var oldID string
	old.DB.GetSetting(ctx, pgSystemIDSetting, &oldID)
	if err := old.DB.BackupTo(ctx, filepath.Join(backups, "_system", "sqsolve-state-20261020T041500Z.db")); err != nil {
		t.Fatal(err)
	}
	kit, err := ReadMasterKey(oldCfg)
	if err != nil {
		t.Fatal(err)
	}
	old.Shutdown(5 * time.Second)
	old.Close()

	// The new server: empty app and db volumes. sqsolve-init runs before
	// PostgreSQL creates its new cluster, so it sees an empty db volume.
	newCluster := testpg.Start(t)
	cfg := clusterConfig(t, newCluster, backups)
	first := *cfg
	first.PGDir = t.TempDir()
	first.PGDataDir = filepath.Join(first.PGDir, "18", "docker")
	out := mustInit(t, &first)
	if !strings.Contains(out, "adopting identity "+old.InstanceID) {
		t.Fatalf("init did not adopt the old identity:\n%s", out)
	}
	if m := marker(t, first.PGDir); m.InstanceID != old.InstanceID {
		t.Fatalf("the new db volume was marked %+v", m)
	}
	a := openApp(t, ctx, cfg)
	if _, err := StageRecovery(cfg, base64.StdEncoding.EncodeToString(kit)); err != nil {
		t.Fatal(err)
	}
	a.Close()
	mustInit(t, cfg)
	if err := ApplyPendingRecovery(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	a = openApp(t, ctx, cfg)
	defer a.Close()
	a.Background(ctx)
	defer a.Shutdown(5 * time.Second)
	waitFor(t, "the new PostgreSQL", 20*time.Second, a.PGReady)
	if a.PGIdentityProblem() != "" || hasEvent(ctx, a, EventClusterChanged, events.Critical) {
		t.Fatalf("the recovery held on the new cluster: %s", a.PGIdentityProblem())
	}
	var newID string
	a.DB.GetSetting(ctx, pgSystemIDSetting, &newID)
	if newID == "" || newID == oldID {
		t.Fatalf("stored %q, old %q", newID, oldID)
	}
}

func TestClusterIdentityGuard(t *testing.T) {
	c := testpg.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := clusterConfig(t, c, t.TempDir())
	start := func() *App {
		a := openApp(t, ctx, cfg)
		a.Background(ctx)
		return a
	}
	stop := func(a *App) { a.Shutdown(5 * time.Second); a.Close() }

	// First connection: the identifier is stored.
	a := start()
	waitFor(t, "PostgreSQL", 20*time.Second, a.PGReady)
	conn, err := c.Connect(ctx, "postgres", c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	var real string
	conn.QueryRow(ctx, `SELECT system_identifier::text FROM pg_control_system()`).Scan(&real)
	conn.Close(ctx)
	var stored string
	a.DB.GetSetting(ctx, pgSystemIDSetting, &stored)
	if stored == "" || stored != real {
		t.Fatalf("stored %q, cluster %q", stored, real)
	}
	// A library the old cluster had; reconciliation would mark it missing.
	now := store.NowT()
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES ('LIB', 'wedding', 'Wedding', 'active', 'wedding', 'wedding', 'resolve', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	a.DB.SetSetting(ctx, pgSystemIDSetting, "1234", "test")
	stop(a)
	hbaPath := filepath.Join(cfg.PGConfDir(), "pg_hba.conf")
	hbaBefore, _ := os.ReadFile(hbaPath)

	// Another cluster: no reconciliation, no pg_hba.conf, a critical event.
	a = start()
	waitFor(t, "postgres.cluster_changed", 20*time.Second, func() bool { return hasEvent(ctx, a, EventClusterChanged, events.Critical) })
	time.Sleep(300 * time.Millisecond)
	var state string
	a.DB.QueryRowContext(ctx, `SELECT state FROM libraries WHERE id = 'LIB'`).Scan(&state)
	if state != "active" {
		t.Fatalf("reconciliation ran on a foreign cluster: state %s", state)
	}
	if hbaNow, _ := os.ReadFile(hbaPath); !bytes.Equal(hbaBefore, hbaNow) {
		t.Fatal("pg_hba.conf was rewritten for a foreign cluster")
	}
	if a.PGReady() {
		t.Fatal("ready on a foreign cluster")
	}
	problem := a.PGIdentityProblem()
	if !strings.Contains(problem, "SQS-S020") || !strings.Contains(problem, "SQSOLVE_PG_ACCEPT_SYSTEM_ID="+real) {
		t.Fatalf("problem %q", problem)
	}
	notices := a.Monitor.Notices()
	if len(notices) != 1 || notices[0].Severity != events.Critical {
		t.Fatalf("health does not show it: %+v", notices)
	}
	if st := a.Monitor.Snapshot(ctx); !attentionHas(st.Attention, "SQS-S020") {
		t.Fatalf("the dashboard does not show it: %+v", st.Attention)
	}
	// doctor, in the console, finds it without the server's state.
	cli, err := Open(ctx, cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.PG.Open(ctx); err != nil {
		t.Fatal(err)
	}
	checks := checkMap(Doctor(ctx, cli))
	if ch := checks["PostgreSQL cluster"]; ch.Status != "fail" || !strings.Contains(ch.Detail, "SQS-S020") {
		t.Fatalf("doctor: %+v", ch)
	}
	if ch := checks["PostgreSQL version"]; ch.Status != "ok" {
		t.Fatalf("doctor version check with the same pg_dump and server: %+v", ch)
	}
	cli.Close()
	stop(a)

	// Accepted explicitly: stored, audited, reconciled.
	cfg.PGAcceptSystemID = real
	a = start()
	defer stop(a)
	waitFor(t, "PostgreSQL", 20*time.Second, a.PGReady)
	a.DB.GetSetting(ctx, pgSystemIDSetting, &stored)
	if stored != real {
		t.Fatalf("stored %q after accepting %q", stored, real)
	}
	waitFor(t, "reconciliation", 10*time.Second, func() bool {
		a.DB.QueryRowContext(ctx, `SELECT state FROM libraries WHERE id = 'LIB'`).Scan(&state)
		return state == "missing"
	})
	entries, _ := a.Audit.List(ctx, 0, 50, "")
	found := false
	for _, e := range entries {
		found = found || e.Action == "postgres.cluster_accepted"
	}
	if !found {
		t.Fatal("the acceptance is not in the audit log")
	}
	if a.PGIdentityProblem() != "" || len(a.Monitor.Notices()) != 0 {
		t.Fatal("the problem is still shown after accepting")
	}
}

func attentionHas(list []health.Attention, sub string) bool {
	for _, a := range list {
		if strings.Contains(a.Message, sub) {
			return true
		}
	}
	return false
}

func checkMap(checks []Check) map[string]Check {
	out := map[string]Check{}
	for _, c := range checks {
		out[c.Name] = c
	}
	return out
}

// doctor reports the identity of the db and app volumes (ADR 0023).
func TestDoctorChecksVolumeMarkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()
	checks := checkMap(Doctor(ctx, a))
	for _, n := range []string{"Database volume marker", "Application volume marker", "Backups volume marker"} {
		if checks[n].Status != "ok" {
			t.Errorf("%s: %+v", n, checks[n])
		}
	}
	os.Remove(filepath.Join(cfg.PGDir, fsx.MarkerName))
	fsx.WriteMarker(cfg.AppDir, fsx.Marker{InstanceID: "OTHER", Volume: "app", CreatedAt: time.Now()})
	checks = checkMap(Doctor(ctx, a))
	if ch := checks["Database volume marker"]; ch.Status != "warn" {
		t.Errorf("missing db marker: %+v", ch)
	}
	// The same codes as init.
	if ch := checks["Application volume marker"]; ch.Status != "fail" || !strings.Contains(ch.Detail, "SQS-S010") {
		t.Errorf("foreign app marker: %+v", ch)
	}
	fsx.WriteMarker(cfg.PGDir, fsx.Marker{InstanceID: "OTHER", Volume: "db", CreatedAt: time.Now()})
	if ch := checkMap(Doctor(ctx, a))["Database volume marker"]; ch.Status != "fail" || !strings.Contains(ch.Detail, "SQS-S011") {
		t.Errorf("foreign db marker: %+v", ch)
	}
	// Swapped mounts: markers of this installation on the wrong path.
	fsx.WriteMarker(cfg.PGDir, fsx.Marker{InstanceID: a.InstanceID, Volume: "backups", CreatedAt: time.Now()})
	fsx.WriteMarker(cfg.BackupDir, fsx.Marker{InstanceID: a.InstanceID, Volume: "db", CreatedAt: time.Now()})
	checks = checkMap(Doctor(ctx, a))
	for _, n := range []string{"Database volume marker", "Backups volume marker"} {
		if ch := checks[n]; ch.Status != "fail" || !strings.Contains(ch.Detail, "SQS-S019") {
			t.Errorf("%s with swapped mounts: %+v", n, ch)
		}
	}
}

// The renewal reports why it failed; the daily check turns it into an error
// event (health.Monitor.checkCerts).
func TestPanelCertificateRenewalFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the folder mode")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()
	writeCert(t, cfg, "sqsolve", 5*24*time.Hour)
	pair, _ := tls.LoadX509KeyPair(panelCertPath(cfg), panelKeyPath(cfg))
	a.cert.Store(&pair)
	dir := filepath.Dir(panelCertPath(cfg))
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	err := a.RenewPanelCert(ctx)
	if err == nil || !strings.Contains(err.Error(), "SQS-C082 could not renew the panel certificate") {
		t.Fatalf("want SQS-C082, got %v", err)
	}
}

func TestRecoveryOfAnUnreadableCopy(t *testing.T) {
	cfg := testConfig(t)
	if err := Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(cfg.DBPath()+".recover", []byte("this is not a database, just text long enough to be read as a header"), 0o600)
	os.WriteFile(masterKeyPath(cfg)+".recover", []byte("a2V5\n"), 0o400)
	keyBefore, _ := os.ReadFile(masterKeyPath(cfg))
	var out strings.Builder
	if err := ApplyPendingRecovery(cfg, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SQS-C084 could not prepare the recovered control plane") {
		t.Fatalf("output: %q", out.String())
	}
	// Nothing was moved: the installation starts as it was, and the staged
	// files are kept aside for the operator.
	if keyNow, _ := os.ReadFile(masterKeyPath(cfg)); !bytes.Equal(keyBefore, keyNow) {
		t.Fatal("the master key was replaced by a failed recovery")
	}
	for _, p := range []string{cfg.DBPath() + ".recover", masterKeyPath(cfg) + ".recover"} {
		if m, _ := filepath.Glob(p + ".refused-*"); len(m) != 1 {
			t.Fatalf("the staged %s was not set aside: %v", filepath.Base(p), m)
		}
	}
}

func TestInitWriteFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the folder mode")
	}
	// A backups folder init may mark but cannot write.
	cfg := testConfig(t)
	os.Chmod(cfg.BackupDir, 0o500)
	defer os.Chmod(cfg.BackupDir, 0o700)
	_, err := initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-C081 could not mark the backups volume") {
		t.Fatalf("want SQS-C081, got %v", err)
	}

	// The PostgreSQL certificate cannot be written.
	cfg = testConfig(t)
	mustInit(t, cfg)
	tlsDir := filepath.Join(cfg.PGConfDir(), "tls")
	os.Remove(filepath.Join(tlsDir, "server.key"))
	blocker := filepath.Join(tlsDir, "server.key.partial")
	os.Mkdir(blocker, 0o700)
	touch(t, filepath.Join(blocker, "x"))
	_, err = initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-C080 could not write the PostgreSQL certificate") {
		t.Fatalf("want SQS-C080, got %v", err)
	}
}
