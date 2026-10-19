// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/scheduler"
	"github.com/donutinit/sqsolve/internal/store"
)

// Job types.
const (
	JobLibraryCreate   = "library.create"
	JobLibraryDelete   = "library.delete"
	JobLibraryUntrash  = "library.untrash"
	JobLibraryPurge    = "library.purge"
	JobBackupCreate    = "backup.create"
	JobBackupVerify    = "backup.verify"
	JobBackupImport    = "backup.import"
	JobRestoreNew      = "restore.new"
	JobRestoreReplace  = "restore.replace"
	JobRestoreRevert   = "restore.revert"
	JobRestoreRecreate = "restore.recreate"
	JobRetention       = "system.retention"
	JobVerifyAll       = "system.verify_all"
	JobVacuum          = "system.vacuum"
	JobControlBackup   = "system.control_backup"
	JobCatalogCheck    = "system.catalog_check"
	JobCatalogRebuild  = "system.catalog_rebuild"
	JobPurgeExpired    = "system.purge"
	JobHousekeeping    = "system.housekeeping"
	JobUpgradeWindow   = "system.upgrade_window"
)

// Schedule kinds.
const (
	ScheduleBackup = "backup"
	ScheduleVerify = "verify"
)

// Register adds every job type and schedule kind.
func (s *Service) Register() {
	j := s.Jobs
	j.Register(jobs.Spec{Type: JobLibraryCreate, Title: "Crear biblioteca", Priority: 90, Lock: jobs.LockWrite, Run: s.runLibraryCreate})
	j.Register(jobs.Spec{Type: JobLibraryDelete, Title: "Mover a la papelera", Priority: 90, Lock: jobs.LockWrite, Heavy: true, Run: s.runTrash})
	j.Register(jobs.Spec{Type: JobLibraryUntrash, Title: "Recuperar de la papelera", Priority: 90, Lock: jobs.LockWrite, Run: s.runUntrash})
	j.Register(jobs.Spec{Type: JobLibraryPurge, Title: "Purgar biblioteca", Priority: 40, Lock: jobs.LockWrite, Run: s.runPurge})
	j.Register(jobs.Spec{Type: JobBackupCreate, Title: "Respaldar", Priority: 50, Lock: jobs.LockRead, Heavy: true, Cancellable: true, Run: s.runBackupCreate})
	j.Register(jobs.Spec{Type: JobBackupVerify, Title: "Verificar respaldo", Priority: 30, Lock: jobs.LockRead, Heavy: true, Cancellable: true, Run: s.runVerify})
	j.Register(jobs.Spec{Type: JobBackupImport, Title: "Importar respaldo", Priority: 60, Lock: jobs.LockRead, Heavy: true, Cancellable: true, Run: s.runImport})
	j.Register(jobs.Spec{Type: JobRestoreNew, Title: "Restaurar como biblioteca nueva", Priority: 100, Lock: jobs.LockWrite, Heavy: true, Cancellable: true, Run: s.runRestoreNew})
	j.Register(jobs.Spec{Type: JobRestoreReplace, Title: "Restaurar reemplazando", Priority: 100, Lock: jobs.LockWrite, Heavy: true, Run: s.runRestoreReplace})
	j.Register(jobs.Spec{Type: JobRestoreRevert, Title: "Revertir restauración", Priority: 100, Lock: jobs.LockWrite, Heavy: true, Run: s.runRevert})
	j.Register(jobs.Spec{Type: JobRestoreRecreate, Title: "Recrear desde respaldo", Priority: 100, Lock: jobs.LockWrite, Heavy: true, Run: s.runRecreate})
	j.Register(jobs.Spec{Type: JobRetention, Title: "Aplicar retención", Priority: 20, Run: s.runRetentionAll})
	j.Register(jobs.Spec{Type: JobVerifyAll, Title: "Verificar últimos respaldos", Priority: 30, Run: s.runVerifyAll})
	j.Register(jobs.Spec{Type: JobVacuum, Title: "Optimizar bibliotecas", Priority: 10, Heavy: true, Cancellable: true, Run: s.runVacuum})
	j.Register(jobs.Spec{Type: JobControlBackup, Title: "Respaldar el plano de control", Priority: 20, Run: s.runControlBackup})
	j.Register(jobs.Spec{Type: JobCatalogCheck, Title: "Revisar catálogo", Priority: 10, Run: s.runCatalogCheck})
	j.Register(jobs.Spec{Type: JobCatalogRebuild, Title: "Reconstruir catálogo", Priority: 60, Run: s.runCatalogRebuild})
	j.Register(jobs.Spec{Type: JobPurgeExpired, Title: "Purgar vencidos", Priority: 10, Run: func(ctx context.Context, r *jobs.Run) error { return s.PurgeExpired(ctx, r) }})
	j.Register(jobs.Spec{Type: JobHousekeeping, Title: "Limpieza", Priority: 5, Run: s.runHousekeeping})
	j.Register(jobs.Spec{Type: JobUpgradeWindow, Title: "Ventana de actualización de Resolve", Priority: 95, Run: s.runUpgradeWindow})

	sc := s.Sched
	sc.RegisterKind(ScheduleBackup, scheduler.Kind{JobType: JobBackupCreate, Jitter: true, Title: "respaldo"})
	sc.RegisterKind(ScheduleVerify, scheduler.Kind{JobType: JobBackupVerify, Jitter: true, Title: "verificación"})
	for kind, jt := range systemSchedules {
		sc.RegisterKind(kind, scheduler.Kind{JobType: jt.job, Title: jt.title})
	}
}

