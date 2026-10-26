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
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

// KindLabel returns the display label of a backup kind.
func KindLabel(k string) string {
	switch k {
	case KindManual:
		return "Manual"
	case KindScheduled:
		return "Scheduled"
	case KindPreRestore:
		return "Before restore"
	case KindPreDelete:
		return "Before delete"
	case KindPreUpgrade:
		return "Before Resolve upgrade"
	case KindImported:
		return "Imported"
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
	// ActivityMark is the pg_stat_database mark of the snapshot (ADR 0022).
	ActivityMark  string
	StartedAt     time.Time
	FinishedAt    *time.Time
	Error         string
	ExpiredReason string
	DeletedAt     *time.Time
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
	pinned, source_db, started_at, finished_at, error, expired_reason, deleted_at, activity_mark`

func scanBackup(row interface{ Scan(...any) error }) (*Backup, error) {
	var b Backup
	var verified, finished, deleted sql.NullString
	var started string
	err := row.Scan(&b.ID, &b.LibraryID, &b.JobID, &b.Kind, &b.Status, &b.VerifyLevel, &b.VerifyStatus, &verified, &b.VerifyDetail, &b.FilePath,
		&b.SizeBytes, &b.SHA256, &b.PGServerVersion, &b.PGDumpVersion, &b.ArchiveVersion, &b.Compression, &b.Fingerprint, &b.TableCount,
		&b.CountsExact, &b.ResolveVersion, &b.Pinned, &b.SourceDB, &started, &finished, &b.Error, &b.ExpiredReason, &deleted, &b.ActivityMark)
	if err != nil {
		return nil, err
	}
	b.VerifiedAt, b.FinishedAt, b.DeletedAt = store.NullT(verified), store.NullT(finished), store.NullT(deleted)
	b.StartedAt = store.ParseT(started)
	return &b, nil
}

// ErrBackupNotFound is returned for unknown backups.
var ErrBackupNotFound = errors.New("SQS-B001 backup not found")

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
// Backups of an auxiliary database of the library (a previous state backed
// up before it is dropped, `<slug>__prev_*`) hold an older state: they only
// count when the library has no other verified backup.
func (s *Service) LatestVerified(ctx context.Context, libraryID string) (*Backup, error) {
	return s.latestVerified(ctx, libraryID, time.Time{})
}

// auxSource reports whether source, the source_db of a backup, is an
// auxiliary database of the library whose database is dbName (previous
// state, trash or restore copy: `<db>__...`; slugs cannot contain "__").
// Imported backups keep their file name there and are not auxiliary.
func auxSource(dbName, source string) bool {
	return dbName != "" && strings.HasPrefix(source, dbName+"__")
}

// latestVerified is LatestVerified ignoring backups dated after notAfter,
// when it is set: a backup dated in the future (the clock went back) says
// nothing about how recent the protection is.
func (s *Service) latestVerified(ctx context.Context, libraryID string, notAfter time.Time) (*Backup, error) {
	var dbName string
	s.DB.QueryRowContext(ctx, `SELECT db_name FROM libraries WHERE id = ?`, libraryID).Scan(&dbName)
	q := `SELECT ` + backupCols + ` FROM backups WHERE library_id = ? AND status = 'ok' AND verify_status IN ('l1_ok', 'l2_ok', 'partial')`
	args := []any{libraryID}
	if !notAfter.IsZero() {
		q += ` AND started_at <= ?`
		args = append(args, store.T(notAfter))
	}
	// Same test as auxSource: auxiliary databases sort last.
	q += ` ORDER BY (? <> '' AND substr(source_db, 1, length(?) + 2) = ? || '__'), started_at DESC LIMIT 1`
	args = append(args, dbName, dbName, dbName)
	b, err := scanBackup(s.DB.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

// ErrBackupPath is a catalog row whose path is not one sqsolve writes: a
// .backup file under libraries/ on the backups volume, also after resolving
// symlinks. Such a row is never read, restored, downloaded or deleted, and
// it is not rewritten either: an administrator reviews it
// (docs/runbooks/invalid-backup-path.md).
var ErrBackupPath = errors.New("SQS-B043 the catalog path of this backup is not valid; it is blocked until an administrator reviews it")

// validBackupRel reports whether rel is a clean relative path of a .backup
// file under libraries/.
func validBackupRel(rel string) bool {
	return rel != "" && !filepath.IsAbs(rel) && filepath.Clean(rel) == rel && strings.HasPrefix(rel, "libraries/") &&
		!slices.Contains(strings.Split(rel, "/"), "..") && backupFileName(filepath.Base(rel))
}

// backupFileName reports whether name is the base name of a .backup file.
func backupFileName(name string) bool {
	return name == filepath.Base(name) && !strings.ContainsRune(name, '/') && strings.HasSuffix(name, ".backup") && len(name) > len(".backup")
}

// AbsPath returns the absolute path of a backup file, or ErrBackupPath when
// the catalog path is not valid or leaves the backups volume.
func (s *Service) AbsPath(b *Backup) (string, error) {
	bad := fmt.Errorf("%w (backup %s)", ErrBackupPath, b.ID)
	if !validBackupRel(b.FilePath) {
		return "", bad
	}
	abs := filepath.Join(s.Cfg.BackupDir, b.FilePath)
	for _, p := range []string{abs, manifestPath(abs)} {
		ok, err := fsx.Confined(s.Cfg.BackupDir, p)
		if err != nil {
			return "", fmt.Errorf("could not check the path of backup %s: %w", b.ID, err)
		}
		if !ok {
			return "", bad
		}
	}
	return abs, nil
}

// backupManifest reads the manifest of b through its checked path.
func (s *Service) backupManifest(b *Backup) (*Manifest, error) {
	abs, err := s.AbsPath(b)
	if err != nil {
		return nil, err
	}
	return ReadManifest(manifestPath(abs))
}

// BackupManifest is the manifest of b, for the panel.
func (s *Service) BackupManifest(b *Backup) (*Manifest, error) { return s.backupManifest(b) }

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
		ActivityMark    string `json:"activity_mark,omitempty"`
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

// errManifestUnreadable is a manifest whose content is not valid.
var errManifestUnreadable = errors.New("SQS-B040 unreadable manifest")

// ErrManifestInvalid is a manifest that is JSON but not a complete version 1
// manifest (api/schemas/manifest.v1.json).
var ErrManifestInvalid = errors.New("SQS-B042 the manifest is not a valid version 1 manifest")

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ReadManifest loads the manifest of a backup and checks that it is a
// complete version 1 manifest whose file is the base name of a .backup file.
func ReadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%w: %w", errManifestUnreadable, err)
	}
	if why := m.invalid(); why != "" {
		return nil, fmt.Errorf("%w: %s", ErrManifestInvalid, why)
	}
	return &m, nil
}

// invalid says why m is not a valid version 1 manifest, or "".
func (m *Manifest) invalid() string {
	switch {
	case m.ManifestVersion != 1:
		return fmt.Sprintf("unsupported manifest_version %d", m.ManifestVersion)
	case !ulid.Valid(m.BackupID):
		return "backup_id is not a valid ID"
	case !ulid.Valid(m.Library.ID):
		return "library.id is not a valid ID"
	case pg.ValidateSlug(m.Library.Slug) != nil:
		return "library.slug is not a valid identifier"
	case KindLabel(m.Kind) == m.Kind:
		return fmt.Sprintf("kind %q is not known", m.Kind)
	case !backupFileName(m.File):
		return "file must be the name of a .backup file in the same folder"
	case m.Size < 0:
		return "size_bytes is negative"
	case !sha256Re.MatchString(m.SHA256):
		return "sha256 is not a SHA-256 checksum in hexadecimal"
	}
	return ""
}

// Version of sqsolve, set by main.
var Version = "dev"

// EventVerifySkipped is emitted when a verification could not run.
const EventVerifySkipped = "backup.verify_skipped"

// EventImportSourceKept is emitted when an import was asked to delete its
// source but the file in _import changed or was replaced meanwhile.
const EventImportSourceKept = "backup.import_source_kept"

// Test hooks let the integration tests act exactly where a crash or a
// concurrent write would. Production code never sets them.
var (
	// hookMarkAndSnapshot runs between reading the activity mark of a
	// backup and exporting its snapshot.
	hookMarkAndSnapshot atomic.Pointer[func()]
	// hookImportStaged runs when an imported file sits as .partial in the
	// library folder, before it is sealed; an error stops the import there,
	// as a crash would.
	hookImportStaged atomic.Pointer[func(partial string) error]
	// hookExportEnqueued runs right after the job of an export from a
	// backup is enqueued.
	hookExportEnqueued atomic.Pointer[func(jobID string)]
	// hookDBVolumeFree replaces the free space measured on the database
	// volume by the restore space check.
	hookDBVolumeFree atomic.Pointer[func() uint64]
	// hookFault makes a PostgreSQL lookup or grant fail at a named point,
	// where no trigger can (PLAN P0.4).
	hookFault atomic.Pointer[func(point string) error]
)

// fault returns the error a test injects at point, or nil.
func fault(point string) error {
	if f := hookFault.Load(); f != nil {
		return (*f)(point)
	}
	return nil
}

// importSettle is how long, in nanoseconds, the size of a file in _import
// must stay the same before it is imported: a file still being copied grows.
var importSettle atomic.Int64

func init() { importSettle.Store(int64(3 * time.Second)) }

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

// errNotCustomArchive is a file pg_restore read without finding the header
// of a custom-format archive.
var errNotCustomArchive = errors.New("SQS-B041 the file does not look like a pg_dump backup in custom format")

func (s *Service) listArchive(ctx context.Context, path string) (ArchiveInfo, error) {
	res, err := s.Exec.Run(ctx, execx.Cmd{Name: "pg_restore", Args: []string{"--list", path}, MaxOutput: 1 << 20})
	if err != nil {
		return ArchiveInfo{}, fmt.Errorf("SQS-B041 pg_restore cannot read the file: %w", err)
	}
	info := ParseArchiveHeader(res.Stdout)
	if info.Format == "" {
		return info, errNotCustomArchive
	}
	return info, nil
}

// readArchiveFully makes pg_restore read every block of an archive, data
// included, without restoring it anywhere. --list only reads the index at
// the start, so a file cut short or damaged further on passes it; this does
// not.
func (s *Service) readArchiveFully(ctx context.Context, path string) error {
	_, err := s.Exec.Run(ctx, execx.Cmd{Name: "pg_restore", Args: []string{"--file=" + os.DevNull, path}})
	if err == nil {
		return nil
	}
	if toolVerdict(ctx, err) {
		return fmt.Errorf("SQS-B072 the archive is incomplete or damaged: %w", err)
	}
	return fmt.Errorf("SQS-B073 the archive could not be read: %w", err)
}

// toolVerdict reports whether the failure of an external program is its
// verdict on the input: it started and exited with an error code while the
// job was still running. A program that could not start, that was killed by
// a signal or whose job was cancelled gives no verdict.
func toolVerdict(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, errNotCustomArchive) {
		return true
	}
	var xe *execx.Error
	return errors.As(err, &xe) && xe.ExitCode > 0
}

// serverTrouble are pg_restore failures caused by the server or its volume,
// not by the archive: they give no verdict on a backup. pg_restore prints
// server errors without their SQLSTATE (it keeps libpq's default error
// verbosity), so the messages themselves are matched.
var serverTrouble = []string{
	// Space, memory and resource limits. "out of shared memory" is the lock
	// table: --single-transaction holds a lock on every restored relation.
	"no space left on device", "could not extend file", "disk full", "out of memory", "out of shared memory",
	"could not resize shared memory", "temp_file_limit",
	// The server's own storage.
	"input/output error", "could not write", "read-only transaction",
	// Connections and sessions.
	"could not connect", "connection to server", "server closed the connection", "terminating connection",
	"the database system is", "too many connections", "remaining connection slots", "canceling statement",
	"deadlock detected", "lock timeout", "password authentication failed", "permission denied", "must be superuser",
}

// restoreVerdict reports whether a failed test restore says something about
// the content of the backup rather than about the server.
func restoreVerdict(ctx context.Context, err error) bool {
	if !toolVerdict(ctx, err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, s := range serverTrouble {
		if strings.Contains(msg, s) {
			return false
		}
	}
	return true
}

// Verification outcomes (docs 11 §5). Only a verdict on the content of a
// backup marks it failed: a checksum that does not match, a manifest or a
// file gone from a mounted volume, an archive pg_restore refuses to read, a
// test restore whose content does not load or does not match the manifest.
// A verification that could not run (no space, cancelled, the server, the
// volume or a tool unavailable) leaves the verification status as it was,
// records why in the detail and emits a warning, never backup.verify_failed.

// verifyFailedError is a verdict: the backup is damaged.
type verifyFailedError struct{ detail string }

func (e *verifyFailedError) Error() string { return "SQS-B020 verification failed: " + e.detail }

// verifyNotRunError is a verification that reached no verdict.
type verifyNotRunError struct{ cause error }

func (e *verifyNotRunError) Error() string {
	return "SQS-B070 the verification could not run: " + e.cause.Error()
}

func (e *verifyNotRunError) Unwrap() error { return e.cause }

func notRunError(cause error) error {
	var nr *verifyNotRunError
	if errors.As(cause, &nr) {
		return nr
	}
	return &verifyNotRunError{cause}
}

// noteVerifyNotRun records a verification that could not run: the detail on
// the backup (its status stays) and a warning event. It returns the error
// the caller reports.
func (s *Service) noteVerifyNotRun(ctx context.Context, b *Backup, jobID string, cause error) error {
	err := notRunError(cause).(*verifyNotRunError)
	c := context.WithoutCancel(ctx)
	s.DB.ExecContext(c, `UPDATE backups SET verify_detail = ? WHERE id = ?`, "The last verification could not run: "+shortErr(err.cause), b.ID)
	name := b.LibraryID
	if l, _ := s.Library(c, b.LibraryID); l != nil {
		name = l.DisplayName
	}
	s.Events.Emit(c, events.Event{Type: EventVerifySkipped, Severity: events.Warning, LibraryID: b.LibraryID, JobID: jobID,
		Message: fmt.Sprintf("The verification of the backup of %q from %s could not run: %s. Its verification status did not change",
			name, b.StartedAt.In(s.Cfg.Location).Format("2006-01-02 15:04"), shortErr(err.cause))})
	return err
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
			Message: "The backups volume is not mounted or does not belong to this installation: " + err.Error()})
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
		c := context.WithoutCancel(ctx)
		if ctx.Err() != nil {
			// Cut by a cancel or by a graceful stop: interrupted, as the
			// startup recovery marks a backup cut by a crash, and not a
			// failure to report (the job says how it ended).
			why := "The backup was cancelled"
			if errors.Is(context.Cause(ctx), jobs.ErrStopped) {
				why = "The process stopped during the backup"
			}
			s.DB.ExecContext(c, `UPDATE backups SET status = 'interrupted', finished_at = ?, error = ? WHERE id = ?`, store.NowT(), why, b.ID)
			return nil, cause
		}
		s.DB.ExecContext(c, `UPDATE backups SET status = 'failed', finished_at = ?, error = ? WHERE id = ?`, store.NowT(), cause.Error(), b.ID)
		s.Events.Emit(ctx, events.Event{Type: events.BackupFailed, Severity: events.Error, LibraryID: l.ID, JobID: b.JobID,
			Message: fmt.Sprintf("Backup of %q failed: %s", l.DisplayName, shortErr(cause))})
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
	err = step("prepare", func() error {
		info, err := s.PG.Database(ctx, dbName)
		if err != nil {
			return err
		}
		if info == nil {
			return fmt.Errorf("SQS-B002 database %s does not exist", dbName)
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
			return fmt.Errorf("SQS-B012 not enough space on the backups volume: %s estimated, %s free", HumanBytes(need), HumanBytes(int64(sp.Free)))
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
	var serverVersion, mark string
	err = step("dump", func() error {
		conn, err := s.PG.ConnectDB(ctx, dbName)
		if err != nil {
			return err
		}
		defer conn.Close(context.Background())
		// The activity mark is read before the snapshot exists, outside the
		// transaction: a write that lands in between is then in the dump and
		// not in the mark, so the activity gate backs up once more. In the
		// other order it would be counted in the mark and missing from the
		// dump, and the gate would never back it up (ADR 0022).
		mark, _ = pg.ActivityMarkFrom(ctx, conn)
		if f := hookMarkAndSnapshot.Load(); f != nil {
			(*f)()
		}
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
			return fmt.Errorf("SQS-B010 pg_dump failed: %w", dumpErr)
		}
		if cerr != nil {
			return fmt.Errorf("SQS-B011 row count: %w", cerr)
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
	err = step("seal", func() error {
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
		m.Server.PostgresVersion, m.Server.DatabaseSize, m.Server.ActivityMark = serverVersion, dbSize, mark
		m.Schema.Fingerprint, m.Schema.Tables = fp.Hash, fp.Tables
		mb, _ := json.MarshalIndent(m, "", "  ")
		if err := fsx.WriteFileAtomic(manifestPath(abs), append(mb, '\n'), 0o600); err != nil {
			return err
		}
		os.Chmod(abs, 0o600)
		_, err = s.DB.ExecContext(ctx, `UPDATE backups SET status = 'ok', finished_at = ?, size_bytes = ?, sha256 = ?, pg_server_version = ?, pg_dump_version = ?,
			archive_version = ?, compression = ?, schema_fingerprint = ?, table_count = ?, counts_exact = ?, activity_mark = ? WHERE id = ?`,
			store.T(finished), size, sum, serverVersion, info.DumpedBy, info.ArchiveVersion, info.Compression, fp.Hash, fp.Tables, exact, mark, b.ID)
		logf("Backup of %s written (%s)", dbName, HumanBytes(size))
		return err
	})
	if err != nil {
		return fail(err)
	}
	if r != nil {
		r.Progress(90)
	}
	// A new backup whose check cannot finish (the job was cancelled) is
	// discarded with the job, so the check is not announced on its own.
	err = step("verify_l1", func() error { return s.checkL1(ctx, b.ID, false) })
	if err != nil {
		return fail(err)
	}
	if dbName == l.DBName && fp.Hash != "" {
		s.recordFingerprint(ctx, l, fp, b.JobID)
	}
	s.Events.Emit(ctx, events.Event{Type: events.BackupSucceeded, LibraryID: l.ID, JobID: b.JobID,
		Message: fmt.Sprintf("Backup of %q succeeded (%s)", l.DisplayName, HumanBytes(b.SizeBytes)), Data: map[string]any{"backup_id": b.ID, "kind": kind}})
	return s.Backup(ctx, b.ID)
}

// verifyL1 checks the SHA-256 against the manifest and reads the archive TOC.
// A verdict returns *verifyFailedError and marks the backup failed; when the
// check cannot run it returns *verifyNotRunError, leaves the status as it
// was and records why.
func (s *Service) verifyL1(ctx context.Context, id string) error {
	return s.checkL1(ctx, id, true)
}

// checkL1 is verifyL1; with notice false a check that cannot run is not
// recorded (the caller does, or discards the backup).
func (s *Service) checkL1(ctx context.Context, id string, notice bool) error {
	// A local catalog read: done even when the job was just cancelled, so
	// the outcome can be recorded on the backup.
	b, err := s.Backup(context.WithoutCancel(ctx), id)
	if err != nil {
		return err
	}
	if b.Status != BackupOK {
		return fmt.Errorf("SQS-B021 the backup is not available (%s)", b.Status)
	}
	// A path that is not valid is no verdict on the backup: the row stays
	// as it is until an administrator reviews it.
	abs, err := s.AbsPath(b)
	if err != nil {
		return err
	}
	verdict := func(detail string) error {
		s.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE backups SET verify_status = 'failed', verified_at = ?, verify_detail = ? WHERE id = ?`,
			store.NowT(), detail, id)
		return &verifyFailedError{detail}
	}
	notRun := func(cause error) error {
		if notice {
			return s.noteVerifyNotRun(ctx, b, "", cause)
		}
		return notRunError(cause)
	}
	if err := ctx.Err(); err != nil {
		return notRun(err)
	}
	// Without the marker the volume may not be mounted: a missing file then
	// says nothing about the backup.
	if err := s.BackupVolumeOK(); err != nil {
		return notRun(err)
	}
	m, err := ReadManifest(manifestPath(abs))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return verdict("the manifest is missing")
	case errors.Is(err, errManifestUnreadable):
		return verdict("the manifest is unreadable")
	case err != nil:
		return notRun(fmt.Errorf("could not read the manifest: %w", err))
	}
	sum, size, err := fileSHA256(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return verdict("the backup file does not exist")
	case err != nil:
		return notRun(fmt.Errorf("could not read the file: %w", err))
	}
	if sum != m.SHA256 || sum != b.SHA256 || size != m.Size {
		return verdict("the SHA-256 checksum does not match the manifest: the file changed or is damaged")
	}
	if _, err := s.listArchive(ctx, abs); err != nil {
		if toolVerdict(ctx, err) {
			return verdict(err.Error())
		}
		return notRun(err)
	}
	status := VerifyL1OK
	if b.VerifyStatus == VerifyL2OK {
		status = VerifyL2OK
	}
	s.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE backups SET verify_status = ?, verify_level = MAX(verify_level, 1), verified_at = ?, verify_detail = ? WHERE id = ?`,
		status, store.NowT(), "Checksum and index OK", id)
	return nil
}

// restoreSpaceNeed estimates the room a restored copy of b takes on the
// database volume: the database size recorded in the manifest plus a fifth
// (indexes being built, WAL), or four times the archive when the manifest
// does not say.
func (s *Service) restoreSpaceNeed(b *Backup) int64 {
	if m, err := s.backupManifest(b); err == nil && m.Server.DatabaseSize > 0 {
		return m.Server.DatabaseSize / 5 * 6
	}
	return b.SizeBytes * 4
}

// checkRestoreSpace refuses to restore b into the database volume when the
// copy does not fit: every restore (verification, new library, replace,
// recreate, export workspace) goes through it before creating a database.
// Only the restored copy needs free space: a replace keeps the live database
// by renaming it, so it keeps the space it already takes. When the volume
// cannot be measured the restore is tried anyway.
func (s *Service) checkRestoreSpace(b *Backup, purpose string) error {
	need := s.restoreSpaceNeed(b)
	sp, err := fsx.Statfs(s.Cfg.PGDir)
	if err != nil {
		return nil
	}
	if f := hookDBVolumeFree.Load(); f != nil {
		sp.Free = (*f)()
	}
	if need > 0 && uint64(need) > sp.Free {
		return fmt.Errorf("SQS-B022 not enough space on the database volume for %s: %s needed, %s free", purpose, HumanBytes(need), HumanBytes(int64(sp.Free)))
	}
	return nil
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
		return fmt.Errorf("SQS-B003 cannot back up a library in state %s", l.State.Label())
	}
	s.Events.Emit(ctx, events.Event{Type: events.BackupStarted, LibraryID: l.ID, JobID: r.Job.ID, Message: fmt.Sprintf("Backing up %q", l.DisplayName)})
	b, err := s.createBackup(ctx, r, l, p.Kind, "")
	if err != nil {
		return err
	}
	r.SetResult(map[string]string{"backup_id": b.ID})
	return r.Step("retention", func() error {
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
		return nil, fmt.Errorf("SQS-B003 cannot back up a library in state %s", l.State.Label())
	}
	return s.Jobs.Enqueue(ctx, JobBackupCreate, libraryID, BackupParams{Kind: KindManual}, by, jobs.Priority(80),
		jobs.Audited("backup.create", "library", libraryID, nil))
}

// SetPinned fixes or releases a backup from retention, in one transaction
// with its audit entry.
func (s *Service) SetPinned(ctx context.Context, by audit.Actor, id string, pinned bool) error {
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE backups SET pinned = ? WHERE id = ?`, pinned, id); err != nil {
			return err
		}
		return s.Audit.RecordTx(ctx, tx, by, "backup.pin", "backup", id, audit.OK, map[string]any{"pinned": pinned})
	})
}

