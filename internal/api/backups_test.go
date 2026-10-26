// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
)

// The catalog lists newest first and hides deleted backups unless the status
// filter asks for them, filters by library, and pages with next_cursor only
// when a page is full. GET /backups/{id} has the same shape, or 404 with
// SQS-B001.
func TestAPIBackupCatalog(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedLibrary(t, "L2", "other", "Other", core.StateActive)
	base := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	e.seedBackup(t, "B1", "L1", core.BackupOK, base)
	e.seedBackup(t, "B2", "L1", "failed", base.Add(time.Hour))
	e.seedBackup(t, "B3", "L1", "deleted", base.Add(2*time.Hour))
	e.seedBackup(t, "B4", "L2", core.BackupOK, base.Add(3*time.Hour))
	for _, c := range []struct{ path, ids, next string }{
		{"/api/v1/backups", "B4,B2,B1", ""},
		{"/api/v1/backups?library_id=L1", "B2,B1", ""},
		{"/api/v1/libraries/L1/backups", "B2,B1", ""},
		{"/api/v1/libraries/L1/backups?status=deleted", "B3", ""},
		{"/api/v1/libraries/L2/backups?status=failed", "", ""},
		{"/api/v1/backups?limit=2", "B4,B2", "B2"},
		{"/api/v1/backups?limit=2&cursor=B2", "B1", ""},
		{"/api/v1/backups?limit=3", "B4,B2,B1", "B1"},
		// Limits out of range keep the default page of 200.
		{"/api/v1/backups?limit=0", "B4,B2,B1", ""},
		{"/api/v1/backups?limit=501", "B4,B2,B1", ""},
		{"/api/v1/backups?limit=two", "B4,B2,B1", ""},
	} {
		r := e.call(t, "GET", c.path, e.read, "", false)
		var out struct {
			Items []BackupDTO `json:"items"`
			Next  string      `json:"next_cursor"`
		}
		decodeInto(t, r, &out)
		var ids []string
		for _, b := range out.Items {
			ids = append(ids, b.ID)
		}
		if r.status != 200 || out.Items == nil || strings.Join(ids, ",") != c.ids || out.Next != c.next {
			t.Errorf("%s: %d %v next %q, want %s next %q", c.path, r.status, ids, out.Next, c.ids, c.next)
		}
	}
	r := e.call(t, "GET", "/api/v1/backups/B1", e.read, "", false)
	var b BackupDTO
	decodeInto(t, r, &b)
	if r.status != 200 || b.ID != "B1" || b.LibraryID != "L1" || b.Kind != "manual" || b.Status != "ok" || b.VerifyStatus != "l1_ok" ||
		b.SizeBytes != 14 || b.SHA256 != "c0ffee" || !b.StartedAt.Equal(base) || b.FinishedAt != nil {
		t.Fatalf("backup: %d %s", r.status, r.body)
	}
	wantProblem(t, "unknown backup", e.call(t, "GET", "/api/v1/backups/NOPE", e.read, "", false), 404, "SQS-B001 backup not found")
}

// Back up now: 404 for an unknown library, SQS-B003 for a library that
// cannot be backed up, the backup scope for tokens, and no job without its
// accepted entry (ADR 0026).
func TestAPIBackupNowRefusals(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedLibrary(t, "L2", "old", "Old", core.StateTrashed)
	wantProblem(t, "unknown library", e.call(t, "POST", "/api/v1/libraries/NOPE/backups", e.admin, "", false), 404, "SQS-L002 library not found")
	wantProblem(t, "trashed library", e.call(t, "POST", "/api/v1/libraries/L2/backups", e.admin, "", false), 422, "SQS-B003 cannot back up a library in state In the trash")
	wantProblem(t, "restore token", e.call(t, "POST", "/api/v1/libraries/L1/backups", e.token(t, "restore"), "", false), 403, a030)
	backup := e.token(t, "backup")
	e.auditDown(t)
	wantProblemPrefix(t, "audit log down", e.call(t, "POST", "/api/v1/libraries/L1/backups", backup, "", false), 503, x040)
	if js := e.jobsOf(t, "L1"); len(js) != 0 {
		t.Fatalf("a backup was queued without its entry: %+v", js)
	}
}

