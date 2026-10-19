// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// Backup kinds.
const (
	KindManual     = "manual"
	KindScheduled  = "scheduled"
	KindPreRestore = "pre_restore"
	KindPreDelete  = "pre_delete"
	KindPreUpgrade = "pre_upgrade"
	KindImported   = "imported"
)

// KindLabel in Spanish.
func KindLabel(k string) string {
	switch k {
	case KindManual:
		return "Manual"
	case KindScheduled:
		return "Programado"
	case KindPreRestore:
		return "Antes de restaurar"
	case KindPreDelete:
		return "Antes de eliminar"
	case KindPreUpgrade:
		return "Antes de actualizar Resolve"
	case KindImported:
		return "Importado"
	}
	return k
}

// Backup statuses and verification states.
const (
	BackupRunning     = "running"
	BackupOK          = "ok"
	BackupFailed      = "failed"
	BackupInterrupted = "interrupted"
	BackupDeleted     = "deleted"

	VerifyNone    = "none"
	VerifyL1OK    = "l1_ok"
	VerifyL2OK    = "l2_ok"
	VerifyFailed  = "failed"
	VerifyPartial = "partial"
)

// Backup is a catalog entry.
type Backup struct {
	ID              string
	LibraryID       string
	JobID           string
	Kind            string
	Status          string
	VerifyLevel     int
	VerifyStatus    string
	VerifiedAt      *time.Time
	VerifyDetail    string
	FilePath        string // relative to the backup volume
	SizeBytes       int64
	SHA256          string
	PGServerVersion string
	PGDumpVersion   string
	ArchiveVersion  string
	Compression     string
	Fingerprint     string
	TableCount      int
	CountsExact     bool
	ResolveVersion  string
	Pinned          bool
	SourceDB        string
	StartedAt       time.Time
	FinishedAt      *time.Time
	Error           string
	ExpiredReason   string
	DeletedAt       *time.Time
}

// Verified reports whether a verification passed.
func (b *Backup) Verified() bool {
	return b.VerifyStatus == VerifyL1OK || b.VerifyStatus == VerifyL2OK || b.VerifyStatus == VerifyPartial
}

// Usable reports whether the backup can be restored.
func (b *Backup) Usable() bool { return b.Status == BackupOK && b.VerifyStatus != VerifyFailed }

// Duration of the backup.
func (b *Backup) Duration() time.Duration {
	if b.FinishedAt == nil {
		return 0
	}
	return b.FinishedAt.Sub(b.StartedAt)
}

const backupCols = `id, library_id, job_id, kind, status, verify_level, verify_status, verified_at, verify_detail, file_path, size_bytes, sha256,
	pg_server_version, pg_dump_version, archive_version, compression, schema_fingerprint, table_count, counts_exact, resolve_version,
	pinned, source_db, started_at, finished_at, error, expired_reason, deleted_at`

func scanBackup(row interface{ Scan(...any) error }) (*Backup, error) {
	var b Backup
	var verified, finished, deleted sql.NullString
	var started string
	err := row.Scan(&b.ID, &b.LibraryID, &b.JobID, &b.Kind, &b.Status, &b.VerifyLevel, &b.VerifyStatus, &verified, &b.VerifyDetail, &b.FilePath,
		&b.SizeBytes, &b.SHA256, &b.PGServerVersion, &b.PGDumpVersion, &b.ArchiveVersion, &b.Compression, &b.Fingerprint, &b.TableCount,
		&b.CountsExact, &b.ResolveVersion, &b.Pinned, &b.SourceDB, &started, &finished, &b.Error, &b.ExpiredReason, &deleted)
	if err != nil {
		return nil, err
	}
	b.VerifiedAt, b.FinishedAt, b.DeletedAt = store.NullT(verified), store.NullT(finished), store.NullT(deleted)
	b.StartedAt = store.ParseT(started)
	return &b, nil
}

