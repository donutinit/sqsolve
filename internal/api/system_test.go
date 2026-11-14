// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/health"
	"github.com/donutinit/sqsolve/internal/store"
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

// The lists of GET /system/status are [] when there is nothing in them, never
// null (docs 10 §1), and they keep their shape: an attention line without its
// panel link, a volume without its marker id.
func TestStatusBodyListsAreNeverNull(t *testing.T) {
	lists := func(st health.Status) map[string]json.RawMessage {
		t.Helper()
		b, err := json.Marshal(statusBody(st))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := lists(health.Status{}); string(got["attention"]) != "[]" || string(got["volumes"]) != "[]" {
		t.Errorf("nothing to report: attention %s, volumes %s", got["attention"], got["volumes"])
	}
	got := lists(health.Status{
		Attention: []health.Attention{{Severity: events.Warning, Message: "Volume Backups is at 85 %", Link: "/health"}},
		Volumes:   []health.Volume{{ID: "backups", Name: "Backups", Path: "/backups", Total: 100, Free: 15, UsedPct: 85}},
	})
	if string(got["attention"]) != `[{"severity":"warning","message":"Volume Backups is at 85 %"}]` ||
		string(got["volumes"]) != `[{"name":"Backups","path":"/backups","total_bytes":100,"free_bytes":15,"used_percent":85}]` {
		t.Errorf("one of each: attention %s, volumes %s", got["attention"], got["volumes"])
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
// problem instead of a partial or empty list. The library L1 and the job J1
// that the routes name exist, so the failure is the table's and not an
// unknown library or job.
func TestAPIListsWhenTheCatalogFails(t *testing.T) {
	for _, c := range []struct{ table, path string }{
		{"libraries", "/api/v1/libraries"},
		{"backups", "/api/v1/backups"},
		{"backups", "/api/v1/libraries/L1/backups"},
		{"jobs", "/api/v1/jobs"},
		{"jobs", "/api/v1/jobs/J1/logs"},
		{"job_logs", "/api/v1/jobs/J1/logs"},
		{"job_steps", "/api/v1/jobs/J1"},
		{"events", "/api/v1/events"},
		{"audit_log", "/api/v1/audit"},
	} {
		e := apiServer(t)
		now := store.NowT()
		e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
		e.exec(t, `INSERT INTO jobs(id, type, state, scheduled_for, created_at, created_by_type) VALUES ('J1', 'system.vacuum', 'succeeded', ?, ?, 'system')`, now, now)
		e.exec(t, `ALTER TABLE `+c.table+` RENAME TO gone`)
		r := e.call(t, "GET", c.path, e.admin, "", false)
		if r.status != 500 || r.problem.Status != 500 || r.problem.Instance != c.path || !strings.Contains(r.problem.Detail, c.table) || strings.Contains(r.body, "items") {
			t.Errorf("%s without %s: %d %s", c.path, c.table, r.status, r.body)
		}
	}
}

// The library or the job that a route names cannot be read when its table
// cannot: that is 500, a failure of sqsolve, and not 422, which is a refusal
// of the request. An unknown library or job stays 404.
func TestAPIDetailRoutesWhenTheCatalogFails(t *testing.T) {
	for _, c := range []struct{ method, table, path string }{
		{"GET", "libraries", "/api/v1/libraries/L1"},
		{"PATCH", "libraries", "/api/v1/libraries/L1"},
		{"GET", "libraries", "/api/v1/libraries/L1/backups"},
		{"GET", "libraries", "/api/v1/libraries/L1/projects"},
		{"GET", "libraries", "/api/v1/libraries/L1/grades"},
		{"GET", "libraries", "/api/v1/libraries/L1/projects/P1/export"},
		{"GET", "libraries", "/api/v1/libraries/L1/grades/S1/export"},
		{"GET", "libraries", "/api/v1/libraries/L1/grades/albums/V1/export"},
		{"GET", "jobs", "/api/v1/jobs/J1"},
	} {
		e := apiServer(t)
		now := store.NowT()
		e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
		e.exec(t, `INSERT INTO jobs(id, type, state, scheduled_for, created_at, created_by_type) VALUES ('J1', 'system.vacuum', 'succeeded', ?, ?, 'system')`, now, now)
		e.exec(t, `ALTER TABLE `+c.table+` RENAME TO gone`)
		r := e.call(t, c.method, c.path, e.admin, `{"notes":"lost"}`, false)
		if r.status != 500 || r.problem.Status != 500 || r.problem.Title != "Internal Server Error" || r.problem.Type != "about:blank" ||
			r.problem.Code != "" || r.problem.Instance != c.path || !strings.Contains(r.problem.Detail, "no such table: "+c.table) {
			t.Errorf("%s %s without %s: %d %s", c.method, c.path, c.table, r.status, r.body)
		}
	}
}