// DeleteBackup removes one backup by hand. The newest verified backup of a
// library that can still be restored or recreated (every state but purged:
// a missing library needs it to be recreated) cannot be deleted.
func (s *Service) DeleteBackup(ctx context.Context, by audit.Actor, id string) error {
	b, err := s.Backup(ctx, id)
	if err != nil {
		return err
	}
	if b.Status == BackupDeleted {
		return nil
	}
	if _, err := s.AbsPath(b); err != nil {
		return err
	}
	if b.Pinned {
		return errors.New("SQS-B033 unpin the backup before deleting it")
	}
	if l, err := s.Library(ctx, b.LibraryID); err == nil && l.State != StatePurged {
		if latest, _ := s.LatestVerified(ctx, l.ID); latest != nil && latest.ID == b.ID {
			return errors.New("SQS-B034 it is the most recent verified backup of the library; it cannot be deleted manually")
		}
	}
	return s.Audit.Do(ctx, by, "backup.delete", "backup", id, map[string]any{"library_id": b.LibraryID}, func() error {
		return s.removeBackupFiles(ctx, b, "deleted manually")
	})
}

// ForgetInvalidBackup removes from the catalog a backup whose path is not
// valid, once an administrator reviewed it (docs/runbooks/
// invalid-backup-path.md): the row is marked deleted, in one transaction
// with its audit entry, and no file is touched.
func (s *Service) ForgetInvalidBackup(ctx context.Context, by audit.Actor, id string) error {
	b, err := s.Backup(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.AbsPath(b); !errors.Is(err, ErrBackupPath) || b.Status == BackupDeleted {
		return errors.New("SQS-B044 only a backup whose catalog path is not valid can be removed from the catalog this way")
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE backups SET status = 'deleted', deleted_at = ?, expired_reason = ? WHERE id = ?`,
			store.NowT(), "removed from the catalog after review: its path was not valid", b.ID); err != nil {
			return err
		}
		return s.Audit.RecordTx(ctx, tx, by, "backup.forget", "backup", b.ID, audit.OK, map[string]any{"library_id": b.LibraryID, "file_path": b.FilePath})
	})
}

func (s *Service) removeBackupFiles(ctx context.Context, b *Backup, reason string) error {
	abs, err := s.AbsPath(b)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(manifestPath(abs)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fsx.SyncDir(filepath.Dir(abs))
	_, err = s.DB.ExecContext(ctx, `UPDATE backups SET status = 'deleted', deleted_at = ?, expired_reason = ? WHERE id = ?`, store.NowT(), reason, b.ID)
	return err
}