// Verify queues backup.verify for the backup's library with the backup in
// its parameters; tokens need the backup scope.
func TestAPIBackupVerify(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedBackup(t, "B1", "L1", core.BackupOK, time.Now())
	r := e.call(t, "POST", "/api/v1/backups/B1/verify", e.token(t, "backup"), "", false)
	var j jobs.Job
	decodeInto(t, r, &j)
	var params core.VerifyParams
	json.Unmarshal(j.Params, &params)
	if r.status != 202 || j.Type != core.JobBackupVerify || j.LibraryID != "L1" || j.State != jobs.Queued || params.BackupID != "B1" || j.CreatedBy.Name != "boss" {
		t.Fatalf("verify: %d %s", r.status, r.body)
	}
	wantProblem(t, "unknown backup", e.call(t, "POST", "/api/v1/backups/NOPE/verify", e.admin, "", false), 404, "SQS-B001 backup not found")
	wantProblem(t, "read token", e.call(t, "POST", "/api/v1/backups/B1/verify", e.read, "", false), 403, a030)
}

// Restoring by replacing needs restore.replace although the route only asks
// for restore.new, the slug as confirmation, a usable backup and a library
// that is live; its job is queued with its accepted entry or not at all.
func TestAPIRestoreReplace(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedLibrary(t, "L2", "old", "Old", core.StateTrashed)
	e.seedBackup(t, "B1", "L1", core.BackupOK, time.Now())
	e.seedBackup(t, "B2", "L1", "failed", time.Now())
	e.seedBackup(t, "B3", "L2", core.BackupOK, time.Now())
	e.exec(t, `INSERT INTO backups(id, library_id, kind, status, verify_status, started_at) VALUES ('B4', 'L1', 'manual', 'ok', 'failed', ?)`, store.NowT())
	const replace = `{"mode":"replace","confirm":"lib"}`
	operator := e.as(t, "olga", auth.RoleOperator)
	wantProblem(t, "operator session", operator.call(t, "POST", "/api/v1/backups/B1/restore", "cookie", replace, true), 403,
		"SQS-A030 replacing requires the restore or admin scope")
	wantProblem(t, "backup token", e.call(t, "POST", "/api/v1/backups/B1/restore", e.token(t, "backup"), replace, false), 403, a030)
	restore := e.token(t, "restore")
	for _, c := range []struct {
		name, backup, body string
		status             int
		detail             string
	}{
		{"display name as confirmation", "B1", `{"mode":"replace","confirm":"Lib"}`, 422, "SQS-R002 type the library identifier to confirm"},
		{"failed backup", "B2", replace, 422, "SQS-R001 the backup cannot be restored (it failed or was deleted)"},
		{"backup that failed verification", "B4", replace, 422, "SQS-R001 the backup cannot be restored (it failed or was deleted)"},
		{"library in the trash", "B3", `{"mode":"replace","confirm":"old"}`, 422, "SQS-R003 cannot replace a library in state In the trash"},
		{"unknown backup", "NOPE", replace, 404, "SQS-B001 backup not found"},
		{"unknown field", "B1", `{"mode":"replace","confirm":"lib","force":true}`, 400, `SQS-C061 invalid JSON body: json: unknown field "force"`},
	} {
		wantProblem(t, c.name, e.call(t, "POST", "/api/v1/backups/"+c.backup+"/restore", restore, c.body, false), c.status, c.detail)
	}
	e.auditDown(t)
	wantProblemPrefix(t, "audit log down", e.call(t, "POST", "/api/v1/backups/B1/restore", restore, replace, false), 503, x040)
	if js := e.jobsOf(t, "L1"); len(js) != 0 {
		t.Fatalf("jobs queued by refused restores: %+v", js)
	}
	e.exec(t, `DROP TRIGGER audit_down`)
	r := e.call(t, "POST", "/api/v1/backups/B1/restore", restore, `{"mode":"replace","confirm":"lib","terminate_sessions":true}`, false)
	var j jobs.Job
	decodeInto(t, r, &j)
	var params core.RestoreParams
	json.Unmarshal(j.Params, &params)
	if r.status != 202 || j.Type != core.JobRestoreReplace || j.LibraryID != "L1" || params.BackupID != "B1" || !params.TerminateSessions {
		t.Fatalf("restore by replacing: %d %s", r.status, r.body)
	}
	found := false
	for _, en := range e.entries(t, "L1") {
		found = found || en.Action == "restore.replace" && en.Outcome == audit.Accepted && en.Details["job_id"] == j.ID && en.Details["backup_id"] == "B1"
	}
	if !found {
		t.Fatalf("no accepted entry: %+v", e.entries(t, "L1"))
	}
}

