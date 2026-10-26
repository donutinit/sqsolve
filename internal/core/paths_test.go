// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/scheduler"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// validManifest is a complete version 1 manifest of backup id of library
// slug, for the file name.
func validManifest(id, libID, slug, name string) map[string]any {
	return map[string]any{
		"manifest_version": 1,
		"backup_id":        id,
		"library":          map[string]any{"id": libID, "slug": slug, "display_name": "Client X"},
		"kind":             KindScheduled,
		"file":             name,
		"size_bytes":       12,
		"sha256":           strings.Repeat("ab", 32),
		"format":           map[string]any{"tool": "pg_dump", "tool_version": "18.6", "format": "custom", "compression": "gzip", "archive_version": "1.16"},
		"server":           map[string]any{"postgres_version": "18.6", "database_size_bytes": 100},
		"schema":           map[string]any{"fingerprint": "sha256:00", "tables": 3},
		"started_at":       "2026-10-18T07:00:00Z",
		"finished_at":      "2026-10-18T07:00:41Z",
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// PLAN P0.1: a manifest is read only when it is a complete version 1
// manifest whose file is a base name of a .backup file.
func TestReadManifestRequiresAValidV1Manifest(t *testing.T) {
	dir := t.TempDir()
	id, lib := ulid.New(), ulid.New()
	name := "client_x_20261018T070000Z_" + id + ".backup"
	cases := []struct {
		name   string
		change func(m map[string]any)
		why    string
	}{
		{"valid", func(map[string]any) {}, ""},
		{"version 0", func(m map[string]any) { m["manifest_version"] = 0 }, "unsupported manifest_version 0"},
		{"version 2", func(m map[string]any) { m["manifest_version"] = 2 }, "unsupported manifest_version 2"},
		{"no version", func(m map[string]any) { delete(m, "manifest_version") }, "unsupported manifest_version 0"},
		{"no backup id", func(m map[string]any) { delete(m, "backup_id") }, "backup_id is not a valid ID"},
		{"malformed backup id", func(m map[string]any) { m["backup_id"] = "../../etc" }, "backup_id is not a valid ID"},
		{"lowercase backup id", func(m map[string]any) { m["backup_id"] = strings.ToLower(id) }, "backup_id is not a valid ID"},
		{"malformed library id", func(m map[string]any) { m["library"].(map[string]any)["id"] = "L1" }, "library.id is not a valid ID"},
		{"invalid slug", func(m map[string]any) { m["library"].(map[string]any)["slug"] = "../x" }, "library.slug is not a valid identifier"},
		{"unknown kind", func(m map[string]any) { m["kind"] = "nightly" }, `kind "nightly" is not known`},
		{"parent file", func(m map[string]any) { m["file"] = "../" + name }, "file must be the name of a .backup file in the same folder"},
		{"absolute file", func(m map[string]any) { m["file"] = "/etc/" + name }, "file must be the name of a .backup file in the same folder"},
		{"nested file", func(m map[string]any) { m["file"] = "sub/" + name }, "file must be the name of a .backup file in the same folder"},
		{"dot dot", func(m map[string]any) { m["file"] = ".." }, "file must be the name of a .backup file in the same folder"},
		{"other suffix", func(m map[string]any) { m["file"] = "client_x.sql" }, "file must be the name of a .backup file in the same folder"},
		{"bare suffix", func(m map[string]any) { m["file"] = ".backup" }, "file must be the name of a .backup file in the same folder"},
		{"negative size", func(m map[string]any) { m["size_bytes"] = -1 }, "size_bytes is negative"},
		{"malformed checksum", func(m map[string]any) { m["sha256"] = "XYZ" }, "sha256 is not a SHA-256 checksum in hexadecimal"},
	}
	for _, c := range cases {
		m := validManifest(id, lib, "client_x", name)
		c.change(m)
		p := filepath.Join(dir, "m.json")
		writeJSON(t, p, m)
		got, err := ReadManifest(p)
		if c.why == "" {
			if err != nil || got.BackupID != id || got.File != name {
				t.Fatalf("%s: %v %+v", c.name, err, got)
			}
			continue
		}
		if !errors.Is(err, ErrManifestInvalid) || err.Error() != "SQS-B042 the manifest is not a valid version 1 manifest: "+c.why {
			t.Errorf("%s: got %v", c.name, err)
		}
	}
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o600)
	if _, err := ReadManifest(filepath.Join(dir, "bad.json")); !errors.Is(err, errManifestUnreadable) {
		t.Fatalf("broken JSON: %v", err)
	}
}

// The documented schema lists exactly the fields of the Go type, and its
// required fields are the ones ReadManifest checks.
func TestManifestSchemaMatchesTheGoType(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "schemas", "manifest.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	var walk func(path string, s map[string]any, typ reflect.Type)
	walk = func(path string, s map[string]any, typ reflect.Type) {
		props, _ := s["properties"].(map[string]any)
		var want []string
		for i := range typ.NumField() {
			f := typ.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			want = append(want, tag)
			sub, ok := props[tag].(map[string]any)
			if !ok {
				t.Errorf("schema %s lacks %q", path, tag)
				continue
			}
			if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeFor[time.Time]() {
				walk(path+"."+tag, sub, f.Type)
			}
		}
		var got []string
		for k := range props {
			got = append(got, k)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("schema %s properties %v, Go type %v", path, got, want)
		}
	}
	walk("manifest", schema, reflect.TypeFor[Manifest]())
	var required []string
	for _, r := range schema["required"].([]any) {
		required = append(required, r.(string))
	}
	slices.Sort(required)
	if want := []string{"backup_id", "file", "kind", "library", "manifest_version", "sha256", "size_bytes"}; !slices.Equal(required, want) {
		t.Errorf("required %v, want %v", required, want)
	}
	lib := schema["properties"].(map[string]any)["library"].(map[string]any)
	if r := lib["required"].([]any); len(r) != 2 || r[0] != "id" || r[1] != "slug" {
		t.Errorf("library.required %v", r)
	}
}

// pathRow inserts a backup row of l with the given relative path.
func (e *bkEnv) pathRow(l *Library, rel string) *Backup {
	e.t.Helper()
	id := ulid.New()
	if _, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO backups(id, library_id, kind, status, verify_status, file_path, size_bytes, started_at, finished_at)
		VALUES (?, ?, 'scheduled', 'ok', 'l1_ok', ?, 4, ?, ?)`, id, l.ID, rel, store.NowT(), store.NowT()); err != nil {
		e.t.Fatal(err)
	}
	b, err := e.svc.Backup(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// escapes builds, on the backups volume of e, a symlinked folder and a
// symlinked file that lead to a file outside the volume, and returns the
// rows that use them, the rows with paths that are invalid as written, and
// the outside file.
func (e *bkEnv) escapes(l *Library) (rows []*Backup, outsideFile string) {
	e.t.Helper()
	outside := e.t.TempDir()
	outsideFile = filepath.Join(outside, "victim.backup")
	os.WriteFile(outsideFile, []byte("not a backup"), 0o600)
	os.WriteFile(filepath.Join(outside, "victim.manifest.json"), []byte("{}"), 0o600)
	libDir := filepath.Join(e.svc.Cfg.BackupDir, "libraries", l.Slug)
	os.MkdirAll(libDir, 0o700)
	os.Symlink(outside, filepath.Join(libDir, "esc"))
	os.Symlink(outsideFile, filepath.Join(libDir, "link.backup"))
	for _, rel := range []string{
		"../" + filepath.Base(outside) + "/victim.backup",
		outsideFile,
		"libraries/" + l.Slug + "/../../../victim.backup",
		"libraries/" + l.Slug + "/esc/victim.backup",
		"libraries/" + l.Slug + "/link.backup",
		"libraries/" + l.Slug + "/notes.txt",
		"_system/" + l.Slug + ".backup",
		"",
	} {
		rows = append(rows, e.pathRow(l, rel))
	}
	return rows, outsideFile
}

// PLAN P0.1: AbsPath only returns paths that stay on the backups volume,
// also through symlinks, and only for .backup files under libraries/.
func TestAbsPathConfinesCatalogPaths(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("client_x", StateActive)
	good := e.backup(l, bkOpt{})
	if p, err := e.svc.AbsPath(good); err != nil || p != filepath.Join(e.svc.Cfg.BackupDir, good.FilePath) {
		t.Fatalf("valid path: %q %v", p, err)
	}
	missing := e.pathRow(l, "libraries/client_x/2026/10/client_x_gone.backup")
	if _, err := e.svc.AbsPath(missing); err != nil {
		t.Fatalf("a valid path whose file is missing: %v", err)
	}
	rows, _ := e.escapes(l)
	for _, b := range rows {
		_, err := e.svc.AbsPath(b)
		if !errors.Is(err, ErrBackupPath) || err.Error() != "SQS-B043 the catalog path of this backup is not valid; it is blocked until an administrator reviews it (backup "+b.ID+")" {
			t.Errorf("%q: %v", b.FilePath, err)
		}
	}
}

// PLAN P0.1: a row whose path is invalid is never deleted, by hand or by
// retention, and its file and the row stay as they are.
func TestInvalidCatalogPathsAreNeverDeleted(t *testing.T) {
	e := newBkEnv(t)
	e.svc.Audit = audit.New(e.svc.DB)
	l := e.library("client_x", StateActive)
	e.backup(l, bkOpt{started: time.Now()})
	rows, outsideFile := e.escapes(l)
	for _, b := range rows {
		if err := e.svc.DeleteBackup(e.ctx, bkActor, b.ID); !errors.Is(err, ErrBackupPath) {
			t.Errorf("delete %q: %v", b.FilePath, err)
		}
	}
	e.policy(l, 1, 1)
	for _, b := range rows {
		e.svc.DB.ExecContext(e.ctx, `UPDATE backups SET started_at = ? WHERE id = ?`, store.T(time.Now().Add(-400*24*time.Hour)), b.ID)
	}
	failed := e.pathRow(l, "libraries/client_x/esc/victim.backup")
	e.svc.DB.ExecContext(e.ctx, `UPDATE backups SET status = 'failed', started_at = ? WHERE id = ?`, store.T(time.Now().Add(-400*24*time.Hour)), failed.ID)
	if _, err := e.svc.applyRetention(e.ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	if !onDisk(outsideFile) || !onDisk(strings.TrimSuffix(outsideFile, ".backup")+".manifest.json") {
		t.Fatal("a file outside the backups volume was deleted")
	}
	for _, b := range append(rows, failed) {
		got, err := e.svc.Backup(e.ctx, b.ID)
		if err != nil || got.Status != b.Status && b.ID != failed.ID || got.FilePath != b.FilePath {
			t.Errorf("row %q changed: %+v %v", b.FilePath, got, err)
		}
	}
	if got, _ := e.svc.Backup(e.ctx, failed.ID); got == nil || got.Status != BackupFailed {
		t.Errorf("the failed row with an invalid path was removed: %+v", got)
	}
}

// PLAN P0.1 and 1.3.2: at startup the rows with an invalid path are reported
// in one critical event, and nothing is rewritten.
func TestCheckCatalogPathsReportsWithoutRewriting(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("client_x", StateActive)
	e.backup(l, bkOpt{})
	rows, _ := e.escapes(l)
	deleted := e.pathRow(l, "../gone.backup")
	e.svc.DB.ExecContext(e.ctx, `UPDATE backups SET status = 'deleted' WHERE id = ?`, deleted.ID)
	var before []string
	for _, b := range rows {
		before = append(before, b.ID+"|"+b.FilePath+"|"+b.Status+"|"+b.VerifyStatus)
	}
	ids, err := e.svc.CheckCatalogPaths(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, b := range rows {
		want = append(want, b.ID)
	}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("invalid %v, want %v", ids, want)
	}
	evs := e.events(events.BackupPathInvalid)
	if len(evs) != 1 || evs[0].Severity != events.Critical || !strings.HasPrefix(evs[0].Message, "8 backups in the catalog have a path outside the backups volume or not valid") {
		t.Fatalf("events %+v", evs)
	}
	for i, b := range rows {
		got, _ := e.svc.Backup(e.ctx, b.ID)
		if s := got.ID + "|" + got.FilePath + "|" + got.Status + "|" + got.VerifyStatus; s != before[i] {
			t.Errorf("row rewritten: %s, was %s", s, before[i])
		}
	}
	// Nothing to report: no event.
	e2 := newBkEnv(t)
	e2.backup(e2.library("other", StateActive), bkOpt{})
	if ids, err := e2.svc.CheckCatalogPaths(e2.ctx); err != nil || len(ids) != 0 || len(e2.events(events.BackupPathInvalid)) != 0 {
		t.Fatalf("clean catalog: %v %v", ids, err)
	}
}

// runJobNow runs one job of typ to its end on a fresh queue.
func runJobNow(t *testing.T, s *Service, typ string) *jobs.Job {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Jobs = jobs.NewManager(s.DB, s.Events, events.NewBus(), s.Log, 1, 1)
	s.Sched = scheduler.New(s.DB, s.Jobs, s.Events, s.Log)
	s.Register()
	j, err := s.Jobs.Enqueue(ctx, typ, "", nil, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	s.Jobs.Start(ctx)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		got, err := s.Jobs.Get(ctx, j.ID)
		if err == nil && got.State.Finished() {
			s.Jobs.Shutdown(5 * time.Second)
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the job did not finish")
	return nil
}

// PLAN P0.1: rebuilding the catalog only adds a manifest that is valid,
// whose file is its own counterpart in the same folder, and whose files
// stay on the volume.
func TestCatalogRebuildConfinesManifests(t *testing.T) {
	e := newBkEnv(t)
	root := filepath.Join(e.svc.Cfg.BackupDir, "libraries", "client_x", "2026", "10")
	outside := t.TempDir()
	lib := ulid.New()
	add := func(dir, base string, m map[string]any, withFile bool) string {
		id := m["backup_id"].(string)
		if withFile {
			os.MkdirAll(dir, 0o700)
			os.WriteFile(filepath.Join(dir, base+".backup"), []byte("PGDMP"), 0o600)
		}
		writeJSON(t, filepath.Join(dir, base+".manifest.json"), m)
		return id
	}
	manifest := func(file string) map[string]any {
		id := ulid.New()
		if file == "" {
			file = "client_x_" + id + ".backup"
		}
		return validManifest(id, lib, "client_x", file)
	}
	// Valid: added.
	m := manifest("")
	good := add(root, strings.TrimSuffix(m["file"].(string), ".backup"), m, true)
	// The file field points out of the folder.
	os.WriteFile(filepath.Join(outside, "victim.backup"), []byte("x"), 0o600)
	m = manifest("../../../../../" + filepath.Base(outside) + "/victim.backup")
	parent := add(root, "parent", m, false)
	// An unsupported version.
	m = manifest("")
	m["manifest_version"] = 2
	v2 := add(root, strings.TrimSuffix(m["file"].(string), ".backup"), m, true)
	// The manifest names the file of another backup in the same folder.
	m = manifest(filepath.Base(filepath.Join(root, "client_x_"+good+".backup")))
	other := add(root, "stray", m, false)
	// A folder that is a symlink out of the volume.
	escDir := filepath.Join(outside, "lib")
	m = manifest("")
	esc := add(escDir, strings.TrimSuffix(m["file"].(string), ".backup"), m, true)
	os.Symlink(escDir, filepath.Join(e.svc.Cfg.BackupDir, "libraries", "escaped"))
	// A backup file that is a symlink out of the volume.
	m = manifest("")
	base := strings.TrimSuffix(m["file"].(string), ".backup")
	linked := add(root, base, m, false)
	os.Symlink(filepath.Join(outside, "victim.backup"), filepath.Join(root, base+".backup"))

	j := runJobNow(t, e.svc, JobCatalogRebuild)
	if j.State != jobs.Succeeded {
		t.Fatalf("rebuild: %s %s", j.State, j.ErrorDetail)
	}
	if _, err := e.svc.Backup(e.ctx, good); err != nil {
		t.Fatalf("the valid manifest was not added: %v", err)
	}
	for name, id := range map[string]string{"parent": parent, "v2": v2, "other": other, "escaped folder": esc, "linked file": linked} {
		if _, err := e.svc.Backup(e.ctx, id); !errors.Is(err, ErrBackupNotFound) {
			t.Errorf("%s was added to the catalog: %v", name, err)
		}
	}
	if !onDisk(filepath.Join(outside, "victim.backup")) {
		t.Fatal("the outside file is gone")
	}
}

// PLAN 1.3.2: after reviewing it, an administrator removes a row whose path
// is not valid from the catalog; no file is touched, the entry is audited,
// and a valid row cannot be removed that way.
func TestForgetInvalidBackup(t *testing.T) {
	e := newBkEnv(t)
	e.svc.Audit = audit.New(e.svc.DB)
	l := e.library("client_x", StateActive)
	good := e.backup(l, bkOpt{})
	rows, outsideFile := e.escapes(l)
	bad := rows[3] // libraries/client_x/esc/victim.backup
	if err := e.svc.ForgetInvalidBackup(e.ctx, audit.CLI, good.ID); err == nil || err.Error() != "SQS-B044 only a backup whose catalog path is not valid can be removed from the catalog this way" {
		t.Fatalf("valid row: %v", err)
	}
	if got, _ := e.svc.Backup(e.ctx, good.ID); got.Status != BackupOK {
		t.Fatal("the valid row changed")
	}
	if err := e.svc.ForgetInvalidBackup(e.ctx, audit.CLI, "01JB0000000000000000000000"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if err := e.svc.ForgetInvalidBackup(e.ctx, audit.CLI, bad.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Backup(e.ctx, bad.ID)
	if got.Status != BackupDeleted || got.FilePath != bad.FilePath || got.ExpiredReason != "removed from the catalog after review: its path was not valid" {
		t.Fatalf("row %+v", got)
	}
	if !onDisk(outsideFile) || !onDisk(filepath.Join(e.svc.Cfg.BackupDir, "libraries", "client_x", "esc")) {
		t.Fatal("a file was touched")
	}
	entries, err := e.svc.Audit.List(e.ctx, 0, 10, bad.ID)
	if err != nil || len(entries) != 1 || entries[0].Action != "backup.forget" || entries[0].Outcome != audit.OK {
		t.Fatalf("audit %+v %v", entries, err)
	}
	// Without its audit entry nothing changes.
	other := rows[4]
	if _, err := e.svc.DB.Exec(`CREATE TRIGGER audit_down BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ForgetInvalidBackup(e.ctx, audit.CLI, other.ID); !errors.Is(err, audit.ErrUnavailable) {
		t.Fatalf("audit down: %v", err)
	}
	if got, _ := e.svc.Backup(e.ctx, other.ID); got.Status != BackupOK {
		t.Fatal("forgotten without its audit entry")
	}
}

// Review of 1.2.2: a backup whose path is blocked (SQS-B043) is not a
// verified backup for any safety rule: not the latest verified one, not
// one of the verified backups retention must keep, not the guard against
// deleting the newest usable one, and not the final backup a purge needs.
func TestBlockedBackupsDoNotCountAsVerified(t *testing.T) {
	e := newBkEnv(t)
	e.svc.Audit = audit.New(e.svc.DB)
	l := e.library("client_x", StateActive)
	usable := e.backup(l, bkOpt{started: time.Now().Add(-48 * time.Hour)})
	e.pathRow(l, "../../elsewhere/client_x.backup") // newer, verified and blocked
	if got, err := e.svc.LatestVerified(e.ctx, l.ID); err != nil || got == nil || got.ID != usable.ID {
		t.Fatalf("latest verified %+v %v, want the usable backup", got, err)
	}
	if err := e.svc.DeleteBackup(e.ctx, bkActor, usable.ID); err == nil || !strings.HasPrefix(err.Error(), "SQS-B034 ") {
		t.Fatalf("the newest usable backup could be deleted: %v", err)
	}
	keep, _, err := e.svc.RetentionPreview(e.ctx, l.ID, Policy{Enabled: true, Cron: "0 2 * * *", Timezone: "UTC", KeepLast: 1, MinVerified: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keep[usable.ID]; !ok {
		t.Fatalf("retention would expire the only usable verified backup: keep %v", keep)
	}

	// A trashed library whose only final backup is blocked is not purged.
	tr := e.library("trashed_x", StateTrashed)
	e.svc.DB.ExecContext(e.ctx, `UPDATE libraries SET trash_db_name = 'trashed_x__trash_202610250000' WHERE id = ?`, tr.ID)
	final := e.pathRow(tr, "libraries/trashed_x/../../../final.backup")
	e.svc.DB.ExecContext(e.ctx, `UPDATE backups SET kind = 'pre_delete' WHERE id = ?`, final.ID)
	s := e.svc
	s.Jobs = jobs.NewManager(s.DB, s.Events, events.NewBus(), s.Log, 1, 1)
	s.Sched = scheduler.New(s.DB, s.Jobs, s.Events, s.Log)
	s.Register()
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	j, err := s.Jobs.Enqueue(ctx, JobLibraryPurge, tr.ID, nil, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	s.Jobs.Start(ctx)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if j, err = s.Jobs.Get(ctx, j.ID); err == nil && j.State.Finished() {
			break
		}
	}
	s.Jobs.Shutdown(5 * time.Second)
	if j.State != jobs.Failed || j.ErrorCode != "SQS-L045" {
		t.Fatalf("purge with a blocked final backup: %s %s %s", j.State, j.ErrorCode, j.ErrorDetail)
	}
}
