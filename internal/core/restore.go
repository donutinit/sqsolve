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
		return fmt.Errorf("SQS-R010 pg_restore falló: %w", err)
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
		return "", fmt.Errorf("SQS-R011 la estructura restaurada no coincide con el manifiesto (%d tablas, esperadas %d)", fp.Tables, m.Schema.Tables)
	}
	if len(m.RowCounts) == 0 {
		return "Estructura correcta; el respaldo no trae conteos de filas", nil
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
			diffs = append(diffs, fmt.Sprintf("%s: %d en lugar de %d", t, got[t], m.RowCounts[t]))
		}
	}
	if len(diffs) > 0 {
		if len(diffs) > 5 {
			diffs = append(diffs[:5], "...")
		}
		return "", fmt.Errorf("SQS-R012 los conteos no coinciden: %s", strings.Join(diffs, "; "))
	}
	return fmt.Sprintf("%d tablas y %d filas coinciden con el manifiesto", len(tables), total), nil
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
			r.Infof("La biblioteca no tiene respaldos que verificar")
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
		return fmt.Errorf("SQS-B021 el respaldo no está disponible (%s)", b.Status)
	}
	l, _ := s.Library(ctx, b.LibraryID)
	name := b.LibraryID
	if l != nil {
		name = l.DisplayName
	}
	fail := func(cause error) error {
		s.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE backups SET verify_status = 'failed', verified_at = ?, verify_detail = ? WHERE id = ?`, store.NowT(), cause.Error(), id)
		s.Events.Emit(ctx, events.Event{Type: events.BackupVerifyFailed, Severity: events.Error, LibraryID: b.LibraryID, JobID: r.Job.ID,
			Message: fmt.Sprintf("Falló la verificación del respaldo de %q del %s: %s", name, b.StartedAt.In(s.Cfg.Location).Format("2006-01-02 15:04"), shortErr(cause))})
		return cause
	}
	if err := r.Step("verificar_l1", func() error { return s.verifyL1(ctx, id) }); err != nil {
		return fail(err)
	}
	r.Progress(15)
	m, err := ReadManifest(manifestPath(s.AbsPath(b)))
	if err != nil {
		return fail(err)
	}
	db := pg.VerifyDB(ulid.Short(r.Job.ID))
	defer s.PG.DropDatabase(context.WithoutCancel(ctx), db)
	err = r.Step("espacio", func() error {
		sp, err := fsx.Statfs(s.Cfg.PGDir)
		if err != nil {
			return nil // cannot measure: try anyway
		}
		need := m.Server.DatabaseSize * 6 / 5
		if need == 0 {
			need = b.SizeBytes * 4
		}
		if uint64(need) > sp.Free {
			return fmt.Errorf("SQS-B022 no hay espacio en el volumen de la base para la restauración de prueba (%s necesarios)", HumanBytes(need))
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	var detail string
	err = r.Step("restaurar_prueba", func() error {
		if err := s.PG.CreateDatabase(ctx, db, pg.AdminRole); err != nil {
			return err
		}
		s.PG.SetDatabaseConnLimit(ctx, db, 2)
		return s.pgRestore(ctx, s.AbsPath(b), db, "")
	})
	if err != nil {
		return fail(err)
	}
	r.Progress(70)
	err = r.Step("comparar", func() error {
		detail, err = s.compareWithManifest(ctx, db, m)
		return err
	})
	if err != nil {
		return fail(err)
	}
	status := VerifyL2OK
	if len(m.RowCounts) == 0 {
		status = VerifyPartial
	}
	s.DB.ExecContext(ctx, `UPDATE backups SET verify_status = ?, verify_level = 2, verified_at = ?, verify_detail = ? WHERE id = ?`, status, store.NowT(), detail, id)
	r.Infof("%s", detail)
	s.Events.Emit(ctx, events.Event{Type: events.BackupVerified, LibraryID: b.LibraryID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Restauración de prueba correcta del respaldo de %q: %s", name, detail)})
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
		return nil, nil, errors.New("SQS-R001 el respaldo no se puede restaurar (falló o se borró)")
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
	b, err := s.Backup(ctx, p.BackupID)
	if err != nil {
		return err
	}
	createdRole := false
	rollback := func(cause error) error {
		c := context.WithoutCancel(ctx)
		s.PG.DropDatabase(c, l.DBName)
		if createdRole {
			s.PG.DropRole(c, l.OwnerRole)
		}
		s.DB.ExecContext(c, `UPDATE library_credentials SET revoked_at = ? WHERE library_id = ?`, store.NowT(), l.ID)
		s.DB.ExecContext(c, `UPDATE libraries SET state = 'purged', purged_at = ?, notes = ? WHERE id = ?`, store.NowT(), "[restauración fallida] "+cause.Error(), l.ID)
		s.Sched.Delete(c, ScheduleBackup, l.ID)
		s.SyncHBA(c)
		s.Events.Emit(c, events.Event{Type: events.RestoreFailed, Severity: events.Error, LibraryID: l.ID, JobID: r.Job.ID,
			Message: fmt.Sprintf("Falló la restauración en la biblioteca nueva %q: %s", l.DisplayName, shortErr(cause))})
		return cause
	}
	var m *Manifest
	if err := r.Step("verificar_respaldo", func() error {
		if err := s.verifyL1(ctx, b.ID); err != nil {
			return err
		}
		m, err = ReadManifest(manifestPath(s.AbsPath(b)))
		return err
	}); err != nil {
		return rollback(err)
	}
	r.Progress(10)
	if err := r.Step("crear_rol", func() error {
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
	if err := r.Step("restaurar", func() error {
		if err := s.PG.CreateDatabase(ctx, l.DBName, l.OwnerRole); err != nil {
			return err
		}
		if err := s.PG.SetAllowConnections(ctx, l.DBName, true); err != nil {
			return err
		}
		return s.pgRestore(ctx, s.AbsPath(b), l.DBName, l.OwnerRole)
	}); err != nil {
		return rollback(err)
	}
	r.Progress(75)
	if err := r.Step("comparar", func() error {
		detail, err := s.compareWithManifest(ctx, l.DBName, m)
		if err == nil {
			r.Infof("%s", detail)
		}
		return err
	}); err != nil {
		return rollback(err)
	}
	if err := r.Step("activar", func() error {
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
		Message: fmt.Sprintf("Se restauró el respaldo del %s en la biblioteca nueva %q", b.StartedAt.In(s.Cfg.Location).Format("2006-01-02 15:04"), l.DisplayName)})
	r.SetResult(map[string]string{"library_id": l.ID})
	s.Jobs.Enqueue(ctx, JobBackupCreate, l.ID, BackupParams{Kind: KindManual}, audit.System, jobs.Unique())
	return nil
}

// SlugAvailableIgnoringCatalog checks only the server (the catalog row was
// created by the caller).
func (s *Service) SlugAvailableIgnoringCatalog(ctx context.Context, slug string) error {
	if db, err := s.PG.Database(ctx, slug); err != nil {
		return err
	} else if db != nil {
		return fmt.Errorf("SQS-L005 ya existe una base de datos %q en el servidor", slug)
	}
	if ok, err := s.PG.RoleExists(ctx, slug); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("SQS-L006 ya existe un rol %q en el servidor", slug)
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
		return nil, errors.New("SQS-R001 el respaldo no se puede restaurar (falló o se borró)")
	}
	l, err := s.Library(ctx, b.LibraryID)
	if err != nil {
		return nil, err
	}
	if confirm != l.Slug {
		return nil, errors.New("SQS-R002 escribe el identificador de la biblioteca para confirmar")
	}
	if l.State != StateActive && l.State != StateMaintenance {
		return nil, fmt.Errorf("SQS-R003 no se puede reemplazar una biblioteca en estado %s", l.State.Label())
	}
	s.Audit.Record(ctx, by, "restore.replace", "library", l.ID, audit.OK, map[string]any{"backup_id": backupID, "terminate_sessions": terminate})
	return s.Jobs.Enqueue(ctx, JobRestoreReplace, l.ID, RestoreParams{BackupID: backupID, TerminateSessions: terminate}, by, jobs.Priority(100))
}

// swapJournal is stored before each rename (docs 11 §7.3).
type swapJournal struct {
	Live     string `json:"live"`
	Prev     string `json:"prev"`
	Restored string `json:"restored"`
	Phase    string `json:"phase"`
	State    string `json:"state_before"`
}

func (s *Service) runRestoreReplace(ctx context.Context, r *jobs.Run) error {
	var p RestoreParams
	r.Params(&p)
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	b, err := s.Backup(ctx, p.BackupID)
	if err != nil {
		return err
	}
	stateBefore := l.State
	restoreDB := pg.RestoreDB(l.Slug, ulid.Short(r.Job.ID))
	blocked := false
	cleanup := func(cause error) error {
		c := context.WithoutCancel(ctx)
		s.PG.DropDatabase(c, restoreDB)
		if blocked {
			s.PG.SetAllowConnections(c, l.DBName, stateBefore != StateMaintenance)
		}
		s.setState(c, l.ID, stateBefore)
		s.Events.Emit(c, events.Event{Type: events.RestoreFailed, Severity: events.Error, LibraryID: l.ID, JobID: r.Job.ID,
			Message: fmt.Sprintf("No se reemplazó %q: %s. La biblioteca activa no se modificó", l.DisplayName, shortErr(cause))})
		return cause
	}
	var m *Manifest
	if err := r.Step("comprobar", func() error {
		if err := s.verifyL1(ctx, b.ID); err != nil {
			return err
		}
		m, err = ReadManifest(manifestPath(s.AbsPath(b)))
		if err != nil {
			return err
		}
		if l.Fingerprint != "" && m.Schema.Fingerprint != "" && l.Fingerprint != m.Schema.Fingerprint {
			r.Warnf("El respaldo tiene otra estructura que la biblioteca actual (otra versión de Resolve)")
		}
		return nil
	}); err != nil {
		return cleanup(err)
	}
	s.Events.Emit(ctx, events.Event{Type: events.RestoreStarted, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Reemplazando %q con el respaldo del %s", l.DisplayName, b.StartedAt.In(s.Cfg.Location).Format("2006-01-02 15:04"))})
	var pre *Backup
	if err := r.Step("respaldo_previo", func() error {
		var err error
		pre, err = s.createBackup(ctx, nil, l, KindPreRestore, "")
		return err
	}); err != nil {
		return cleanup(err)
	}
	r.Progress(30)
	if err := r.Step("restaurar", func() error {
		r.Journal(swapJournal{Live: l.DBName, Restored: restoreDB, Phase: "restoring", State: string(stateBefore)})
		if err := s.PG.CreateDatabase(ctx, restoreDB, l.OwnerRole); err != nil {
			return err
		}
		return s.pgRestore(ctx, s.AbsPath(b), restoreDB, l.OwnerRole)
	}); err != nil {
		return cleanup(err)
	}
	r.Progress(60)
	if err := r.Step("comparar", func() error {
		detail, err := s.compareWithManifest(ctx, restoreDB, m)
		if err == nil {
			r.Infof("%s", detail)
		}
		return err
	}); err != nil {
		return cleanup(err)
	}
	if err := r.Step("esperar_sesiones", func() error {
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
				return fmt.Errorf("SQS-R020 siguen %d sesiones abiertas en la biblioteca; pide que cierren Resolve o marca cerrar sesiones", len(ss))
			}
			r.Infof("Esperando a que se cierren %d sesiones", len(ss))
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
	j := swapJournal{Live: l.DBName, Prev: prev, Restored: restoreDB, State: string(stateBefore)}
	if err := r.Step("bloquear", func() error {
		s.setState(ctx, l.ID, StateRestoring)
		if err := s.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
			return err
		}
		blocked = true
		n, err := s.PG.TerminateAll(ctx, l.DBName)
		if n > 0 {
			r.Warnf("Se cerraron %d sesiones", n)
		}
		return err
	}); err != nil {
		return cleanup(err)
	}
	// From here on the journal drives recovery; errors no longer clean up blindly.
	err = r.Step("intercambiar", func() error {
		j.Phase = "rename_live"
		r.Journal(j)
		if err := s.PG.RenameDatabase(ctx, l.DBName, prev); err != nil {
			return err
		}
		j.Phase = "rename_restored"
		r.Journal(j)
		if err := s.PG.RenameDatabase(ctx, restoreDB, l.DBName); err != nil {
			// Put the live database back.
			if err2 := s.PG.RenameDatabase(context.WithoutCancel(ctx), prev, l.DBName); err2 != nil {
				return fmt.Errorf("%v; además no se pudo devolver la base original: %v", err, err2)
			}
			return err
		}
		j.Phase = "swapped"
		return r.Journal(j)
	})
	if err != nil {
		return cleanup(err)
	}
	if err := r.Step("reabrir", func() error {
		return s.reopenAfterSwap(ctx, l, prev, pre.ID, stateBefore, m.Schema.Fingerprint, m.Schema.Tables)
	}); err != nil {
		return err
	}
	s.dropSupersededPrev(ctx, r, l, prev)
	s.Events.Emit(ctx, events.Event{Type: events.RestoreSucceeded, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Se reemplazó %q con el respaldo del %s. El estado anterior se conserva %d días para revertir", l.DisplayName,
			b.StartedAt.In(s.Cfg.Location).Format("2006-01-02 15:04"), s.Settings(ctx).PrevDays)})
	return nil
}

// dropSupersededPrev removes the state kept by an earlier restore once a new
// restore keeps a newer one. The library only tracks one previous state, so
// the older database would otherwise stay forever and block a purge. Its
// content is in the pre_restore backup taken by that earlier restore.
func (s *Service) dropSupersededPrev(ctx context.Context, r *jobs.Run, before *Library, current string) {
	old := before.PrevDB
	if old == "" || old == current {
		return
	}
	if b, err := s.Backup(ctx, before.PrevBackupID); err != nil || !b.Usable() {
		r.Warnf("Se conserva %s: no se encontró el respaldo que lo cubre; se borrará al vencer", old)
		return
	}
	if err := s.PG.DropDatabase(ctx, old); err != nil {
		r.Warnf("No se pudo borrar el estado anterior %s: %v", old, err)
		return
	}
	r.Infof("Se borró %s, el estado que conservaba la restauración anterior", old)
}

func (s *Service) reopenAfterSwap(ctx context.Context, l *Library, prev, preBackupID string, stateBefore State, fp string, tables int) error {
	roles, err := s.activeRoles(ctx, l.ID)
	if err != nil {
		return err
	}
	if err := s.PG.SecureDatabase(ctx, l.DBName, roles); err != nil {
		return err
	}
	if err := s.PG.SetAllowConnections(ctx, prev, false); err != nil {
		return err
	}
	if stateBefore != StateMaintenance {
		stateBefore = StateActive
		if err := s.PG.SetAllowConnections(ctx, l.DBName, true); err != nil {
			return err
		}
	}
	exp := s.now().Add(time.Duration(s.Settings(ctx).PrevDays) * 24 * time.Hour)
	_, err = s.DB.ExecContext(ctx, `UPDATE libraries SET state = ?, prev_db_name = ?, prev_expires_at = ?, prev_backup_id = ?, schema_fingerprint = ?,
		schema_table_count = ?, fingerprint_checked_at = ?, updated_at = ? WHERE id = ?`,
		string(stateBefore), prev, store.T(exp), preBackupID, fp, tables, store.NowT(), store.NowT(), l.ID)
	return err
}

// Revert swaps back to the state kept before the last restore.
func (s *Service) Revert(ctx context.Context, by audit.Actor, libraryID, confirm string) (*jobs.Job, error) {
	l, err := s.Library(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	if confirm != l.Slug {
		return nil, errors.New("SQS-R002 escribe el identificador de la biblioteca para confirmar")
	}
	if l.PrevDB == "" {
		return nil, errors.New("SQS-R030 no hay un estado anterior que recuperar")
	}
	s.Audit.Record(ctx, by, "restore.revert", "library", l.ID, audit.OK, map[string]any{"prev": l.PrevDB})
	return s.Jobs.Enqueue(ctx, JobRestoreRevert, l.ID, RestoreParams{TerminateSessions: true}, by, jobs.Priority(100))
}

func (s *Service) runRevert(ctx context.Context, r *jobs.Run) error {
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	if l.PrevDB == "" {
		return errors.New("SQS-R030 no hay un estado anterior que recuperar")
	}
	if db, err := s.PG.Database(ctx, l.PrevDB); err != nil || db == nil {
		return fmt.Errorf("SQS-R031 ya no existe la base del estado anterior (%s)", l.PrevDB)
	}
	stateBefore := l.State
	var pre *Backup
	if err := r.Step("respaldo_previo", func() error {
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
	j := swapJournal{Live: l.DBName, Prev: newPrev, Restored: oldPrev, State: string(stateBefore)}
	if err := r.Step("bloquear", func() error {
		s.setState(ctx, l.ID, StateRestoring)
		if err := s.PG.SetAllowConnections(ctx, l.DBName, false); err != nil {
			return err
		}
		_, err := s.PG.TerminateAll(ctx, l.DBName)
		return err
	}); err != nil {
		s.PG.SetAllowConnections(context.WithoutCancel(ctx), l.DBName, stateBefore != StateMaintenance)
		s.setState(context.WithoutCancel(ctx), l.ID, stateBefore)
		return err
	}
	if err := r.Step("intercambiar", func() error {
		j.Phase = "rename_live"
		r.Journal(j)
		if err := s.PG.RenameDatabase(ctx, l.DBName, newPrev); err != nil {
			return err
		}
		j.Phase = "rename_restored"
		r.Journal(j)
		if err := s.PG.RenameDatabase(ctx, oldPrev, l.DBName); err != nil {
			s.PG.RenameDatabase(context.WithoutCancel(ctx), newPrev, l.DBName)
			return err
		}
		j.Phase = "swapped"
		return r.Journal(j)
	}); err != nil {
		s.PG.SetAllowConnections(context.WithoutCancel(ctx), l.DBName, stateBefore != StateMaintenance)
		s.setState(context.WithoutCancel(ctx), l.ID, stateBefore)
		return err
	}
	conn, err := s.PG.ConnectDB(ctx, l.DBName)
	var fp pg.Fingerprint
	if err == nil {
		fp, _ = pg.SchemaFingerprint(ctx, conn)
		conn.Close(context.Background())
	}
	if err := r.Step("reabrir", func() error { return s.reopenAfterSwap(ctx, l, newPrev, pre.ID, stateBefore, fp.Hash, fp.Tables) }); err != nil {
		return err
	}
	s.Events.Emit(ctx, events.Event{Type: events.RestoreReverted, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Se revirtió %q al estado anterior a la última restauración", l.DisplayName)})
	return nil
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
		return nil, errors.New("SQS-B050 nombre de archivo inválido")
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
		return errors.New("SQS-B051 el archivo está fuera de la carpeta de importación")
	}
	var info ArchiveInfo
	if err := r.Step("leer", func() error {
		info, err = s.listArchive(ctx, src)
		return err
	}); err != nil {
		return err
	}
	id := ulid.New()
	now := s.now()
	rel := filepath.Join("libraries", l.Slug, now.UTC().Format("2006"), now.UTC().Format("01"), fmt.Sprintf("%s_%s_%s.backup", l.Slug, now.UTC().Format("20060102T150405Z"), id))
	abs := filepath.Join(s.Cfg.BackupDir, rel)
	moved := false
	err = r.Step("copiar", func() error {
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return err
		}
		// _import lives in the backups volume, so a source that will be
		// deleted anyway is moved instead of copied.
		if p.DeleteSource {
			if err := os.Rename(src, abs+".partial"); err == nil {
				moved = true
				return fsx.SealFile(abs+".partial", abs)
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
		out, err := os.OpenFile(abs+".partial", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, &progressReader{r: in, total: st.Size(), run: r}); err != nil {
			out.Close()
			os.Remove(abs + ".partial")
			return err
		}
		if err := out.Close(); err != nil {
			os.Remove(abs + ".partial")
			return err
		}
		return fsx.SealFile(abs+".partial", abs)
	})
	if err != nil {
		return err
	}
	// Until the catalog row exists, a failure puts the file back where it was.
	undo := func(err error) error {
		if moved {
			os.Rename(abs, src)
		} else {
			os.Remove(abs)
		}
		os.Remove(manifestPath(abs))
		return err
	}
	sum, size, err := fileSHA256(abs)
	if err != nil {
		return undo(err)
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
		store.T(now), store.T(now), store.NowT(), "Importado: índice legible; sin conteos de filas")
	if err != nil {
		return undo(err)
	}
	if p.DeleteSource && !moved {
		os.Remove(src)
	}
	r.Infof("Importado %s (%s, %s)", p.Path, HumanBytes(size), info.DumpedBy)
	r.SetResult(map[string]string{"backup_id": id})
	return nil
}

// Recreate rebuilds a library whose database is missing (for example after
// moving to a new server) from a backup, keeping its slug and, when the
// master key is the same, its passwords: workstations reconnect unchanged.
func (s *Service) Recreate(ctx context.Context, by audit.Actor, libraryID, backupID string) (*jobs.Job, error) {
	l, err := s.Library(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	if l.State != StateMissing {
		return nil, errors.New("SQS-R040 solo se recrean bibliotecas sin base en el servidor")
	}
	if backupID == "" {
		b, err := s.LatestVerified(ctx, l.ID)
		if err != nil {
			return nil, err
		}
		if b == nil {
			return nil, errors.New("SQS-R041 la biblioteca no tiene respaldos verificados")
		}
		backupID = b.ID
	}
	s.Audit.Record(ctx, by, "restore.recreate", "library", l.ID, audit.OK, map[string]any{"backup_id": backupID})
	return s.Jobs.Enqueue(ctx, JobRestoreRecreate, l.ID, RestoreParams{BackupID: backupID}, by, jobs.Priority(100))
}

func (s *Service) runRecreate(ctx context.Context, r *jobs.Run) error {
	var p RestoreParams
	r.Params(&p)
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	b, err := s.Backup(ctx, p.BackupID)
	if err != nil {
		return err
	}
	var m *Manifest
	if err := r.Step("verificar_respaldo", func() error {
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
			if err := s.PG.CreateLibraryRole(ctx, c.PGRole, pg.RoleOptions{Verifier: v, Login: true, ConnLimit: -1}); err != nil {
				return err
			}
			if c.Kind == "member" {
				s.PG.GrantRole(ctx, l.OwnerRole, c.PGRole)
				s.PG.SetRoleDefaultRole(ctx, c.PGRole, l.DBName, l.OwnerRole)
			}
			if kept {
				r.Infof("Rol %s recreado con su contraseña original", c.PGRole)
			} else {
				r.Warnf("Rol %s recreado con contraseña nueva (la clave maestra no coincide)", c.PGRole)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := r.Step("restaurar", func() error {
		if err := s.PG.CreateDatabase(ctx, l.DBName, l.OwnerRole); err != nil {
			return err
		}
		return s.pgRestore(ctx, s.AbsPath(b), l.DBName, l.OwnerRole)
	}); err != nil {
		s.PG.DropDatabase(context.WithoutCancel(ctx), l.DBName)
		return err
	}
	if err := r.Step("comparar", func() error {
		detail, err := s.compareWithManifest(ctx, l.DBName, m)
		if err == nil {
			r.Infof("%s", detail)
		}
		return err
	}); err != nil {
		s.PG.DropDatabase(context.WithoutCancel(ctx), l.DBName)
		return err
	}
	if err := r.Step("activar", func() error {
		roles, err := s.activeRoles(ctx, l.ID)
		if err != nil {
			return err
		}
		if err := s.PG.SecureDatabase(ctx, l.DBName, roles); err != nil {
			return err
		}
		s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'active', schema_fingerprint = ?, schema_table_count = ?, prev_db_name = '', updated_at = ? WHERE id = ?`,
			m.Schema.Fingerprint, m.Schema.Tables, store.NowT(), l.ID)
		if err := s.ensurePolicy(ctx, l.ID); err != nil {
			return err
		}
		return s.SyncHBA(ctx)
	}); err != nil {
		return err
	}
	s.Events.Emit(ctx, events.Event{Type: events.RestoreSucceeded, LibraryID: l.ID, JobID: r.Job.ID,
		Message: fmt.Sprintf("Se recreó %q desde el respaldo del %s", l.DisplayName, b.StartedAt.In(s.Cfg.Location).Format("2006-01-02 15:04"))})
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
