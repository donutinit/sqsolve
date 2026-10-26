// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/scheduler"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

// auditedCatalog is catalogOnly with an audit log, a job queue (no workers),
// an active library with an owner credential, and one backup file that is
// not the newest verified one.
func auditedCatalog(t *testing.T) (*Service, *Library, *Backup) {
	t.Helper()
	ctx := context.Background()
	s, l := catalogOnly(t)
	s.Audit = audit.New(s.DB)
	s.Jobs = jobs.NewManager(s.DB, s.Events, events.NewBus(), s.Log, 1, 1)
	s.Sched = scheduler.New(s.DB, s.Jobs, s.Events, s.Log)
	s.Register()
	s.Box, _ = secrets.NewBox(secrets.RandomBytes(32))
	s.Cfg.BackupDir = t.TempDir()
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'active', prev_db_name = '', prev_backup_id = '' WHERE id = ?`, l.ID)
	enc, _ := s.Box.Seal(secrets.New("the password"), credAAD("C1"))
	now := store.NowT()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO library_credentials(id, library_id, kind, pg_role, password_enc, created_at) VALUES ('C1', ?, 'owner', 'lib', ?, ?)`, l.ID, enc, now); err != nil {
		t.Fatal(err)
	}
	for _, b := range []struct{ id, status string }{{"B1", "ok"}, {"B2", "ok"}} {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO backups(id, library_id, kind, status, verify_level, verify_status, file_path, started_at) VALUES (?, ?, 'manual', ?, 1, 'ok', ?, ?)`,
			b.id, l.ID, b.status, filepath.Join("libraries", b.id+".backup"), now); err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Join(s.Cfg.BackupDir, "libraries"), 0o700)
		os.WriteFile(filepath.Join(s.Cfg.BackupDir, "libraries", b.id+".backup"), []byte("dump"), 0o600)
	}
	l, _ = s.Library(ctx, l.ID)
	latest, _ := s.LatestVerified(ctx, l.ID)
	old := "B1"
	if latest != nil && latest.ID == "B1" {
		old = "B2"
	}
	b, err := s.Backup(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	return s, l, b
}

func breakAudit(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.DB.Exec(`CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
}

// Audit AUD-004: delete and purge recorded "ok" before queueing their job,
// so a failed queue left a false success and a failed entry did not stop the
// job; a password was returned and a backup file deleted although their
// entry could not be written. Without the entry, nothing happens now.
func TestDestructiveActionsNeedTheirAuditEntry(t *testing.T) {
	ctx := context.Background()
	s, l, b := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	breakAudit(t, s)
	unavailable := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, audit.ErrUnavailable) {
			t.Errorf("%s: want SQS-X040, got %v", what, err)
		}
	}
	_, err := s.Trash(ctx, by, l.ID, l.Slug)
	unavailable("library.delete", err)
	_, err = s.BackupNow(ctx, by, l.ID)
	unavailable("backup.create", err)
	_, err = s.RestoreReplace(ctx, by, b.ID, l.Slug, false)
	unavailable("restore.replace", err)
	if list, _ := s.Jobs.List(ctx, jobs.Filter{}); len(list) != 0 {
		t.Errorf("%d jobs queued without their entry", len(list))
	}
	pass, err := s.RevealPassword(ctx, by, l.ID, "")
	unavailable("library.credential_reveal", err)
	if pass.Reveal() != "" {
		t.Error("a password was returned without its entry")
	}
	unavailable("backup.delete", s.DeleteBackup(ctx, by, b.ID))
	if _, err := os.Stat(mustAbs(s, b)); err != nil {
		t.Errorf("the backup file was deleted without its entry: %v", err)
	}
	if got, _ := s.Backup(ctx, b.ID); got.Status != BackupOK {
		t.Errorf("the backup is %s", got.Status)
	}
}

// With a working log, the entry of a queued job is written with it, and a
// change outside SQLite writes its result after it.
func TestDestructiveActionsAuditEntries(t *testing.T) {
	ctx := context.Background()
	s, l, b := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	j, err := s.Trash(ctx, by, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBackup(ctx, by, b.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := s.Audit.List(ctx, 0, 10, "")
	var got []string
	for i := len(list) - 1; i >= 0; i-- {
		got = append(got, list[i].Action+":"+list[i].Outcome)
	}
	want := "library.delete:accepted backup.delete:accepted backup.delete:ok"
	if s := strings.Join(got, " "); s != want {
		t.Fatalf("entries %s, want %s", s, want)
	}
	if list[2].Details["job_id"] != j.ID || list[2].Details["slug"] != l.Slug {
		t.Fatalf("details %+v", list[2].Details)
	}
}

// PLAN P0.3: the settings, a pin, a library edit and a backup policy are
// stored with their audit entry or not at all (SQS-X040).
func TestCatalogChangesNeedTheirAuditEntry(t *testing.T) {
	ctx := context.Background()
	s, l, b := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	settings := s.Settings(ctx)
	policy, _ := s.Policy(ctx, l.ID)
	breakAudit(t, s)
	unavailable := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, audit.ErrUnavailable) || !strings.HasPrefix(err.Error(), "SQS-X040 ") {
			t.Errorf("%s: want SQS-X040, got %v", what, err)
		}
	}
	changed := settings
	changed.AdvertisedHost = "10.9.8.7"
	unavailable("settings.update", s.SaveSettings(ctx, by, changed))
	if got := s.Settings(ctx); got.AdvertisedHost != settings.AdvertisedHost {
		t.Errorf("the settings were saved: %q", got.AdvertisedHost)
	}
	unavailable("backup.pin", s.SetPinned(ctx, by, b.ID, true))
	if got, _ := s.Backup(ctx, b.ID); got.Pinned {
		t.Error("the backup was pinned")
	}
	unavailable("library.update", s.UpdateLibrary(ctx, by, l.ID, UpdateInput{DisplayName: "Renamed", ConnLimit: l.ConnLimit}))
	if got, _ := s.Library(ctx, l.ID); got.DisplayName != l.DisplayName {
		t.Errorf("the library was renamed to %q", got.DisplayName)
	}
	p := policy
	p.KeepLast = policy.KeepLast + 7
	unavailable("library.policy", s.SetPolicy(ctx, by, l.ID, p))
	if got, _ := s.Policy(ctx, l.ID); got.KeepLast != policy.KeepLast {
		t.Errorf("the policy was stored: keep %d", got.KeepLast)
	}
}

// With a working log each of those changes leaves its entry.
func TestCatalogChangesAreAudited(t *testing.T) {
	ctx := context.Background()
	s, l, b := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	st := s.Settings(ctx)
	st.AdvertisedHost = "10.9.8.7"
	if err := s.SaveSettings(ctx, by, st); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, by, b.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLibrary(ctx, by, l.ID, UpdateInput{DisplayName: "Renamed", ConnLimit: l.ConnLimit}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Policy(ctx, l.ID)
	p.KeepLast++
	if err := s.SetPolicy(ctx, by, l.ID, p); err != nil {
		t.Fatal(err)
	}
	list, _ := s.Audit.List(ctx, 0, 20, "")
	var got []string
	for i := len(list) - 1; i >= 0; i-- {
		got = append(got, list[i].Action+":"+list[i].Outcome)
	}
	want := "settings.update:ok backup.pin:ok library.update:accepted library.update:ok library.policy:accepted library.policy:ok"
	if s := strings.Join(got, " "); s != want {
		t.Fatalf("entries %s, want %s", s, want)
	}
}
