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
	JobBackupExport    = "backup.export"
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
	j.Register(jobs.Spec{Type: JobLibraryCreate, Title: "Create library", Priority: 90, Lock: jobs.LockWrite, Run: s.runLibraryCreate})
	j.Register(jobs.Spec{Type: JobLibraryDelete, Title: "Move to trash", Priority: 90, Lock: jobs.LockWrite, Heavy: true, Run: s.runTrash})
	j.Register(jobs.Spec{Type: JobLibraryUntrash, Title: "Restore from trash", Priority: 90, Lock: jobs.LockWrite, Run: s.runUntrash})
	j.Register(jobs.Spec{Type: JobLibraryPurge, Title: "Purge library", Priority: 40, Lock: jobs.LockWrite, Run: s.runPurge})
	j.Register(jobs.Spec{Type: JobBackupCreate, Title: "Back up", Priority: 50, Lock: jobs.LockRead, Heavy: true, Cancellable: true, Run: s.runBackupCreate})
	j.Register(jobs.Spec{Type: JobBackupVerify, Title: "Verify backup", Priority: 30, Lock: jobs.LockRead, Heavy: true, Cancellable: true, Run: s.runVerify})
	j.Register(jobs.Spec{Type: JobBackupExport, Title: "Prepare export from backup", Priority: 40, Lock: jobs.LockRead, Heavy: true, Cancellable: true, Run: s.runBackupExport})
	j.Register(jobs.Spec{Type: JobBackupImport, Title: "Import backup", Priority: 60, Lock: jobs.LockRead, Heavy: true, Cancellable: true, Run: s.runImport})
	j.Register(jobs.Spec{Type: JobRestoreNew, Title: "Restore as a new library", Priority: 100, Lock: jobs.LockWrite, Heavy: true, Cancellable: true, Run: s.runRestoreNew})
	j.Register(jobs.Spec{Type: JobRestoreReplace, Title: "Restore by replacing", Priority: 100, Lock: jobs.LockWrite, Heavy: true, Run: s.runRestoreReplace})
	j.Register(jobs.Spec{Type: JobRestoreRevert, Title: "Revert restore", Priority: 100, Lock: jobs.LockWrite, Heavy: true, Run: s.runRevert})
	j.Register(jobs.Spec{Type: JobRestoreRecreate, Title: "Recreate from backup", Priority: 100, Lock: jobs.LockWrite, Heavy: true, Run: s.runRecreate})
	j.Register(jobs.Spec{Type: JobRetention, Title: "Apply retention", Priority: 20, Run: s.runRetentionAll})
	j.Register(jobs.Spec{Type: JobVerifyAll, Title: "Verify latest backups", Priority: 30, Run: s.runVerifyAll})
	j.Register(jobs.Spec{Type: JobVacuum, Title: "Optimize libraries", Priority: 10, Heavy: true, Cancellable: true, Run: s.runVacuum})
	j.Register(jobs.Spec{Type: JobControlBackup, Title: "Back up the control plane", Priority: 20, Run: s.runControlBackup})
	j.Register(jobs.Spec{Type: JobCatalogCheck, Title: "Check catalog", Priority: 10, Run: s.runCatalogCheck})
	j.Register(jobs.Spec{Type: JobCatalogRebuild, Title: "Rebuild catalog", Priority: 60, Run: s.runCatalogRebuild})
	j.Register(jobs.Spec{Type: JobPurgeExpired, Title: "Purge expired", Priority: 10, Run: func(ctx context.Context, r *jobs.Run) error { return s.PurgeExpired(ctx, r) }})
	j.Register(jobs.Spec{Type: JobHousekeeping, Title: "Housekeeping", Priority: 5, Run: s.runHousekeeping})
	j.Register(jobs.Spec{Type: JobUpgradeWindow, Title: "Resolve upgrade window", Priority: 95, Run: s.runUpgradeWindow})

	sc := s.Sched
	sc.RegisterKind(ScheduleBackup, scheduler.Kind{JobType: JobBackupCreate, Jitter: true, Title: "backup", Gate: s.BackupGate})
	sc.RegisterKind(ScheduleVerify, scheduler.Kind{JobType: JobBackupVerify, Jitter: true, Title: "verification"})
	for kind, jt := range systemSchedules {
		sc.RegisterKind(kind, scheduler.Kind{JobType: jt.job, Title: jt.title})
	}
}

