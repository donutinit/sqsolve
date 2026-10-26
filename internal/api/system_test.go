// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/core"
)

// GET /system/status reports what the dashboard shows, here with
// PostgreSQL down: its error, the library and job counts and the attention
// list. GET /system/version names the build and the installation. Both need
// a credential.
func TestAPIStatusAndVersion(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedLibrary(t, "L2", "old", "Old", core.StateTrashed)
	enqueue(t, e, core.JobBackupVerify, "L1", core.VerifyParams{BackupID: "B1"})
	r := e.call(t, "GET", "/api/v1/system/status", e.read, "", false)
	var st struct {
		Postgres struct {
			Up    bool   `json:"up"`
			Error string `json:"error"`
		} `json:"postgres"`
		Volumes []struct {
			Name string `json:"name"`
		} `json:"volumes"`
		Jobs      map[string]int `json:"jobs"`
		Libraries map[string]int `json:"libraries"`
		Attention []struct {
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"attention"`
	}
	decodeInto(t, r, &st)
	if r.status != 200 || st.Postgres.Up || st.Postgres.Error != "SQS-P001 PostgreSQL is not available" ||
		st.Libraries["total"] != 2 || st.Libraries["active"] != 1 || st.Jobs["queued"] != 1 || st.Jobs["running"] != 0 || len(st.Volumes) != 3 {
		t.Fatalf("status: %d %s", r.status, r.body)
	}
	found := false
	for _, a := range st.Attention {
		found = found || a.Severity == "critical" && a.Message == "PostgreSQL is not responding: workstations cannot open projects"
	}
	if !found {
		t.Errorf("attention without PostgreSQL: %+v", st.Attention)
	}
	r = e.call(t, "GET", "/api/v1/system/version", e.read, "", false)
	var v map[string]string
	decodeInto(t, r, &v)
	if r.status != 200 || v["sqsolve"] != app.Version || v["commit"] != app.Commit || v["instance_id"] != e.a.InstanceID || v["postgres"] != "" {
		t.Fatalf("version: %d %s", r.status, r.body)
	}
	for _, p := range []string{"/api/v1/system/status", "/api/v1/system/version"} {
		wantProblem(t, p+" without credentials", e.call(t, "GET", p, "", "", false), 401, "SQS-A004 invalid or expired session")
	}
}

// Anything under /api/ that is not a route, including a known path with a
// method it does not take, is 404 with SQS-C062 before any credential is
// read.
func TestAPIUnknownRoutes(t *testing.T) {
	e := apiServer(t)
	for _, c := range [][3]string{
		{"GET", "/api/v1/nope", ""},
		{"GET", "/api/v2/libraries", e.admin},
		{"GET", "/api/", ""},
		{"POST", "/api/v1/libraries/L1", e.admin},
		{"PUT", "/api/v1/backups/B1", e.admin},
		{"DELETE", "/api/v1/jobs/J1", e.read},
		{"GET", "/api/v1/libraries/L1/credentials", e.admin},
	} {
		r := e.call(t, c[0], c[1], c[2], "", false)
		wantProblem(t, c[0]+" "+c[1], r, 404, "SQS-C062 unknown API route")
		if r.problem.Instance != c[1] || r.problem.Type != "/docs/errors/SQS-C062" || r.problem.Title != "Not Found" {
			t.Errorf("%s %s: %+v", c[0], c[1], r.problem)
		}
	}
}

// A catalog table that cannot be read makes a list route answer 500 with a
// problem instead of a partial or empty list.
func TestAPIListsWhenTheCatalogFails(t *testing.T) {
	for _, c := range []struct{ table, path string }{
		{"libraries", "/api/v1/libraries"},
		{"backups", "/api/v1/backups"},
		{"backups", "/api/v1/libraries/L1/backups"},
		{"jobs", "/api/v1/jobs"},
		{"job_logs", "/api/v1/jobs/J1/logs"},
		{"events", "/api/v1/events"},
		{"audit_log", "/api/v1/audit"},
	} {
		e := apiServer(t)
		e.exec(t, `ALTER TABLE `+c.table+` RENAME TO gone`)
		r := e.call(t, "GET", c.path, e.admin, "", false)
		if r.status != 500 || r.problem.Status != 500 || r.problem.Instance != c.path || !strings.Contains(r.problem.Detail, c.table) || strings.Contains(r.body, "items") {
			t.Errorf("%s without %s: %d %s", c.path, c.table, r.status, r.body)
		}
	}
}