// Restoring as new checks the backup, the display name and the identifier
// before asking PostgreSQL whether the identifier is free; without
// PostgreSQL that question fails with SQS-P001 and no library is added. An
// omitted mode means new.
func TestAPIRestoreAsNewRefusals(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedBackup(t, "B1", "L1", core.BackupOK, time.Now())
	e.seedBackup(t, "B2", "L1", "failed", time.Now())
	for _, c := range []struct {
		name, backup, body string
		status             int
		detail             string
	}{
		{"failed backup", "B2", `{"mode":"new","slug":"copy","display_name":"Copy"}`, 422, "SQS-R001 the backup cannot be restored (it failed or was deleted)"},
		{"blank display name", "B1", `{"slug":"copy","display_name":" "}`, 422, "SQS-L003 the display name must be 1 to 80 characters long"},
		{"invalid identifier", "B1", `{"mode":"new","slug":"Copy!","display_name":"Copy"}`, 422,
			"SQS-L001 invalid identifier: use 3 to 40 characters: lowercase letters, digits and underscore, starting with a letter"},
		{"reserved identifier", "B1", `{"slug":"postgres_copy","display_name":"Copy"}`, 422, `SQS-L001 invalid identifier: cannot start with "postgres"`},
		{"identifier in use", "B1", `{"slug":"lib","display_name":"Copy"}`, 422, `SQS-L004 a library with identifier "lib" already exists`},
		{"PostgreSQL down", "B1", `{"mode":"new","slug":"copy","display_name":"Copy"}`, 503, "SQS-P001 PostgreSQL is not available"},
		{"unknown backup", "NOPE", `{"slug":"copy","display_name":"Copy"}`, 404, "SQS-B001 backup not found"},
	} {
		wantProblem(t, c.name, e.call(t, "POST", "/api/v1/backups/"+c.backup+"/restore", e.admin, c.body, false), c.status, c.detail)
	}
	wantProblem(t, "read token", e.call(t, "POST", "/api/v1/backups/B1/restore", e.read, `{"slug":"copy","display_name":"Copy"}`, false), 403, a030)
	var n int
	e.a.DB.QueryRow(`SELECT count(*) FROM libraries`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d libraries after refused restores", n)
	}
}

// The backup file is streamed with its checksum only after its ok entry is
// written; a backup that is not ok or whose file is gone is 404, and tokens
// need the restore scope.
func TestAPIBackupDownload(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedBackup(t, "B1", "L1", core.BackupOK, time.Now())
	e.seedBackup(t, "B2", "L1", "failed", time.Now())
	e.seedBackup(t, "B3", "L1", core.BackupOK, time.Now())
	os.MkdirAll(filepath.Join(e.a.Cfg.BackupDir, "libraries"), 0o700)
	if err := os.WriteFile(filepath.Join(e.a.Cfg.BackupDir, "libraries", "B1.backup"), []byte("the dump bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := e.token(t, "restore")
	r := e.call(t, "GET", "/api/v1/backups/B1/download", restore, "", false)
	if r.status != 200 || r.body != "the dump bytes" || r.header.Get("Content-Type") != "application/octet-stream" ||
		r.header.Get("Content-Disposition") != `attachment; filename="B1.backup"` || r.header.Get("X-Sqsolve-Sha256") != "c0ffee" {
		t.Fatalf("download: %d %v %q", r.status, r.header, r.body)
	}
	found := false
	for _, en := range e.entries(t, "B1") {
		found = found || en.Action == "backup.download" && en.Outcome == audit.OK && en.TargetType == "backup" && en.Actor.Type == "token"
	}
	if !found {
		t.Fatalf("no download entry: %+v", e.entries(t, "B1"))
	}
	wantProblem(t, "failed backup", e.call(t, "GET", "/api/v1/backups/B2/download", restore, "", false), 404, "SQS-B001 backup not found")
	wantProblem(t, "unknown backup", e.call(t, "GET", "/api/v1/backups/NOPE/download", restore, "", false), 404, "SQS-B001 backup not found")
	if r := e.call(t, "GET", "/api/v1/backups/B3/download", restore, "", false); r.status != 404 || r.header.Get("X-Sqsolve-Sha256") != "" || len(e.entries(t, "B3")) != 0 {
		t.Fatalf("download of a missing file: %d %s", r.status, r.body)
	}
	wantProblem(t, "backup token", e.call(t, "GET", "/api/v1/backups/B1/download", e.token(t, "backup"), "", false), 403, a030)
	e.auditDown(t)
	r = e.call(t, "GET", "/api/v1/backups/B1/download", restore, "", false)
	wantProblemPrefix(t, "audit log down", r, 503, x040)
	if strings.Contains(r.body, "the dump bytes") {
		t.Fatal("the file went out without its entry")
	}
}