// ErrBackupNotFound is returned for unknown backups.
var ErrBackupNotFound = errors.New("SQS-B001 respaldo no encontrado")

// Backup returns one backup.
func (s *Service) Backup(ctx context.Context, id string) (*Backup, error) {
	b, err := scanBackup(s.DB.QueryRowContext(ctx, `SELECT `+backupCols+` FROM backups WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBackupNotFound
	}
	return b, err
}

// BackupFilter filters the catalog.
type BackupFilter struct {
	LibraryID      string
	Statuses       []string
	IncludeDeleted bool
	Limit          int
	Before         string
}

// Backups lists backups, newest first.
func (s *Service) Backups(ctx context.Context, f BackupFilter) ([]*Backup, error) {
	q := `SELECT ` + backupCols + ` FROM backups WHERE 1=1`
	var args []any
	if f.LibraryID != "" {
		q += ` AND library_id = ?`
		args = append(args, f.LibraryID)
	}
	if len(f.Statuses) > 0 {
		q += ` AND status IN (?` + strings.Repeat(",?", len(f.Statuses)-1) + `)`
		for _, st := range f.Statuses {
			args = append(args, st)
		}
	} else if !f.IncludeDeleted {
		q += ` AND status <> 'deleted'`
	}
	if f.Before != "" {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	q += ` ORDER BY started_at DESC, id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Backup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// LatestVerified returns the newest usable verified backup of a library.
func (s *Service) LatestVerified(ctx context.Context, libraryID string) (*Backup, error) {
	b, err := scanBackup(s.DB.QueryRowContext(ctx, `SELECT `+backupCols+` FROM backups WHERE library_id = ? AND status = 'ok'
		AND verify_status IN ('l1_ok', 'l2_ok', 'partial') ORDER BY started_at DESC LIMIT 1`, libraryID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

// AbsPath returns the absolute path of a backup file.
func (s *Service) AbsPath(b *Backup) string { return filepath.Join(s.Cfg.BackupDir, b.FilePath) }

func manifestPath(path string) string { return strings.TrimSuffix(path, ".backup") + ".manifest.json" }

// Manifest is written next to each backup (docs 10 §8.1).
type Manifest struct {
	ManifestVersion int    `json:"manifest_version"`
	BackupID        string `json:"backup_id"`
	Library         struct {
		ID          string `json:"id"`
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
	} `json:"library"`
	Kind   string `json:"kind"`
	File   string `json:"file"`
	Size   int64  `json:"size_bytes"`
	SHA256 string `json:"sha256"`
	Format struct {
		Tool           string `json:"tool"`
		ToolVersion    string `json:"tool_version"`
		Format         string `json:"format"`
		Compression    string `json:"compression"`
		ArchiveVersion string `json:"archive_version"`
	} `json:"format"`
	Server struct {
		PostgresVersion string `json:"postgres_version"`
		DatabaseSize    int64  `json:"database_size_bytes"`
	} `json:"server"`
	Schema struct {
		Fingerprint string `json:"fingerprint"`
		Tables      int    `json:"tables"`
	} `json:"schema"`
	ResolveVersion string           `json:"resolve_version_declared"`
	CountsExact    bool             `json:"counts_exact"`
	RowCounts      map[string]int64 `json:"row_counts"`
	StartedAt      time.Time        `json:"started_at"`
	FinishedAt     time.Time        `json:"finished_at"`
	SqsolveVersion string           `json:"sqsolve_version"`
	InstanceID     string           `json:"instance_id"`
}

// ReadManifest loads the manifest of a backup.
func ReadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("SQS-B040 manifiesto ilegible: %w", err)
	}
	return &m, nil
}

// Version of sqsolve, set by main.
var Version = "dev"

// BackupParams are the parameters of backup.create.
type BackupParams struct {
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
}

// BackupVolumeOK checks the marker of the backup volume.
func (s *Service) BackupVolumeOK() error {
	return fsx.CheckMarker(s.Cfg.BackupDir, s.InstanceID)
}

// ArchiveInfo is read from pg_restore --list.
type ArchiveInfo struct {
	ArchiveVersion string
	Compression    string
	DumpedFrom     string
	DumpedBy       string
	Format         string
	Entries        int
}

// ParseArchiveHeader reads the header comment of pg_restore --list.
func ParseArchiveHeader(out string) ArchiveInfo {
	var a ArchiveInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, ";"))
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Dump Version":
			a.ArchiveVersion = v
		case "Compression":
			a.Compression = v
		case "Dumped from database version":
			a.DumpedFrom = v
		case "Dumped by pg_dump version":
			a.DumpedBy = v
		case "Format":
			a.Format = v
		case "TOC Entries":
			fmt.Sscan(v, &a.Entries)
		}
	}
	return a
}