var systemSchedules = map[string]struct{ job, cron, title string }{
	"system.retention":      {JobRetention, "0 4 * * *", "retención"},
	"system.verify_all":     {JobVerifyAll, "0 5 * * 0", "verificación semanal"},
	"system.vacuum":         {JobVacuum, "0 3 * * 0", "optimización"},
	"system.control_backup": {JobControlBackup, "15 4 * * *", "respaldo del plano de control"},
	"system.catalog_check":  {JobCatalogCheck, "30 4 * * *", "revisión del catálogo"},
	"system.purge":          {JobPurgeExpired, "20 4 * * *", "purga"},
	"system.housekeeping":   {JobHousekeeping, "45 4 * * *", "limpieza"},
}

// EnsureSystemSchedules creates the default system schedules once.
func (s *Service) EnsureSystemSchedules(ctx context.Context) error {
	tz := s.Cfg.Location.String()
	for kind, d := range systemSchedules {
		existing, err := s.Sched.Get(ctx, kind, "")
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}
		if err := s.Sched.Upsert(ctx, kind, "", d.cron, tz, true, 24*time.Hour); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) runRetentionAll(ctx context.Context, r *jobs.Run) error {
	libs, err := s.Libraries(ctx, ListOptions{IncludePurged: true})
	if err != nil {
		return err
	}
	total := 0
	for _, l := range libs {
		n, err := s.applyRetention(ctx, l.ID)
		if err != nil {
			r.Warnf("%s: %v", l.Slug, err)
			continue
		}
		total += n
	}
	r.Infof("Retención aplicada a %d bibliotecas; %d respaldos borrados", len(libs), total)
	return nil
}

func (s *Service) runVerifyAll(ctx context.Context, r *jobs.Run) error {
	libs, err := s.Libraries(ctx, ListOptions{States: []State{StateActive, StateMaintenance}})
	if err != nil {
		return err
	}
	for _, l := range libs {
		b, err := s.LatestVerified(ctx, l.ID)
		if err != nil || b == nil {
			continue
		}
		if _, err := s.Jobs.Enqueue(ctx, JobBackupVerify, l.ID, VerifyParams{BackupID: b.ID}, audit.System); err != nil {
			r.Warnf("%s: %v", l.Slug, err)
		}
	}
	return nil
}

func (s *Service) runVacuum(ctx context.Context, r *jobs.Run) error {
	libs, err := s.Libraries(ctx, ListOptions{States: []State{StateActive, StateMaintenance}})
	if err != nil {
		return err
	}
	for i, l := range libs {
		if err := ctx.Err(); err != nil {
			return err
		}
		args := append(s.PG.ConnArgs(l.DBName), "--analyze", "--skip-locked")
		if _, err := s.Exec.Run(ctx, execxCmd("vacuumdb", args, s)); err != nil {
			r.Warnf("%s: %v", l.Slug, err)
		} else {
			r.Infof("%s optimizada", l.Slug)
		}
		r.Progress((i + 1) * 100 / max(1, len(libs)))
	}
	return nil
}

