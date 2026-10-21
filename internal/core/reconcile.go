// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/store"
)

func execxCmd(name string, args []string, s *Service) execx.Cmd {
	return execx.Cmd{Name: name, Args: args, Credential: s.PG.AdminCredential()}
}

// Reconcile repairs what a previous process left half done (docs 05 §6.4).
// It must run after jobs.MarkInterrupted and before the dispatcher starts.
func (s *Service) Reconcile(ctx context.Context, interrupted []*jobs.Job) []string {
	var notes []string
	note := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		notes = append(notes, msg)
		s.Log.Warn("reconciliation: " + msg)
	}

	// 1. Swaps in progress, guided by the journal. undecided holds the
	// libraries whose interrupted recreate could not be checked: step 4 does
	// not adopt their database.
	undecided := map[string]bool{}
	for _, j := range interrupted {
		if j.Type != JobRestoreReplace && j.Type != JobRestoreRevert && j.Type != JobLibraryDelete && j.Type != JobRestoreRecreate {
			continue
		}
		steps, err := s.Jobs.Steps(ctx, j.ID)
		if err != nil {
			continue
		}
		var journal swapJournal
		var trashMove map[string]string
		var recreate recreateJournal
		for _, st := range steps {
			var tmp swapJournal
			if json.Unmarshal(st.Data, &tmp) == nil && tmp.Live != "" {
				journal = tmp
			}
			var tm map[string]string
			if json.Unmarshal(st.Data, &tm) == nil && tm["from"] != "" && tm["to"] != "" {
				trashMove = tm
			}
			var rj recreateJournal
			if json.Unmarshal(st.Data, &rj) == nil && rj.Created != "" {
				recreate = rj
			}
		}
		l, err := s.Library(ctx, j.LibraryID)
		if err != nil {
			continue
		}
		if j.Type == JobLibraryDelete {
			s.reconcileTrash(ctx, l, trashMove, note)
			continue
		}
		if j.Type == JobRestoreRecreate {
			if s.reconcileRecreate(ctx, l, recreate, note) {
				undecided[l.ID] = true
			}
			continue
		}
		if journal.Live != "" {
			s.reconcileSwap(ctx, l, journal, note)
		}
		// A library still restoring (no journal, or a swap that could not be
		// decided) is handled by step 4.
	}

	// 2. Backups that were running.
	rows, _ := s.Backups(ctx, BackupFilter{Statuses: []string{BackupRunning}})
	for _, b := range rows {
		abs := s.AbsPath(b)
		removeIfExists(abs + ".partial")
		removeIfExists(abs)
		removeIfExists(manifestPath(abs))
		s.DB.ExecContext(ctx, `UPDATE backups SET status = 'interrupted', finished_at = ?, error = 'The process stopped during the backup' WHERE id = ?`, store.NowT(), b.ID)
		note("backup %s interrupted; its leftovers were deleted", b.ID)
	}
	filepath.WalkDir(filepath.Join(s.Cfg.BackupDir, "libraries"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".partial") {
			os.Remove(p)
		}
		return nil
	})

	// 3. Auxiliary databases nobody owns any more.
	if dbs, err := s.PG.Databases(ctx); err == nil {
		for _, d := range dbs {
			if strings.HasPrefix(d.Name, "sqsolve_verify_") || strings.HasPrefix(d.Name, "sqsolve_export_") || strings.Contains(d.Name, "__restore_") {
				if err := s.PG.DropDatabase(ctx, d.Name); err == nil {
					note("deleted auxiliary database %s", d.Name)
				}
			}
		}
	}

	s.DB.ExecContext(ctx, `UPDATE export_workspaces SET state = 'closed' WHERE state IN ('preparing', 'ready')`)

	// 4. Libraries stuck in transient states, and the access every library
	// state promises (ADR 0024): whatever a cut left locked is opened again,
	// and a library in maintenance is closed to its roles only.
	libs, _ := s.Libraries(ctx, ListOptions{})
	dbs := map[string]pg.DBInfo{}
	invalid := map[string]bool{}
	all, dbErr := s.PG.Databases(ctx)
	for _, d := range all {
		if d.Invalid {
			// A DROP DATABASE cut halfway: it can only be dropped, so it is as
			// good as missing (docs 07 §4).
			invalid[d.Name] = true
			continue
		}
		dbs[d.Name] = d
	}
	// missing reports a live library whose database is not on the server.
	missing := func(l *Library) {
		s.setState(ctx, l.ID, StateMissing)
		msg := fmt.Sprintf("The database of %q does not exist on the server. Recreate it from its latest backup", l.DisplayName)
		if invalid[l.DBName] {
			msg = fmt.Sprintf("The database of %q was left invalid by an interrupted DROP DATABASE: PostgreSQL can no longer open it and only allows dropping it (Health lists it). Recreating the library from its latest backup needs the name free", l.DisplayName)
		}
		if _, ok := dbs[l.PrevDB]; ok && l.PrevDB != "" {
			msg += fmt.Sprintf(", or use Revert to put back its previous state %s", l.PrevDB)
		}
		s.Events.Emit(ctx, events.Event{Type: "library.missing", Severity: events.Critical, LibraryID: l.ID, Message: msg})
		note("%s has no database on the server", l.Slug)
	}
	// access applies the access of st to l; closed says its database did not
	// accept connections.
	access := func(l *Library, st State, closed bool) {
		changed, err := s.applyAccess(ctx, l, st)
		switch {
		case err != nil:
			note("%s: could not apply its access: %v", l.Slug, err)
		case st == StateMaintenance && (closed || len(changed) > 0):
			note("%s: maintenance now blocks its roles and keeps the database open (ADR 0024)", l.Slug)
		default:
			if len(changed) > 0 {
				note("%s: login given back to %s", l.Slug, strings.Join(changed, ", "))
			}
			if closed {
				note("%s: the database accepts connections again", l.Slug)
			}
		}
	}
	for _, l := range libs {
		d, exists := dbs[l.DBName]
		switch l.State {
		case StateProvisioning:
			if db, err := s.PG.Database(ctx, l.DBName); err == nil && db == nil {
				s.PG.DropRole(ctx, l.OwnerRole)
				s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'purged', purged_at = ?, notes = notes || ? WHERE id = ?`, store.NowT(), "\n[creation interrupted]", l.ID)
				note("creation of %s interrupted; rolled back", l.Slug)
			}
		case StateRestoring:
			// A swap that reconciliation could not decide, or a library left
			// restoring without a journal: it gets back the state it had
			// before the restore (maintenance stays closed to its roles), or
			// becomes missing when its database is not on the server.
			before := s.stateBeforeRestore(ctx, l.ID)
			switch {
			case dbErr != nil:
				note("%s was left restoring and the databases could not be listed (%v); it stays restoring until the next start", l.Slug, dbErr)
			case !exists:
				missing(l)
			default:
				s.setState(ctx, l.ID, before)
				access(l, before, !d.AllowConn)
				note("%s was left restoring; reopened as %s", l.Slug, strings.ToLower(before.Label()))
			}
		case StateActive, StateMaintenance:
			if dbErr == nil && !exists {
				missing(l)
				continue
			}
			access(l, l.State, exists && !d.AllowConn)
		case StateMissing:
			if invalid[l.DBName] {
				note("%s: a database with its name was left invalid by an interrupted DROP DATABASE; it was not adopted. PostgreSQL can no longer open it and only allows dropping it (Health lists it); recreating the library needs the name free", l.Slug)
				access(l, l.State, false)
				continue
			}
			if exists && d.Owner == l.OwnerRole && undecided[l.ID] {
				note("%s: left without a database; check %s, left by an interrupted recreate, before using it", l.Slug, l.DBName)
				access(l, l.State, false)
				continue
			}
			if exists && d.Owner == l.OwnerRole {
				// The volume came back, a revert cut after its rename put a
				// closed __prev back under the name, or something else
				// created a database with it: it is adopted only once its
				// content was checked, and never dropped here.
				if why := s.notAdoptable(ctx, l, d); why != "" {
					note("%s: a database with its name is on the server again but was not adopted: %s; it stays missing. The database is kept and may hold data of the library: look at it before doing anything with it (docs 11 §7.7)", l.Slug, why)
					s.Events.Emit(ctx, events.Event{Type: "library.missing", Severity: events.Warning, LibraryID: l.ID,
						Message: fmt.Sprintf("A database named %q is on the server again, but %q stays missing: %s. The database is kept and may hold data of the library: look at it before doing anything with it (its size is on the Health page; docs 11 §7.7 shows how to open it as the PostgreSQL superuser). sqsolve checks it again at every start", l.DBName, l.DisplayName, why)})
					access(l, l.State, false)
					continue
				}
				s.setState(ctx, l.ID, StateActive)
				s.applyAccess(ctx, l, StateActive)
				s.Events.Emit(ctx, events.Event{Type: "library.found", LibraryID: l.ID,
					Message: fmt.Sprintf("The database of %q is on the server again; the library is active", l.DisplayName)})
				note("%s has its database again; active", l.Slug)
				continue
			}
			if exists {
				note("%s: a database with its name exists but belongs to %s; it stays missing", l.Slug, d.Owner)
			}
			access(l, l.State, false)
		case StateAwaitingInit:
			if l.InitWindowExpiresAt != nil && s.now().After(*l.InitWindowExpiresAt) {
				s.PG.SetRoleCreateDB(ctx, l.OwnerRole, false)
				s.setState(ctx, l.ID, StateInitExpired)
				access(l, StateInitExpired, false)
				continue
			}
			access(l, l.State, false)
		case StateInitExpired:
			access(l, l.State, false)
		}
	}

	s.migrateConnLimits(ctx, note)

	// 5. pg_hba.conf from the catalog.
	if err := s.SyncHBA(ctx); err != nil {
		note("could not regenerate pg_hba.conf: %v", err)
	}
	return notes
}

// reconcileSwap applies the table of docs 11 §7.3. It never drops the live
// database nor the previous one.
func (s *Service) reconcileSwap(ctx context.Context, l *Library, j swapJournal, note func(string, ...any)) {
	exists := func(name string) (bool, error) {
		if name == "" {
			return false, nil
		}
		d, err := s.PG.Database(ctx, name)
		return d != nil, err
	}
	live, errLive := exists(j.Live)
	prev, errPrev := exists(j.Prev)
	restored, errRestored := exists(j.Restored)
	if err := cmp.Or(errLive, errPrev, errRestored); err != nil {
		note("%s: could not check the databases of the swap (%v); nothing was changed", l.Slug, err)
		return
	}
	before := j.before()
	switch {
	case live && !prev:
		// No rename happened (the cut may have been during the lock). A
		// restore database left over is dropped later.
		s.unrecordPrev(ctx, l.ID, j)
		s.applyAccess(ctx, l, before)
		s.setState(ctx, l.ID, before)
		note("%s: the swap did not start; the library is unchanged", l.Slug)
	case !live && prev && restored:
		if err := s.PG.RenameDatabase(ctx, j.Restored, j.Live); err != nil {
			note("%s: could not complete the swap: %v", l.Slug, err)
			return
		}
		s.finishSwap(ctx, l, j, before)
		note("%s: completed the interrupted swap", l.Slug)
	case live && prev:
		s.finishSwap(ctx, l, j, before)
		note("%s: the swap was already done; reopened", l.Slug)
	case !live && prev && !restored:
		if err := s.PG.RenameDatabase(ctx, j.Prev, j.Live); err == nil {
			s.unrecordPrev(ctx, l.ID, j)
			s.applyAccess(ctx, l, before)
			s.setState(ctx, l.ID, before)
		}
		note("%s: anomalous state; the previous database was put back under its name", l.Slug)
	default:
		note("%s: none of the swap databases was found (%s, %s, %s)", l.Slug, j.Live, j.Prev, j.Restored)
	}
}

func (s *Service) finishSwap(ctx context.Context, l *Library, j swapJournal, before State) {
	fp := s.fingerprintOf(ctx, j.Live)
	// The swap recorded the backup that covers the previous state in the
	// catalog before renaming; older journals did not.
	preID := ""
	if l.PrevDB == j.Prev {
		preID = l.PrevBackupID
	} else if bs, err := s.Backups(ctx, BackupFilter{LibraryID: l.ID, Statuses: []string{BackupOK}, Limit: 5}); err == nil {
		for _, b := range bs {
			if b.Kind == KindPreRestore {
				preID = b.ID
				break
			}
		}
	}
	s.reopenAfterSwap(ctx, l, j.Prev, preID, before, fp.Hash, fp.Tables)
}

// reconcileRecreate handles a restore.recreate cut before its comparison.
// pg_restore runs in a single transaction, so the database it created is
// empty: it is dropped and the library stays missing, instead of being
// adopted by step 4 as an active, empty library that backups would then
// capture. A database with tables (pg_restore had committed; it is adopted
// without the comparison), one the comparison verified, or one of another
// owner is kept. It returns true when the check could not finish: step 4 then
// leaves the library missing instead of adopting a database nobody checked.
func (s *Service) reconcileRecreate(ctx context.Context, l *Library, j recreateJournal, note func(string, ...any)) (undecided bool) {
	if j.Created == "" || j.Created != l.DBName || j.Phase == "verified" || l.State != StateMissing {
		return false
	}
	d, err := s.PG.Database(ctx, j.Created)
	if err != nil {
		note("%s: could not look for %s, left by an interrupted recreate: %v; it was kept", l.Slug, j.Created, err)
		return true
	}
	if d == nil || d.Owner != l.OwnerRole {
		return false
	}
	conn, err := s.PG.ConnectDB(ctx, j.Created)
	if err != nil {
		note("%s: could not inspect %s, left by an interrupted recreate: %v; it was kept", l.Slug, j.Created, err)
		return true
	}
	fp, err := pg.SchemaFingerprint(ctx, conn)
	conn.Close(context.Background())
	if err != nil {
		note("%s: could not read the structure of %s, left by an interrupted recreate: %v; it was kept", l.Slug, j.Created, err)
		return true
	}
	if fp.Tables > 0 {
		return false
	}
	if err := s.PG.DropDatabase(ctx, j.Created); err != nil {
		note("%s: could not drop the empty database an interrupted recreate created: %v", l.Slug, err)
		return true
	}
	note("%s: dropped the empty database an interrupted recreate had created; the library stays without a database, recreate it again", l.Slug)
	return false
}

func (s *Service) reconcileTrash(ctx context.Context, l *Library, move map[string]string, note func(string, ...any)) {
	moved := false
	if move != nil {
		d, err := s.PG.Database(ctx, move["to"])
		if err != nil {
			// Not knowing is not "not moved": undoing the lock now could
			// leave the data under an untracked trash name. The library keeps
			// its lock and state for the next start.
			note("%s: could not check whether the deletion moved the database to %s (%v); nothing was changed", l.Slug, move["to"], err)
			return
		}
		moved = d != nil
	}
	if !moved {
		// Interrupted before the rename (during the lock, or between the
		// journal and the rename): undo the lock.
		if l.State != StateTrashed && l.State != StatePurged {
			s.applyAccess(ctx, l, l.State)
			if l.State == StateAwaitingInit || l.State == StateInitExpired {
				// A database created during the initialization window.
				if d, err := s.PG.Database(ctx, l.DBName); err != nil {
					note("%s: could not check its database to accept connections again: %v", l.Slug, err)
				} else if d != nil {
					s.PG.SetAllowConnections(ctx, l.DBName, true)
				}
			}
			note("%s: the deletion was interrupted before moving the database; the lock was undone", l.Slug)
		}
		return
	}
	if l.State != StateTrashed {
		days := s.Settings(ctx).TrashDays
		purge := s.now().AddDate(0, 0, days)
		s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'trashed', trash_db_name = ?, trashed_at = ?, purge_after = ?, updated_at = ? WHERE id = ?`,
			move["to"], store.NowT(), store.T(purge), store.NowT(), l.ID)
		s.Sched.SetEnabled(ctx, l.ID, false)
		note("%s: completed the move to the trash", l.Slug)
	}
}

