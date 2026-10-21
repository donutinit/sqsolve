// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// pgRestore loads a backup into an existing empty database. With role set,
// objects end up owned by that role.
func (s *Service) pgRestore(ctx context.Context, path, db, role string) error {
	args := append(s.PG.ConnArgs(db), "--no-owner", "--no-privileges", "--single-transaction", "--exit-on-error")
	if role != "" {
		args = append(args, "--role="+role)
	}
	args = append(args, path)
	_, err := s.Exec.Run(ctx, execx.Cmd{Name: "pg_restore", Args: args, Credential: s.PG.AdminCredential()})
	if err != nil {
		return fmt.Errorf("SQS-R010 pg_restore failed: %w", err)
	}
	return nil
}

// compareWithManifest checks counts and fingerprint of a restored database.
func (s *Service) compareWithManifest(ctx context.Context, db string, m *Manifest) (string, error) {
	conn, err := s.PG.ConnectDB(ctx, db)
	if err != nil {
		return "", err
	}
	defer conn.Close(context.Background())
	fp, err := pg.SchemaFingerprint(ctx, conn)
	if err != nil {
		return "", err
	}
	if m.Schema.Fingerprint != "" && fp.Hash != m.Schema.Fingerprint {
		return "", fmt.Errorf("SQS-R011 the restored structure does not match the manifest (%d tables, expected %d)", fp.Tables, m.Schema.Tables)
	}
	if len(m.RowCounts) == 0 {
		return "Structure OK; the backup has no row counts", nil
	}
	tables := make([]string, 0, len(m.RowCounts))
	for t := range m.RowCounts {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	got, err := pg.CountRows(ctx, conn, tables, true)
	if err != nil {
		return "", err
	}
	var diffs []string
	var total int64
	for _, t := range tables {
		total += got[t]
		if m.CountsExact && got[t] != m.RowCounts[t] {
			diffs = append(diffs, fmt.Sprintf("%s: %d instead of %d", t, got[t], m.RowCounts[t]))
		}
	}
	if len(diffs) > 0 {
		if len(diffs) > 5 {
			diffs = append(diffs[:5], "...")
		}
		return "", fmt.Errorf("SQS-R012 row counts do not match: %s", strings.Join(diffs, "; "))
	}
	return fmt.Sprintf("%d tables and %d rows match the manifest", len(tables), total), nil
}

// VerifyParams of backup.verify.
type VerifyParams struct {
	BackupID string `json:"backup_id,omitempty"`
	Target   string `json:"target,omitempty"`
}

func (s *Service) runVerify(ctx context.Context, r *jobs.Run) error {
	var p VerifyParams
	r.Params(&p)
	id := p.BackupID
	if id == "" {
		lib := r.Job.LibraryID
		if lib == "" {
			lib = p.Target
		}
		b, err := s.LatestVerified(ctx, lib)
		if err != nil {
			return err
		}
		if b == nil {
			r.Infof("The library has no backups to verify")
			return nil
		}
		id = b.ID
	}
	return s.verifyL2(ctx, r, id)
}

func (s *Service) verifyL2(ctx context.Context, r *jobs.Run, id string) error {
	b, err := s.Backup(ctx, id)
	if err != nil {
		return err
	}
	if b.Status != BackupOK {
		return fmt.Errorf("SQS-B021 the backup is not available (%s)", b.Status)
	}
	l, _ := s.Library(ctx, b.LibraryID)
	name := b.LibraryID
	if l != nil {
		name = l.DisplayName
	}
	// fail records a verdict on the content of the backup.
	fail := func(cause error) error {
		s.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE backups SET verify_status = 'failed', verified_at = ?, verify_detail = ? WHERE id = ?`, store.NowT(), cause.Error(), id)
		s.Events.Emit(ctx, events.Event{Type: events.BackupVerifyFailed, Severity: events.Error, LibraryID: b.LibraryID, JobID: r.Job.ID,
			Message: fmt.Sprintf("Verification failed for the backup of %q from %s: %s", name, b.StartedAt.In(s.Cfg.Location).Format("2006-01-02 15:04"), shortErr(cause))})
		return cause
	}
	// outcome turns an error into a verdict only when it is one and the job
	// is still running; anything else is a verification that could not run.
	outcome := func(err error, isVerdict bool) error {
		if isVerdict && ctx.Err() == nil {
			return fail(err)
		}
		return s.noteVerifyNotRun(ctx, b, r.Job.ID, err)
	}
	if err := r.Step("verify_l1", func() error { return s.checkL1(ctx, id, false) }); err != nil {
		var vf *verifyFailedError
		return outcome(err, errors.As(err, &vf))
	}
	r.Progress(15)
	m, err := ReadManifest(manifestPath(s.AbsPath(b)))
	if err != nil {
		// The level 1 check has just read it.
		return outcome(err, false)
	}
	db := pg.VerifyDB(ulid.Short(r.Job.ID))
	defer s.PG.DropDatabase(context.WithoutCancel(ctx), db)
	if err := r.Step("space", func() error { return s.checkRestoreSpace(b, "the test restore") }); err != nil {
		return outcome(err, false)
	}
	var detail string
	var loadErr error
	err = r.Step("test_restore", func() error {
		if err := s.PG.CreateDatabase(ctx, db, pg.AdminRole); err != nil {
			return err
		}
		s.PG.SetDatabaseConnLimit(ctx, db, 2)
		loadErr = s.pgRestore(ctx, s.AbsPath(b), db, "")
		return loadErr
	})
	if err != nil {
		return outcome(err, loadErr != nil && restoreVerdict(ctx, loadErr))
	}
	r.Progress(70)
	err = r.Step("compare", func() error {
		detail, err = s.compareWithManifest(ctx, db, m)
		return err
	})
	if err != nil {
		// Only a structure or a row count that differs is about the content;
		// a connection or a query that fails is not.
		msg := err.Error()
		return outcome(err, strings.HasPrefix(msg, "SQS-R011 ") || strings.HasPrefix(msg, "SQS-R012 "))
	}
	status := VerifyL2OK
	if len(m.RowCounts) == 0 {
		status = VerifyPartial
	}
	s.DB.ExecContext(ctx, `UPDATE backups SET verify_status = ?, verify_level = 2, verified_at = ?, verify_detail = ? WHERE id = ?`, status, store.NowT(), detail, id)
	r.Infof("%s", detail)
	s.Events.Emit(ctx, events.Event{Type: events.BackupVerified, LibraryID: b.LibraryID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Test restore OK for the backup of %q: %s", name, detail)})
	return nil
}

// RestoreParams of restore.new and restore.replace.
type RestoreParams struct {
	BackupID          string `json:"backup_id"`
	Slug              string `json:"slug,omitempty"`
	DisplayName       string `json:"display_name,omitempty"`
	TerminateSessions bool   `json:"terminate_sessions,omitempty"`
}

// RestoreAsNew enqueues a restore into a new library.
func (s *Service) RestoreAsNew(ctx context.Context, by audit.Actor, backupID, slug, displayName string) (*Library, *jobs.Job, error) {
	b, err := s.Backup(ctx, backupID)
	if err != nil {
		return nil, nil, err
	}
	if !b.Usable() {
		return nil, nil, errors.New("SQS-R001 the backup cannot be restored (it failed or was deleted)")
	}
	name, err := ValidateDisplayName(displayName)
	if err != nil {
		return nil, nil, err
	}
	if err := s.SlugAvailable(ctx, slug); err != nil {
		return nil, nil, err
	}
	id := ulid.New()
	now := store.NowT()
	src, _ := s.Library(ctx, b.LibraryID)
	rv := b.ResolveVersion
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, resolve_version, created_at, updated_at)
		VALUES (?, ?, ?, 'provisioning', ?, ?, 'restore', ?, ?, ?)`, id, slug, name, slug, slug, rv, now, now); err != nil {
		return nil, nil, err
	}
	if src != nil {
		if p, err := s.Policy(ctx, src.ID); err == nil {
			s.DB.ExecContext(ctx, `INSERT INTO backup_policies(library_id, enabled, schedule_cron, timezone, keep_last, keep_daily, keep_weekly, keep_monthly, min_verified, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, p.Enabled, p.Cron, p.Timezone, p.KeepLast, p.KeepDaily, p.KeepWeekly, p.KeepMonthly, p.MinVerified, now)
		}
	}
	s.Audit.Record(ctx, by, "restore.new", "library", id, audit.OK, map[string]any{"backup_id": backupID, "slug": slug})
	j, err := s.Jobs.Enqueue(ctx, JobRestoreNew, id, RestoreParams{BackupID: backupID, Slug: slug, DisplayName: name}, by, jobs.Priority(100))
	if err != nil {
		return nil, nil, err
	}
	l, err := s.Library(ctx, id)
	return l, j, err
}

func (s *Service) runRestoreNew(ctx context.Context, r *jobs.Run) error {
	var p RestoreParams
	r.Params(&p)
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	// The job may run long after it was queued, or again through Retry: the
	// row must still be the one this restore prepared, otherwise the slug may
	// belong to another library by now. Nothing is touched.
	if l.State != StateProvisioning {
		return fmt.Errorf("SQS-R050 the library is no longer waiting for this restore (state %s); nothing was changed", l.State.Label())
	}
	b, err := s.Backup(ctx, p.BackupID)
	if err != nil {
		return err
	}
	createdRole, createdDB := false, false
	// rollback drops only what this run created.
	rollback := func(cause error) error {
		c := context.WithoutCancel(ctx)
		if createdDB {
			s.PG.DropDatabase(c, l.DBName)
		}
		if createdRole {
			s.PG.DropRole(c, l.OwnerRole)
		}
		s.DB.ExecContext(c, `UPDATE library_credentials SET revoked_at = ? WHERE library_id = ?`, store.NowT(), l.ID)
		s.DB.ExecContext(c, `UPDATE libraries SET state = 'purged', purged_at = ?, notes = ? WHERE id = ?`, store.NowT(), "[restore failed] "+cause.Error(), l.ID)
		s.Sched.Delete(c, ScheduleBackup, l.ID)
		s.SyncHBA(c)
		s.Events.Emit(c, events.Event{Type: events.RestoreFailed, Severity: events.Error, LibraryID: l.ID, JobID: r.Job.ID,
			Message: fmt.Sprintf("Restore into the new library %q failed: %s", l.DisplayName, shortErr(cause))})
		return cause
	}
	if err := r.Step("space", func() error { return s.checkRestoreSpace(b, "the restore") }); err != nil {
		return rollback(err)
	}
	var m *Manifest
	if err := r.Step("verify_backup", func() error {
		if err := s.verifyL1(ctx, b.ID); err != nil {
			return err
		}
		m, err = ReadManifest(manifestPath(s.AbsPath(b)))
		return err
	}); err != nil {
		return rollback(err)
	}
	r.Progress(10)
	if err := r.Step("create_role", func() error {
		if err := s.SlugAvailableIgnoringCatalog(ctx, l.Slug); err != nil {
			return err
		}
		pass := secrets.GeneratePassword(24)
		v, err := pg.ScramVerifier(pass)
		if err != nil {
			return err
		}
		if err := s.PG.CreateLibraryRole(ctx, l.OwnerRole, pg.RoleOptions{Verifier: v, Login: true, ConnLimit: -1}); err != nil {
			return err
		}
		createdRole = true
		_, err = s.addCredential(ctx, l.ID, "owner", l.OwnerRole, "", pass)
		return err
	}); err != nil {
		return rollback(err)
	}
	if err := r.Step("restore", func() error {
		if err := s.createDatabase(ctx, l.DBName, l.OwnerRole); err != nil {
			return err
		}
		createdDB = true
		if err := s.PG.SetAllowConnections(ctx, l.DBName, true); err != nil {
			return err
		}
		return s.pgRestore(ctx, s.AbsPath(b), l.DBName, l.OwnerRole)
	}); err != nil {
		return rollback(err)
	}
	r.Progress(75)
	if err := r.Step("compare", func() error {
		detail, err := s.compareWithManifest(ctx, l.DBName, m)
		if err == nil {
			r.Infof("%s", detail)
		}
		return err
	}); err != nil {
		return rollback(err)
	}
	if err := r.Step("activate", func() error {
		if err := s.PG.SecureDatabase(ctx, l.DBName, []string{l.OwnerRole}); err != nil {
			return err
		}
		if _, err := s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'active', schema_fingerprint = ?, schema_table_count = ?, fingerprint_checked_at = ?, updated_at = ? WHERE id = ?`,
			m.Schema.Fingerprint, m.Schema.Tables, store.NowT(), store.NowT(), l.ID); err != nil {
			return err
		}
		if err := s.ensurePolicy(ctx, l.ID); err != nil {
			return err
		}
		return s.SyncHBA(ctx)
	}); err != nil {
		return rollback(err)
	}
	s.Events.Emit(ctx, events.Event{Type: events.RestoreSucceeded, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Restored the backup from %s into the new library %q", b.StartedAt.In(s.Cfg.Location).Format("2006-01-02 15:04"), l.DisplayName)})
	r.SetResult(map[string]string{"library_id": l.ID})
	s.Jobs.Enqueue(ctx, JobBackupCreate, l.ID, BackupParams{Kind: KindManual}, audit.System, jobs.Unique())
	return nil
}

// createDatabase creates a library database. One that already exists
// (42P04) belongs to someone else: the error says it was left untouched, and
// the caller, which never set its "created" flag, does not drop it.
func (s *Service) createDatabase(ctx context.Context, name, owner string) error {
	err := s.PG.CreateDatabase(ctx, name, owner)
	if pg.IsCode(err, "42P04") {
		return fmt.Errorf("SQS-R052 a database %q already exists on the server; it was left untouched", name)
	}
	return err
}

// SlugAvailableIgnoringCatalog checks only the server (the catalog row was
// created by the caller).
func (s *Service) SlugAvailableIgnoringCatalog(ctx context.Context, slug string) error {
	if taken, err := s.PG.DatabaseNameTaken(ctx, slug); err != nil {
		return err
	} else if taken {
		return fmt.Errorf("SQS-L005 a database %q already exists on the server", slug)
	}
	if ok, err := s.PG.RoleExists(ctx, slug); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("SQS-L006 a role %q already exists on the server", slug)
	}
	return nil
}

// RestoreReplace enqueues a restore that replaces the live library.
func (s *Service) RestoreReplace(ctx context.Context, by audit.Actor, backupID, confirm string, terminate bool) (*jobs.Job, error) {
	b, err := s.Backup(ctx, backupID)
	if err != nil {
		return nil, err
	}
	if !b.Usable() {
		return nil, errors.New("SQS-R001 the backup cannot be restored (it failed or was deleted)")
	}
	l, err := s.Library(ctx, b.LibraryID)
	if err != nil {
		return nil, err
	}
	if confirm != l.Slug {
		return nil, errors.New("SQS-R002 type the library identifier to confirm")
	}
	if l.State != StateActive && l.State != StateMaintenance {
		return nil, fmt.Errorf("SQS-R003 cannot replace a library in state %s", l.State.Label())
	}
	s.Audit.Record(ctx, by, "restore.replace", "library", l.ID, audit.OK, map[string]any{"backup_id": backupID, "terminate_sessions": terminate})
	return s.Jobs.Enqueue(ctx, JobRestoreReplace, l.ID, RestoreParams{BackupID: backupID, TerminateSessions: terminate}, by, jobs.Priority(100))
}

// swapJournal is stored before locking and before each rename (docs 11 §7.3).
type swapJournal struct {
	Live     string `json:"live"`
	Prev     string `json:"prev"`
	Restored string `json:"restored"`
	Phase    string `json:"phase"`
	State    string `json:"state_before"`
	// The previous state the catalog tracked before this swap; it is put back
	// if the swap does not happen.
	PrevBefore        string `json:"prev_before,omitempty"`
	PrevBackupBefore  string `json:"prev_backup_before,omitempty"`
	PrevExpiresBefore string `json:"prev_expires_before,omitempty"`
}

// names lists the databases the journal names.
func (j swapJournal) names() []string {
	var out []string
	for _, n := range []string{j.Live, j.Prev, j.Restored} {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// before is the state the library goes back to when the swap is undone or
// completed: maintenance stays maintenance, anything else is active.
func (j swapJournal) before() State {
	if State(j.State) == StateMaintenance {
		return StateMaintenance
	}
	return StateActive
}

func newSwapJournal(l *Library, prev, restored string) swapJournal {
	j := swapJournal{Live: l.DBName, Prev: prev, Restored: restored, State: string(l.State), PrevBefore: l.PrevDB, PrevBackupBefore: l.PrevBackupID}
	if l.PrevExpiresAt != nil {
		j.PrevExpiresBefore = store.T(*l.PrevExpiresAt)
	}
	return j
}

// recordPrev points the catalog at the database about to hold the previous
// state, before the rename that creates it: a swap that stops halfway never
// leaves the original untracked (docs 11 §7.2).
func (s *Service) recordPrev(ctx context.Context, id, prev, backupID string) error {
	exp := s.now().Add(time.Duration(s.Settings(ctx).PrevDays) * 24 * time.Hour)
	_, err := s.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = ?, prev_expires_at = ?, prev_backup_id = ?, updated_at = ? WHERE id = ?`,
		prev, store.T(exp), backupID, store.NowT(), id)
	return err
}

// unrecordPrev puts back the previous state the catalog tracked before a
// swap that did not happen, unless something else changed it since.
func (s *Service) unrecordPrev(ctx context.Context, id string, j swapJournal) {
	if j.Prev == "" {
		return // journaled before any previous state was named
	}
	var exp any
	if j.PrevExpiresBefore != "" {
		exp = j.PrevExpiresBefore
	}
	s.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = ?, prev_expires_at = ?, prev_backup_id = ? WHERE id = ? AND prev_db_name = ?`,
		j.PrevBefore, exp, j.PrevBackupBefore, id, j.Prev)
}

// swap renames j.Live to j.Prev and j.Restored to j.Live, journaling each
// rename and recording the previous state in the catalog before the first.
// If the second rename fails the original goes back under its name; stuck
// reports that this failed too and the original is left as j.Prev.
func (s *Service) swap(ctx context.Context, r *jobs.Run, l *Library, j *swapJournal, backupID string) (stuck bool, err error) {
	c := context.WithoutCancel(ctx)
	j.Phase = "rename_live"
	if err := r.Journal(*j); err != nil {
		return false, err
	}
	if err := s.recordPrev(ctx, l.ID, j.Prev, backupID); err != nil {
		return false, err
	}
	if err := s.renameDB(ctx, j.Live, j.Prev); err != nil {
		s.unrecordPrev(c, l.ID, *j)
		return false, err
	}
	j.Phase = "rename_restored"
	r.Journal(*j)
	if err := s.renameDB(ctx, j.Restored, j.Live); err != nil {
		if err2 := s.PG.RenameDatabase(c, j.Prev, j.Live); err2 != nil {
			return true, fmt.Errorf("%w; the original could not be put back: %v", err, err2)
		}
		s.unrecordPrev(c, l.ID, *j)
		return false, err
	}
	j.Phase = "swapped"
	return false, r.Journal(*j)
}

// swapStuck handles a swap whose second rename and put-back both failed: no
// database has the library's name and the original sits under prev, which
// the catalog already tracks. The library is reported missing, so nothing
// writes to it, and Revert puts the original back.
func (s *Service) swapStuck(ctx context.Context, r *jobs.Run, l *Library, prev string, cause error) error {
	c := context.WithoutCancel(ctx)
	s.setState(c, l.ID, StateMissing)
	s.SyncHBA(c)
	err := fmt.Errorf("SQS-R053 the swap stopped halfway and the original database could not be put back under its name; it is kept as %s and Revert puts it back: %w", prev, cause)
	s.Events.Emit(c, events.Event{Type: events.RestoreFailed, Severity: events.Critical, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("%q has no database under its name: %s", l.DisplayName, shortErr(err))})
	return err
}

// reopenFailed reports a swap that completed but whose reopening failed. The
// catalog already has the library in its usable state with the previous state
// recorded; the access is applied again by reconciliation at the next start
// and every time maintenance is turned on or off.
func (s *Service) reopenFailed(ctx context.Context, r *jobs.Run, l *Library, cause error) error {
	err := fmt.Errorf("SQS-R054 the databases were swapped but reopening the library failed: %w. The restored data is in place; reconciliation at the next start, or turning maintenance on and off, applies the access again", cause)
	s.Events.Emit(context.WithoutCancel(ctx), events.Event{Type: events.RestoreFailed, Severity: events.Critical, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("%q: %s", l.DisplayName, shortErr(err))})
	return err
}

func (s *Service) runRestoreReplace(ctx context.Context, r *jobs.Run) error {
	var p RestoreParams
	r.Params(&p)
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	// Re-validate what RestoreReplace checked when the job was queued.
	if l.State != StateActive && l.State != StateMaintenance {
		return fmt.Errorf("SQS-R003 cannot replace a library in state %s", l.State.Label())
	}
	b, err := s.Backup(ctx, p.BackupID)
	if err != nil {
		return err
	}
	stateBefore := l.State
	restoreDB := pg.RestoreDB(l.Slug, ulid.Short(r.Job.ID))
	blocked, createdRestore := false, false
	cleanup := func(cause error) error {
		c := context.WithoutCancel(ctx)
		if createdRestore {
			s.PG.DropDatabase(c, restoreDB)
		}
		if blocked {
			s.applyAccess(c, l, stateBefore)
		}
		s.setState(c, l.ID, stateBefore)
		s.Events.Emit(c, events.Event{Type: events.RestoreFailed, Severity: events.Error, LibraryID: l.ID, JobID: r.Job.ID,
			Message: fmt.Sprintf("%q was not replaced: %s. The active library was not modified", l.DisplayName, shortErr(cause))})
		return cause
	}
	if err := r.Step("space", func() error { return s.checkRestoreSpace(b, "the replacement") }); err != nil {
		return cleanup(err)
	}
	var m *Manifest
	if err := r.Step("check", func() error {
		if err := s.verifyL1(ctx, b.ID); err != nil {
			return err
		}
		m, err = ReadManifest(manifestPath(s.AbsPath(b)))
		if err != nil {
			return err
		}
		if l.Fingerprint != "" && m.Schema.Fingerprint != "" && l.Fingerprint != m.Schema.Fingerprint {
			r.Warnf("The backup has a different structure than the current library (another Resolve version)")
		}
		return nil
	}); err != nil {
		return cleanup(err)
	}
	s.Events.Emit(ctx, events.Event{Type: events.RestoreStarted, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Replacing %q with the backup from %s", l.DisplayName, b.StartedAt.In(s.Cfg.Location).Format("2006-01-02 15:04"))})
	var pre *Backup
	if err := r.Step("pre_restore_backup", func() error {
		var err error
		pre, err = s.createBackup(ctx, nil, l, KindPreRestore, "")
		return err
	}); err != nil {
		return cleanup(err)
	}
	r.Progress(30)
	if err := r.Step("restore", func() error {
		r.Journal(swapJournal{Live: l.DBName, Restored: restoreDB, Phase: "restoring", State: string(stateBefore)})
		if err := s.createDatabase(ctx, restoreDB, l.OwnerRole); err != nil {
			return err
		}
		createdRestore = true
		return s.pgRestore(ctx, s.AbsPath(b), restoreDB, l.OwnerRole)
	}); err != nil {
		return cleanup(err)
	}
	r.Progress(60)
	if err := r.Step("compare", func() error {
		detail, err := s.compareWithManifest(ctx, restoreDB, m)
		if err == nil {
			r.Infof("%s", detail)
		}
		return err
	}); err != nil {
		return cleanup(err)
	}
	if err := r.Step("wait_for_sessions", func() error {
		if p.TerminateSessions {
			return nil
		}
		deadline := s.now().Add(10 * time.Minute)
		for {
			ss, err := s.PG.Sessions(ctx, l.DBName)
			if err != nil {
				return err
			}
			if len(ss) == 0 {
				return nil
			}
			if s.now().After(deadline) {
				return fmt.Errorf("SQS-R020 %d sessions are still open on the library; ask users to close Resolve or choose to terminate sessions", len(ss))
			}
			r.Infof("Waiting for %d sessions to close", len(ss))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
	}); err != nil {
		return cleanup(err)
	}
	prev := pg.PrevDB(l.Slug, s.now())
	if l.PrevDB == prev {
		prev = pg.PrevDB(l.Slug, s.now().Add(time.Minute))
	}
	j := newSwapJournal(l, prev, restoreDB)
	if err := r.Step("lock", func() error {
		// Reconciliation needs the journal to undo the lock after a cut, so a
		// failed write stops here, while nothing has changed yet.
		j.Phase = "lock"
		if err := r.Journal(j); err != nil {
			return fmt.Errorf("SQS-R057 the job journal could not be written, so nothing was locked: %w", err)
		}
		s.setState(ctx, l.ID, StateRestoring)
		blocked = true
		if err := s.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
			return err
		}
		return s.closeSessions(ctx, r, l.DBName)
	}); err != nil {
		return cleanup(err)
	}
	// From here on the journal and the catalog drive recovery.
	var stuck bool
	err = r.Step("swap", func() error {
		var err error
		stuck, err = s.swap(ctx, r, l, &j, pre.ID)
		return err
	})
	if stuck {
		s.PG.DropDatabase(context.WithoutCancel(ctx), restoreDB)
		return s.swapStuck(ctx, r, l, prev, err)
	}
	if err != nil {
		return cleanup(err)
	}
	if err := r.Step("reopen", func() error {
		return s.reopenAfterSwap(ctx, l, prev, pre.ID, stateBefore, m.Schema.Fingerprint, m.Schema.Tables)
	}); err != nil {
		return s.reopenFailed(ctx, r, l, err)
	}
	// The superseded state is handled before the new one is backed up, so the
	// newest backup of the library stays the one of the newest state.
	s.dropSupersededPrev(ctx, r, l, prev)
	s.backupPrevState(ctx, r, l, prev)
	s.Events.Emit(ctx, events.Event{Type: events.RestoreSucceeded, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Replaced %q with the backup from %s. The previous state is kept for %d days so you can revert", l.DisplayName,
			b.StartedAt.In(s.Cfg.Location).Format("2006-01-02 15:04"), s.Settings(ctx).PrevDays)})
	return nil
}

// dropSupersededPrev removes the state kept by an earlier restore once a new
// restore keeps a newer one: the library only tracks one previous state, so
// the older database would otherwise stay forever and block a purge. It is
// dropped only with a verified backup of that database itself; otherwise it
// is kept and the orphan sweep tries again later.
func (s *Service) dropSupersededPrev(ctx context.Context, r *jobs.Run, before *Library, current string) {
	old := before.PrevDB
	if old == "" || old == current {
		return
	}
	if err := s.dropPrevDB(ctx, r, before, old, before.PrevBackupID); err != nil {
		s.prevKept(ctx, r, before, old, err)
		return
	}
	r.Infof("Deleted %s, the state kept by the previous restore", old)
}

// backupPrevState takes a verified backup of the previous state right after
// the swap, when nobody can write to it any more. The pre_restore backup was
// taken before the lock and misses what editors saved while the restore ran
// (docs 11 §7.2); this one becomes the backup that covers the previous state.
func (s *Service) backupPrevState(ctx context.Context, r *jobs.Run, l *Library, prev string) {
	b, err := s.backupClosedDB(ctx, l, prev)
	if err != nil {
		r.Warnf("Could not back up the previous state %s: %v. It is kept until a backup of it succeeds", prev, err)
		return
	}
	s.DB.ExecContext(ctx, `UPDATE libraries SET prev_backup_id = ? WHERE id = ? AND prev_db_name = ?`, b.ID, l.ID, prev)
	r.Infof("Backed up the previous state %s (%s)", prev, HumanBytes(b.SizeBytes))
}

// reopenAfterSwap gives the library its access back after a swap (ADR 0024:
// one in maintenance stays closed to its roles) and records the previous
// state; prev is empty when nothing was swapped out. Every part is applied
// even if one fails, and the catalog always leaves "restoring".
func (s *Service) reopenAfterSwap(ctx context.Context, l *Library, prev, preBackupID string, stateBefore State, fp string, tables int) error {
	st := StateActive
	if stateBefore == StateMaintenance {
		st = StateMaintenance
	}
	var errs []error
	if prev != "" {
		errs = append(errs, s.PG.SetAllowConnections(ctx, prev, false))
	}
	_, err := s.applyAccess(ctx, l, st)
	errs = append(errs, err)
	if prev != "" {
		exp := s.now().Add(time.Duration(s.Settings(ctx).PrevDays) * 24 * time.Hour)
		_, err = s.DB.ExecContext(ctx, `UPDATE libraries SET state = ?, prev_db_name = ?, prev_expires_at = ?, prev_backup_id = ?, schema_fingerprint = ?,
			schema_table_count = ?, fingerprint_checked_at = ?, updated_at = ? WHERE id = ?`,
			string(st), prev, store.T(exp), preBackupID, fp, tables, store.NowT(), store.NowT(), l.ID)
	} else {
		_, err = s.DB.ExecContext(ctx, `UPDATE libraries SET state = ?, prev_db_name = '', prev_expires_at = NULL, prev_backup_id = '', schema_fingerprint = ?,
			schema_table_count = ?, fingerprint_checked_at = ?, updated_at = ? WHERE id = ?`,
			string(st), fp, tables, store.NowT(), store.NowT(), l.ID)
	}
	errs = append(errs, err)
	return errors.Join(errs...)
}

// Revert swaps back to the state kept before the last restore. For a library
// with no database under its name it puts the previous state back.
func (s *Service) Revert(ctx context.Context, by audit.Actor, libraryID, confirm string) (*jobs.Job, error) {
	l, err := s.Library(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	if confirm != l.Slug {
		return nil, errors.New("SQS-R002 type the library identifier to confirm")
	}
	if l.PrevDB == "" {
		return nil, errors.New("SQS-R030 there is no previous state to recover")
	}
	if err := revertable(l); err != nil {
		return nil, err
	}
	s.Audit.Record(ctx, by, "restore.revert", "library", l.ID, audit.OK, map[string]any{"prev": l.PrevDB})
	return s.Jobs.Enqueue(ctx, JobRestoreRevert, l.ID, RestoreParams{TerminateSessions: true}, by, jobs.Priority(100), jobs.Unique())
}

func revertable(l *Library) error {
	switch l.State {
	case StateActive, StateMaintenance, StateMissing:
		return nil
	}
	return fmt.Errorf("SQS-R051 cannot revert a library in state %s", l.State.Label())
}

func (s *Service) runRevert(ctx context.Context, r *jobs.Run) error {
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	// Re-validate what Revert checked when the job was queued.
	if err := revertable(l); err != nil {
		return err
	}
	if l.PrevDB == "" {
		return errors.New("SQS-R030 there is no previous state to recover")
	}
	if db, err := s.PG.Database(ctx, l.PrevDB); err != nil {
		return fmt.Errorf("could not check the previous-state database %s: %w", l.PrevDB, err)
	} else if db == nil {
		return fmt.Errorf("SQS-R031 the previous-state database no longer exists (%s)", l.PrevDB)
	}
	if l.State == StateMissing {
		return s.revertMissing(ctx, r, l)
	}
	stateBefore := l.State
	var pre *Backup
	if err := r.Step("pre_restore_backup", func() error {
		var err error
		pre, err = s.createBackup(ctx, nil, l, KindPreRestore, "")
		return err
	}); err != nil {
		return err
	}
	oldPrev := l.PrevDB
	newPrev := pg.PrevDB(l.Slug, s.now())
	if newPrev == oldPrev {
		newPrev = pg.PrevDB(l.Slug, s.now().Add(time.Minute))
	}
	j := newSwapJournal(l, newPrev, oldPrev)
	// compensate undoes the lock on any failure before the swap.
	compensate := func(cause error) error {
		c := context.WithoutCancel(ctx)
		s.applyAccess(c, l, stateBefore)
		s.setState(c, l.ID, stateBefore)
		return cause
	}
	if err := r.Step("lock", func() error {
		// Journal before locking, so reconciliation can undo the lock.
		j.Phase = "lock"
		if err := r.Journal(j); err != nil {
			return fmt.Errorf("SQS-R057 the job journal could not be written, so nothing was locked: %w", err)
		}
		s.setState(ctx, l.ID, StateRestoring)
		if err := s.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
			return err
		}
		return s.closeSessions(ctx, r, l.DBName)
	}); err != nil {
		return compensate(err)
	}
	var stuck bool
	err = r.Step("swap", func() error {
		var err error
		stuck, err = s.swap(ctx, r, l, &j, pre.ID)
		return err
	})
	if stuck {
		return s.swapStuck(ctx, r, l, newPrev, err)
	}
	if err != nil {
		return compensate(err)
	}
	fp := s.fingerprintOf(ctx, l.DBName)
	if err := r.Step("reopen", func() error { return s.reopenAfterSwap(ctx, l, newPrev, pre.ID, stateBefore, fp.Hash, fp.Tables) }); err != nil {
		return s.reopenFailed(ctx, r, l, err)
	}
	s.backupPrevState(ctx, r, l, newPrev)
	s.Events.Emit(ctx, events.Event{Type: events.RestoreReverted, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Reverted %q to the state before the last restore", l.DisplayName)})
	return nil
}

// revertMissing puts the previous state back under the name of a library
// that has no database (a swap that stopped halfway, or a lost database):
// there is nothing to swap it with.
func (s *Service) revertMissing(ctx context.Context, r *jobs.Run, l *Library) error {
	if db, err := s.PG.Database(ctx, l.DBName); err != nil {
		return err
	} else if db != nil {
		return fmt.Errorf("SQS-R052 a database %q already exists on the server; it was left untouched", l.DBName)
	}
	prev := l.PrevDB
	if err := r.Step("rename", func() error { return s.renameDB(ctx, prev, l.DBName) }); err != nil {
		return err
	}
	fp := s.fingerprintOf(ctx, l.DBName)
	if err := r.Step("reopen", func() error {
		if err := s.reopenAfterSwap(ctx, l, "", "", StateActive, fp.Hash, fp.Tables); err != nil {
			return err
		}
		if err := s.ensurePolicy(ctx, l.ID); err != nil {
			return err
		}
		return s.SyncHBA(ctx)
	}); err != nil {
		return s.reopenFailed(ctx, r, l, err)
	}
	s.Events.Emit(ctx, events.Event{Type: events.RestoreReverted, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Put the previous state %s of %q back under its name", prev, l.DisplayName)})
	return nil
}

// fingerprintOf reads the schema fingerprint of db; empty if it cannot.
func (s *Service) fingerprintOf(ctx context.Context, db string) pg.Fingerprint {
	var fp pg.Fingerprint
	if conn, err := s.PG.ConnectDB(ctx, db); err == nil {
		fp, _ = pg.SchemaFingerprint(ctx, conn)
		conn.Close(context.Background())
	}
	return fp
}

// ImportParams of backup.import.
type ImportParams struct {
	Path         string `json:"path"` // relative to _import
	DeleteSource bool   `json:"delete_source"`
}

// ImportDir is where external backups are dropped.
func (s *Service) ImportDir() string { return filepath.Join(s.Cfg.BackupDir, "_import") }

// ImportCandidates lists files waiting in the import folder.
func (s *Service) ImportCandidates() ([]os.FileInfo, error) {
	entries, err := os.ReadDir(s.ImportDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []os.FileInfo
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") && !strings.HasSuffix(e.Name(), ".partial") {
			if fi, err := e.Info(); err == nil {
				out = append(out, fi)
			}
		}
	}
	return out, nil
}

// Import enqueues the import of a file in _import into a library's catalog.
func (s *Service) Import(ctx context.Context, by audit.Actor, libraryID, name string, deleteSource bool) (*jobs.Job, error) {
	if name == "" || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
		return nil, errors.New("SQS-B050 invalid file name")
	}
	if _, err := s.Library(ctx, libraryID); err != nil {
		return nil, err
	}
	s.Audit.Record(ctx, by, "backup.import", "library", libraryID, audit.OK, map[string]any{"file": name})
	return s.Jobs.Enqueue(ctx, JobBackupImport, libraryID, ImportParams{Path: name, DeleteSource: deleteSource}, by)
}

func (s *Service) runImport(ctx context.Context, r *jobs.Run) error {
	var p ImportParams
	r.Params(&p)
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	if err := s.BackupVolumeOK(); err != nil {
		return err
	}
	src := filepath.Join(s.ImportDir(), p.Path)
	if ok, err := fsx.Within(s.ImportDir(), src); err != nil || !ok {
		return errors.New("SQS-B051 the file is outside the import folder")
	}
	var info ArchiveInfo
	// orig is the file as it was when it stopped changing: what is imported.
	var orig os.FileInfo
	if err := r.Step("read", func() error {
		if orig, err = waitImportStable(ctx, src, p.Path); err != nil {
			return err
		}
		info, err = s.listArchive(ctx, src)
		return err
	}); err != nil {
		return err
	}
	linked := false
	id := ulid.New()
	now := s.now()
	rel := filepath.Join("libraries", l.Slug, now.UTC().Format("2006"), now.UTC().Format("01"), fmt.Sprintf("%s_%s_%s.backup", l.Slug, now.UTC().Format("20060102T150405Z"), id))
	abs := filepath.Join(s.Cfg.BackupDir, rel)
	partial := abs + ".partial"
	err = r.Step("copy", func() error {
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return err
		}
		// The source is never moved: until the backup is in the catalog the
		// file in _import may be the user's only copy, and recovery deletes
		// any .partial left in the library folders. When the source will be
		// deleted, a hard link (same volume) avoids copying the data; the
		// name in _import goes only once the catalog row exists.
		if p.DeleteSource {
			if err := os.Link(src, partial); err == nil {
				linked = true
				return sealImport(partial, abs)
			}
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		st, err := in.Stat()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, &progressReader{r: in, total: st.Size(), run: r}); err != nil {
			out.Close()
			os.Remove(partial)
			return err
		}
		if err := out.Close(); err != nil {
			os.Remove(partial)
			return err
		}
		return sealImport(partial, abs)
	})
	if err != nil {
		return err
	}
	// Until the catalog row exists, a failure removes the copy; the source
	// in _import is untouched.
	undo := func(err error) error {
		os.Remove(abs)
		os.Remove(manifestPath(abs))
		return err
	}
	// --list in the read step only saw the index; a file cut short (still
	// being copied when it was picked) fails here.
	if err := r.Step("full_read", func() error { return s.readArchiveFully(ctx, abs) }); err != nil {
		return undo(err)
	}
	sum, size, err := fileSHA256(abs)
	if err != nil {
		return undo(err)
	}
	// A link shares the source's data: a write into the source during the
	// import changed the file being catalogued, so it is not imported.
	if linked {
		if cur, err := os.Stat(abs); err != nil || !sameImportFile(cur, orig) {
			return undo(fmt.Errorf("SQS-B074 %s changed during the import; nothing was imported and the file is still in the import folder: import it again once it no longer changes", p.Path))
		}
	}
	m := Manifest{ManifestVersion: 1, BackupID: id, Kind: KindImported, File: filepath.Base(abs), Size: size, SHA256: sum,
		StartedAt: now.UTC(), FinishedAt: now.UTC(), SqsolveVersion: Version, InstanceID: s.InstanceID, ResolveVersion: l.ResolveVersion}
	m.Library.ID, m.Library.Slug, m.Library.DisplayName = l.ID, l.Slug, l.DisplayName
	m.Format.Tool, m.Format.ToolVersion, m.Format.Format, m.Format.Compression, m.Format.ArchiveVersion = "pg_dump", info.DumpedBy, "custom", info.Compression, info.ArchiveVersion
	m.Server.PostgresVersion = info.DumpedFrom
	mb, _ := jsonIndent(m)
	if err := fsx.WriteFileAtomic(manifestPath(abs), mb, 0o600); err != nil {
		return undo(err)
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO backups(id, library_id, job_id, kind, status, file_path, size_bytes, sha256, pg_server_version, pg_dump_version,
		archive_version, compression, counts_exact, resolve_version, source_db, started_at, finished_at, verify_status, verify_level, verified_at, verify_detail)
		VALUES (?, ?, ?, 'imported', 'ok', ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, 'l1_ok', 1, ?, ?)`,
		id, l.ID, r.Job.ID, rel, size, sum, info.DumpedFrom, info.DumpedBy, info.ArchiveVersion, info.Compression, l.ResolveVersion, p.Path,
		store.T(now), store.T(now), store.NowT(), "Imported: the whole archive was read in full; no row counts")
	if err != nil {
		return undo(err)
	}
	if p.DeleteSource {
		s.removeImportSource(ctx, r, l, src, p.Path, orig)
	}
	r.Infof("Imported %s (%s, %s)", p.Path, HumanBytes(size), info.DumpedBy)
	r.SetResult(map[string]string{"backup_id": id})
	return nil
}

// sameImportFile reports whether cur is still the file orig described: the
// same file (not one put under the same name) with the same size and
// modification time.
func sameImportFile(cur, orig os.FileInfo) bool {
	return os.SameFile(cur, orig) && cur.Size() == orig.Size() && cur.ModTime().Equal(orig.ModTime())
}

// removeImportSource deletes the imported file from _import, only when it is
// still the file that was imported. One the user replaced or wrote to while
// the import ran is kept, with a warning: it may be their only copy.
func (s *Service) removeImportSource(ctx context.Context, r *jobs.Run, l *Library, src, name string, orig os.FileInfo) {
	cur, err := os.Stat(src)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil || !sameImportFile(cur, orig) {
		msg := fmt.Sprintf("Kept %s in the import folder: it changed or was replaced while it was being imported, so it is not the file now in the catalog; delete it by hand if you no longer need it", name)
		r.Warnf("%s", msg)
		s.Events.Emit(context.WithoutCancel(ctx), events.Event{Type: EventImportSourceKept, Severity: events.Warning, LibraryID: l.ID, JobID: r.Job.ID, Message: msg})
		return
	}
	if err := os.Remove(src); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.Warnf("Could not delete %s from the import folder: %v", name, err)
	}
}

// sealImport seals the staged copy of an imported file into its place.
func sealImport(partial, abs string) error {
	if f := hookImportStaged.Load(); f != nil {
		if err := (*f)(partial); err != nil {
			return err
		}
	}
	if err := fsx.SealFile(partial, abs); err != nil {
		os.Remove(partial)
		return err
	}
	return nil
}

// waitImportStable refuses a file in _import that is still changing: its
// size and modification time must stay the same for importSettle. It
// returns the file as it settled.
func waitImportStable(ctx context.Context, path, name string) (os.FileInfo, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(importSettle.Load())):
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !sameImportFile(after, before) {
		return nil, fmt.Errorf("SQS-B071 %s is still being written (it went from %s to %s); import it when the copy has finished",
			name, HumanBytes(before.Size()), HumanBytes(after.Size()))
	}
	return after, nil
}

// Recreate rebuilds a library whose database is missing (for example after
// moving to a new server) from a backup, keeping its slug and, when the
// master key is the same, its passwords: workstations reconnect unchanged.
// The identifier is typed to confirm, and a second request while one is
// queued or running returns that job.
func (s *Service) Recreate(ctx context.Context, by audit.Actor, libraryID, backupID, confirm string) (*jobs.Job, error) {
	l, err := s.Library(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	if l.State != StateMissing {
		return nil, errors.New("SQS-R040 only libraries with no database on the server can be recreated")
	}
	if confirm != l.Slug {
		return nil, errors.New("SQS-R002 type the library identifier to confirm")
	}
	if backupID == "" {
		b, err := s.LatestVerified(ctx, l.ID)
		if err != nil {
			return nil, err
		}
		if b == nil {
			return nil, errors.New("SQS-R041 the library has no verified backups")
		}
		backupID = b.ID
	}
	s.Audit.Record(ctx, by, "restore.recreate", "library", l.ID, audit.OK, map[string]any{"backup_id": backupID})
	return s.Jobs.Enqueue(ctx, JobRestoreRecreate, l.ID, RestoreParams{BackupID: backupID}, by, jobs.Priority(100), jobs.Unique())
}

// dropCreated drops the database a failed recreate created. A drop that
// fails is added to the job error, after the cause (which keeps its code):
// the database stays on the server and must be checked before recreating
// again (docs 11 §7.7).
func (s *Service) dropCreated(ctx context.Context, name string, cause error) error {
	if err := s.PG.DropDatabase(context.WithoutCancel(ctx), name); err != nil {
		return fmt.Errorf("%w. The database %s this run created could not be dropped (%v): check it on the server and drop it before recreating again", cause, name, err)
	}
	return cause
}

// recreateJournal is stored by restore.recreate before it creates the
// library database ("creating") and once the comparison passed ("verified").
type recreateJournal struct {
	Created string `json:"created"`
	Phase   string `json:"phase"`
}

func (s *Service) runRecreate(ctx context.Context, r *jobs.Run) error {
	var p RestoreParams
	r.Params(&p)
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	// Re-validate what Recreate checked when the job was queued: a second
	// click, a Retry or a database that came back must find nothing to do.
	if l.State != StateMissing {
		return errors.New("SQS-R040 only libraries with no database on the server can be recreated")
	}
	if db, err := s.PG.Database(ctx, l.DBName); err != nil {
		return err
	} else if db != nil {
		return fmt.Errorf("SQS-R052 a database %q already exists on the server; it was left untouched", l.DBName)
	}
	b, err := s.Backup(ctx, p.BackupID)
	if err != nil {
		return err
	}
	if err := r.Step("space", func() error { return s.checkRestoreSpace(b, "the recreation") }); err != nil {
		return err
	}
	createdDB := false
	dropCreated := func(cause error) error {
		if createdDB {
			return s.dropCreated(ctx, l.DBName, cause)
		}
		return cause
	}
	var m *Manifest
	if err := r.Step("verify_backup", func() error {
		if err := s.verifyL1(ctx, b.ID); err != nil {
			return err
		}
		m, err = ReadManifest(manifestPath(s.AbsPath(b)))
		return err
	}); err != nil {
		return err
	}
	if err := r.Step("roles", func() error {
		creds, err := s.Credentials(ctx, l.ID)
		if err != nil {
			return err
		}
		for _, c := range creds {
			if c.RevokedAt != nil {
				continue
			}
			if ok, _ := s.PG.RoleExists(ctx, c.PGRole); ok {
				continue
			}
			pass, err := s.Box.Open(c.passwordEnc, credAAD(c.ID))
			kept := err == nil
			if !kept {
				pass = secrets.GeneratePassword(24)
				enc, err := s.Box.Seal(pass, credAAD(c.ID))
				if err != nil {
					return err
				}
				s.DB.ExecContext(ctx, `UPDATE library_credentials SET password_enc = ?, rotated_at = ? WHERE id = ?`, enc, store.NowT(), c.ID)
			}
			v, err := pg.ScramVerifier(pass)
			if err != nil {
				return err
			}
			// The connection limit lives on the roles (docs 07 §8).
			if err := s.PG.CreateLibraryRole(ctx, c.PGRole, pg.RoleOptions{Verifier: v, Login: true, ConnLimit: l.ConnLimit}); err != nil {
				return err
			}
			if c.Kind == "member" {
				s.PG.GrantRole(ctx, l.OwnerRole, c.PGRole)
				s.PG.SetRoleDefaultRole(ctx, c.PGRole, l.DBName, l.OwnerRole)
			}
			if kept {
				r.Infof("Role %s recreated with its original password", c.PGRole)
			} else {
				r.Warnf("Role %s recreated with a new password (the master key does not match)", c.PGRole)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := r.Step("restore", func() error {
		// Checked again right before creating it: the journal then names a
		// database this run is about to create, which reconciliation drops if
		// a cut leaves it empty (docs 11 §7.7).
		if db, err := s.PG.Database(ctx, l.DBName); err != nil {
			return err
		} else if db != nil {
			return fmt.Errorf("SQS-R052 a database %q already exists on the server; it was left untouched", l.DBName)
		}
		if err := r.Journal(recreateJournal{Created: l.DBName, Phase: "creating"}); err != nil {
			return fmt.Errorf("SQS-R058 the job journal could not be written, so the database was not created: %w", err)
		}
		if err := s.createDatabase(ctx, l.DBName, l.OwnerRole); err != nil {
			return err
		}
		createdDB = true
		return s.pgRestore(ctx, s.AbsPath(b), l.DBName, l.OwnerRole)
	}); err != nil {
		return dropCreated(err)
	}
	if err := r.Step("compare", func() error {
		detail, err := s.compareWithManifest(ctx, l.DBName, m)
		if err != nil {
			return err
		}
		r.Infof("%s", detail)
		if err := r.Journal(recreateJournal{Created: l.DBName, Phase: "verified"}); err != nil {
			r.Warnf("Could not record that %s was verified: %v", l.DBName, err)
		}
		return nil
	}); err != nil {
		return dropCreated(err)
	}
	if err := r.Step("activate", func() error {
		// Grants, LOGIN and the members' per-database role, which stayed with
		// the lost database.
		if _, err := s.applyAccess(ctx, l, StateActive); err != nil {
			return err
		}
		// A previous state still on the server stays tracked, so Revert can
		// bring it back; otherwise the reference is cleared. A check that
		// fails keeps it: only a database known to be gone is forgotten.
		gone := l.PrevDB == ""
		if !gone {
			db, err := s.PG.Database(ctx, l.PrevDB)
			if err != nil {
				r.Warnf("Could not check the previous state %s (%v); it stays tracked", l.PrevDB, err)
			}
			gone = err == nil && db == nil
		}
		if gone {
			s.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = '', prev_expires_at = NULL, prev_backup_id = '' WHERE id = ?`, l.ID)
		}
		s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'active', schema_fingerprint = ?, schema_table_count = ?, updated_at = ? WHERE id = ?`,
			m.Schema.Fingerprint, m.Schema.Tables, store.NowT(), l.ID)
		if err := s.ensurePolicy(ctx, l.ID); err != nil {
			return err
		}
		return s.SyncHBA(ctx)
	}); err != nil {
		return err
	}
	s.Events.Emit(ctx, events.Event{Type: events.RestoreSucceeded, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Recreated %q from the backup from %s", l.DisplayName, b.StartedAt.In(s.Cfg.Location).Format("2006-01-02 15:04"))})
	return nil
}

// progressReader reports copy progress to a job every few percent.
type progressReader struct {
	r     io.Reader
	total int64
	done  int64
	last  int
	run   *jobs.Run
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.total > 0 {
		if pct := int(p.done * 100 / p.total); pct >= p.last+2 {
			p.last = pct
			p.run.Progress(pct)
		}
	}
	return n, err
}
