// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
)

// auditedCatalog is catalogOnly with an audit log, a job queue (no workers),
// an active library with an owner credential, and one backup file that is
// not the newest verified one, on a backups volume with this installation's
// marker.
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
	s.InstanceID = "AUDIT"
	if err := fsx.WriteMarker(s.Cfg.BackupDir, fsx.Marker{InstanceID: "AUDIT", Volume: fsx.VolumeBackups, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
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

// TestRotatePasswordWithoutPostgreSQL: a rotation that cannot reach
// PostgreSQL fails with SQS-P001, which callers can match; the failure is in
// the audit log and the stored password stays.
func TestRotatePasswordWithoutPostgreSQL(t *testing.T) {
	ctx := context.Background()
	s, l, _ := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	err := s.RotatePassword(ctx, by, l.ID, "C1")
	if !errors.Is(err, pg.ErrUnavailable) || err.Error() != "SQS-P001 PostgreSQL is not available" {
		t.Fatalf("rotation without PostgreSQL: %v", err)
	}
	list, _ := s.Audit.List(ctx, 0, 10, "")
	var got []string
	for i := len(list) - 1; i >= 0; i-- {
		got = append(got, list[i].Action+":"+list[i].Outcome)
	}
	if trail := strings.Join(got, " "); trail != "library.credential_rotate:accepted library.credential_rotate:failed" {
		t.Fatalf("entries %s", trail)
	}
	// The newest entry comes first: list[0] is the failed result.
	if d := list[0].Details; d["error"] != "SQS-P001 PostgreSQL is not available" || d["role"] != "lib" {
		t.Fatalf("details %+v", d)
	}
	pass, err := s.RevealPassword(ctx, by, l.ID, "C1")
	if err != nil || pass.Reveal() != "the password" {
		t.Fatalf("the stored password changed: %v", err)
	}
}

// ADR 0028: a second recreate with the same backup gets the queued job; one
// with another backup is refused with SQS-J005 instead of silently getting
// the job of the first backup.
func TestRecreateWithAnotherBackupIsRefused(t *testing.T) {
	ctx := context.Background()
	s, l, _ := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'missing' WHERE id = ?`, l.ID)
	first, err := s.Recreate(ctx, by, l.ID, "B1", l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Recreate(ctx, by, l.ID, "B1", l.Slug)
	if err != nil || again.ID != first.ID {
		t.Fatalf("same backup: %v %v", again, err)
	}
	_, err = s.Recreate(ctx, by, l.ID, "B2", l.Slug)
	if !errors.Is(err, jobs.ErrConflict) || err.Error() != "SQS-J005 another job of this type is already queued or running for this library with different parameters; wait for it or cancel it (job "+first.ID+")" {
		t.Fatalf("another backup: %v", err)
	}
	if list, _ := s.Jobs.List(ctx, jobs.Filter{LibraryID: l.ID}); len(list) != 1 {
		t.Fatalf("%d jobs queued", len(list))
	}
}

// An expression typed for the default policy is checked whatever the mode:
// in activity mode "every night" was saved and silently had no effect.
func TestDefaultPolicyCronIsCheckedInActivityMode(t *testing.T) {
	ctx := context.Background()
	s, _, _ := auditedCatalog(t)
	st := s.Settings(ctx)
	st.DefaultPolicy.Mode, st.DefaultPolicy.CheckMinutes, st.DefaultPolicy.Cron = ModeActivity, 15, "every night"
	err := s.SaveSettings(ctx, audit.System, st)
	if !errors.Is(err, scheduler.ErrInvalidCron) || !strings.HasPrefix(err.Error(), `SQS-J020 invalid schedule expression: "every night"`) {
		t.Fatalf("got %v", err)
	}
	if got := s.Settings(ctx).DefaultPolicy.Cron; got == "every night" {
		t.Fatal("the expression was saved")
	}
}

// Review of 1.2.2: a default policy stored by 1.2.1 in activity mode may
// carry an expression that was never checked (it had no effect). A new
// library still gets its policy and its schedule; the expression only
// counts when someone types it again.
func TestNewLibraryPolicyIgnoresAnUnusedStoredExpression(t *testing.T) {
	ctx := context.Background()
	s, l, _ := auditedCatalog(t)
	st := s.Settings(ctx)
	st.DefaultPolicy.Mode, st.DefaultPolicy.CheckMinutes, st.DefaultPolicy.Cron = ModeActivity, 15, "0 3 * *"
	if err := s.DB.SetSetting(ctx, settingsKey, st, "1.2.1"); err != nil {
		t.Fatal(err)
	}
	s.DB.ExecContext(ctx, `DELETE FROM backup_policies WHERE library_id = ?`, l.ID)
	if err := s.ensurePolicy(ctx, l.ID); err != nil {
		t.Fatalf("ensurePolicy: %v", err)
	}
	p, err := s.Policy(ctx, l.ID)
	if err != nil || p.Mode != ModeActivity || p.Cron != ActivityCron(15) {
		t.Fatalf("policy %+v %v", p, err)
	}
	if sc, err := s.Sched.Get(ctx, ScheduleBackup, l.ID); err != nil || sc == nil || sc.Cron != ActivityCron(15) {
		t.Fatalf("schedule %+v %v", sc, err)
	}
}

// j005 is the refusal of a Unique request whose parameters differ from those
// of job id, which holds the type and the library (ADR 0028).
func j005(id string) string {
	return "SQS-J005 another job of this type is already queued or running for this library with different parameters; wait for it or cancel it (job " + id + ")"
}

// accepted returns the entries of action with outcome accepted.
func accepted(t *testing.T, s *Service, action string) []audit.Entry {
	t.Helper()
	list, err := s.Audit.List(context.Background(), 0, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.Entry
	for _, e := range list {
		if e.Action == action && e.Outcome == audit.Accepted {
			out = append(out, e)
		}
	}
	return out
}

// jobsOfType counts the jobs of a type queued for the library.
func jobsOfType(t *testing.T, s *Service, libraryID, typ string) []*jobs.Job {
	t.Helper()
	list, err := s.Jobs.List(context.Background(), jobs.Filter{LibraryID: libraryID, Type: typ})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// K-03, ADR 0029: a second replacement with the same backup and the same
// choice for the open sessions gets the queued job; another backup or the
// other choice is refused with SQS-J005. The state is checked first, and
// once the job ended the identity is free again.
func TestReplaceTwiceIsOneJobAndOtherParametersAreRefused(t *testing.T) {
	ctx := context.Background()
	s, l, _ := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	first, err := s.RestoreReplace(ctx, by, "B1", l.Slug, false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.RestoreReplace(ctx, by, "B1", l.Slug, false)
	if err != nil || again.ID != first.ID || again.State != jobs.Queued {
		t.Fatalf("same request: %+v %v", again, err)
	}
	for _, c := range []struct {
		backup    string
		terminate bool
	}{{"B2", false}, {"B1", true}} {
		_, err := s.RestoreReplace(ctx, by, c.backup, l.Slug, c.terminate)
		if !errors.Is(err, jobs.ErrConflict) || err.Error() != j005(first.ID) {
			t.Fatalf("backup %s, terminate %v: %v", c.backup, c.terminate, err)
		}
	}
	if list, _ := s.Jobs.List(ctx, jobs.Filter{LibraryID: l.ID}); len(list) != 1 {
		t.Fatalf("%d jobs queued", len(list))
	}
	all, err := s.Audit.List(ctx, 0, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	var entries []audit.Entry
	for _, e := range all {
		if e.Action == "restore.replace" {
			entries = append(entries, e)
		}
	}
	if len(entries) != 1 || entries[0].Outcome != audit.Accepted || entries[0].Details["job_id"] != first.ID || entries[0].Details["backup_id"] != "B1" {
		t.Fatalf("audit entries %+v", entries)
	}

	// The state first: a library being swapped refuses any replacement.
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring' WHERE id = ?`, l.ID)
	if _, err := s.RestoreReplace(ctx, by, "B1", l.Slug, false); err == nil || err.Error() != "SQS-R003 cannot replace a library in state Restoring" {
		t.Fatalf("restoring: %v", err)
	}
	if list, _ := s.Jobs.List(ctx, jobs.Filter{LibraryID: l.ID}); len(list) != 1 {
		t.Fatalf("%d jobs queued", len(list))
	}
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'active' WHERE id = ?`, l.ID)

	// Once the job ended, the identity is free.
	if err := s.Jobs.Cancel(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	next, err := s.RestoreReplace(ctx, by, "B2", l.Slug, false)
	if err != nil || next.ID == first.ID {
		t.Fatalf("after the cancel: %+v %v", next, err)
	}
	if list, _ := s.Jobs.List(ctx, jobs.Filter{LibraryID: l.ID}); len(list) != 2 {
		t.Fatalf("%d jobs", len(list))
	}
	if n := len(accepted(t, s, "restore.replace")); n != 2 {
		t.Fatalf("%d accepted entries", n)
	}
}

// K-03, ADR 0029: delete, recover and purge have no parameters, so a second
// request while the first is queued gets that job and writes no entry.
func TestDeleteRecoverAndPurgeTwiceAreOneJob(t *testing.T) {
	ctx := context.Background()
	s, l, _ := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	twice := func(typ, action string, queue func() (*jobs.Job, error)) {
		t.Helper()
		first, err := queue()
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		again, err := queue()
		if err != nil || again.ID != first.ID || again.State != jobs.Queued {
			t.Fatalf("%s again: %+v %v", action, again, err)
		}
		if n := len(jobsOfType(t, s, l.ID, typ)); n != 1 {
			t.Fatalf("%s: %d jobs", action, n)
		}
		all, err := s.Audit.List(ctx, 0, 200, "")
		if err != nil {
			t.Fatal(err)
		}
		var entries []audit.Entry
		for _, e := range all {
			if e.Action == action {
				entries = append(entries, e)
			}
		}
		if len(entries) != 1 || entries[0].Outcome != audit.Accepted || entries[0].Details["job_id"] != first.ID {
			t.Fatalf("%s entries %+v", action, entries)
		}
	}
	twice(JobLibraryDelete, "library.delete", func() (*jobs.Job, error) { return s.Trash(ctx, by, l.ID, l.Slug) })
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'trashed', trash_db_name = 'lib__trash_202611120000' WHERE id = ?`, l.ID)
	twice(JobLibraryUntrash, "library.untrash", func() (*jobs.Job, error) { return s.RestoreFromTrash(ctx, by, l.ID) })
	twice(JobLibraryPurge, "library.purge", func() (*jobs.Job, error) { return s.Purge(ctx, by, l.ID, l.Slug) })
}

