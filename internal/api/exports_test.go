// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// An object id as Resolve stores them; nothing with it exists anywhere.
const someUUID = "00000000-0000-4000-8000-0000000000aa"

// exportPaths are the GET routes of the experimental export that read a
// library or a prepared backup.
var exportPaths = []string{
	"/api/v1/libraries/L1/projects",
	"/api/v1/libraries/L1/grades",
	"/api/v1/libraries/L1/projects/" + someUUID + "/export",
	"/api/v1/libraries/L1/grades/" + someUUID + "/export",
	"/api/v1/libraries/L1/grades/albums/" + someUUID + "/export",
	"/api/v1/backups/B1/export",
	"/api/v1/backups/B1/export/projects/" + someUUID,
	"/api/v1/backups/B1/export/grades/" + someUUID,
	"/api/v1/backups/B1/export/grades/albums/" + someUUID,
}

// With the experimental export off (the default) every export route answers
// 409 with SQS-E002 before PostgreSQL is asked anything; closing a backup
// that was never prepared has nothing to do.
func TestAPIExportsDisabled(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedBackup(t, "B1", "L1", core.BackupOK, time.Now())
	const off = "SQS-E002 experimental export is disabled; an administrator can enable it in Settings"
	for _, p := range exportPaths {
		wantProblem(t, p, e.call(t, "GET", p, e.admin, "", false), 409, off)
	}
	wantProblem(t, "prepare", e.call(t, "POST", "/api/v1/backups/B1/export", e.admin, "", false), 409, off)
	if r := e.call(t, "DELETE", "/api/v1/backups/B1/export", e.admin, "", false); r.status != 204 || r.body != "" {
		t.Fatalf("close without a workspace: %d %s", r.status, r.body)
	}
}