// notAdoptable inspects the database a missing library finds again under its
// name (docs 11 §7.7). It returns why it is not adopted: it could not be
// inspected, or it has no tables while the library had some (for example a
// recreate cut during pg_restore whose drop also failed). A library that
// never had tables (not used by Resolve yet) adopts an empty database.
//
// A database that does not accept connections (a __prev, which sqsolve
// closes, put back under the name by a revert cut after its rename; or one
// closed by a version before ADR 0024) is opened for the check, as
// backupClosedDB does: a missing library has no pg_hba.conf lines, so only
// sqsolve_admin can reach it. It is closed again unless it is adopted.
func (s *Service) notAdoptable(ctx context.Context, l *Library, d pg.DBInfo) (why string) {
	if !d.AllowConn {
		if err := s.PG.SetAllowConnections(ctx, l.DBName, true); err != nil {
			return fmt.Sprintf("it does not accept connections and could not be opened for the check (%v)", err)
		}
		defer func() {
			if why == "" {
				return
			}
			if err := s.PG.SetAllowConnections(context.WithoutCancel(ctx), l.DBName, false); err != nil {
				why += fmt.Sprintf("; it was opened for the check and could not be closed again (%v)", err)
			}
		}()
	}
	conn, err := s.PG.ConnectDB(ctx, l.DBName)
	if err != nil {
		return fmt.Sprintf("it could not be inspected (%v)", err)
	}
	fp, err := pg.SchemaFingerprint(ctx, conn)
	conn.Close(context.Background())
	if err != nil {
		return fmt.Sprintf("its structure could not be read (%v)", err)
	}
	if fp.Tables == 0 && l.TableCount > 0 {
		return fmt.Sprintf("it has no tables, while the library had %d", l.TableCount)
	}
	return ""
}

// stateBeforeRestore is the state a library had before the swap that left it
// restoring, from the journal of its latest replace or revert: maintenance or
// active (also when no journal says).
func (s *Service) stateBeforeRestore(ctx context.Context, libraryID string) State {
	var data string
	err := s.DB.QueryRowContext(ctx, `SELECT st.data FROM job_steps st JOIN jobs j ON j.id = st.job_id
		WHERE j.library_id = ? AND j.type IN (?, ?) AND st.data LIKE '%"state_before"%'
		ORDER BY j.id DESC, st.seq DESC LIMIT 1`, libraryID, JobRestoreReplace, JobRestoreRevert).Scan(&data)
	var j swapJournal
	if err != nil || json.Unmarshal([]byte(data), &j) != nil {
		return StateActive
	}
	return j.before()
}
