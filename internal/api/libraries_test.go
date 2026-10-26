// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
)

// a030 is the refusal of guard for a role or token without the action.
const a030 = "SQS-A030 the role or the token scopes do not allow this action"

// GET /libraries hides purged libraries unless the state filter asks for
// them and sorts by display name; an empty result is an empty list. GET
// /libraries/{id} has the same shape, or 404 with SQS-L002.
func TestAPILibraryListAndDetail(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "zulu", "Zulu", core.StateActive)
	e.seedLibrary(t, "L2", "alpha", "alpha", core.StateTrashed)
	e.seedLibrary(t, "L3", "gone", "Gone", core.StatePurged)
	list := func(query string) string {
		t.Helper()
		r := e.call(t, "GET", "/api/v1/libraries"+query, e.read, "", false)
		var out struct {
			Items []LibraryDTO `json:"items"`
		}
		decodeInto(t, r, &out)
		if r.status != 200 || out.Items == nil {
			t.Fatalf("list %q: %d %s", query, r.status, r.body)
		}
		var ids []string
		for _, l := range out.Items {
			ids = append(ids, l.ID)
		}
		return strings.Join(ids, ",")
	}
	for query, want := range map[string]string{"": "L2,L1", "?state=purged": "L3", "?state=trashed": "L2", "?state=maintenance": ""} {
		if got := list(query); got != want {
			t.Errorf("libraries%s: %q, want %q", query, got, want)
		}
	}
	r := e.call(t, "GET", "/api/v1/libraries/L1", e.read, "", false)
	var l LibraryDTO
	decodeInto(t, r, &l)
	if r.status != 200 || l.ID != "L1" || l.Slug != "zulu" || l.DisplayName != "Zulu" || l.State != "active" || l.StateLabel != "Active" ||
		l.Database != "zulu" || l.OwnerRole != "zulu" || l.ConnLimit != -1 || l.CreatedAt.IsZero() {
		t.Fatalf("library: %d %s", r.status, r.body)
	}
	wantProblem(t, "unknown library", e.call(t, "GET", "/api/v1/libraries/NOPE", e.read, "", false), 404, "SQS-L002 library not found")
}

