// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/drp"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// Export of .drp and .drx from a backup (docs 22 §7): a job restores the
// archive into a temporary database and, while that workspace lives, the
// export session reads it exactly like a live library. The library itself is
// never touched, so a single project or grade can be recovered from any
// backup without a restore.

// ExportWorkspaceTTL is how long a prepared workspace stays, counted from
// its last use.
const ExportWorkspaceTTL = 2 * time.Hour

// ErrExportNotReady is returned while no workspace is ready for the backup.
var ErrExportNotReady = errors.New("SQS-E007 the backup is not prepared for export; prepare it first")

// Workspace states.
const (
	WorkspacePreparing = "preparing"
	WorkspaceReady     = "ready"
	WorkspaceFailed    = "failed"
	WorkspaceClosed    = "closed"
)

// ExportWorkspace is the temporary database of a backup export.
type ExportWorkspace struct {
	ID        string    `json:"id"`
	BackupID  string    `json:"backup_id"`
	DBName    string    `json:"-"`
	JobID     string    `json:"job_id"`
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Live reports whether the workspace is being prepared or ready, and not expired.
func (w *ExportWorkspace) Live(now time.Time) bool {
	return w != nil && (w.State == WorkspaceReady || w.State == WorkspacePreparing) && now.Before(w.ExpiresAt)
}

// ExportWorkspaceParams of backup.export.
type ExportWorkspaceParams struct {
	WorkspaceID string `json:"workspace_id"`
}

const workspaceCols = `id, backup_id, db_name, job_id, state, error, created_at, expires_at`

func scanWorkspace(row interface{ Scan(...any) error }) (*ExportWorkspace, error) {
	var w ExportWorkspace
	var created, expires string
	if err := row.Scan(&w.ID, &w.BackupID, &w.DBName, &w.JobID, &w.State, &w.Error, &created, &expires); err != nil {
		return nil, err
	}
	w.CreatedAt, w.ExpiresAt = store.ParseT(created), store.ParseT(expires)
	return &w, nil
}

// BackupExportWorkspace returns the latest workspace of a backup, or nil.
func (s *Service) BackupExportWorkspace(ctx context.Context, backupID string) (*ExportWorkspace, error) {
	w, err := scanWorkspace(s.DB.QueryRowContext(ctx, `SELECT `+workspaceCols+` FROM export_workspaces WHERE backup_id = ? ORDER BY created_at DESC LIMIT 1`, backupID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return w, err
}

func (s *Service) workspace(ctx context.Context, id string) (*ExportWorkspace, error) {
	w, err := scanWorkspace(s.DB.QueryRowContext(ctx, `SELECT `+workspaceCols+` FROM export_workspaces WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("SQS-E007 export space %s does not exist", id)
	}
	return w, err
}

// PrepareBackupExport enqueues the restore of a backup into a workspace. A
// workspace that is already live is returned as is.
func (s *Service) PrepareBackupExport(ctx context.Context, by audit.Actor, backupID string) (*ExportWorkspace, *jobs.Job, error) {
	if !s.Settings(ctx).ExperimentalExports {
		return nil, nil, ErrExportsDisabled
	}
	b, err := s.Backup(ctx, backupID)
	if err != nil {
		return nil, nil, err
	}
	if !b.Usable() {
		return nil, nil, errors.New("SQS-E005 the backup is not available (it failed or was deleted)")
	}
	if w, _ := s.BackupExportWorkspace(ctx, backupID); w.Live(s.now()) {
		j, _ := s.Jobs.Get(ctx, w.JobID)
		return w, j, nil
	}
	w := &ExportWorkspace{ID: ulid.New(), BackupID: b.ID, State: WorkspacePreparing, CreatedAt: s.now(), ExpiresAt: s.now().Add(ExportWorkspaceTTL)}
	w.DBName = pg.ExportDB(ulid.Short(w.ID))
	// The row exists before the job: a dispatcher that starts the job at
	// once must find its workspace.
	_, err = s.DB.ExecContext(ctx, `INSERT INTO export_workspaces(`+workspaceCols+`) VALUES (?, ?, ?, '', ?, '', ?, ?)`,
		w.ID, w.BackupID, w.DBName, w.State, store.T(w.CreatedAt), store.T(w.ExpiresAt))
	if err != nil {
		return nil, nil, err
	}
	j, err := s.Jobs.Enqueue(ctx, JobBackupExport, b.LibraryID, ExportWorkspaceParams{WorkspaceID: w.ID}, by, jobs.Priority(50),
		jobs.Audited("backup.export_prepare", "backup", b.ID, map[string]any{"workspace": w.ID}))
	if err != nil {
		s.DB.ExecContext(context.WithoutCancel(ctx), `DELETE FROM export_workspaces WHERE id = ?`, w.ID)
		return nil, nil, err
	}
	if f := hookExportEnqueued.Load(); f != nil {
		(*f)(j.ID)
	}
	w.JobID = j.ID
	if _, err := s.DB.ExecContext(ctx, `UPDATE export_workspaces SET job_id = ? WHERE id = ?`, w.JobID, w.ID); err != nil {
		return nil, nil, err
	}
	return w, j, nil
}

func (s *Service) runBackupExport(ctx context.Context, r *jobs.Run) error {
	var p ExportWorkspaceParams
	r.Params(&p)
	w, err := s.workspace(ctx, p.WorkspaceID)
	if err != nil {
		return err
	}
	b, err := s.Backup(ctx, w.BackupID)
	if err != nil {
		return err
	}
	fail := func(cause error) error {
		s.PG.DropDatabase(context.WithoutCancel(ctx), w.DBName)
		s.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE export_workspaces SET state = 'failed', error = ? WHERE id = ?`, cause.Error(), w.ID)
		return cause
	}
	if b.Status != BackupOK {
		return fail(fmt.Errorf("SQS-B021 the backup is not available (%s)", b.Status))
	}
	if err := r.Step("space", func() error { return s.checkRestoreSpace(b, "the export workspace") }); err != nil {
		return fail(err)
	}
	if err := r.Step("verify_l1", func() error { return s.verifyL1(ctx, b.ID) }); err != nil {
		return fail(err)
	}
	r.Progress(15)
	err = r.Step("temporary_restore", func() error {
		if err := s.PG.CreateDatabase(ctx, w.DBName, pg.AdminRole); err != nil {
			return err
		}
		s.PG.SetDatabaseConnLimit(ctx, w.DBName, 4)
		return s.pgRestore(ctx, s.AbsPath(b), w.DBName, "")
	})
	if err != nil {
		return fail(err)
	}
	expires := s.now().Add(ExportWorkspaceTTL)
	s.DB.ExecContext(ctx, `UPDATE export_workspaces SET state = 'ready', expires_at = ? WHERE id = ?`, store.T(expires), w.ID)
	r.Infof("Backup ready to export until %s", expires.In(s.Cfg.Location).Format("15:04"))
	return nil
}

// OpenBackupExport opens an export session on the ready workspace of a
// backup and extends its life. The caller must Close the session.
func (s *Service) OpenBackupExport(ctx context.Context, backupID string) (*drp.Session, *ExportWorkspace, error) {
	if !s.Settings(ctx).ExperimentalExports {
		return nil, nil, ErrExportsDisabled
	}
	w, err := s.BackupExportWorkspace(ctx, backupID)
	if err != nil {
		return nil, nil, err
	}
	if w == nil || w.State != WorkspaceReady || !s.now().Before(w.ExpiresAt) {
		return nil, w, ErrExportNotReady
	}
	conn, err := s.PG.ConnectDB(ctx, w.DBName)
	if err != nil {
		return nil, w, fmt.Errorf("%w: %v", ErrExportUnavailable, err)
	}
	s.DB.ExecContext(ctx, `UPDATE export_workspaces SET expires_at = ? WHERE id = ?`, store.T(s.now().Add(ExportWorkspaceTTL)), w.ID)
	sess, err := drp.Open(ctx, conn)
	return sess, w, err
}

// CloseBackupExport drops the workspace of a backup, if any.
func (s *Service) CloseBackupExport(ctx context.Context, by audit.Actor, backupID string) error {
	w, err := s.BackupExportWorkspace(ctx, backupID)
	if err != nil || w == nil || w.State == WorkspaceClosed {
		return err
	}
	return s.Audit.Do(ctx, by, "backup.export_close", "backup", backupID, map[string]any{"workspace": w.ID}, func() error {
		return s.closeWorkspace(ctx, w)
	})
}

func (s *Service) closeWorkspace(ctx context.Context, w *ExportWorkspace) error {
	if err := s.PG.DropDatabase(ctx, w.DBName); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE export_workspaces SET state = 'closed' WHERE id = ?`, w.ID)
	return err
}

// PurgeExportWorkspaces drops the workspaces whose time ran out and returns
// how many it closed.
func (s *Service) PurgeExportWorkspaces(ctx context.Context) int {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+workspaceCols+` FROM export_workspaces WHERE state IN ('preparing', 'ready') AND expires_at < ?`, store.T(s.now()))
	if err != nil {
		return 0
	}
	var due []*ExportWorkspace
	for rows.Next() {
		if w, err := scanWorkspace(rows); err == nil {
			due = append(due, w)
		}
	}
	rows.Close()
	n := 0
	for _, w := range due {
		if s.closeWorkspace(ctx, w) == nil {
			n++
		}
	}
	return n
}

// AuditBackupExport records a download from a backup in the audit log. The
// download is not served when it returns an error (audit.ErrUnavailable).
func (s *Service) AuditBackupExport(ctx context.Context, by audit.Actor, b *Backup, sess *drp.Session, kind, id string, info *drp.ExportInfo) error {
	return s.Audit.Require(ctx, by, "backup.export", "backup", b.ID, audit.OK, map[string]any{
		"library_id": b.LibraryID, "kind": kind, "id": id, "name": info.Name, "files": info.Files, "bytes": info.Bytes,
		"fingerprint": sess.Fingerprint, "verified": sess.KnownOK, "notes": info.Notes,
	})
}