var systemSchedules = map[string]struct{ job, cron, title string }{
	"system.retention":      {JobRetention, "0 4 * * *", "retention"},
	"system.verify_all":     {JobVerifyAll, "0 5 * * 0", "weekly verification"},
	"system.vacuum":         {JobVacuum, "0 3 * * 0", "optimization"},
	"system.control_backup": {JobControlBackup, "15 4 * * *", "control plane backup"},
	"system.catalog_check":  {JobCatalogCheck, "30 4 * * *", "catalog check"},
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
	r.Infof("Retention applied to %d libraries; %d backups deleted", len(libs), total)
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
			r.Infof("%s optimized", l.Slug)
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
	r.Infof("Control plane copy at %s", filepath.Base(dst))
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
			s.DB.ExecContext(ctx, `UPDATE backups SET verify_status = 'failed', verify_detail = 'The file does not exist on the volume' WHERE id = ?`, b.ID)
			r.Warnf("Missing file for %s", b.FilePath)
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
			Message: fmt.Sprintf("Catalog and files do not match: %d files are missing and %d files are not in the catalog", missing, orphans)})
	}
	r.Infof("%d backups checked; %d missing; %d orphaned", len(bs), missing, orphans)
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
			r.Warnf("Manifest without file: %s", p)
			return nil
		}
		libID := m.Library.ID
		if _, err := s.Library(ctx, libID); err != nil {
			now := store.NowT()
			_, err := s.DB.ExecContext(ctx, `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, notes, created_at, updated_at, purged_at)
				VALUES (?, ?, ?, 'purged', ?, ?, 'restore', ?, ?, ?, ?)`, libID, m.Library.Slug, m.Library.DisplayName, m.Library.Slug, m.Library.Slug,
				"Rebuilt from the manifests on the backups volume", now, now, now)
			if err != nil {
				r.Warnf("Could not register library %s: %v", m.Library.Slug, err)
				return nil
			}
		}
		rel, _ := filepath.Rel(s.Cfg.BackupDir, backupFile)
		_, err = s.DB.ExecContext(ctx, `INSERT INTO backups(id, library_id, kind, status, file_path, size_bytes, sha256, pg_server_version, pg_dump_version, archive_version,
			compression, schema_fingerprint, table_count, counts_exact, resolve_version, started_at, finished_at, verify_status, verify_detail)
			VALUES (?, ?, ?, 'ok', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'none', 'Recovered from the volume; verify it before using it')`,
			m.BackupID, libID, m.Kind, rel, m.Size, m.SHA256, m.Server.PostgresVersion, m.Format.ToolVersion, m.Format.ArchiveVersion,
			m.Format.Compression, m.Schema.Fingerprint, m.Schema.Tables, m.CountsExact, m.ResolveVersion, store.T(m.StartedAt), store.T(m.FinishedAt))
		if err != nil {
			r.Warnf("%s: %v", m.BackupID, err)
			return nil
		}
		if err := s.verifyL1(ctx, m.BackupID); err != nil {
			r.Warnf("%s failed verification: %v", rel, err)
		}
		added++
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	r.Infof("Added %d backups to the catalog", added)
	r.SetResult(map[string]int{"added": added})
	return nil
}

func (s *Service) runHousekeeping(ctx context.Context, r *jobs.Run) error {
	if n, err := s.Jobs.Purge(ctx, s.now().Add(-90*24*time.Hour)); err == nil && n > 0 {
		r.Infof("Deleted %d old jobs", n)
	}
	if n, err := s.Events.Purge(ctx, s.now().Add(-180*24*time.Hour)); err == nil && n > 0 {
		r.Infof("Deleted %d old events", n)
	}
	if n := s.PurgeExportWorkspaces(ctx); n > 0 {
		r.Infof("Closed %d expired exports from backups", n)
	}
	if broken, n, err := s.Audit.Verify(ctx); err == nil {
		if broken != 0 {
			s.Events.Emit(ctx, events.Event{Type: events.AuditChainBroken, Severity: events.Critical,
				Message: fmt.Sprintf("The audit chain is broken at entry %d: someone modified the sqsolve database", broken)})
		} else {
			r.Infof("Audit log intact (%d entries)", n)
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
		if b != nil && s.now().Sub(b.StartedAt) > time.Duration(hours)*time.Hour && s.UnchangedSince(ctx, l) != nil {
			// Old but still current: nothing changed since it was taken.
			continue
		}
		if b == nil || s.now().Sub(b.StartedAt) > time.Duration(hours)*time.Hour {
			s.Events.Emit(ctx, events.Event{Type: events.BackupStale, Severity: events.Warning, LibraryID: l.ID,
				Message: fmt.Sprintf("%q has no verified backup in the last %d hours", l.DisplayName, hours)})
		}
	}
	return nil
}

// UpgradeWindow starts the Resolve upgrade procedure.
func (s *Service) UpgradeWindow(ctx context.Context, by audit.Actor, target string) (*jobs.Job, error) {
	target = strings.TrimSpace(target)
	if target == "" || len(target) > 20 {
		return nil, errors.New("SQS-C040 type the target Resolve version")
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
		err := r.Step("backup_"+l.Slug, func() error {
			var err error
			b, err = s.createBackup(ctx, nil, l, KindPreUpgrade, "")
			if err != nil {
				return err
			}
			return s.verifyL2(ctx, r, b.ID)
		})
		if err != nil {
			return fmt.Errorf("SQS-B060 the window stops: %s was not backed up and verified: %w", l.Slug, err)
		}
		out = append(out, res{l.Slug, b.ID, l.Fingerprint})
		r.Progress((i + 1) * 100 / len(libs))
	}
	r.SetResult(map[string]any{"target": p.Target, "libraries": out})
	r.Infof("All libraries have a verified backup taken before Resolve %s. Workstations can now be upgraded", p.Target)
	return nil
}