// PATCH changes only the fields it carries and records library.update;
// every invalid value is refused with its code before anything changes, and
// a new connection limit for a live library goes to PostgreSQL first.
func TestAPILibraryPatch(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedLibrary(t, "L2", "waiting", "Waiting", core.StateAwaitingInit)
	r := e.call(t, "PATCH", "/api/v1/libraries/L1", e.admin, `{"notes":"Grade by Friday","resolve_version":" 20.2 "}`, false)
	var l LibraryDTO
	decodeInto(t, r, &l)
	if r.status != 200 || l.DisplayName != "Lib" || l.Notes != "Grade by Friday" || l.ResolveVersion != "20.2" || l.ConnLimit != -1 {
		t.Fatalf("patch notes: %d %s", r.status, r.body)
	}
	if !e.hasEntry(t, "L1", "library.update", audit.OK) {
		t.Errorf("no library.update entry: %+v", e.entries(t, "L1"))
	}
	// Without a live database the limit is only stored.
	r = e.call(t, "PATCH", "/api/v1/libraries/L2", e.admin, `{"display_name":"  Waiting room ","connection_limit":8}`, false)
	decodeInto(t, r, &l)
	if r.status != 200 || l.DisplayName != "Waiting room" || l.ConnLimit != 8 {
		t.Fatalf("patch name and limit: %d %s", r.status, r.body)
	}
	for _, c := range []struct {
		name, body string
		status     int
		detail     string
	}{
		{"blank name", `{"display_name":"  "}`, 422, "SQS-L003 the display name must be 1 to 80 characters long"},
		{"name too long", `{"display_name":"` + strings.Repeat("n", 81) + `"}`, 422, "SQS-L003 the display name must be 1 to 80 characters long"},
		{"control character", `{"display_name":"a\u0007b"}`, 422, "SQS-L003 the display name cannot contain control characters"},
		{"limit above 1000", `{"connection_limit":1001}`, 422, "SQS-L012 invalid connection limit (-1 for unlimited, up to 1000)"},
		{"limit below -1", `{"connection_limit":-2}`, 422, "SQS-L012 invalid connection limit (-1 for unlimited, up to 1000)"},
		{"long Resolve version", `{"resolve_version":"` + strings.Repeat("9", 21) + `"}`, 422, "SQS-L013 Resolve version too long"},
		{"long notes", `{"notes":"` + strings.Repeat("n", 10001) + `"}`, 422, "SQS-L014 notes too long"},
		{"unknown field", `{"slug":"other"}`, 400, `SQS-C061 invalid JSON body: json: unknown field "slug"`},
		{"live library without PostgreSQL", `{"connection_limit":5,"notes":"lost"}`, 503, "SQS-P001 PostgreSQL is not available"},
	} {
		wantProblem(t, c.name, e.call(t, "PATCH", "/api/v1/libraries/L1", e.admin, c.body, false), c.status, c.detail)
	}
	r = e.call(t, "GET", "/api/v1/libraries/L1", e.read, "", false)
	decodeInto(t, r, &l)
	if l.DisplayName != "Lib" || l.Notes != "Grade by Friday" || l.ResolveVersion != "20.2" || l.ConnLimit != -1 {
		t.Fatalf("a refused patch changed the library: %s", r.body)
	}
	wantProblem(t, "unknown library", e.call(t, "PATCH", "/api/v1/libraries/NOPE", e.admin, `{"notes":"x"}`, false), 404, "SQS-L002 library not found")
	wantProblem(t, "read token", e.call(t, "PATCH", "/api/v1/libraries/L1", e.read, `{"notes":"x"}`, false), 403, a030)
}

