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
	"github.com/donutinit/sqsolve/internal/pg"
)

// A pg_hba.conf that cannot be written stops the sync with its code, before
// PostgreSQL is asked to validate or reload anything (this service has no
// PostgreSQL client at all).
func TestSyncHBAReportsAFileItCannotWrite(t *testing.T) {
	e := newBkEnv(t)
	e.library("hba_lib", StateActive)
	// A file sits where the configuration folder goes.
	if err := os.WriteFile(e.svc.Cfg.PGConfDir(), []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := e.svc.SyncHBA(e.ctx)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-P041 could not write pg_hba.conf: ") {
		t.Fatalf("sync with a configuration folder that cannot be written: %v", err)
	}
}

// Stored settings that cannot be read stop the rewrite of pg_hba.conf before
// anything is written (SQS-P044): the factory defaults that Settings answers
// then would turn "TLS only" off and open the maintenance database. HBAProblem
// reports the error.
func TestSyncHBAWritesNothingWithoutTheStoredSettings(t *testing.T) {
	ctx := context.Background()
	s, hbaPath := hbaAuditCatalog(t)
	st := s.Settings(ctx)
	st.RequireTLS, st.MaintenanceDBAccess = true, false
	if err := s.DB.SetSetting(ctx, settingsKey, st, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE settings SET value = '{' WHERE key = ?`, settingsKey); err != nil {
		t.Fatal(err)
	}
	err := s.SyncHBA(ctx)
	if want := "SQS-P044 the stored settings could not be read, so pg_hba.conf was not rewritten: unexpected end of JSON input"; err == nil || err.Error() != want {
		t.Fatalf("sync with unreadable settings: %v, want %q", err, want)
	}
	if _, serr := os.Stat(hbaPath); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("pg_hba.conf written without the stored settings: %v", serr)
	}
	if p := s.HBAProblem(); p == nil || p.Error() != err.Error() {
		t.Fatalf("problem after the refused rewrite: %v", p)
	}
}

// hbaAuditCatalog is auditedCatalog with the configuration folder in place,
// so that pg_hba.conf can be written.
func hbaAuditCatalog(t *testing.T) (*Service, string) {
	t.Helper()
	s, _, _ := auditedCatalog(t)
	if err := os.MkdirAll(s.Cfg.PGConfDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return s, filepath.Join(s.Cfg.PGConfDir(), "pg_hba.conf")
}

// auditTrail returns every audit entry, oldest first, and their actions and
// outcomes as "action:outcome" separated by spaces.
func auditTrail(t *testing.T, s *Service) ([]audit.Entry, string) {
	t.Helper()
	list, err := s.Audit.List(context.Background(), 0, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	var es []audit.Entry
	var got []string
	for i := len(list) - 1; i >= 0; i-- {
		es = append(es, list[i])
		got = append(got, list[i].Action+":"+list[i].Outcome)
	}
	return es, strings.Join(got, " ")
}

// The rewrite of pg_hba.conf that follows a change of
// "TLS only" or of the access to the maintenance database has an audit entry
// of its own, postgres.hba, with whoever saved the settings: accepted before
// the file is written, then its result, failed here with the error of the
// rewrite. A save that changes neither setting writes no such entry and
// leaves the file alone, and a failed rewrite does not undo the settings.
func TestSettingsChangeAuditsItsHBARewrite(t *testing.T) {
	ctx := context.Background()
	s, hbaPath := hbaAuditCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}

	st := s.Settings(ctx)
	st.AdvertisedHost = "10.9.8.7"
	if err := s.SaveSettings(ctx, by, st); err != nil {
		t.Fatal(err)
	}
	if _, got := auditTrail(t, s); strings.Contains(got, "postgres.hba") {
		t.Fatalf("a save that changes no access setting: %s", got)
	}
	if _, err := os.Stat(hbaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pg_hba.conf written by a save that changes no access setting: %v", err)
	}

	st.RequireTLS = true
	err := s.SaveSettings(ctx, by, st)
	if !errors.Is(err, pg.ErrUnavailable) || err.Error() != "SQS-P001 PostgreSQL is not available" {
		t.Fatalf("rewrite without PostgreSQL: %v", err)
	}
	es, got := auditTrail(t, s)
	if want := "settings.update:ok settings.update:ok postgres.hba:accepted postgres.hba:failed"; got != want {
		t.Fatalf("entries %s, want %s", got, want)
	}
	for _, e := range es[2:] {
		if e.Actor != by || e.TargetType != "settings" || e.TargetID != "runtime" || e.Details["cause"] != "settings.update" ||
			e.Details["require_tls"] != true || e.Details["maintenance_db_access"] != true {
			t.Errorf("postgres.hba %s: %+v", e.Outcome, e)
		}
	}
	if _, ok := es[2].Details["error"]; ok {
		t.Errorf("accepted entry with an error: %+v", es[2].Details)
	}
	if es[3].Details["error"] != err.Error() {
		t.Errorf("failed entry with error %v, want %q", es[3].Details["error"], err.Error())
	}
	b, rerr := os.ReadFile(hbaPath)
	if rerr != nil || !strings.Contains(string(b), "hostssl\t\"lib\"\t\"lib\"\tall\tscram-sha-256") {
		t.Fatalf("pg_hba.conf without TLS only: %v\n%s", rerr, b)
	}
	if !s.Settings(ctx).RequireTLS {
		t.Error("the settings were undone by the failed rewrite")
	}
}

// A rewrite of pg_hba.conf that failed is retried by saving the same
// settings again, which answers the error of the new attempt instead of
// "saved"; while the last rewrite has not taken effect any save retries it,
// and HBAProblem reports its error.
func TestSavingTheSameSettingsAgainRetriesTheHBARewrite(t *testing.T) {
	ctx := context.Background()
	s, hbaPath := hbaAuditCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	// A folder where the new file is written first: the rewrite fails
	// before pg_hba.conf exists (SQS-P041).
	if err := os.Mkdir(hbaPath+".partial", 0o700); err != nil {
		t.Fatal(err)
	}
	if p := s.HBAProblem(); p != nil {
		t.Fatalf("problem before any rewrite: %v", p)
	}
	st := s.Settings(ctx)
	st.RequireTLS = true
	err := s.SaveSettings(ctx, by, st)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-P041 could not write pg_hba.conf: ") {
		t.Fatalf("rewrite that cannot write the file: %v", err)
	}
	if p := s.HBAProblem(); p == nil || p.Error() != err.Error() {
		t.Errorf("problem after the failed rewrite: %v", p)
	}
	if _, err := os.Stat(hbaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pg_hba.conf after a failed write: %v", err)
	}

	// The same settings again: the file is written and the reload fails
	// (there is no PostgreSQL here), which is what the save answers.
	if err := os.Remove(hbaPath + ".partial"); err != nil {
		t.Fatal(err)
	}
	err = s.SaveSettings(ctx, by, st)
	if !errors.Is(err, pg.ErrUnavailable) || err.Error() != "SQS-P001 PostgreSQL is not available" {
		t.Fatalf("same settings saved again: %v", err)
	}
	if b, rerr := os.ReadFile(hbaPath); rerr != nil || !strings.Contains(string(b), "hostssl\t\"lib\"\t\"lib\"\tall\tscram-sha-256") {
		t.Fatalf("pg_hba.conf without TLS only: %v\n%s", rerr, b)
	}
	if p := s.HBAProblem(); p == nil || p.Error() != err.Error() {
		t.Errorf("problem after the second attempt: %v", p)
	}

	// Any save retries while the problem lasts, and stores its settings.
	st.AdvertisedHost = "10.9.8.7"
	if err := s.SaveSettings(ctx, by, st); !errors.Is(err, pg.ErrUnavailable) {
		t.Fatalf("save of another setting while the rewrite is pending: %v", err)
	}
	if got := s.Settings(ctx).AdvertisedHost; got != "10.9.8.7" {
		t.Errorf("advertised host %q not stored", got)
	}
	attempt := " settings.update:ok postgres.hba:accepted postgres.hba:failed"
	if _, got := auditTrail(t, s); got != strings.TrimSpace(strings.Repeat(attempt, 3)) {
		t.Fatalf("entries %s", got)
	}
}

// ADR 0026: without its accepted entry the rewrite does not run (SQS-X040),
// so pg_hba.conf is not written; the settings stay stored with their own
// entry, HBAProblem reports the refusal, and saving the same settings again
// rewrites the file once the audit log takes the entry.
func TestSettingsHBARewriteNeedsItsAuditEntry(t *testing.T) {
	ctx := context.Background()
	s, hbaPath := hbaAuditCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER hba_audit_down BEFORE INSERT ON audit_log WHEN NEW.action = 'postgres.hba'
		BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	st := s.Settings(ctx)
	st.RequireTLS = true
	err := s.SaveSettings(ctx, by, st)
	if !errors.Is(err, audit.ErrUnavailable) || !strings.HasPrefix(err.Error(), "SQS-X040 the audit log could not be written, so the action was not done: ") {
		t.Fatalf("rewrite without its entry: %v", err)
	}
	if _, err := os.Stat(hbaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pg_hba.conf written without its entry: %v", err)
	}
	if _, got := auditTrail(t, s); got != "settings.update:ok" {
		t.Fatalf("entries %s", got)
	}
	if !s.Settings(ctx).RequireTLS {
		t.Error("the settings were not stored")
	}
	if p := s.HBAProblem(); !errors.Is(p, audit.ErrUnavailable) || p.Error() != err.Error() {
		t.Errorf("problem after the refused rewrite: %v", p)
	}

	if _, err := s.DB.ExecContext(ctx, `DROP TRIGGER hba_audit_down`); err != nil {
		t.Fatal(err)
	}
	err = s.SaveSettings(ctx, by, st)
	if !errors.Is(err, pg.ErrUnavailable) || err.Error() != "SQS-P001 PostgreSQL is not available" {
		t.Fatalf("same settings saved again: %v", err)
	}
	if b, rerr := os.ReadFile(hbaPath); rerr != nil || !strings.Contains(string(b), "hostssl\t\"lib\"\t\"lib\"\tall\tscram-sha-256") {
		t.Fatalf("pg_hba.conf without TLS only: %v\n%s", rerr, b)
	}
	if _, got := auditTrail(t, s); got != "settings.update:ok settings.update:ok postgres.hba:accepted postgres.hba:failed" {
		t.Fatalf("entries %s", got)
	}
}
