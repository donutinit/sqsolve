// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/store"
)

func jsonIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return append(b, '\n'), err
}

func removeIfExists(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Trash enqueues moving a library to the trash.
func (s *Service) Trash(ctx context.Context, by audit.Actor, id, confirm string) (*jobs.Job, error) {
	l, err := s.Library(ctx, id)
	if err != nil {
		return nil, err
	}
	if confirm != l.Slug {
		return nil, errors.New("SQS-L040 type the library identifier to confirm")
	}
	switch l.State {
	case StateActive, StateMaintenance, StateAwaitingInit, StateInitExpired:
	case StateMissing:
		// After recovering an installation, a library nobody wants back has no
		// database to back up: it goes to the trash as a catalog entry only.
	default:
		return nil, fmt.Errorf("SQS-L041 cannot delete a library in state %s", l.State.Label())
	}
	s.Audit.Record(ctx, by, "library.delete", "library", id, audit.OK, map[string]any{"slug": l.Slug})
	return s.Jobs.Enqueue(ctx, JobLibraryDelete, id, nil, by, jobs.Priority(90))
}

func (s *Service) runTrash(ctx context.Context, r *jobs.Run) error {
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	hasDB := l.Live()
	if hasDB {
		if err := r.Step("final_backup", func() error {
			b, err := s.createBackup(ctx, nil, l, KindPreDelete, "")
			if err != nil {
				return err
			}
			r.Infof("Final backup %s verified", HumanBytes(b.SizeBytes))
			return nil
		}); err != nil {
			return err
		}
	}
	r.Progress(50)
	if err := r.Step("lock", func() error {
		roles, err := s.activeRoles(ctx, l.ID)
		if err != nil {
			return err
		}
		for _, role := range roles {
			if ok, _ := s.PG.RoleExists(ctx, role); !ok {
				continue // gone with the server (state "missing")
			}
			if err := s.PG.SetRoleLogin(ctx, role, false); err != nil {
				return err
			}
			s.PG.SetRoleCreateDB(ctx, role, false)
		}
		if hasDB {
			if err := s.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
				return err
			}
			if _, err := s.PG.TerminateAll(ctx, l.DBName); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	trash := ""
	if hasDB {
		trash = pg.TrashDB(l.Slug, s.now())
		if err := r.Step("move", func() error {
			r.Journal(map[string]string{"from": l.DBName, "to": trash})
			return s.PG.RenameDatabase(ctx, l.DBName, trash)
		}); err != nil {
			s.PG.SetAllowConnections(context.WithoutCancel(ctx), l.DBName, true)
			return err
		}
	} else if db, _ := s.PG.Database(ctx, l.DBName); db != nil {
		// A database created during an expired window: keep it in the trash too.
		trash = pg.TrashDB(l.Slug, s.now())
		s.PG.SetAllowConnections(ctx, l.DBName, false)
		s.PG.TerminateAll(ctx, l.DBName)
		if err := s.PG.RenameDatabase(ctx, l.DBName, trash); err != nil {
			return err
		}
	}
	days := s.Settings(ctx).TrashDays
	purge := s.now().Add(time.Duration(days) * 24 * time.Hour)
	if _, err := s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'trashed', trash_db_name = ?, trashed_at = ?, purge_after = ?, updated_at = ? WHERE id = ?`,
		trash, store.NowT(), store.T(purge), store.NowT(), l.ID); err != nil {
		return err
	}
	s.Sched.SetEnabled(ctx, l.ID, false)
	if err := s.SyncHBA(ctx); err != nil {
		return err
	}
	s.Events.Emit(ctx, events.Event{Type: events.LibraryTrashed, Severity: events.Warning, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("%q was moved to the trash; it will be permanently deleted on %s", l.DisplayName, purge.In(s.Cfg.Location).Format("2006-01-02"))})
	return nil
}

// RestoreFromTrash brings a trashed library back.
func (s *Service) RestoreFromTrash(ctx context.Context, by audit.Actor, id string) (*jobs.Job, error) {
	l, err := s.Library(ctx, id)
	if err != nil {
		return nil, err
	}
	if l.State != StateTrashed {
		return nil, errors.New("SQS-L042 the library is not in the trash")
	}
	if other, err := s.LibraryBySlug(ctx, l.Slug); err == nil && other.ID != l.ID {
		return nil, fmt.Errorf("SQS-L043 another library with identifier %q already exists", l.Slug)
	}
	s.Audit.Record(ctx, by, "library.untrash", "library", id, audit.OK, nil)
	return s.Jobs.Enqueue(ctx, JobLibraryUntrash, id, nil, by, jobs.Priority(90))
}

func (s *Service) runUntrash(ctx context.Context, r *jobs.Run) error {
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	if l.TrashDB != "" {
		if err := r.Step("move", func() error {
			if db, _ := s.PG.Database(ctx, l.DBName); db != nil {
				return fmt.Errorf("SQS-L005 a database %q already exists", l.DBName)
			}
			if err := s.PG.RenameDatabase(ctx, l.TrashDB, l.DBName); err != nil {
				return err
			}
			return s.PG.SetAllowConnections(ctx, l.DBName, true)
		}); err != nil {
			return err
		}
	}
	state := StateActive
	if l.TrashDB == "" {
		state = StateInitExpired
	}
	if err := r.Step("reactivate", func() error {
		roles, err := s.activeRoles(ctx, l.ID)
		if err != nil {
			return err
		}
		for _, role := range roles {
			if err := s.PG.SetRoleLogin(ctx, role, true); err != nil {
				return err
			}
		}
		_, err = s.DB.ExecContext(ctx, `UPDATE libraries SET state = ?, trash_db_name = '', trashed_at = NULL, purge_after = NULL, updated_at = ? WHERE id = ?`,
			string(state), store.NowT(), l.ID)
		if err != nil {
			return err
		}
		if state == StateActive {
			if err := s.ensurePolicy(ctx, l.ID); err != nil {
				return err
			}
		}
		return s.SyncHBA(ctx)
	}); err != nil {
		return err
	}
	s.Events.Emit(ctx, events.Event{Type: events.LibraryRestoredFromTrash, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("%q was restored from the trash", l.DisplayName)})
	return nil
}

// Purge deletes a trashed library for good (its backups stay).
func (s *Service) Purge(ctx context.Context, by audit.Actor, id, confirm string) (*jobs.Job, error) {
	l, err := s.Library(ctx, id)
	if err != nil {
		return nil, err
	}
	if l.State != StateTrashed {
		return nil, errors.New("SQS-L044 only libraries in the trash can be purged")
	}
	if confirm != l.Slug {
		return nil, errors.New("SQS-L040 type the library identifier to confirm")
	}
	s.Audit.Record(ctx, by, "library.purge", "library", id, audit.OK, nil)
	return s.Jobs.Enqueue(ctx, JobLibraryPurge, id, nil, by)
}

func (s *Service) runPurge(ctx context.Context, r *jobs.Run) error {
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	if l.State != StateTrashed {
		return errors.New("SQS-L044 only libraries in the trash can be purged")
	}
	if l.TrashDB != "" {
		if err := r.Step("check_backup", func() error {
			bs, err := s.Backups(ctx, BackupFilter{LibraryID: l.ID, Statuses: []string{BackupOK}})
			if err != nil {
				return err
			}
			for _, b := range bs {
				if b.Kind == KindPreDelete && b.Verified() {
					return nil
				}
			}
			return errors.New("SQS-L045 there is no verified final backup; not purging")
		}); err != nil {
			return err
		}
		if err := r.Step("drop_database", func() error { return s.PG.DropDatabase(ctx, l.TrashDB) }); err != nil {
			return err
		}
	}
	if l.PrevDB != "" {
		// The state kept before the last restore is covered by its pre_restore backup.
		if err := r.Step("drop_previous_state", func() error { return s.PG.DropDatabase(ctx, l.PrevDB) }); err != nil {
			return err
		}
		s.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = '', prev_expires_at = NULL WHERE id = ?`, l.ID)
	}
	// Any other database owned by the library roles (for example created during
	// an init window) is reported and kept: dropping it is a manual decision.
	if owned, err := s.PG.DatabasesOwnedBy(ctx, l.OwnerRole); err == nil && len(owned) > 0 {
		return fmt.Errorf("SQS-L046 role %s still owns: %s; review them before purging", l.OwnerRole, strings.Join(owned, ", "))
	}
	if err := r.Step("drop_roles", func() error {
		creds, err := s.Credentials(ctx, l.ID)
		if err != nil {
			return err
		}
		for i := len(creds) - 1; i >= 0; i-- {
			if ok, _ := s.PG.RoleExists(ctx, creds[i].PGRole); !ok {
				continue
			}
			if err := s.PG.DropRole(ctx, creds[i].PGRole); err != nil {
				return err
			}
		}
		s.DB.ExecContext(ctx, `UPDATE library_credentials SET revoked_at = COALESCE(revoked_at, ?), password_enc = '' WHERE library_id = ?`, store.NowT(), l.ID)
		return nil
	}); err != nil {
		return err
	}
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'purged', purged_at = ?, trash_db_name = '', updated_at = ? WHERE id = ?`, store.NowT(), store.NowT(), l.ID)
	s.Sched.Delete(ctx, ScheduleBackup, l.ID)
	s.Sched.Delete(ctx, ScheduleVerify, l.ID)
	s.Events.Emit(ctx, events.Event{Type: events.LibraryPurged, Severity: events.Warning, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("%q was permanently deleted from the server; its backups are kept according to retention", l.DisplayName)})
	return nil
}

// PurgeExpired purges trash and previous states whose time is up.
func (s *Service) PurgeExpired(ctx context.Context, r *jobs.Run) error {
	libs, err := s.Libraries(ctx, ListOptions{})
	if err != nil {
		return err
	}
	now := s.now()
	for _, l := range libs {
		if l.State == StateTrashed && l.PurgeAfter != nil && now.After(*l.PurgeAfter) {
			if _, err := s.Jobs.Enqueue(ctx, JobLibraryPurge, l.ID, nil, audit.System, jobs.Unique()); err != nil {
				r.Warnf("Could not queue the purge of %s: %v", l.Slug, err)
			}
		}
		if l.PrevDB != "" && l.PrevExpiresAt != nil && now.After(*l.PrevExpiresAt) {
			ok := false
			if b, err := s.Backup(ctx, l.PrevBackupID); err == nil && b.Usable() && b.Verified() {
				ok = true
			}
			if !ok {
				r.Warnf("Keeping %s: there is no verified backup of the previous state", l.PrevDB)
				continue
			}
			if err := s.PG.DropDatabase(ctx, l.PrevDB); err != nil {
				r.Warnf("Could not delete %s: %v", l.PrevDB, err)
				continue
			}
			s.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = '', prev_expires_at = NULL, prev_backup_id = '' WHERE id = ?`, l.ID)
			r.Infof("Deleted previous state %s", l.PrevDB)
		}
		s.sweepOrphanPrev(ctx, r, l, now)
	}
	return nil
}

// sweepOrphanPrev drops previous-state databases that no library row points
// to anymore (left by older versions or by an interrupted job) once they are
// older than the retention for previous states.
func (s *Service) sweepOrphanPrev(ctx context.Context, r *jobs.Run, l *Library, now time.Time) {
	if !l.Live() {
		return
	}
	owned, err := s.PG.DatabasesOwnedBy(ctx, l.OwnerRole)
	if err != nil {
		return
	}
	keep := time.Duration(s.Settings(ctx).PrevDays) * 24 * time.Hour
	prefix := l.Slug + "__prev_"
	for _, name := range owned {
		if !strings.HasPrefix(name, prefix) || name == l.PrevDB {
			continue
		}
		t, err := time.Parse("200601021504", strings.TrimPrefix(name, prefix))
		if err != nil || now.Sub(t) < keep {
			continue
		}
		if err := s.PG.DropDatabase(ctx, name); err != nil {
			r.Warnf("Could not delete %s: %v", name, err)
			continue
		}
		r.Infof("Deleted %s, a previous state nobody was using anymore", name)
	}
}
