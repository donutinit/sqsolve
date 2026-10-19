// SPDX-License-Identifier: AGPL-3.0-or-later

// Package store opens the SQLite database of the control plane, applies
// migrations and offers small helpers shared by the domain packages.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// TimeFormat has a fixed width so text comparison orders correctly.
const TimeFormat = "2006-01-02T15:04:05.000000Z"

// T formats a time for storage.
func T(t time.Time) string { return t.UTC().Format(TimeFormat) }

// NowT returns the current time formatted for storage.
func NowT() string { return T(time.Now()) }

// ParseT parses a stored time. Empty or invalid values give the zero time.
func ParseT(s string) time.Time {
	t, err := time.Parse(TimeFormat, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// NullT turns a stored nullable time into a pointer.
func NullT(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := ParseT(s.String)
	return &t
}

// TPtr formats a nullable time.
func TPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return T(*t)
}

// DB is the control plane database.
type DB struct {
	*sql.DB
	Path string
}

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("no encontrado")

// Open opens (and creates) the database at path and applies migrations.
// Before applying migrations to an existing database a copy is written to
// backupDir with VACUUM INTO.
func Open(ctx context.Context, path, backupDir string) (*DB, error) {
	existed := true
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		existed = false
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=synchronous(FULL)&_txlock=immediate"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(8)
	db := &DB{DB: sqldb, Path: path}
	if err := db.PingContext(ctx); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("SQS-X010 no se pudo abrir %s: %w", path, err)
	}
	if existed {
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
			return nil, err
		}
	}
	if err := db.migrate(ctx, existed, backupDir); err != nil {
		sqldb.Close()
		return nil, err
	}
	return db, nil
}

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

func loadMigrations() ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("nombre de migración inválido: %s", name)
		}
		b, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, migration{v, name, string(b), hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func (db *DB) migrate(ctx context.Context, existed bool, backupDir string) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	all, err := loadMigrations()
	if err != nil {
		return err
	}
	applied := map[int]string{}
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		var c string
		if err := rows.Scan(&v, &c); err != nil {
			rows.Close()
			return err
		}
		applied[v] = c
	}
	rows.Close()
	var pending []migration
	for _, m := range all {
		if c, ok := applied[m.version]; ok {
			if c != m.checksum {
				return fmt.Errorf("SQS-X011 la migración %s cambió después de aplicarse", m.name)
			}
			continue
		}
		pending = append(pending, m)
	}
	known := map[int]bool{}
	for _, m := range all {
		known[m.version] = true
	}
	for v := range applied {
		if !known[v] {
			return fmt.Errorf("SQS-X012 la base de datos tiene la migración %d, más nueva que esta versión de sqsolve; usa la versión con la que se creó o restaura la copia previa", v)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	if existed && len(applied) > 0 && backupDir != "" {
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return err
		}
		dst := filepath.Join(backupDir, fmt.Sprintf("sqsolve-antes-de-%04d-%s.db", pending[0].version, time.Now().UTC().Format("20060102T150405Z")))
		if err := db.BackupTo(ctx, dst); err != nil {
			return fmt.Errorf("SQS-X013 no se pudo copiar la base antes de migrar: %w", err)
		}
	}
	for _, m := range pending {
		err := db.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("migración %s: %w", m.name, err)
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
				m.version, m.name, m.checksum, NowT())
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Tx runs fn in a transaction.
func (db *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// BackupTo writes a consistent copy of the database to dst.
func (db *DB) BackupTo(ctx context.Context, dst string) error {
	if strings.ContainsAny(dst, "'\x00") {
		return errors.New("ruta inválida")
	}
	tmp := dst + ".partial"
	os.Remove(tmp)
	if _, err := db.ExecContext(ctx, "VACUUM INTO '"+tmp+"'"); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	f, err := os.Open(tmp)
	if err == nil {
		f.Sync()
		f.Close()
	}
	return os.Rename(tmp, dst)
}

// IntegrityCheck runs PRAGMA integrity_check.
func (db *DB) IntegrityCheck(ctx context.Context) error {
	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("SQS-X014 integridad de SQLite: %s", res)
	}
	return nil
}

// GetSetting decodes a JSON setting into out. It reports whether it exists.
func (db *DB) GetSetting(ctx context.Context, key string, out any) (bool, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(v), out)
}

// SetSetting stores a JSON setting.
func (db *DB) SetSetting(ctx context.Context, key string, value any, by string) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at, updated_by) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		key, string(b), NowT(), by)
	return err
}

// JSON marshals v or returns "{}".
func JSON(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
