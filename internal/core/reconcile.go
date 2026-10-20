// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
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
		s.Log.Warn("reconciliación: " + msg)
	}

	// 1. Swaps in progress, guided by the journal.
	for _, j := range interrupted {
		if j.Type != JobRestoreReplace && j.Type != JobRestoreRevert && j.Type != JobLibraryDelete {
			continue
		}
		steps, err := s.Jobs.Steps(ctx, j.ID)
		if err != nil {
			continue
		}
		var journal swapJournal
		var trashMove map[string]string
		for _, st := range steps {
			var tmp swapJournal
			if json.Unmarshal(st.Data, &tmp) == nil && tmp.Live != "" {
				journal = tmp
			}
			var tm map[string]string
			if json.Unmarshal(st.Data, &tm) == nil && tm["from"] != "" && tm["to"] != "" {
				trashMove = tm
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
		if journal.Live != "" {
			s.reconcileSwap(ctx, l, journal, note)
		} else if l.State == StateRestoring {
			s.setState(ctx, l.ID, StateActive)
		}
	}

	// 2. Backups that were running.
	rows, _ := s.Backups(ctx, BackupFilter{Statuses: []string{BackupRunning}})
	for _, b := range rows {
		abs := s.AbsPath(b)
		removeIfExists(abs + ".partial")
		removeIfExists(abs)
		removeIfExists(manifestPath(abs))
		s.DB.ExecContext(ctx, `UPDATE backups SET status = 'interrupted', finished_at = ?, error = 'El proceso se detuvo durante el respaldo' WHERE id = ?`, store.NowT(), b.ID)
		note("respaldo %s interrumpido; se borraron sus restos", b.ID)
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
					note("se borró la base auxiliar %s", d.Name)
				}
			}
		}
	}

	s.DB.ExecContext(ctx, `UPDATE export_workspaces SET state = 'closed' WHERE state IN ('preparing', 'ready')`)

	// 4. Libraries stuck in transient states.
	libs, _ := s.Libraries(ctx, ListOptions{})
	for _, l := range libs {
		switch l.State {
		case StateProvisioning:
			if db, _ := s.PG.Database(ctx, l.DBName); db == nil {
				s.PG.DropRole(ctx, l.OwnerRole)
				s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'purged', purged_at = ?, notes = notes || ? WHERE id = ?`, store.NowT(), "\n[creación interrumpida]", l.ID)
				note("creación de %s interrumpida; se deshizo", l.Slug)
			}
		case StateRestoring:
			s.setState(ctx, l.ID, StateActive)
			s.PG.SetAllowConnections(ctx, l.DBName, true)
			note("%s quedó en restauración; se reabrió", l.Slug)
		case StateActive, StateMaintenance:
			if db, err := s.PG.Database(ctx, l.DBName); err == nil && db == nil {
				s.setState(ctx, l.ID, StateMissing)
				s.Events.Emit(ctx, events.Event{Type: "library.missing", Severity: events.Critical, LibraryID: l.ID,
					Message: fmt.Sprintf("La base de %q no existe en el servidor. Recréala desde su último respaldo", l.DisplayName)})
				note("%s no tiene base en el servidor", l.Slug)
			}
		case StateAwaitingInit:
			if l.InitWindowExpiresAt != nil && s.now().After(*l.InitWindowExpiresAt) {
				s.PG.SetRoleCreateDB(ctx, l.OwnerRole, false)
				s.setState(ctx, l.ID, StateInitExpired)
			}
		}
	}

	// 5. pg_hba.conf from the catalog.
	if err := s.SyncHBA(ctx); err != nil {
		note("no se pudo regenerar pg_hba.conf: %v", err)
	}
	return notes
}

// reconcileSwap applies the table of docs 11 §7.3. It never drops the live
// database nor the previous one.
func (s *Service) reconcileSwap(ctx context.Context, l *Library, j swapJournal, note func(string, ...any)) {
	exists := func(name string) bool {
		if name == "" {
			return false
		}
		d, _ := s.PG.Database(ctx, name)
		return d != nil
	}
	live, prev, restored := exists(j.Live), exists(j.Prev), exists(j.Restored)
	before := State(j.State)
	if before != StateMaintenance {
		before = StateActive
	}
	switch {
	case live && !prev:
		// No rename happened. A restore database left over is dropped later.
		s.PG.SetAllowConnections(ctx, j.Live, before == StateActive)
		s.setState(ctx, l.ID, before)
		note("%s: el intercambio no empezó; la biblioteca sigue igual", l.Slug)
	case !live && prev && restored:
		if err := s.PG.RenameDatabase(ctx, j.Restored, j.Live); err != nil {
			note("%s: no se pudo completar el intercambio: %v", l.Slug, err)
			return
		}
		s.finishSwap(ctx, l, j, before)
		note("%s: se completó el intercambio interrumpido", l.Slug)
	case live && prev:
		s.finishSwap(ctx, l, j, before)
		note("%s: el intercambio ya estaba hecho; se reabrió", l.Slug)
	case !live && prev && !restored:
		if err := s.PG.RenameDatabase(ctx, j.Prev, j.Live); err == nil {
			s.PG.SetAllowConnections(ctx, j.Live, before == StateActive)
			s.setState(ctx, l.ID, before)
		}
		note("%s: estado anómalo; se devolvió la base anterior a su nombre", l.Slug)
	default:
		note("%s: no se encontró ninguna de las bases del intercambio (%s, %s, %s)", l.Slug, j.Live, j.Prev, j.Restored)
	}
}

func (s *Service) finishSwap(ctx context.Context, l *Library, j swapJournal, before State) {
	conn, err := s.PG.ConnectDB(ctx, j.Live)
	var fp pg.Fingerprint
	if err == nil {
		fp, _ = pg.SchemaFingerprint(ctx, conn)
		conn.Close(context.Background())
	}
	preID := ""
	if bs, err := s.Backups(ctx, BackupFilter{LibraryID: l.ID, Statuses: []string{BackupOK}, Limit: 5}); err == nil {
		for _, b := range bs {
			if b.Kind == KindPreRestore {
				preID = b.ID
				break
			}
		}
	}
	s.reopenAfterSwap(ctx, l, j.Prev, preID, before, fp.Hash, fp.Tables)
}

func (s *Service) reconcileTrash(ctx context.Context, l *Library, move map[string]string, note func(string, ...any)) {
	if move == nil {
		// Interrupted before renaming: undo the block.
		roles, _ := s.activeRoles(ctx, l.ID)
		for _, r := range roles {
			s.PG.SetRoleLogin(ctx, r, true)
		}
		if l.Live() {
			s.PG.SetAllowConnections(ctx, l.DBName, l.State != StateMaintenance)
		}
		note("%s: la eliminación se interrumpió antes de mover la base; se deshizo el bloqueo", l.Slug)
		return
	}
	if d, _ := s.PG.Database(ctx, move["to"]); d != nil && l.State != StateTrashed {
		days := s.Settings(ctx).TrashDays
		purge := s.now().AddDate(0, 0, days)
		s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'trashed', trash_db_name = ?, trashed_at = ?, purge_after = ?, updated_at = ? WHERE id = ?`,
			move["to"], store.NowT(), store.T(purge), store.NowT(), l.ID)
		s.Sched.SetEnabled(ctx, l.ID, false)
		note("%s: se completó el paso a la papelera", l.Slug)
	}
}
