// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/store"
)

// hookAdoptionCheck runs in notAdoptable once the database is open for the
// check (tests).
var hookAdoptionCheck atomic.Pointer[func(db string)]

func execxCmd(name string, args []string, s *Service) execx.Cmd {
	return execx.Cmd{Name: name, Args: args, Credential: s.PG.AdminCredential()}
}

// Reconcile repairs what a previous process left half done (docs 05 §6.4).
// It must run after jobs.MarkInterrupted and before the dispatcher starts.
// Besides the jobs interrupted now, it tries again the ones earlier starts
// could not settle.
func (s *Service) Reconcile(ctx context.Context, interrupted []*jobs.Job) []string {
	var notes []string
	note := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		notes = append(notes, msg)
		s.Log.Warn("reconciliation: " + msg)
	}

	// 1. Journals of the destructive jobs a stop interrupted, now or at an
	// earlier start that could not settle them (docs 11 §8). A job that is
	// not settled stays pending and is tried again at the next start;
	// meanwhile pending holds its library, whose state and access step 4
	// leaves alone, and keep the databases its journal names, which the
	// sweep of step 3 never drops. A journal that cannot be read names
	// nothing known, so the sweep keeps every __restore database instead,
	// and so it does when the pending jobs themselves cannot be read.
	pending := map[string]bool{}
	keep := map[string]bool{}
	toSettle, retried, pendingKnown := s.journalsToReconcile(ctx, interrupted, note)
	sweepRestores := pendingKnown
	for _, j := range toSettle {
		why := ""
		jnote := func(format string, args ...any) {
			why = fmt.Sprintf(format, args...)
			note(format, args...)
		}
		settled, names, read := s.reconcileJournal(ctx, j, retried[j.ID], jnote)
		if settled {
			if err := s.Jobs.Reconciled(ctx, j.ID); err != nil {
				note("job %s was settled, but this could not be recorded (%v); the next start checks it again", j.ID, err)
			}
			continue
		}
		pending[j.LibraryID] = true
		for _, n := range names {
			keep[n] = true
		}
		if !read {
			sweepRestores = false
		}
		s.reportPending(ctx, j, why, names, note)
	}

	// 2. Backups that were running.
	rows, _ := s.Backups(ctx, BackupFilter{Statuses: []string{BackupRunning}})
	for _, b := range rows {
		if abs, err := s.AbsPath(b); err != nil {
			note("backup %s interrupted; its files were not touched: %v", b.ID, err)
		} else {
			removeIfExists(abs + ".partial")
			removeIfExists(abs)
			removeIfExists(manifestPath(abs))
		}
		s.DB.ExecContext(ctx, `UPDATE backups SET status = 'interrupted', finished_at = ?, error = 'The process stopped during the backup' WHERE id = ?`, store.NowT(), b.ID)
		note("backup %s interrupted; its leftovers were deleted", b.ID)
	}
	filepath.WalkDir(filepath.Join(s.Cfg.BackupDir, "libraries"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".partial") {
			os.Remove(p)
		}
		return nil
	})

	// 3. Auxiliary databases nobody owns any more. A database the journal of
	// a pending job names is kept: a __restore one may be the half of a swap
	// the operator needs to finish it by hand.
	if dbs, err := s.PG.Databases(ctx); err == nil {
		for _, d := range dbs {
			restore := strings.Contains(d.Name, "__restore_")
			if !restore && !strings.HasPrefix(d.Name, "sqsolve_verify_") && !strings.HasPrefix(d.Name, "sqsolve_export_") {
				continue
			}
			if keep[d.Name] {
				note("kept %s: an interrupted job that reconciliation has not settled names it", d.Name)
				continue
			}
			if restore && !pendingKnown {
				note("kept %s: the interrupted jobs earlier starts did not settle could not be read, and one may name it", d.Name)
				continue
			}
			if restore && !sweepRestores {
				note("kept %s: the journal of an interrupted job that reconciliation has not settled could not be read, and it may name it", d.Name)
				continue
			}
			if err := s.PG.DropDatabase(ctx, d.Name); err == nil {
				note("deleted auxiliary database %s", d.Name)
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
		if pending[l.ID] {
			continue // reported in step 1
		}
		d, exists := dbs[l.DBName]
		switch l.State {
		case StateProvisioning:
			if db, err := s.PG.Database(ctx, l.DBName); err == nil && db == nil {
				s.PG.DropRole(ctx, l.OwnerRole)
				s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'purged', purged_at = ?, notes = notes || ? WHERE id = ?`, store.NowT(), "\n[creation interrupted]", l.ID)
				note("creation of %s interrupted; rolled back", l.Slug)
			}
		case StateRestoring:
			// A library left restoring without a journal it could settle
			// from (a swap that is not settled is pending and skipped above):
			// it gets back the state it had before the restore (maintenance
			// stays closed to its roles), or becomes missing when its
			// database is not on the server.
			before := s.stateBeforeRestore(ctx, l.ID)
			switch {
			case !pendingKnown:
				// Its journal may be one of the pending jobs that could not
				// be read.
				note("%s was left restoring and the interrupted jobs earlier starts did not settle could not be read; it stays restoring until the next start", l.Slug)
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

	// A previous state whose database is gone is forgotten, also for a
	// library adopted above with its former __prev. Not for a library with a
	// pending journal (a swap records its previous state before the rename
	// that creates it), nor when the pending jobs could not be read.
	if pendingKnown {
		for _, l := range libs {
			if !pending[l.ID] {
				s.forgetGonePrev(ctx, l, note)
			}
		}
	}

	s.migrateConnLimits(ctx, note)

	// 5. pg_hba.conf from the catalog.
	if err := s.SyncHBA(ctx); err != nil {
		note("could not regenerate pg_hba.conf: %v", err)
	}
	return notes
}

// journalsToReconcile returns the interrupted jobs whose type reconciliation
// settles from a journal: the ones interrupted now and the ones earlier
// starts left pending, oldest first. retried holds the second ones. known is
// false when they could not be read; a warning event says what is kept then.
func (s *Service) journalsToReconcile(ctx context.Context, interrupted []*jobs.Job, note func(string, ...any)) (out []*jobs.Job, retried map[string]bool, known bool) {
	seen := map[string]bool{}
	retried = map[string]bool{}
	add := func(j *jobs.Job) bool {
		if spec, ok := s.Jobs.Spec(j.Type); !ok || !spec.Reconcile || seen[j.ID] {
			return false
		}
		seen[j.ID] = true
		out = append(out, j)
		return true
	}
	for _, j := range interrupted {
		add(j)
	}
	earlier, err := s.Jobs.PendingReconciliation(ctx)
	if err != nil {
		note("could not read the interrupted jobs earlier starts did not settle (%v); only the ones interrupted now are reconciled", err)
		s.Events.Emit(ctx, events.Event{Type: "library.reconcile_pending", Severity: events.Warning,
			Message: fmt.Sprintf("Reconciliation could not read the interrupted jobs earlier starts did not settle (%v). Until it can, every __restore database is kept, libraries left restoring stay restoring and no previous state is forgotten; sqsolve tries again at every start (docs 11 §8)", err)})
	}
	for _, j := range earlier {
		if add(j) {
			retried[j.ID] = true
		}
	}
	slices.SortFunc(out, func(a, b *jobs.Job) int { return cmp.Compare(a.ID, b.ID) })
	return out, retried, err == nil
}

// reconcileJournal settles one interrupted job from its journal. It reports
// whether the job is settled, the databases the journal of a swap or a
// revert names, and whether the journal could be read at all. retried says
// that an earlier start left the job pending (or settled it without being
// able to record it).
//
// A journal no longer describes a library that moved on, and acting on it
// could undo later work, so it is settled without changing anything when a
// newer restore, revert, delete or recreate of the library succeeded or is
// itself pending, and, for a swap tried again, when the library is no longer
// restoring: a pending swap always leaves it restoring, a state every other
// operation refuses. A delete or a recreate tried again only acts on the
// state the library has now (a delete applies the access of that state or
// records the move its journal names; a recreate acts only on a missing
// library), so it needs no more.
func (s *Service) reconcileJournal(ctx context.Context, j *jobs.Job, retried bool, note func(string, ...any)) (settled bool, names []string, read bool) {
	steps, err := s.Jobs.Steps(ctx, j.ID)
	if err != nil {
		note("job %s: could not read its journal (%v)", j.ID, err)
		return false, nil, false
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
	if j.Type == JobRestoreReplace || j.Type == JobRestoreRevert {
		names = journal.names()
	}
	l, err := s.Library(ctx, j.LibraryID)
	if errors.Is(err, ErrLibraryNotFound) {
		return true, nil, true // nothing left to reconcile
	}
	if err != nil {
		note("job %s: could not read its library (%v)", j.ID, err)
		return false, names, true
	}
	newer, err := s.Jobs.NewerJob(ctx, j, JobRestoreReplace, JobRestoreRevert, JobLibraryDelete, JobRestoreRecreate)
	if err != nil {
		note("job %s: could not look for newer jobs of its library (%v)", j.ID, err)
		return false, names, true
	}
	if newer != "" {
		note("%s: the newer job %s of the library supersedes the interrupted job %s; nothing was changed", l.Slug, newer, j.ID)
		return true, nil, true
	}
	if retried && len(names) > 0 && l.State != StateRestoring {
		note("%s: the library moved on since the interrupted job %s (it is %s, no longer restoring); nothing was changed", l.Slug, j.ID, strings.ToLower(l.State.Label()))
		return true, nil, true
	}
	switch j.Type {
	case JobLibraryDelete:
		return s.reconcileTrash(ctx, l, trashMove, note), nil, true
	case JobRestoreRecreate:
		return !s.reconcileRecreate(ctx, l, recreate, note), nil, true
	}
	if journal.Live == "" {
		// Cut before its journal: nothing was locked or renamed, and step 4
		// gives a library left restoring its state back.
		return true, nil, true
	}
	return s.reconcileSwap(ctx, l, journal, note), names, true
}

// reportPending says, in a note and a warning event, that a job is not
// settled: why, that its library is left as it is and which databases are
// kept.
func (s *Service) reportPending(ctx context.Context, j *jobs.Job, why string, names []string, note func(string, ...any)) {
	slug, display := j.LibraryID, j.LibraryID
	if l, err := s.Library(ctx, j.LibraryID); err == nil {
		slug, display = l.Slug, l.DisplayName
	}
	if why == "" {
		why = "its outcome could not be decided"
	}
	kept, keptNote := "", ""
	if len(names) > 0 {
		list := strings.Join(names, ", ")
		kept = fmt.Sprintf(", and the databases its journal names are kept (%s)", list)
		keptNote = fmt.Sprintf(" and the databases its journal names are kept (%s)", list)
	}
	note("%s: the interrupted job %s is not settled; its state and access are left as they are%s; the next start tries again", slug, j.ID, keptNote)
	s.Events.Emit(ctx, events.Event{Type: "library.reconcile_pending", Severity: events.Warning, LibraryID: j.LibraryID, JobID: j.ID,
		Message: fmt.Sprintf("Reconciliation could not settle the interrupted job %q of %q: %s. The library's state and access are left as they are%s. sqsolve tries again at every start (docs 11 §8)",
			s.Jobs.Title(j.Type), display, why, kept)})
}

// forgetGonePrev forgets the previous state of l when its database is no
// longer on the server (a revert of a missing library cut after its rename,
// a swap that stopped halfway and was put back): Revert would only fail with
// SQS-R031. An invalid database still holds the name and keeps it, and a
// lookup that fails changes nothing.
func (s *Service) forgetGonePrev(ctx context.Context, l *Library, note func(string, ...any)) {
	if l.PrevDB == "" {
		return
	}
	taken, err := s.PG.DatabaseNameTaken(ctx, l.PrevDB)
	if err != nil || taken {
		return
	}
	if s.clearPrev(ctx, l.ID, l.PrevDB) {
		note("%s: forgot its previous state %s, which is no longer on the server", l.Slug, l.PrevDB)
	}
}

// reconcileSwap applies the table of docs 11 §7.3 and reports whether the
// swap is settled. It never drops the live database nor the previous one. A
// check or a rename that fails, or a state no rule covers, leaves everything
// as it is for the next start.
func (s *Service) reconcileSwap(ctx context.Context, l *Library, j swapJournal, note func(string, ...any)) (settled bool) {
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
		return false
	}
	before := j.before()
	switch {
	case live && !prev:
		// No rename happened (the cut may have been during the lock). A
		// restore database left over is dropped later. Only a library the
		// lock left restoring gets its state back: one that is not was never
		// locked (the cut came before the lock took effect) or, for a journal
		// tried again at a later start, has moved on since, and a journal must
		// not undo later work. Step 4 applies the access of the state it has.
		s.unrecordPrev(ctx, l.ID, j)
		if l.State == StateRestoring {
			s.applyAccess(ctx, l, before)
			s.setState(ctx, l.ID, before)
		}
		note("%s: the swap did not start; the library is unchanged", l.Slug)
	case !live && prev && restored:
		if err := s.PG.RenameDatabase(ctx, j.Restored, j.Live); err != nil {
			note("%s: could not complete the swap: %v", l.Slug, err)
			return false
		}
		s.finishSwap(ctx, l, j, before, note)
		note("%s: completed the interrupted swap", l.Slug)
	case live && prev:
		s.finishSwap(ctx, l, j, before, note)
		note("%s: the swap was already done; reopened", l.Slug)
	case !live && prev && !restored:
		if err := s.PG.RenameDatabase(ctx, j.Prev, j.Live); err != nil {
			note("%s: anomalous state; could not put the previous database %s back under its name: %v", l.Slug, j.Prev, err)
			return false
		}
		s.unrecordPrev(ctx, l.ID, j)
		s.applyAccess(ctx, l, before)
		s.setState(ctx, l.ID, before)
		note("%s: anomalous state; the previous database was put back under its name", l.Slug)
	case restored && j.Prev == "":
		// Journaled before the lock, while restoring the copy: no rename
		// was planned, so the copy is not half of a swap. The sweep drops it
		// and step 4 reports the library missing.
		note("%s: the swap had not started (no previous state was planned) and %s is not on the server; %s is a leftover", l.Slug, j.Live, j.Restored)
	case restored:
		// Neither the live database nor the previous one: only the one the
		// swap was bringing in is left, and no rule says what it should be.
		note("%s: of the databases of the swap only %s is on the server; nothing was changed", l.Slug, j.Restored)
		return false
	default:
		note("%s: none of the swap databases was found (%s, %s, %s)", l.Slug, j.Live, j.Prev, j.Restored)
	}
	return true
}

func (s *Service) finishSwap(ctx context.Context, l *Library, j swapJournal, before State, note func(string, ...any)) {
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
	if err := s.reopenAfterSwap(ctx, l, j.Prev, preID, before, fp.Hash, fp.Tables); err != nil {
		note("%s: the swap is complete, but reopening the library reported: %v", l.Slug, err)
	}
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

// reconcileTrash settles an interrupted delete and reports whether it is
// settled: not when it cannot tell whether the database was moved, nor when
// the move it finds cannot be recorded (the data would sit under a trash
// name the catalog does not know).
func (s *Service) reconcileTrash(ctx context.Context, l *Library, move map[string]string, note func(string, ...any)) (settled bool) {
	moved := false
	if move != nil {
		d, err := s.PG.Database(ctx, move["to"])
		if err != nil {
			// Not knowing is not "not moved": undoing the lock now could
			// leave the data under an untracked trash name. The library keeps
			// its lock and state for the next start.
			note("%s: could not check whether the deletion moved the database to %s (%v); nothing was changed", l.Slug, move["to"], err)
			return false
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
					return false
				} else if d != nil {
					s.PG.SetAllowConnections(ctx, l.DBName, true)
				}
			}
			note("%s: the deletion was interrupted before moving the database; the lock was undone", l.Slug)
		}
		return true
	}
	if l.State != StateTrashed {
		days := s.Settings(ctx).TrashDays
		purge := s.now().AddDate(0, 0, days)
		if _, err := s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'trashed', trash_db_name = ?, trashed_at = ?, purge_after = ?, updated_at = ? WHERE id = ?`,
			move["to"], store.NowT(), store.T(purge), store.NowT(), l.ID); err != nil {
			note("%s: the database was moved to the trash as %s, but this could not be recorded (%v); nothing else was changed", l.Slug, move["to"], err)
			return false
		}
		s.Sched.SetEnabled(ctx, l.ID, false)
		note("%s: completed the move to the trash", l.Slug)
	}
	return true
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
// sqsolve_admin can reach it. A database that is not adopted always ends
// closed, even if the check panics, and so does one found open (a check cut
// by a hard kill leaves it open): closing it is harmless and reversible.
func (s *Service) notAdoptable(ctx context.Context, l *Library, d pg.DBInfo) (why string) {
	adopted := false
	defer func() {
		if adopted {
			return
		}
		if err := s.PG.SetAllowConnections(context.WithoutCancel(ctx), l.DBName, false); err != nil {
			why += fmt.Sprintf("; it could not be closed to connections again (%v)", err)
		}
	}()
	if !d.AllowConn {
		if err := s.PG.SetAllowConnections(ctx, l.DBName, true); err != nil {
			return fmt.Sprintf("it does not accept connections and could not be opened for the check (%v)", err)
		}
	}
	if f := hookAdoptionCheck.Load(); f != nil {
		(*f)(l.DBName)
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
	adopted = true
	return ""
}

// notNamedByPendingJournal refuses a database that the journal of a swap or
// a revert still pending reconciliation names (SQS-R090): it may be the half
// of the swap the next start or the operator needs. Not being able to tell
// refuses it too.
func (s *Service) notNamedByPendingJournal(ctx context.Context, name string) error {
	kept := func(err error) error {
		return fmt.Errorf("SQS-R091 could not tell whether an interrupted job pending reconciliation names %s, so it was kept: %w", name, err)
	}
	js, err := s.Jobs.PendingReconciliation(ctx)
	if err != nil {
		return kept(err)
	}
	for _, j := range js {
		if j.Type != JobRestoreReplace && j.Type != JobRestoreRevert {
			continue
		}
		steps, err := s.Jobs.Steps(ctx, j.ID)
		if err != nil {
			return kept(fmt.Errorf("job %s: %w", j.ID, err))
		}
		for _, st := range steps {
			var sj swapJournal
			if json.Unmarshal(st.Data, &sj) == nil && slices.Contains(sj.names(), name) {
				return fmt.Errorf("SQS-R090 %s is named by an interrupted job that reconciliation at startup has not settled yet; it was kept", name)
			}
		}
	}
	return nil
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