// DELETE queues the move to the trash only with the slug as confirmation and
// from a state that allows it. The job and its accepted entry are written
// together: with the audit log down neither exists (ADR 0026).
func TestAPILibraryDelete(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedLibrary(t, "L2", "old", "Old", core.StateTrashed)
	wantProblem(t, "display name as confirmation", e.call(t, "DELETE", "/api/v1/libraries/L1", e.admin, `{"confirm":"Lib"}`, false),
		422, "SQS-L040 type the library identifier to confirm")
	wantProblem(t, "already in the trash", e.call(t, "DELETE", "/api/v1/libraries/L2", e.admin, `{"confirm":"old"}`, false),
		422, "SQS-L041 cannot delete a library in state In the trash")
	wantProblem(t, "read token", e.call(t, "DELETE", "/api/v1/libraries/L1", e.read, `{"confirm":"lib"}`, false), 403, a030)
	wantProblem(t, "no body", e.call(t, "DELETE", "/api/v1/libraries/L1", e.admin, "", false), 400, "SQS-C061 invalid JSON body: the body is empty")
	e.auditDown(t)
	wantProblemPrefix(t, "audit log down", e.call(t, "DELETE", "/api/v1/libraries/L1", e.admin, `{"confirm":"lib"}`, false), 503, x040)
	if js := e.jobsOf(t, "L1"); len(js) != 0 {
		t.Fatalf("jobs queued by refused deletes: %+v", js)
	}
	e.exec(t, `DROP TRIGGER audit_down`)
	r := e.call(t, "DELETE", "/api/v1/libraries/L1", e.admin, `{"confirm":"lib"}`, false)
	var j jobs.Job
	decodeInto(t, r, &j)
	if r.status != 202 || j.Type != core.JobLibraryDelete || j.State != jobs.Queued || j.LibraryID != "L1" {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	found := false
	for _, en := range e.entries(t, "L1") {
		found = found || en.Action == "library.delete" && en.Outcome == audit.Accepted && en.Details["job_id"] == j.ID && en.Details["slug"] == "lib"
	}
	if !found {
		t.Fatalf("no accepted entry with the job: %+v", e.entries(t, "L1"))
	}
}

// Maintenance is refused in a state that cannot change it. Otherwise its
// accepted entry comes first: a PostgreSQL failure leaves that entry and a
// failed one with the error, and with the audit log down the change is not
// tried. Either way the library keeps its state.
func TestAPIMaintenance(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	const path = "/api/v1/libraries/L1/maintenance"
	wantProblem(t, "reopen an active library", e.call(t, "POST", path, e.admin, `{"enabled":false}`, false), 422, "SQS-L015 cannot change maintenance in state Active")
	wantProblem(t, "read token", e.call(t, "POST", path, e.read, `{"enabled":true}`, false), 403, a030)
	wantProblem(t, "unknown field", e.call(t, "POST", path, e.admin, `{"on":true}`, false), 400, `SQS-C061 invalid JSON body: json: unknown field "on"`)
	// apiServer has no PostgreSQL.
	wantProblemPrefix(t, "PostgreSQL down", e.call(t, "POST", path, e.admin, `{"enabled":true,"terminate_sessions":true}`, false), 503, "SQS-P001 PostgreSQL is not available")
	var accepted, failed bool
	for _, en := range e.entries(t, "L1") {
		if en.Action != "library.maintenance" || en.Details["on"] != true || en.Details["terminate_sessions"] != true {
			continue
		}
		accepted = accepted || en.Outcome == audit.Accepted
		failed = failed || en.Outcome == audit.Failed && strings.HasPrefix(en.Details["error"].(string), "SQS-P001 ")
	}
	if !accepted || !failed {
		t.Fatalf("maintenance entries: %+v", e.entries(t, "L1"))
	}
	e.auditDown(t)
	wantProblemPrefix(t, "audit log down", e.call(t, "POST", path, e.admin, `{"enabled":true}`, false), 503, x040)
	r := e.call(t, "GET", "/api/v1/libraries/L1", e.read, "", false)
	var l LibraryDTO
	decodeInto(t, r, &l)
	if l.State != "active" {
		t.Fatalf("state after refused maintenance: %s", r.body)
	}
}

// GET connection never carries a password. Reveal returns it only after its
// ok entry is written, never for a revoked credential and never to a token
// without the admin scope.
func TestAPIConnectionAndReveal(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedLibrary(t, "L2", "bare", "Bare", core.StateActive)
	e.seedCredential(t, "C1", "L1", "owner", "lib", "silver halide 400")
	e.seedCredential(t, "C2", "L1", "member", "lib_ana", "cyan magenta yellow")
	e.exec(t, `UPDATE library_credentials SET revoked_at = ? WHERE id = 'C2'`, store.NowT())
	r := e.call(t, "GET", "/api/v1/libraries/L1/connection", e.read, "", false)
	var c map[string]any
	decodeInto(t, r, &c)
	if _, ok := c["password"]; r.status != 200 || ok || c["user"] != "lib" || c["database"] != "lib" || c["port"] != float64(1) ||
		c["library_name"] != "Lib" || strings.Contains(r.body, "silver") {
		t.Fatalf("connection: %d %s", r.status, r.body)
	}
	wantProblem(t, "library without credentials", e.call(t, "GET", "/api/v1/libraries/L2/connection", e.read, "", false), 422, "SQS-L030 credential not found")
	wantProblem(t, "unknown credential", e.call(t, "GET", "/api/v1/libraries/L1/connection?credential=NOPE", e.read, "", false), 422, "SQS-L030 credential not found")
	wantProblem(t, "unknown library", e.call(t, "GET", "/api/v1/libraries/NOPE/connection", e.read, "", false), 404, "SQS-L002 library not found")

	const reveal = "/api/v1/libraries/L1/connection/reveal"
	r = e.call(t, "POST", reveal, e.admin, "", false)
	decodeInto(t, r, &c)
	if r.status != 200 || c["password"] != "silver halide 400" || c["user"] != "lib" || c["database"] != "lib" {
		t.Fatalf("reveal: %d %s", r.status, r.body)
	}
	found := false
	for _, en := range e.entries(t, "L1") {
		found = found || en.Action == "library.credential_reveal" && en.Outcome == audit.OK && en.Details["role"] == "lib" && en.Actor.Type == "token"
	}
	if !found {
		t.Fatalf("no reveal entry: %+v", e.entries(t, "L1"))
	}
	wantProblem(t, "revoked credential", e.call(t, "POST", reveal+"?credential=C2", e.admin, "", false), 422, "SQS-L031 the credential is revoked")
	wantProblem(t, "read token", e.call(t, "POST", reveal, e.read, "", false), 403, a030)
	wantProblem(t, "restore token", e.call(t, "POST", reveal, e.token(t, "restore"), "", false), 403, a030)
	wantProblem(t, "cookie without CSRF", e.call(t, "POST", reveal, "cookie", "", false), 401, "SQS-A031 missing X-CSRF-Token header")
	if r := e.call(t, "POST", reveal, "cookie", "", true); r.status != 200 {
		t.Fatalf("reveal with a session: %d %s", r.status, r.body)
	}
	e.auditDown(t)
	r = e.call(t, "POST", reveal, e.admin, "", false)
	wantProblemPrefix(t, "audit log down", r, 503, x040)
	if strings.Contains(r.body, "silver") {
		t.Fatalf("the password went out without its entry: %s", r.body)
	}
	if r := e.call(t, "GET", "/api/v1/libraries/L1/connection", e.read, "", false); r.status != 200 {
		t.Fatalf("connection data needs no entry: %d %s", r.status, r.body)
	}
}

// Sessions of a library without a live database are an empty list without
// asking PostgreSQL; a live library needs it (503 with SQS-P001 when down).
func TestAPISessions(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedLibrary(t, "L2", "old", "Old", core.StateTrashed)
	r := e.call(t, "GET", "/api/v1/libraries/L2/sessions", e.read, "", false)
	if r.status != 200 || strings.Join(strings.Fields(r.body), "") != `{"items":[]}` {
		t.Fatalf("sessions of a trashed library: %d %s", r.status, r.body)
	}
	wantProblem(t, "live library", e.call(t, "GET", "/api/v1/libraries/L1/sessions", e.read, "", false), 503, "SQS-P001 PostgreSQL is not available")
	wantProblem(t, "unknown library", e.call(t, "GET", "/api/v1/libraries/NOPE/sessions", e.read, "", false), 404, "SQS-L002 library not found")
}

// The backup policy: the installation's default until one is stored, every
// invalid field refused with its code without changing anything, activity
// mode stored with its check cadence as the schedule, and an empty mode
// stored as cron.
func TestAPIBackupPolicy(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	const path = "/api/v1/libraries/L1/backup-policy"
	get := func() core.Policy {
		t.Helper()
		r := e.call(t, "GET", path, e.read, "", false)
		var p core.Policy
		decodeInto(t, r, &p)
		if r.status != 200 {
			t.Fatalf("policy: %d %s", r.status, r.body)
		}
		return p
	}
	def := get()
	if def.Mode != core.ModeActivity || def.CheckMinutes != 15 || def.Cron != "*/15 * * * *" || def.KeepLast != 10 || def.MinVerified != 1 || !def.Enabled {
		t.Fatalf("default policy: %+v", def)
	}
	const valid = `"enabled":true,"mode":"cron","cron":"0 3 * * *","timezone":"UTC"`
	for _, c := range []struct {
		name, body string
		status     int
		detail     string
	}{
		{"unknown mode", `{"mode":"weekly","cron":"0 3 * * *","timezone":"UTC","keep_last":3,"min_verified":1}`, 422, "SQS-B091 unknown schedule mode"},
		{"activity cadence", `{"mode":"activity","check_minutes":7,"timezone":"UTC","keep_last":3,"min_verified":1}`, 422,
			"SQS-B090 activity-based mode checks every 5, 10, 15, 30 or 60 minutes"},
		{"bad expression", `{"mode":"cron","cron":"bogus","timezone":"UTC","keep_last":3,"min_verified":1}`, 422,
			`SQS-J020 invalid schedule expression: "bogus" (use 5 fields: minute hour day month day-of-week)`},
		{"unknown time zone", `{"mode":"cron","cron":"0 3 * * *","timezone":"Mars/Base","keep_last":3,"min_verified":1}`, 422,
			`SQS-J020 invalid schedule expression: time zone "Mars/Base"`},
		{"negative retention", `{` + valid + `,"keep_last":3,"keep_weekly":-1,"min_verified":1}`, 422, "SQS-B030 retention values must be between 0 and 1000"},
		{"retention over 1000", `{` + valid + `,"keep_daily":1001,"min_verified":1}`, 422, "SQS-B030 retention values must be between 0 and 1000"},
		{"keeps nothing", `{` + valid + `,"min_verified":1}`, 422, "SQS-B031 retention must keep at least one backup"},
		{"no verified backup kept", `{` + valid + `,"keep_last":3,"min_verified":0}`, 422, "SQS-B032 keep at least one verified backup (1 to 100)"},
		{"read-only field", `{` + valid + `,"keep_last":3,"min_verified":1,"keep_forever":true}`, 400, `SQS-C061 invalid JSON body: json: unknown field "keep_forever"`},
	} {
		wantProblem(t, c.name, e.call(t, "PUT", path, e.admin, c.body, false), c.status, c.detail)
	}
	body := `{` + valid + `,"keep_last":3,"min_verified":1}`
	wantProblem(t, "unknown library", e.call(t, "PUT", "/api/v1/libraries/NOPE/backup-policy", e.admin, body, false), 404, "SQS-L002 library not found")
	wantProblem(t, "read token", e.call(t, "PUT", path, e.read, body, false), 403, a030)
	if got := get(); got != def {
		t.Fatalf("a refused policy was stored: %+v", got)
	}

	r := e.call(t, "PUT", path, e.admin, `{"enabled":true,"mode":"activity","check_minutes":30,"cron":"0 3 * * *","timezone":"UTC","keep_last":5,"keep_daily":7,"min_verified":2}`, false)
	if r.status != 200 {
		t.Fatalf("activity policy: %d %s", r.status, r.body)
	}
	if p := get(); p.Mode != core.ModeActivity || p.CheckMinutes != 30 || p.Cron != "*/30 * * * *" || p.KeepLast != 5 || p.KeepDaily != 7 || p.KeepWeekly != 0 || p.MinVerified != 2 {
		t.Fatalf("stored activity policy: %+v", p)
	}
	if !e.hasEntry(t, "L1", "library.policy", audit.OK) {
		t.Errorf("no library.policy entry: %+v", e.entries(t, "L1"))
	}
	if r := e.call(t, "PUT", path, e.admin, `{"enabled":false,"cron":"30 2 * * 1","timezone":"UTC","keep_last":1,"min_verified":1}`, false); r.status != 200 {
		t.Fatalf("cron policy: %d %s", r.status, r.body)
	}
	if p := get(); p.Mode != core.ModeCron || p.Cron != "30 2 * * 1" || p.Enabled || p.KeepLast != 1 || p.KeepDaily != 0 {
		t.Fatalf("stored cron policy: %+v", p)
	}
}

// The backup policy of a library that does not exist is 404 SQS-L002, as
// every other library route answers, not the default policy.
func TestAPIBackupPolicyOfAnUnknownLibrary(t *testing.T) {
	e := apiServer(t)
	wantProblem(t, "unknown library", e.call(t, "GET", "/api/v1/libraries/NOPE/backup-policy", e.admin, "", false), 404, "SQS-L002 library not found")
}
