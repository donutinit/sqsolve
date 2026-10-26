// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
)

func enqueue(t *testing.T, e *apiEnv, typ, lib string, params any) *jobs.Job {
	t.Helper()
	j, err := e.a.Jobs.Enqueue(context.Background(), typ, lib, params, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// The job list filters by state, type and library and pages by 50 with
// next_cursor; a job comes with its steps in order, and its log is read
// after a sequence number. An unknown job is 404 with SQS-J002.
func TestAPIJobsListAndDetail(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	e.seedLibrary(t, "L2", "other", "Other", core.StateActive)
	var first *jobs.Job
	for i := 0; i < 51; i++ {
		j := enqueue(t, e, core.JobBackupVerify, "L1", core.VerifyParams{BackupID: "B1"})
		if first == nil {
			first = j
		}
	}
	vacuum := enqueue(t, e, core.JobVacuum, "", nil)
	failed := enqueue(t, e, core.JobBackupCreate, "L2", core.BackupParams{Kind: core.KindManual})
	e.exec(t, `UPDATE jobs SET state = 'failed', error_code = 'SQS-B012' WHERE id = ?`, failed.ID)
	page := func(query string) ([]string, string) {
		t.Helper()
		r := e.call(t, "GET", "/api/v1/jobs"+query, e.read, "", false)
		var out struct {
			Items []jobs.Job `json:"items"`
			Next  string     `json:"next_cursor"`
		}
		decodeInto(t, r, &out)
		if r.status != 200 {
			t.Fatalf("jobs%s: %d %s", query, r.status, r.body)
		}
		var ids []string
		for _, j := range out.Items {
			ids = append(ids, j.ID)
		}
		return ids, out.Next
	}
	ids, next := page("")
	if len(ids) != 50 || next != ids[49] {
		t.Fatalf("first page: %d jobs, next %q", len(ids), next)
	}
	rest, end := page("?cursor=" + next)
	seen := map[string]bool{}
	for _, id := range append(ids, rest...) {
		seen[id] = true
	}
	if len(rest) != 3 || end != "" || len(seen) != 53 {
		t.Fatalf("second page: %v next %q, %d distinct jobs", rest, end, len(seen))
	}
	for query, want := range map[string]string{"?type=system.vacuum": vacuum.ID, "?library_id=L2": failed.ID, "?state=failed": failed.ID,
		"?state=failed&library_id=L1": "", "?type=backup.create&state=queued": ""} {
		got, next := page(query)
		if (want == "" && len(got) != 0) || (want != "" && (len(got) != 1 || got[0] != want)) || next != "" {
			t.Errorf("jobs%s: %v next %q, want %q", query, got, next, want)
		}
	}

	now := store.NowT()
	e.exec(t, `INSERT INTO job_steps(job_id, seq, name, state, started_at, finished_at, data) VALUES (?, 1, 'restore', 'succeeded', ?, ?, '{"rows":3}'), (?, 2, 'compare', 'running', ?, NULL, '{}')`,
		first.ID, now, now, first.ID, now)
	e.exec(t, `INSERT INTO job_logs(job_id, seq, ts, level, message) VALUES (?, 1, ?, 'info', 'one'), (?, 2, ?, 'warn', 'two'), (?, 3, ?, 'info', 'three')`,
		first.ID, now, first.ID, now, first.ID, now)
	r := e.call(t, "GET", "/api/v1/jobs/"+first.ID, e.read, "", false)
	var detail struct {
		Job   jobs.Job    `json:"job"`
		Steps []jobs.Step `json:"steps"`
	}
	decodeInto(t, r, &detail)
	var data map[string]int
	if len(detail.Steps) > 0 {
		json.Unmarshal(detail.Steps[0].Data, &data)
	}
	if r.status != 200 || detail.Job.ID != first.ID || detail.Job.Type != core.JobBackupVerify || detail.Job.Title != "Verify backup" ||
		len(detail.Steps) != 2 || detail.Steps[0].Name != "restore" || detail.Steps[0].FinishedAt == nil || data["rows"] != 3 ||
		detail.Steps[1].Name != "compare" || detail.Steps[1].State != "running" {
		t.Fatalf("job detail: %d %s", r.status, r.body)
	}
	logs := func(query string) []jobs.LogLine {
		t.Helper()
		r := e.call(t, "GET", "/api/v1/jobs/"+first.ID+"/logs"+query, e.read, "", false)
		var out struct {
			Items []jobs.LogLine `json:"items"`
		}
		decodeInto(t, r, &out)
		if r.status != 200 {
			t.Fatalf("logs%s: %d %s", query, r.status, r.body)
		}
		return out.Items
	}
	if l := logs(""); len(l) != 3 || l[0].Message != "one" || l[1].Level != "warn" {
		t.Errorf("logs: %+v", l)
	}
	if l := logs("?after=2"); len(l) != 1 || l[0].Seq != 3 || l[0].Message != "three" {
		t.Errorf("logs after 2: %+v", l)
	}
	wantProblem(t, "unknown job", e.call(t, "GET", "/api/v1/jobs/NOPE", e.read, "", false), 404, "SQS-J002 job not found")
}

// Cancelling needs job.cancel and also the permission that starts the job's
// type: an operator may not cancel a restore by replacing nor a system job,
// and each refusal leaves a denied entry naming the type (docs 10 §2). A
// token without job.cancel is refused before the job is read.
func TestAPIJobCancelPermissions(t *testing.T) {
	e := apiServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	replace := enqueue(t, e, core.JobRestoreReplace, "L1", core.RestoreParams{BackupID: "B1"})
	vacuum := enqueue(t, e, core.JobVacuum, "", nil)
	operator := e.as(t, "olga", auth.RoleOperator)
	backup := e.token(t, "backup")
	for _, j := range []*jobs.Job{replace, vacuum} {
		wantProblem(t, "operator cancels "+j.Type, operator.call(t, "POST", "/api/v1/jobs/"+j.ID+"/cancel", "cookie", "", true), 403, a030)
		denied := 0
		for _, en := range e.entries(t, j.ID) {
			if en.Action == "job.cancel" && en.Outcome == audit.Denied && en.Actor.Name == "olga" && en.Details["type"] == j.Type {
				denied++
			}
		}
		wantProblem(t, "backup token cancels "+j.Type, e.call(t, "POST", "/api/v1/jobs/"+j.ID+"/cancel", backup, "", false), 403, a030)
		if got, _ := e.a.Jobs.Get(context.Background(), j.ID); denied != 1 || len(e.entries(t, j.ID)) != 1 || got.State != jobs.Queued {
			t.Errorf("%s after refused cancels: entries %+v, state %s", j.Type, e.entries(t, j.ID), got.State)
		}
	}
	wantProblem(t, "operator cancels an unknown job", operator.call(t, "POST", "/api/v1/jobs/NOPE/cancel", "cookie", "", true), 404, "SQS-J002 job not found")
}