func (s *Service) listArchive(ctx context.Context, path string) (ArchiveInfo, error) {
	res, err := s.Exec.Run(ctx, execx.Cmd{Name: "pg_restore", Args: []string{"--list", path}, MaxOutput: 1 << 20})
	if err != nil {
		return ArchiveInfo{}, fmt.Errorf("SQS-B041 pg_restore no puede leer el archivo: %w", err)
	}
	info := ParseArchiveHeader(res.Stdout)
	if info.Format == "" {
		return info, errors.New("SQS-B041 el archivo no parece un respaldo de pg_dump en formato custom")
	}
	return info, nil
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// createBackup dumps a library database. It is called from jobs.
func (s *Service) createBackup(ctx context.Context, r *jobs.Run, l *Library, kind, dbName string) (*Backup, error) {
	if dbName == "" {
		dbName = l.DBName
	}
	if err := s.BackupVolumeOK(); err != nil {
		s.Events.Emit(ctx, events.Event{Type: events.StorageVolumeMissing, Severity: events.Critical, LibraryID: l.ID,
			Message: "El volumen de respaldos no está montado o no es de esta instalación: " + err.Error()})
		return nil, err
	}
	id := ulid.New()
	start := s.now()
	stamp := start.UTC().Format("20060102T150405Z")
	rel := filepath.Join("libraries", l.Slug, start.UTC().Format("2006"), start.UTC().Format("01"), fmt.Sprintf("%s_%s_%s.backup", l.Slug, stamp, id))
	abs := filepath.Join(s.Cfg.BackupDir, rel)
	partial := abs + ".partial"
	b := &Backup{ID: id, LibraryID: l.ID, Kind: kind, Status: BackupRunning, FilePath: rel, StartedAt: start, SourceDB: dbName, ResolveVersion: l.ResolveVersion}
	if r != nil {
		b.JobID = r.Job.ID
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO backups(id, library_id, job_id, kind, status, file_path, source_db, resolve_version, started_at) VALUES (?, ?, ?, ?, 'running', ?, ?, ?, ?)`,
		b.ID, b.LibraryID, b.JobID, b.Kind, b.FilePath, b.SourceDB, b.ResolveVersion, store.T(start))
	if err != nil {
		return nil, err
	}
	fail := func(cause error) (*Backup, error) {
		os.Remove(partial)
		os.Remove(abs)
		os.Remove(manifestPath(abs))
		s.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE backups SET status = 'failed', finished_at = ?, error = ? WHERE id = ?`, store.NowT(), cause.Error(), b.ID)
		s.Events.Emit(ctx, events.Event{Type: events.BackupFailed, Severity: events.Error, LibraryID: l.ID, JobID: b.JobID,
			Message: fmt.Sprintf("Falló el respaldo de %q: %s", l.DisplayName, shortErr(cause))})
		return nil, cause
	}
	step := func(name string, fn func() error) error {
		if r == nil {
			return fn()
		}
		return r.Step(name, fn)
	}
	logf := func(format string, args ...any) {
		if r != nil {
			r.Infof(format, args...)
		}
	}

	var dbSize int64
	err = step("preparar", func() error {
		info, err := s.PG.Database(ctx, dbName)
		if err != nil {
			return err
		}
		if info == nil {
			return fmt.Errorf("SQS-B002 no existe la base %s", dbName)
		}
		dbSize = info.SizeBytes
		need := dbSize / 2
		if last, _ := s.LatestVerified(ctx, l.ID); last != nil && last.SizeBytes*3/2 > need {
			need = last.SizeBytes * 3 / 2
		}
		sp, err := fsx.Statfs(s.Cfg.BackupDir)
		if err != nil {
			return err
		}
		if uint64(need) > sp.Free {
			return fmt.Errorf("SQS-B012 espacio insuficiente en el volumen de respaldos: se estiman %s y hay %s libres", HumanBytes(need), HumanBytes(int64(sp.Free)))
		}
		return os.MkdirAll(filepath.Dir(abs), 0o700)
	})
	if err != nil {
		return fail(err)
	}
	if r != nil {
		r.Progress(10)
	}

	var counts map[string]int64
	var exact = true
	var fp pg.Fingerprint
	var serverVersion string
	err = step("volcar", func() error {
		conn, err := s.PG.ConnectDB(ctx, dbName)
		if err != nil {
			return err
		}
		defer conn.Close(context.Background())
		tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return err
		}
		defer tx.Rollback(context.Background())
		var snapshot string
		if err := tx.QueryRow(ctx, `SELECT pg_export_snapshot()`).Scan(&snapshot); err != nil {
			return err
		}
		tx.QueryRow(ctx, `SHOW server_version`).Scan(&serverVersion)
		if i := strings.IndexByte(serverVersion, ' '); i > 0 {
			serverVersion = serverVersion[:i]
		}
		args := append(s.PG.ConnArgs(dbName), "--format=custom", "--quote-all-identifiers", "--snapshot="+snapshot,
			"--lock-wait-timeout=60000", "--file="+partial)
		var wg sync.WaitGroup
		var dumpErr error
		dumped, ticking := make(chan struct{}), make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(dumped)
			_, dumpErr = s.Exec.Run(ctx, execx.Cmd{Name: "pg_dump", Args: args, Credential: s.PG.AdminCredential()})
		}()
		if r != nil {
			// The archive grows while pg_dump runs; compare it with the last
			// backup of this library, or a quarter of the database size.
			expected := dbSize / 4
			if last, _ := s.LatestVerified(ctx, l.ID); last != nil && last.SizeBytes > 0 {
				expected = last.SizeBytes
			}
			go func() {
				defer close(ticking)
				t := time.NewTicker(2 * time.Second)
				defer t.Stop()
				for {
					select {
					case <-dumped:
						return
					case <-t.C:
						if st, err := os.Stat(partial); err == nil && expected > 0 {
							frac := float64(st.Size()) / float64(expected)
							if frac > 0.95 {
								frac = 0.95
							}
							r.Progress(10 + int(frac*55))
						}
					}
				}
			}()
		} else {
			close(ticking)
		}
		tables, estimate, cerr := pg.PublicTables(ctx, tx.Conn())
		if cerr == nil {
			exact = estimate <= 10_000_000
			counts, cerr = pg.CountRows(ctx, tx, tables, exact)
		}
		if cerr == nil {
			fp, cerr = pg.SchemaFingerprint(ctx, tx.Conn())
		}
		wg.Wait()
		<-ticking
		if dumpErr != nil {
			return fmt.Errorf("SQS-B010 pg_dump falló: %w", dumpErr)
		}
		if cerr != nil {
			return fmt.Errorf("SQS-B011 conteo de filas: %w", cerr)
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		return fail(err)
	}
	if r != nil {
		r.Progress(70)
	}

	var info ArchiveInfo
	err = step("sellar", func() error {
		if err := fsx.SealFile(partial, abs); err != nil {
			return err
		}
		sum, size, err := fileSHA256(abs)
		if err != nil {
			return err
		}
		b.SHA256, b.SizeBytes = sum, size
		info, err = s.listArchive(ctx, abs)
		if err != nil {
			return err
		}
		finished := s.now()
		m := Manifest{ManifestVersion: 1, BackupID: b.ID, Kind: kind, File: filepath.Base(abs), Size: size, SHA256: sum,
			ResolveVersion: l.ResolveVersion, CountsExact: exact, RowCounts: counts, StartedAt: start.UTC(), FinishedAt: finished.UTC(),
			SqsolveVersion: Version, InstanceID: s.InstanceID}
		m.Library.ID, m.Library.Slug, m.Library.DisplayName = l.ID, l.Slug, l.DisplayName
		m.Format.Tool, m.Format.ToolVersion, m.Format.Format, m.Format.Compression, m.Format.ArchiveVersion = "pg_dump", info.DumpedBy, "custom", info.Compression, info.ArchiveVersion
		m.Server.PostgresVersion, m.Server.DatabaseSize = serverVersion, dbSize
		m.Schema.Fingerprint, m.Schema.Tables = fp.Hash, fp.Tables
		mb, _ := json.MarshalIndent(m, "", "  ")
		if err := fsx.WriteFileAtomic(manifestPath(abs), append(mb, '\n'), 0o600); err != nil {
			return err
		}
		os.Chmod(abs, 0o600)
		_, err = s.DB.ExecContext(ctx, `UPDATE backups SET status = 'ok', finished_at = ?, size_bytes = ?, sha256 = ?, pg_server_version = ?, pg_dump_version = ?,
			archive_version = ?, compression = ?, schema_fingerprint = ?, table_count = ?, counts_exact = ? WHERE id = ?`,
			store.T(finished), size, sum, serverVersion, info.DumpedBy, info.ArchiveVersion, info.Compression, fp.Hash, fp.Tables, exact, b.ID)
		logf("Respaldo de %s escrito (%s)", dbName, HumanBytes(size))
		return err
	})
	if err != nil {
		return fail(err)
	}
	if r != nil {
		r.Progress(90)
	}
	err = step("verificar_l1", func() error { return s.verifyL1(ctx, b.ID) })
	if err != nil {
		return fail(err)
	}
	if dbName == l.DBName && fp.Hash != "" {
		s.recordFingerprint(ctx, l, fp, b.JobID)
	}
	s.Events.Emit(ctx, events.Event{Type: events.BackupSucceeded, LibraryID: l.ID, JobID: b.JobID,
		Message: fmt.Sprintf("Respaldo de %q correcto (%s)", l.DisplayName, HumanBytes(b.SizeBytes)), Data: map[string]any{"backup_id": b.ID, "kind": kind}})
	return s.Backup(ctx, b.ID)
}