// K-03, ADR 0029: a purge by hand and the purge of expired trash are one
// job, whichever was queued first; a request that joins the job writes no
// entry of its own. The automatic purge is queued with its entry (actor
// system), so the job a purge by hand joins has one.
func TestPurgeByHandSharesTheJobOfTheAutomaticPurge(t *testing.T) {
	ctx := context.Background()
	s, l, _ := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'trashed', trash_db_name = 'lib__trash_202611120000', purge_after = ? WHERE id = ?`,
		store.T(s.now().Add(-time.Hour)), l.ID)
	p, err := s.Purge(ctx, by, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeExpired(ctx, &jobs.Run{}); err != nil {
		t.Fatal(err)
	}
	if list := jobsOfType(t, s, l.ID, JobLibraryPurge); len(list) != 1 || list[0].ID != p.ID {
		t.Fatalf("after the pass: %+v", list)
	}
	if err := s.Jobs.Cancel(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeExpired(ctx, &jobs.Run{}); err != nil {
		t.Fatal(err)
	}
	var auto *jobs.Job
	for _, j := range jobsOfType(t, s, l.ID, JobLibraryPurge) {
		if j.State == jobs.Queued {
			if auto != nil {
				t.Fatalf("two queued purges: %s and %s", auto.ID, j.ID)
			}
			auto = j
		}
	}
	if auto == nil || auto.CreatedBy.Type != "system" {
		t.Fatalf("automatic purge %+v", auto)
	}
	byHand, err := s.Purge(ctx, by, l.ID, l.Slug)
	if err != nil || byHand.ID != auto.ID {
		t.Fatalf("purge by hand: %+v %v", byHand, err)
	}
	es := accepted(t, s, "library.purge")
	if len(es) != 2 {
		t.Fatalf("%d accepted library.purge entries", len(es))
	}
	if e := es[1]; e.Actor.Type != "user" || e.Details["job_id"] != p.ID {
		t.Fatalf("entry of the purge by hand %+v", e)
	}
	l, _ = s.Library(ctx, l.ID)
	if e := es[0]; e.Actor != audit.System || e.TargetType != "library" || e.TargetID != l.ID || e.Details["job_id"] != auto.ID ||
		e.Details["slug"] != l.Slug || e.Details["purge_after"] != store.T(*l.PurgeAfter) || len(e.Details) != 3 {
		t.Fatalf("entry of the automatic purge %+v", e)
	}
}

// F49: the settings.update entry carries every setting as saved, the
// second-factor requirement and the default policy included, so the chain
// of entries shows what each save changed. No setting is a secret.
func TestSettingsUpdateAuditCarriesEverySetting(t *testing.T) {
	ctx := context.Background()
	s, _, _ := auditedCatalog(t)
	st := s.Settings(ctx)
	st.InstanceName, st.AdvertisedHost = "Studio B", "nas.studio"
	st.InitWindowMinutes, st.TrashDays, st.PrevDays, st.StaleHours, st.ImportMaxMB = 45, 10, 3, 36, 2048
	st.TOTPRequiredAdmins, st.ExperimentalExports = true, true
	st.DefaultPolicy.Mode, st.DefaultPolicy.Cron, st.DefaultPolicy.KeepLast = ModeCron, "0 2 * * *", 4
	if err := s.SaveSettings(ctx, audit.System, st); err != nil {
		t.Fatal(err)
	}
	list, err := s.Audit.List(ctx, 0, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	for _, e := range list {
		if e.Action == "settings.update" {
			if d != nil {
				t.Fatal("two settings.update entries")
			}
			d = e.Details
		}
	}
	want := map[string]any{
		"instance_name": "Studio B", "advertised_host": "nas.studio", "init_window_minutes": 45.0, "trash_days": 10.0,
		"prev_days": 3.0, "stale_hours": 36.0, "import_max_mb": 2048.0, "maintenance_db_access": true, "require_tls": false,
		"totp_required_admins": true, "experimental_exports": true, "experimental_resolvedbkey": false,
	}
	for k, v := range want {
		if d[k] != v {
			t.Errorf("%s: %#v, want %#v", k, d[k], v)
		}
	}
	dp, _ := d["default_policy"].(map[string]any)
	if dp["mode"] != ModeCron || dp["cron"] != "0 2 * * *" || dp["keep_last"] != 4.0 || dp["keep_daily"] != 14.0 || dp["timezone"] != "UTC" {
		t.Errorf("default_policy: %#v", d["default_policy"])
	}
	if len(d) != len(want)+1 {
		t.Errorf("details %#v", d)
	}
}

// In activity mode the default policy is stored with its
// check cadence as the expression, as SetPolicy stores the policy of a
// library. An empty expression no longer puts the default policy back to its
// factory values, and a typed one is not kept without effect. In cron mode
// the expression is stored as typed, and an empty one is refused.
func TestSaveSettingsStoresTheCadenceOfAnActivityDefault(t *testing.T) {
	ctx := context.Background()
	s, _, _ := auditedCatalog(t)
	save := func(p Policy) error {
		st := s.Settings(ctx)
		st.DefaultPolicy = p
		return s.SaveSettings(ctx, audit.System, st)
	}
	stored := func() Policy {
		t.Helper()
		var raw Settings
		if ok, err := s.DB.GetSetting(ctx, settingsKey, &raw); err != nil || !ok {
			t.Fatalf("stored settings: %v %v", ok, err)
		}
		return raw.DefaultPolicy
	}
	activity := Policy{Enabled: true, Mode: ModeActivity, CheckMinutes: 30, Timezone: "UTC",
		KeepLast: 4, KeepDaily: 5, KeepWeekly: 6, KeepMonthly: 7, MinVerified: 2}
	want := activity
	want.Cron = ActivityCron(30)
	for _, typed := range []string{"", "0 3 * * *"} {
		p := activity
		p.Cron = typed
		if err := save(p); err != nil {
			t.Fatalf("activity with %q: %v", typed, err)
		}
		if got := s.Settings(ctx).DefaultPolicy; got != want {
			t.Fatalf("activity with %q read back as %+v", typed, got)
		}
		if got := stored(); got != want {
			t.Fatalf("activity with %q stored as %+v", typed, got)
		}
	}

	cron := activity
	cron.Mode, cron.Cron = ModeCron, "30 1 * * *"
	if err := save(cron); err != nil {
		t.Fatalf("cron: %v", err)
	}
	if got := s.Settings(ctx).DefaultPolicy; got != cron {
		t.Fatalf("cron read back as %+v", got)
	}
	if got := stored(); got != cron {
		t.Fatalf("cron stored as %+v", got)
	}

	empty := cron
	empty.Cron = ""
	err := save(empty)
	if !errors.Is(err, scheduler.ErrInvalidCron) || err.Error() != `SQS-J020 invalid schedule expression: "" (use 5 fields: minute hour day month day-of-week)` {
		t.Fatalf("cron without an expression: %v", err)
	}
	if got := s.Settings(ctx).DefaultPolicy; got != cron {
		t.Fatalf("a refused save changed the default policy: %+v", got)
	}
}

// A default policy stored by an earlier version is completed when read: the
// interval is never 0, and in activity mode the expression is the check
// cadence, so an unused expression does not reach the Schedule tab or the
// API of a library without a policy of its own.
func TestStoredDefaultPolicyIsCompletedWhenRead(t *testing.T) {
	ctx := context.Background()
	s, l, _ := auditedCatalog(t)
	store := func(p Policy) {
		t.Helper()
		st := s.Settings(ctx)
		st.DefaultPolicy = p
		if err := s.DB.SetSetting(ctx, settingsKey, st, "1.2.3"); err != nil {
			t.Fatal(err)
		}
	}

	store(Policy{Enabled: true, Mode: ModeActivity, CheckMinutes: 30, Cron: "0 3 * * *", Timezone: "UTC",
		KeepLast: 4, KeepDaily: 5, KeepWeekly: 6, KeepMonthly: 7, MinVerified: 1})
	dp := s.Settings(ctx).DefaultPolicy
	if dp.Mode != ModeActivity || dp.CheckMinutes != 30 || dp.Cron != ActivityCron(30) ||
		dp.KeepLast != 4 || dp.KeepDaily != 5 || dp.KeepWeekly != 6 || dp.KeepMonthly != 7 {
		t.Fatalf("activity with an unused expression read as %+v", dp)
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM backup_policies WHERE library_id = ?`, l.ID); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Policy(ctx, l.ID); err != nil || p.Cron != ActivityCron(30) {
		t.Fatalf("policy of a library without its own: %+v %v", p, err)
	}

	store(Policy{Enabled: true, Mode: ModeCron, Cron: "30 1 * * *", Timezone: "UTC", KeepLast: 4, MinVerified: 1})
	if dp := s.Settings(ctx).DefaultPolicy; dp.Mode != ModeCron || dp.Cron != "30 1 * * *" || dp.CheckMinutes != 15 {
		t.Fatalf("cron without an interval read as %+v", dp)
	}

	store(Policy{Enabled: true, Mode: ModeActivity, Cron: ActivityCron(15), Timezone: "UTC", KeepLast: 4, MinVerified: 1})
	if dp := s.Settings(ctx).DefaultPolicy; dp.Mode != ModeActivity || dp.CheckMinutes != 15 || dp.Cron != ActivityCron(15) {
		t.Fatalf("activity without an interval read as %+v", dp)
	}

	// The record 1.2.3 left when the "Schedule (cron)" field was saved empty.
	// It has been read as the factory policy since then, and still is: the
	// retention numbers stored with it were never in effect nor shown. This
	// case passes before and after the change; it pins that reading.
	store(Policy{Enabled: true, Mode: ModeActivity, CheckMinutes: 30, Timezone: "UTC", KeepLast: 1, MinVerified: 1})
	if dp, def := s.Settings(ctx).DefaultPolicy, DefaultSettings("UTC").DefaultPolicy; dp != def {
		t.Fatalf("policy stored without an expression read as %+v, want the factory %+v", dp, def)
	}
}

