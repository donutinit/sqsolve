// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
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

// Trash enqueues moving a library to the trash. A second request while one
// is queued or running returns that job (ADR 0029).
func (s *Service) Trash(ctx context.Context, by audit.Actor, id, confirm string) (*jobs.Job, error) {
	l, err := s.Library(ctx, id)
	if err != nil {
		return nil, err
	}
	if confirm != l.Slug {
		return nil, errors.New("SQS-L040 type the library identifier to confirm")
	}
	if err := trashable(l); err != nil {
		return nil, err
	}
	return s.Jobs.Enqueue(ctx, JobLibraryDelete, id, nil, by, jobs.Priority(90), jobs.Unique(),
		jobs.Audited("library.delete", "library", id, map[string]any{"slug": l.Slug}))
}

func trashable(l *Library) error {
	switch l.State {
	case StateActive, StateMaintenance, StateAwaitingInit, StateInitExpired:
	case StateMissing:
		// After recovering an installation, a library nobody wants back has no
		// database to back up: it goes to the trash as a catalog entry only.
	default:
		return fmt.Errorf("SQS-L041 cannot delete a library in state %s", l.State.Label())
	}
	return nil
}

func (s *Service) runTrash(ctx context.Context, r *jobs.Run) error {
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	// Re-validate what Trash checked when the job was queued: a retried
	// delete of a library already in the trash, or purged and whose slug
	// another library uses now, must not touch anything.
	if err := trashable(l); err != nil {
		return err
	}
	if l.State == StateMissing {
		// Its own database came back (volume remounted): it is not deleted
		// without the final backup. A database of another owner with that name
		// is not the library's and is left alone.
		if db, err := s.PG.Database(ctx, l.DBName); err != nil {
			return err
		} else if db != nil && db.Owner == l.OwnerRole {
			return fmt.Errorf("SQS-L050 the database %q exists again on the server; restart sqsolve so the library becomes active, then delete it", l.DBName)
		}
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
	// A database created during an initialization window goes to the trash
	// too; for a missing library the name is not its database.
	windowDB := false
	if l.State == StateAwaitingInit || l.State == StateInitExpired {
		db, err := s.PG.Database(ctx, l.DBName)
		if ferr := fault("trash.window_lookup"); ferr != nil {
			db, err = nil, ferr
		}
		if err != nil {
			return err
		}
		windowDB = db != nil
	}
	// Until the rename (the point of no return) any failure gives the library
	// back the access its state promises: LOGIN for active credentials and an
	// open database (ADR 0024).
	undo := func(cause error) error {
		c := context.WithoutCancel(ctx)
		_, err := s.applyAccess(c, l, l.State)
		if windowDB {
			err = errors.Join(err, s.PG.SetAllowConnections(c, l.DBName, true))
		}
		if err != nil {
			r.Warnf("Could not fully undo the lock: %v", err)
		}
		return cause
	}
	if err := r.Step("lock", func() error {
		// Journal before locking, so reconciliation undoes the lock after a cut.
		if err := r.Journal(map[string]string{"phase": "lock", "from": l.DBName}); err != nil {
			return fmt.Errorf("SQS-L053 the job journal could not be written, so nothing was locked: %w", err)
		}
		roles, err := s.activeRoles(ctx, l.ID)
		if err != nil {
			return err
		}
		for _, role := range roles {
			ok, err := s.PG.RoleExists(ctx, role)
			if ferr := fault("trash.role_lookup"); ferr != nil {
				ok, err = false, ferr
			}
			if err != nil {
				return fmt.Errorf("could not check the role %s: %w", role, err)
			}
			if !ok {
				continue // gone with the server (state "missing")
			}
			if err := s.PG.SetRoleLogin(ctx, role, false); err != nil {
				return err
			}
			err = s.PG.SetRoleCreateDB(ctx, role, false)
			if ferr := fault("trash.createdb"); ferr != nil {
				err = ferr
			}
			if err != nil {
				return err
			}
		}
		if hasDB || windowDB {
			if err := s.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
				return err
			}
			return s.closeSessions(ctx, r, l.DBName)
		}
		return nil
	}); err != nil {
		return undo(err)
	}
	trash := ""
	if hasDB || windowDB {
		trash = pg.TrashDB(l.Slug, s.now())
		if err := r.Step("move", func() error {
			// Without this journal a cut after the rename could not be completed.
			if err := r.Journal(map[string]string{"from": l.DBName, "to": trash}); err != nil {
				return fmt.Errorf("SQS-L055 the job journal could not be written, so the database was not moved to the trash: %w", err)
			}
			return s.renameDB(ctx, l.DBName, trash)
		}); err != nil {
			return undo(err)
		}
	}
	days := s.Settings(ctx).TrashDays
	purge := s.now().Add(time.Duration(days) * 24 * time.Hour)
	if _, err := s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'trashed', trash_db_name = ?, trashed_at = ?, purge_after = ?, updated_at = ?
		WHERE id = ? AND state NOT IN ('trashed', 'purged')`,
		trash, store.NowT(), store.T(purge), store.NowT(), l.ID); err != nil {
		err = fmt.Errorf("SQS-L056 the library could not be recorded in the trash: %w", err)
		if trash == "" {
			// Nothing was moved: as before the point of no return, the
			// library gets back the access its state promises.
			return undo(err)
		}
		// The database is already under its trash name: the lock stays and
		// the job is left to reconciliation, which records the move at the
		// next start (reconcileTrash) instead of reporting the library
		// missing while its data sits under a name the catalog does not know.
		return fmt.Errorf("%w. Its database is kept as %s; %w", err, trash, jobs.ErrUnsettled)
	}
	s.Sched.SetEnabled(ctx, l.ID, false)
	if err := s.SyncHBA(ctx); err != nil {
		return err
	}
	s.Events.Emit(ctx, events.Event{Type: events.LibraryTrashed, Severity: events.Warning, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("%q was moved to the trash; it will be permanently deleted on %s", l.DisplayName, purge.In(s.Cfg.Location).Format("2006-01-02"))})
	return nil
}

// RestoreFromTrash brings a trashed library back. A second request while one
// is queued or running returns that job (ADR 0029).
func (s *Service) RestoreFromTrash(ctx context.Context, by audit.Actor, id string) (*jobs.Job, error) {
	l, err := s.Library(ctx, id)
	if err != nil {
		return nil, err
	}
	if l.State != StateTrashed {
		return nil, errors.New("SQS-L042 the library is not in the trash")
	}
	return s.Jobs.Enqueue(ctx, JobLibraryUntrash, id, nil, by, jobs.Priority(90), jobs.Unique(), jobs.Audited("library.untrash", "library", id, nil))
}

func (s *Service) runUntrash(ctx context.Context, r *jobs.Run) error {
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	// Re-validate what RestoreFromTrash checked when the job was queued.
	if l.State != StateTrashed {
		return errors.New("SQS-L042 the library is not in the trash")
	}
	if l.TrashDB != "" {
		if db, err := s.PG.Database(ctx, l.TrashDB); err != nil {
			return err
		} else if db == nil {
			return fmt.Errorf("SQS-L051 the database in the trash (%s) no longer exists on the server", l.TrashDB)
		}
		journaled := false
		if err := r.Step("move", func() error {
			// Any database holds the name, an invalid one included, which
			// would refuse the rename only after the journal.
			taken, err := s.PG.DatabaseNameTaken(ctx, l.DBName)
			if ferr := fault("untrash.name_lookup"); ferr != nil {
				taken, err = false, ferr
			}
			if err != nil {
				return err
			}
			if taken {
				return fmt.Errorf("SQS-L005 a database %q already exists", l.DBName)
			}
			// Without this journal a recovery that stops after the rename
			// could not be finished.
			if err := r.Journal(untrashJournal{TrashDB: l.TrashDB, DBName: l.DBName}); err != nil {
				return fmt.Errorf("SQS-L057 the job journal could not be written, so the database was not moved back from the trash: %w", err)
			}
			journaled = true
			if err := s.PG.RenameDatabase(ctx, l.TrashDB, l.DBName); err != nil {
				return err
			}
			return s.PG.SetAllowConnections(ctx, l.DBName, true)
		}); err != nil {
			if journaled {
				// A refused rename may still have been committed (an
				// answer lost with the connection).
				return errUntrash(err)
			}
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
		if l.TrashDB != "" {
			// The database is back under its name: reconciliation finishes
			// the recovery.
			return errUntrash(err)
		}
		return err
	}
	s.Events.Emit(ctx, events.Event{Type: events.LibraryRestoredFromTrash, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("%q was restored from the trash", l.DisplayName)})
	return nil
}

// untrashJournal is stored by library.untrash before it renames the database
// of the trash back to the library's name, so a recovery that stops after the
// rename is finished by reconciliation. Its keys differ from those of every
// other journal, since reconciliation tries each shape on every step.
type untrashJournal struct {
	TrashDB string `json:"untrash_from"`
	DBName  string `json:"untrash_to"`
}

// errUntrash wraps what stops a recovery from the trash once its journal is
// written: the database may be back under its name, so the job is unsettled
// (jobs.ErrUnsettled) and reconciliation finishes it.
func errUntrash(err error) error {
	err = fmt.Errorf("SQS-L058 the recovery from the trash stopped before it finished: %w", err)
	return fmt.Errorf("%w. %w", err, jobs.ErrUnsettled)
}

// Purge deletes a trashed library for good (its backups stay). A second
// request while one is queued or running returns that job, and it shares its
// job with the purge of expired trash (PurgeExpired: the same type, the same
// empty parameters), whichever was queued first (ADR 0029).
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
	return s.Jobs.Enqueue(ctx, JobLibraryPurge, id, nil, by, jobs.Unique(), jobs.Audited("library.purge", "library", id, nil))
}

// purgeJournal is stored by library.purge before it drops anything, so a
// purge cut or failed after a drop is finished by reconciliation. Journals
// written up to 1.2.3 do not name the previous state.
type purgeJournal struct {
	Library string `json:"purge_of"`
	TrashDB string `json:"trash_db"`
	PrevDB  string `json:"prev_db"`
}

// errPurge wraps what stops a purge; after a drop, or in the step that drops
// the roles, the purge is also unsettled (jobs.ErrUnsettled) and
// reconciliation finishes it.
func errPurge(err error, dropped bool) error {
	err = fmt.Errorf("SQS-L047 the purge stopped before it finished: %w", err)
	if dropped {
		return fmt.Errorf("%w. %w", err, jobs.ErrUnsettled)
	}
	return err
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
			// On a backups volume that is not this installation's (a disk not
			// mounted leaves an empty folder, where every path still checks)
			// the final backup cannot be seen, so it proves nothing.
			if err := s.BackupVolumeOK(); err != nil {
				return err
			}
			bs, err := s.Backups(ctx, BackupFilter{LibraryID: l.ID, Statuses: []string{BackupOK}})
			if err != nil {
				return err
			}
			for _, b := range bs {
				if b.Kind == KindPreDelete && b.Verified() && !s.blocked(b) {
					return nil
				}
			}
			return errors.New("SQS-L045 there is no verified final backup; not purging")
		}); err != nil {
			return err
		}
	}
	// A database of the library role that the purge does not drop itself
	// stops it here, before the journal and before any drop: the library
	// stays in the trash with its database and can still be recovered. A
	// lookup that fails stops it too, with nothing dropped and nothing left
	// for reconciliation.
	if err := r.Step("check_owned", func() error { return s.checkOwned(ctx, l) }); err != nil {
		var owned *ownedError
		if errors.As(err, &owned) {
			return err
		}
		return errPurge(err, false)
	}
	// From the journal on, a purge that stops after a drop is finished by
	// reconciliation instead of leaving a trashed library without its
	// database (PLAN P0.4).
	if err := r.Step("journal", func() error { return r.Journal(purgeJournal{Library: l.ID, TrashDB: l.TrashDB, PrevDB: l.PrevDB}) }); err != nil {
		return errPurge(err, false)
	}
	if l.PrevDB != "" {
		// The state kept before the last restore goes only with a verified
		// backup of its own; without one nothing is purged.
		if err := r.Step("drop_previous_state", func() error { return s.dropPrevDB(ctx, r, l, l.PrevDB, l.PrevBackupID) }); err != nil {
			return err
		}
		if !s.clearPrev(ctx, l.ID, l.PrevDB) {
			// The purge of expired items drops an expired previous state
			// without the library's lock and may have forgotten it first: a
			// row that no longer names it is the same outcome. A row that
			// still names it, or that cannot be read, is not.
			if cur, err := s.Library(ctx, l.ID); err != nil || cur.PrevDB == l.PrevDB {
				return errPurge(fmt.Errorf("the previous state %s was dropped, but the catalog still names it", l.PrevDB), true)
			}
		}
	}
	if l.TrashDB != "" {
		if err := r.Step("drop_database", func() error {
			err := s.PG.DropDatabase(ctx, l.TrashDB)
			if ferr := fault("purge.drop"); ferr != nil {
				err = ferr
			}
			return err
		}); err != nil {
			// The drop may have happened on the server before the error (a
			// lost connection): reconciliation looks whether the database
			// is still there.
			return errPurge(err, true)
		}
	}
	if err := r.Step("drop_roles", func() error { return s.finishPurge(ctx, l) }); err != nil {
		var owned *ownedError
		if errors.As(err, &owned) {
			return err
		}
		// A role may be gone already, also for a library that had no
		// database to drop: from the journal on, a purge that stops here is
		// finished by reconciliation, as one cut here is.
		return errPurge(err, true)
	}
	s.Events.Emit(ctx, events.Event{Type: events.LibraryPurged, Severity: events.Warning, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("%q was permanently deleted from the server; its backups are kept according to retention", l.DisplayName)})
	return nil
}

// ownedError is a library role that still owns databases: dropping them is a
// manual decision, so the purge stops without reconciliation, normally before
// anything is dropped (checkOwned).
type ownedError struct {
	role  string
	names []string
}

func (e *ownedError) Error() string {
	return fmt.Sprintf("SQS-L046 role %s still owns: %s; review them before purging", e.role, strings.Join(e.names, ", "))
}

// checkOwned refuses a purge, before it journals or drops anything, when the
// owner role of l owns a database the purge does not drop itself: dropping it
// is a manual decision. The purge drops the database of the trash in any
// condition, and the previous state only while PostgreSQL can open it: one
// left invalid by an interrupted DROP DATABASE is never dropped (dropPrevDB
// keeps it with SQS-R062), so it is reported here, before anything is
// journaled.
func (s *Service) checkOwned(ctx context.Context, l *Library) error {
	prevUsable := false
	if l.PrevDB != "" {
		d, err := s.PG.Database(ctx, l.PrevDB)
		if err != nil {
			return fmt.Errorf("could not check the previous-state database %s: %w", l.PrevDB, err)
		}
		prevUsable = d != nil
	}
	owned, err := s.PG.DatabasesOwnedBy(ctx, l.OwnerRole)
	if ferr := fault("purge.owned_check"); ferr != nil {
		err = ferr
	}
	if err != nil {
		return fmt.Errorf("could not list the databases of %s: %w", l.OwnerRole, err)
	}
	var other []string
	for _, name := range owned {
		if name == l.TrashDB || (name == l.PrevDB && prevUsable) {
			continue
		}
		other = append(other, name)
	}
	if len(other) > 0 {
		return &ownedError{l.OwnerRole, other}
	}
	return nil
}

// finishPurge drops the roles of a library whose databases are gone and
// records it purged: credentials revoked, state, schedules, in one
// transaction. Every lookup and write is checked; the backups are kept.
func (s *Service) finishPurge(ctx context.Context, l *Library) error {
	// Any other database owned by the library roles (for example created
	// during an init window) is reported and kept: dropping it is a manual
	// decision. checkOwned looked before the first drop; this is checked
	// again here, right before the roles are dropped.
	owned, err := s.PG.DatabasesOwnedBy(ctx, l.OwnerRole)
	if ferr := fault("purge.owned_lookup"); ferr != nil {
		err = ferr
	}
	if err != nil {
		return fmt.Errorf("could not list the databases of %s: %w", l.OwnerRole, err)
	}
	if len(owned) > 0 {
		return &ownedError{l.OwnerRole, owned}
	}
	creds, err := s.Credentials(ctx, l.ID)
	if err != nil {
		return err
	}
	for i := len(creds) - 1; i >= 0; i-- {
		ok, err := s.PG.RoleExists(ctx, creds[i].PGRole)
		if ferr := fault("purge.role_lookup"); ferr != nil {
			err = ferr
		}
		if err != nil {
			return fmt.Errorf("could not check the role %s: %w", creds[i].PGRole, err)
		}
		if !ok {
			continue
		}
		if err := s.PG.DropRole(ctx, creds[i].PGRole); err != nil {
			return err
		}
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		now := store.NowT()
		if _, err := tx.ExecContext(ctx, `UPDATE library_credentials SET revoked_at = COALESCE(revoked_at, ?), password_enc = '' WHERE library_id = ?`, now, l.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE libraries SET state = 'purged', purged_at = ?, trash_db_name = '', updated_at = ? WHERE id = ?`, now, now, l.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM schedules WHERE target_id = ? AND kind IN (?, ?)`, l.ID, ScheduleBackup, ScheduleVerify)
		return err
	})
}

// reconcilePurge finishes a purge that stopped after a drop, from its
// journal, and reports whether it is settled. A purge cut before it dropped
// the previous state or the database of the trash leaves the library in the
// trash, where it can be purged again; one whose databases are gone gets its
// roles dropped and is recorded purged. The backups are always kept.
func (s *Service) reconcilePurge(ctx context.Context, l *Library, j purgeJournal, note func(string, ...any)) (settled bool) {
	if j.Library == "" || l.State != StateTrashed {
		return true
	}
	// The previous state first, as the purge drops it first: the one the
	// journal names (journals written up to 1.2.3 name none) and the one the
	// catalog still names.
	for _, name := range slices.Compact([]string{j.PrevDB, l.PrevDB, j.TrashDB}) {
		if name == "" {
			continue
		}
		d, err := s.PG.Database(ctx, name)
		if err != nil {
			note("%s: could not check whether the purge dropped %s (%v); nothing was changed", l.Slug, name, err)
			return false
		}
		if d != nil {
			note("%s: the purge stopped before dropping %s; the library stays in the trash", l.Slug, name)
			return true
		}
	}
	if err := s.finishPurge(ctx, l); err != nil {
		note("%s: could not finish the interrupted purge: %v", l.Slug, err)
		return false
	}
	s.Events.Emit(ctx, events.Event{Type: events.LibraryPurged, Severity: events.Warning, LibraryID: l.ID,
		Message: fmt.Sprintf("Finished the interrupted purge of %q; its backups are kept according to retention", l.DisplayName)})
	note("%s: finished the interrupted purge", l.Slug)
	return true
}

// PurgeExpired purges trash and previous states whose time is up.
func (s *Service) PurgeExpired(ctx context.Context, r *jobs.Run) error {
	libs, err := s.Libraries(ctx, ListOptions{})
	if err != nil {
		return err
	}
	now := s.now()
	for _, l := range libs {
		// The previous state first: the purge queued below drops one too, and
		// two drops of the same database at the same time disturb each other.
		if l.PrevDB != "" && l.PrevExpiresAt != nil && now.After(*l.PrevExpiresAt) {
			s.expirePrev(ctx, r, l)
		}
		if l.State == StateTrashed && l.PurgeAfter != nil && now.After(*l.PurgeAfter) {
			// Queued with its audit entry, as a purge by hand is (ADR 0026):
			// without the entry nothing is queued and a later pass tries
			// again. A purge by hand that joins this job writes no entry.
			if _, err := s.Jobs.Enqueue(ctx, JobLibraryPurge, l.ID, nil, audit.System, jobs.Unique(),
				jobs.Audited("library.purge", "library", l.ID, map[string]any{"slug": l.Slug, "purge_after": store.T(*l.PurgeAfter)})); err != nil {
				r.Warnf("Could not queue the purge of %s: %v", l.Slug, err)
			}
		}
		s.sweepOrphanPrev(ctx, r, l, now)
	}
	return nil
}

// expirePrev drops the expired previous state of l. It runs without the
// library's job lock, so it reads the row again and clears the reference
// only if it still names the same database (a revert may have swapped it).
func (s *Service) expirePrev(ctx context.Context, r *jobs.Run, l *Library) {
	switch l.State {
	case StateActive, StateMaintenance, StateTrashed:
	default:
		// A missing library may have nothing else: the previous state can be
		// the only copy of its latest data (a swap that stopped halfway).
		r.Warnf("Keeping %s: the library is %s", l.PrevDB, l.State.Label())
		return
	}
	if cur, err := s.Library(ctx, l.ID); err != nil || cur.PrevDB != l.PrevDB || cur.State == StateRestoring {
		return
	}
	if err := s.dropPrevDB(ctx, r, l, l.PrevDB, l.PrevBackupID); err != nil {
		s.prevKept(ctx, r, l, l.PrevDB, err)
		return
	}
	if !s.clearPrev(ctx, l.ID, l.PrevDB) {
		r.Warnf("Deleted previous state %s, but the catalog could not be updated; the next pass or the next start forgets it", l.PrevDB)
		return
	}
	r.Infof("Deleted previous state %s", l.PrevDB)
}

// clearPrev forgets the previous state of a library if the row still names
// prev, and reports whether it did.
func (s *Service) clearPrev(ctx context.Context, id, prev string) bool {
	res, err := s.DB.ExecContext(ctx, `UPDATE libraries SET prev_db_name = '', prev_expires_at = NULL, prev_backup_id = '' WHERE id = ? AND prev_db_name = ?`, id, prev)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// sweepOrphanPrev drops previous-state databases that no library row points
// to anymore (left by older versions, by an interrupted job or by a swap
// that stopped halfway) once they are older than the retention for previous
// states, and only with a verified backup of each one.
func (s *Service) sweepOrphanPrev(ctx context.Context, r *jobs.Run, l *Library, now time.Time) {
	if l.State != StateActive && l.State != StateMaintenance {
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
		if err := s.dropPrevDB(ctx, r, l, name, ""); err != nil {
			s.prevKept(ctx, r, l, name, err)
			continue
		}
		r.Infof("Deleted %s, a previous state nobody was using anymore", name)
	}
}

// dropPrevDB drops a previous-state database of l only when a verified backup
// of that database itself exists (backupID, taken after its swap) and is not
// blocked, or can be taken now. A database that no longer exists counts as
// dropped; one that an interrupted DROP DATABASE left invalid is kept
// (SQS-R062), since sqsolve never drops it. One that the journal of a job
// still pending reconciliation names is never dropped.
func (s *Service) dropPrevDB(ctx context.Context, r *jobs.Run, l *Library, name, backupID string) error {
	if name == l.DBName || !strings.HasPrefix(name, l.Slug+"__prev_") {
		return fmt.Errorf("SQS-R056 %s is not a previous state of %s; it was not dropped", name, l.Slug)
	}
	if err := s.notNamedByPendingJournal(ctx, name); err != nil {
		return err
	}
	d, err := s.PG.Database(ctx, name)
	if err != nil {
		return err
	}
	if d == nil {
		// Database hides an invalid database, which keeps its name and
		// whatever files are left (docs 07 §10).
		if taken, err := s.PG.DatabaseNameTaken(ctx, name); err != nil {
			return err
		} else if taken {
			return fmt.Errorf("SQS-R062 %s was left invalid by an interrupted DROP DATABASE and is never dropped automatically; it was kept, drop it by hand after checking Health", name)
		}
		return nil
	}
	// On a backups volume that is not this installation's (a disk not mounted
	// leaves an empty folder, where every path still checks) no backup can be
	// seen or taken, so the previous state is kept.
	if err := s.BackupVolumeOK(); err != nil {
		return err
	}
	// A blocked backup (SQS-B043, or SQS-B045 while its path cannot be
	// checked) is not read, so it covers nothing: a new one is taken.
	covered := false
	if b, err := s.Backup(ctx, backupID); err == nil && b.Usable() && b.Verified() && b.SourceDB == name && !s.blocked(b) {
		covered = true
	}
	if !covered {
		b, err := s.backupClosedDB(ctx, l, name)
		if err != nil {
			return fmt.Errorf("SQS-R055 %s has no verified backup of its own and one could not be taken now (%v); it was kept", name, err)
		}
		r.Infof("Backed up %s before deleting it (%s)", name, HumanBytes(b.SizeBytes))
	}
	return s.PG.DropDatabase(ctx, name)
}

// backupClosedDB takes a verified backup of a database of l that workstations
// cannot reach (a previous state or an orphan). It opens the database to
// connections for the dump, which pg_hba.conf only allows to sqsolve through
// the local socket, and closes it again.
func (s *Service) backupClosedDB(ctx context.Context, l *Library, name string) (*Backup, error) {
	d, err := s.PG.Database(ctx, name)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fmt.Errorf("SQS-B002 database %s does not exist", name)
	}
	if !d.AllowConn {
		if err := s.PG.SetAllowConnections(ctx, name, true); err != nil {
			return nil, err
		}
		defer s.PG.SetAllowConnections(context.WithoutCancel(ctx), name, false)
	}
	return s.createBackup(ctx, nil, l, KindPreRestore, name)
}

// prevKept reports a previous state that was not dropped.
func (s *Service) prevKept(ctx context.Context, r *jobs.Run, l *Library, name string, cause error) {
	r.Warnf("Keeping %s: %v", name, cause)
	s.Events.Emit(ctx, events.Event{Type: "library.prev_kept", Severity: events.Warning, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("The previous state %s of %q was kept: %s", name, l.DisplayName, shortErr(cause))})
}