// verifyL1 checks the SHA-256 against the manifest and reads the archive TOC.
func (s *Service) verifyL1(ctx context.Context, id string) error {
	b, err := s.Backup(ctx, id)
	if err != nil {
		return err
	}
	abs := s.AbsPath(b)
	res := func(status, detail string) error {
		s.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE backups SET verify_status = ?, verify_level = CASE WHEN ? = 'failed' THEN verify_level ELSE MAX(verify_level, 1) END,
			verified_at = ?, verify_detail = ? WHERE id = ?`, status, status, store.NowT(), detail, id)
		if status == VerifyFailed {
			return fmt.Errorf("SQS-B020 verificación fallida: %s", detail)
		}
		return nil
	}
	m, err := ReadManifest(manifestPath(abs))
	if err != nil {
		return res(VerifyFailed, "manifiesto ausente o ilegible")
	}
	sum, size, err := fileSHA256(abs)
	if err != nil {
		return res(VerifyFailed, "no se pudo leer el archivo: "+err.Error())
	}
	if sum != m.SHA256 || sum != b.SHA256 || size != m.Size {
		return res(VerifyFailed, "la suma SHA-256 no coincide con el manifiesto: el archivo cambió o está dañado")
	}
	if _, err := s.listArchive(ctx, abs); err != nil {
		return res(VerifyFailed, err.Error())
	}
	status := VerifyL1OK
	if b.VerifyStatus == VerifyL2OK {
		status = VerifyL2OK
	}
	return res(status, "Suma e índice correctos")
}

// HumanBytes formats a size.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

func (s *Service) runBackupCreate(ctx context.Context, r *jobs.Run) error {
	var p BackupParams
	r.Params(&p)
	if p.Kind == "" {
		p.Kind = KindManual
	}
	l, err := s.Library(ctx, r.Job.LibraryID)
	if err != nil {
		return err
	}
	if l.State != StateActive && l.State != StateMaintenance {
		return fmt.Errorf("SQS-B003 no se respalda una biblioteca en estado %s", l.State.Label())
	}
	s.Events.Emit(ctx, events.Event{Type: events.BackupStarted, LibraryID: l.ID, JobID: r.Job.ID, Message: fmt.Sprintf("Respaldando %q", l.DisplayName)})
	b, err := s.createBackup(ctx, r, l, p.Kind, "")
	if err != nil {
		return err
	}
	r.SetResult(map[string]string{"backup_id": b.ID})
	return r.Step("retencion", func() error {
		_, err := s.applyRetention(ctx, l.ID)
		return err
	})
}

// BackupNow enqueues a manual backup.
func (s *Service) BackupNow(ctx context.Context, by audit.Actor, libraryID string) (*jobs.Job, error) {
	l, err := s.Library(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	if l.State != StateActive && l.State != StateMaintenance {
		return nil, fmt.Errorf("SQS-B003 no se respalda una biblioteca en estado %s", l.State.Label())
	}
	s.Audit.Record(ctx, by, "backup.create", "library", libraryID, audit.OK, nil)
	return s.Jobs.Enqueue(ctx, JobBackupCreate, libraryID, BackupParams{Kind: KindManual}, by, jobs.Priority(80))
}

// SetPinned fixes or releases a backup from retention.
func (s *Service) SetPinned(ctx context.Context, by audit.Actor, id string, pinned bool) error {
	if _, err := s.DB.ExecContext(ctx, `UPDATE backups SET pinned = ? WHERE id = ?`, pinned, id); err != nil {
		return err
	}
	return s.Audit.Record(ctx, by, "backup.pin", "backup", id, audit.OK, map[string]any{"pinned": pinned})
}

// DeleteBackup removes one backup by hand. The newest verified backup of a
// live library cannot be deleted.
func (s *Service) DeleteBackup(ctx context.Context, by audit.Actor, id string) error {
	b, err := s.Backup(ctx, id)
	if err != nil {
		return err
	}
	if b.Status == BackupDeleted {
		return nil
	}
	if b.Pinned {
		return errors.New("SQS-B033 suelta el respaldo fijado antes de borrarlo")
	}
	if l, err := s.Library(ctx, b.LibraryID); err == nil && (l.Live() || l.State == StateTrashed) {
		if latest, _ := s.LatestVerified(ctx, l.ID); latest != nil && latest.ID == b.ID {
			return errors.New("SQS-B034 es el respaldo verificado más reciente de la biblioteca; no se borra a mano")
		}
	}
	if err := s.removeBackupFiles(ctx, b, "borrado a mano"); err != nil {
		return err
	}
	return s.Audit.Record(ctx, by, "backup.delete", "backup", id, audit.OK, map[string]any{"library_id": b.LibraryID})
}

func (s *Service) removeBackupFiles(ctx context.Context, b *Backup, reason string) error {
	abs := s.AbsPath(b)
	if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(manifestPath(abs)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fsx.SyncDir(filepath.Dir(abs))
	_, err := s.DB.ExecContext(ctx, `UPDATE backups SET status = 'deleted', deleted_at = ?, expired_reason = ? WHERE id = ?`, store.NowT(), reason, b.ID)
	return err
}