// A library without a policy of its own takes the default one when it gets
// its policy (ensurePolicy), with its mode, its expression and its
// retention; a library that has one keeps it when the default changes.
func TestNewLibraryTakesTheDefaultPolicy(t *testing.T) {
	ctx := context.Background()
	s, l, _ := auditedCatalog(t)
	setDefault := func(p Policy) {
		t.Helper()
		st := s.Settings(ctx)
		st.DefaultPolicy = p
		if err := s.SaveSettings(ctx, audit.System, st); err != nil {
			t.Fatal(err)
		}
	}
	ensure := func(fresh bool) {
		t.Helper()
		if fresh {
			if _, err := s.DB.ExecContext(ctx, `DELETE FROM backup_policies WHERE library_id = ?`, l.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.ensurePolicy(ctx, l.ID); err != nil {
			t.Fatalf("ensurePolicy: %v", err)
		}
	}
	// Read from the table: Policy falls back to the default policy.
	row := func() (p Policy) {
		t.Helper()
		if err := s.DB.QueryRowContext(ctx, `SELECT mode, check_minutes, schedule_cron, keep_last, keep_daily, keep_weekly, keep_monthly
			FROM backup_policies WHERE library_id = ?`, l.ID).Scan(&p.Mode, &p.CheckMinutes, &p.Cron, &p.KeepLast, &p.KeepDaily, &p.KeepWeekly, &p.KeepMonthly); err != nil {
			t.Fatal(err)
		}
		return p
	}
	schedule := func(cron string) {
		t.Helper()
		if sc, err := s.Sched.Get(ctx, ScheduleBackup, l.ID); err != nil || sc == nil || sc.Cron != cron || !sc.Enabled {
			t.Fatalf("schedule %+v %v, want %q enabled", sc, err, cron)
		}
	}

	setDefault(Policy{Enabled: true, Mode: ModeCron, Cron: "30 1 * * *", Timezone: "UTC",
		KeepLast: 4, KeepDaily: 5, KeepWeekly: 6, KeepMonthly: 7, MinVerified: 1})
	ensure(true)
	if p := row(); p.Mode != ModeCron || p.Cron != "30 1 * * *" || p.KeepLast != 4 || p.KeepDaily != 5 || p.KeepWeekly != 6 || p.KeepMonthly != 7 {
		t.Fatalf("policy from a cron default %+v", p)
	}
	schedule("30 1 * * *")

	setDefault(Policy{Enabled: true, Mode: ModeActivity, CheckMinutes: 30, Timezone: "UTC",
		KeepLast: 9, KeepDaily: 9, KeepWeekly: 9, KeepMonthly: 9, MinVerified: 1})
	ensure(false)
	if p := row(); p.Mode != ModeCron || p.Cron != "30 1 * * *" || p.KeepLast != 4 || p.KeepDaily != 5 || p.KeepWeekly != 6 || p.KeepMonthly != 7 {
		t.Fatalf("a library with its own policy took the new default: %+v", p)
	}
	schedule("30 1 * * *")

	ensure(true)
	if p := row(); p.Mode != ModeActivity || p.CheckMinutes != 30 || p.Cron != ActivityCron(30) ||
		p.KeepLast != 9 || p.KeepDaily != 9 || p.KeepWeekly != 9 || p.KeepMonthly != 9 {
		t.Fatalf("policy from an activity default %+v", p)
	}
	schedule(ActivityCron(30))
}