func (s *Service) runControlBackup(ctx context.Context, r *jobs.Run) error {
	if err := s.BackupVolumeOK(); err != nil {
		return err
	}
	dir := filepath.Join(s.Cfg.BackupDir, "_system")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	now := s.now().UTC()
	dst := filepath.Join(dir, "sqsolve-state-"+now.Format("20060102T150405Z")+".db")
	if err := s.DB.BackupTo(ctx, dst); err != nil {
		return err
	}
	// Current month of the audit log, rewritten atomically.
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var buf strings.Builder
	if _, err := s.Audit.Export(ctx, &buf, start, start.AddDate(0, 1, 0)); err == nil {
		fsx.WriteFileAtomic(filepath.Join(dir, "audit-"+now.Format("200601")+".jsonl"), []byte(buf.String()), 0o600)
	}
	// Keep the newest 30 copies.
	matches, _ := filepath.Glob(filepath.Join(dir, "sqsolve-state-*.db"))
	sort.Strings(matches)
	for i := 0; i < len(matches)-30; i++ {
		os.Remove(matches[i])
	}
	r.Infof("Copia del plano de control en %s", filepath.Base(dst))
	return nil
}

// runCatalogCheck compares the catalog with the files on disk.
func (s *Service) runCatalogCheck(ctx context.Context, r *jobs.Run) error {
	if err := s.BackupVolumeOK(); err != nil {
		return err
	}
	bs, err := s.Backups(ctx, BackupFilter{Statuses: []string{BackupOK}})
	if err != nil {
		return err
	}
	missing := 0
	known := map[string]bool{}
	for _, b := range bs {
		known[b.FilePath] = true
		if _, err := os.Stat(s.AbsPath(b)); errors.Is(err, os.ErrNotExist) {
			missing++
			s.DB.ExecContext(ctx, `UPDATE backups SET verify_status = 'failed', verify_detail = 'El archivo no existe en el volumen' WHERE id = ?`, b.ID)
			r.Warnf("Falta el archivo de %s", b.FilePath)
		}
	}
	orphans := 0
	root := filepath.Join(s.Cfg.BackupDir, "libraries")
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".backup") {
			return nil
		}
		rel, _ := filepath.Rel(s.Cfg.BackupDir, p)
		if !known[rel] {
			orphans++
		}
		return nil
	})
	if missing > 0 || orphans > 0 {
		s.Events.Emit(ctx, events.Event{Type: "backup.catalog_mismatch", Severity: events.Warning,
			Message: fmt.Sprintf("Catálogo y archivos no coinciden: %d archivos faltan y %d archivos no están en el catálogo", missing, orphans)})
	}
	r.Infof("%d respaldos revisados; %d faltan; %d huérfanos", len(bs), missing, orphans)
	return nil
}