// With the export on, the library must be active or in maintenance (503
// with SQS-E005 and its state) and its database reachable. Listing needs
// read; downloading needs the export scope.
func TestAPIExportsNeedALiveLibrary(t *testing.T) {
	e := apiServer(t)
	e.enableExports(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedLibrary(t, "L2", "old", "Old", core.StateTrashed)
	export := e.token(t, "export")
	wantProblem(t, "trashed library", e.call(t, "GET", "/api/v1/libraries/L2/projects", e.read, "", false), 503,
		"SQS-E005 the library is not available for export: it is in state In the trash")
	for _, p := range exportPaths[:5] {
		// apiServer has no PostgreSQL.
		wantProblemPrefix(t, p, e.call(t, "GET", p, export, "", false), 503, "SQS-E005 the library is not available for export: ")
		wantProblem(t, p+" of an unknown library", e.call(t, "GET", strings.Replace(p, "/L1/", "/NOPE/", 1), export, "", false), 404, "SQS-L002 library not found")
	}
	for _, p := range exportPaths[2:5] {
		wantProblem(t, p+" with a read token", e.call(t, "GET", p, e.read, "", false), 403, a030)
		wantProblem(t, p+" with a backup token", e.call(t, "GET", p, e.token(t, "backup"), "", false), 403, a030)
	}
}

// Preparing an export from a backup queues backup.export with its accepted
// entry and a workspace, and a second request returns the live workspace.
// Until it is ready the status says so and downloads answer 409 with
// SQS-E007. Without the audit log nothing is queued and no workspace is
// left behind.
func TestAPIBackupExportPrepare(t *testing.T) {
	e := apiServer(t)
	e.enableExports(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedBackup(t, "B1", "L1", core.BackupOK, time.Now())
	e.seedBackup(t, "B2", "L1", "failed", time.Now())
	export := e.token(t, "export")
	workspaces := func() int {
		var n int
		e.a.DB.QueryRow(`SELECT count(*) FROM export_workspaces`).Scan(&n)
		return n
	}
	e.auditDown(t)
	wantProblemPrefix(t, "audit log down", e.call(t, "POST", "/api/v1/backups/B1/export", export, "", false), 503, x040)
	if n, js := workspaces(), e.jobsOf(t, "L1"); n != 0 || len(js) != 0 {
		t.Fatalf("a refused preparation left %d workspaces and jobs %+v", n, js)
	}
	e.exec(t, `DROP TRIGGER audit_down`)
	wantProblem(t, "failed backup", e.call(t, "POST", "/api/v1/backups/B2/export", export, "", false), 422, "SQS-E005 the backup is not available (it failed or was deleted)")
	wantProblem(t, "unknown backup", e.call(t, "POST", "/api/v1/backups/NOPE/export", export, "", false), 404, "SQS-B001 backup not found")
	wantProblem(t, "read token", e.call(t, "POST", "/api/v1/backups/B1/export", e.read, "", false), 403, a030)

	type prepared struct {
		Workspace core.ExportWorkspace `json:"workspace"`
		Job       jobs.Job             `json:"job"`
	}
	r := e.call(t, "POST", "/api/v1/backups/B1/export", export, "", false)
	var p prepared
	decodeInto(t, r, &p)
	if r.status != 202 || p.Workspace.ID == "" || p.Workspace.BackupID != "B1" || p.Workspace.State != core.WorkspacePreparing ||
		p.Workspace.JobID != p.Job.ID || p.Job.Type != core.JobBackupExport || p.Job.LibraryID != "L1" || !p.Workspace.ExpiresAt.After(time.Now()) {
		t.Fatalf("prepare: %d %s", r.status, r.body)
	}
	found := false
	for _, en := range e.entries(t, "B1") {
		found = found || en.Action == "backup.export_prepare" && en.Outcome == audit.Accepted && en.Details["job_id"] == p.Job.ID && en.Details["workspace"] == p.Workspace.ID
	}
	if !found {
		t.Fatalf("no accepted entry: %+v", e.entries(t, "B1"))
	}
	var again prepared
	decodeInto(t, e.call(t, "POST", "/api/v1/backups/B1/export", export, "", false), &again)
	if again.Workspace.ID != p.Workspace.ID || again.Job.ID != p.Job.ID || workspaces() != 1 || len(e.jobsOf(t, "L1")) != 1 {
		t.Fatalf("a second preparation: %+v", again)
	}

	r = e.call(t, "GET", "/api/v1/backups/B1/export", e.read, "", false)
	var st struct {
		Workspace    *core.ExportWorkspace `json:"workspace"`
		Ready        bool                  `json:"ready"`
		Experimental bool                  `json:"experimental"`
	}
	decodeInto(t, r, &st)
	if r.status != 200 || st.Ready || !st.Experimental || st.Workspace == nil || st.Workspace.ID != p.Workspace.ID || st.Workspace.State != core.WorkspacePreparing {
		t.Fatalf("status while preparing: %d %s", r.status, r.body)
	}
	if strings.Contains(r.body, "sqsolve_export") || strings.Contains(r.body, "db_name") {
		t.Fatalf("the status shows the workspace database: %s", r.body)
	}
	for _, path := range exportPaths[6:] {
		wantProblem(t, path, e.call(t, "GET", path, export, "", false), 409, "SQS-E007 the backup is not prepared for export; prepare it first")
	}
	wantProblem(t, "download from an unknown backup", e.call(t, "GET", "/api/v1/backups/NOPE/export/projects/"+someUUID, export, "", false), 404, "SQS-B001 backup not found")
	wantProblem(t, "status of an unknown backup", e.call(t, "GET", "/api/v1/backups/NOPE/export", e.read, "", false), 404, "SQS-B001 backup not found")
	r = e.call(t, "GET", "/api/v1/backups/B2/export", e.read, "", false)
	decodeInto(t, r, &st)
	if r.status != 200 || st.Ready || st.Workspace != nil {
		t.Fatalf("status of a backup never prepared: %d %s", r.status, r.body)
	}
}

// A ready workspace is read in PostgreSQL: when its database cannot be
// reached the status and the downloads answer 503 with SQS-E005. Closing
// drops that database after an accepted entry, so a PostgreSQL failure
// leaves the workspace and a failed entry, and with the audit log down
// nothing is tried.
func TestAPIBackupExportReadyWorkspace(t *testing.T) {
	e := apiServer(t)
	e.enableExports(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedBackup(t, "B1", "L1", core.BackupOK, time.Now())
	export := e.token(t, "export")
	if r := e.call(t, "POST", "/api/v1/backups/B1/export", export, "", false); r.status != 202 {
		t.Fatalf("prepare: %d %s", r.status, r.body)
	}
	e.exec(t, `UPDATE export_workspaces SET state = 'ready'`)
	for _, path := range exportPaths[5:] {
		wantProblemPrefix(t, path, e.call(t, "GET", path, export, "", false), 503, "SQS-E005 the library is not available for export: ")
	}
	wantProblemPrefix(t, "close without PostgreSQL", e.call(t, "DELETE", "/api/v1/backups/B1/export", export, "", false), 503, "SQS-P001 PostgreSQL is not available")
	var accepted, failed bool
	for _, en := range e.entries(t, "B1") {
		if en.Action == "backup.export_close" {
			accepted = accepted || en.Outcome == audit.Accepted
			failed = failed || en.Outcome == audit.Failed && strings.HasPrefix(en.Details["error"].(string), "SQS-P001 ")
		}
	}
	state := func() string {
		var s string
		e.a.DB.QueryRow(`SELECT state FROM export_workspaces`).Scan(&s)
		return s
	}
	if !accepted || !failed || state() != core.WorkspaceReady {
		t.Fatalf("close entries %+v, workspace %s", e.entries(t, "B1"), state())
	}
	wantProblem(t, "close with a read token", e.call(t, "DELETE", "/api/v1/backups/B1/export", e.read, "", false), 403, a030)
	wantProblem(t, "close an unknown backup", e.call(t, "DELETE", "/api/v1/backups/NOPE/export", export, "", false), 404, "SQS-B001 backup not found")
	e.auditDown(t)
	wantProblemPrefix(t, "audit log down", e.call(t, "DELETE", "/api/v1/backups/B1/export", export, "", false), 503, x040)
	if state() != core.WorkspaceReady {
		t.Fatalf("workspace after a refused close: %s", state())
	}
}