// runCatalogRebuild adds backups found on disk to the catalog.
func (s *Service) runCatalogRebuild(ctx context.Context, r *jobs.Run) error {
	if err := s.BackupVolumeOK(); err != nil {
		return err
	}
	root := filepath.Join(s.Cfg.BackupDir, "libraries")
	added := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".manifest.json") {
			return nil
		}
		m, err := ReadManifest(p)
		if err != nil {
			r.Warnf("%s: %v", p, err)
			return nil
		}
		if _, err := s.Backup(ctx, m.BackupID); err == nil {
			return nil
		}
		backupFile := filepath.Join(filepath.Dir(p), m.File)
		if _, err := os.Stat(backupFile); err != nil {
			r.Warnf("Manifiesto sin archivo: %s", p)
			return nil
		}
		libID := m.Library.ID
		if _, err := s.Library(ctx, libID); err != nil {
			now := store.NowT()
			_, err := s.DB.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, notes, created_at, updated_at, purged_at)
				VALUES (?, ?, ?, 'purged', ?, ?, 'restore', ?, ?, ?, ?)`, libID, m.Library.Slug, m.Library.DisplayName, m.Library.Slug, m.Library.Slug,
				"Reconstruida desde los manifiestos del volumen de respaldos", now, now, now)
			if err != nil {
				r.Warnf("No se pudo registrar la biblioteca %s: %v", m.Library.Slug, err)
				return nil
			}
		}
		rel, _ := filepath.Rel(s.Cfg.BackupDir, backupFile)
		_, err = s.DB.ExecContext(ctx, `INSERT INTO backups(id, library_id, kind, status, file_path, size_bytes, sha256, pg_server_version, pg_dump_version, archive_version,
			compression, schema_fingerprint, table_count, counts_exact, resolve_version, started_at, finished_at, verify_status, verify_detail)
			VALUES (?, ?, ?, 'ok', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'none', 'Recuperado del volumen; verifícalo antes de usarlo')`,
			m.BackupID, libID, m.Kind, rel, m.Size, m.SHA256, m.Server.PostgresVersion, m.Format.ToolVersion, m.Format.ArchiveVersion,
			m.Format.Compression, m.Schema.Fingerprint, m.Schema.Tables, m.CountsExact, m.ResolveVersion, store.T(m.StartedAt), store.T(m.FinishedAt))
		if err != nil {
			r.Warnf("%s: %v", m.BackupID, err)
			return nil
		}
		if err := s.verifyL1(ctx, m.BackupID); err != nil {
			r.Warnf("%s no pasó la verificación: %v", rel, err)
		}
		added++
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	r.Infof("Se agregaron %d respaldos al catálogo", added)
	r.SetResult(map[string]int{"added": added})
	return nil
}

func (s *Service) runHousekeeping(ctx context.Context, r *jobs.Run) error {
	if n, err := s.Jobs.Purge(ctx, s.now().Add(-90*24*time.Hour)); err == nil && n > 0 {
		r.Infof("Se borraron %d tareas antiguas", n)
	}
	if n, err := s.Events.Purge(ctx, s.now().Add(-180*24*time.Hour)); err == nil && n > 0 {
		r.Infof("Se borraron %d eventos antiguos", n)
	}
	if broken, n, err := s.Audit.Verify(ctx); err == nil {
		if broken != 0 {
			s.Events.Emit(ctx, events.Event{Type: events.AuditChainBroken, Severity: events.Critical,
				Message: fmt.Sprintf("La cadena de auditoría está rota en la entrada %d: alguien modificó la base de sqsolve", broken)})
		} else {
			r.Infof("Auditoría íntegra (%d entradas)", n)
		}
	}
	if err := s.DB.IntegrityCheck(ctx); err != nil {
		return err
	}
	return s.CheckStale(ctx)
}

// CheckStale warns about live libraries without a recent verified backup.
func (s *Service) CheckStale(ctx context.Context) error {
	hours := s.Settings(ctx).StaleHours
	libs, err := s.Libraries(ctx, ListOptions{States: []State{StateActive, StateMaintenance}})
	if err != nil {
		return err
	}
	for _, l := range libs {
		if s.now().Sub(l.CreatedAt) < time.Duration(hours)*time.Hour {
			continue
		}
		b, _ := s.LatestVerified(ctx, l.ID)
		if b == nil || s.now().Sub(b.StartedAt) > time.Duration(hours)*time.Hour {
			s.Events.Emit(ctx, events.Event{Type: events.BackupStale, Severity: events.Warning, LibraryID: l.ID,
				Message: fmt.Sprintf("%q no tiene un respaldo verificado en las últimas %d horas", l.DisplayName, hours)})
		}
	}
	return nil
}

// UpgradeWindow starts the Resolve upgrade procedure.
func (s *Service) UpgradeWindow(ctx context.Context, by audit.Actor, target string) (*jobs.Job, error) {
	target = strings.TrimSpace(target)
	if target == "" || len(target) > 20 {
		return nil, errors.New("SQS-C040 escribe la versión de Resolve de destino")
	}
	s.Audit.Record(ctx, by, "system.upgrade_window", "system", "", audit.OK, map[string]any{"target": target})
	return s.Jobs.Enqueue(ctx, JobUpgradeWindow, "", map[string]string{"target": target}, by)
}

func (s *Service) runUpgradeWindow(ctx context.Context, r *jobs.Run) error {
	var p struct{ Target string }
	r.Params(&p)
	libs, err := s.Libraries(ctx, ListOptions{States: []State{StateActive, StateMaintenance}})
	if err != nil {
		return err
	}
	type res struct {
		Slug        string `json:"slug"`
		BackupID    string `json:"backup_id"`
		Fingerprint string `json:"fingerprint_before"`
	}
	var out []res
	for i, l := range libs {
		var b *Backup
		err := r.Step("respaldo_"+l.Slug, func() error {
			var err error
			b, err = s.createBackup(ctx, nil, l, KindPreUpgrade, "")
			if err != nil {
				return err
			}
			return s.verifyL2(ctx, r, b.ID)
		})
		if err != nil {
			return fmt.Errorf("SQS-B060 la ventana se detiene: %s no quedó respaldada y verificada: %w", l.Slug, err)
		}
		out = append(out, res{l.Slug, b.ID, l.Fingerprint})
		r.Progress((i + 1) * 100 / len(libs))
	}
	r.SetResult(map[string]any{"target": p.Target, "libraries": out})
	r.Infof("Todas las bibliotecas tienen un respaldo verificado previo a Resolve %s. Ya pueden actualizar las estaciones", p.Target)
	return nil
}
