// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// sseEvent is one event of a job stream.
type sseEvent struct{ name, data string }

// readSSE reads events and comment lines from a job stream until it ends,
// handing each event to onEvent.
func readSSE(t *testing.T, body io.Reader, onEvent func(sseEvent)) (evs []sseEvent, pings int) {
	t.Helper()
	sc := bufio.NewScanner(body)
	var cur sseEvent
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == ": ping":
			pings++
		case strings.HasPrefix(line, "event: "):
			cur.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		case line == "" && cur.name != "":
			evs = append(evs, cur)
			if onEvent != nil {
				onEvent(cur)
			}
			cur = sseEvent{}
		}
	}
	return evs, pings
}

// The job stream relays the updates the job manager publishes on the bus,
// pings while nothing happens, and ends with "done" when an update says
// the job finished.
func TestJobStreamRelaysTheBus(t *testing.T) {
	a, srv, admin := jobsPanel(t)
	ctx := context.Background()
	j, _ := a.Jobs.Enqueue(ctx, core.JobBackupCreate, "LIB1", nil, admin)
	b, _ := jobsLogin(t, srv, "op", jobsOpPass)
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(rctx, "GET", srv.URL+"/jobs/"+j.ID+"/events", nil)
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream headers %v", resp.Header)
	}
	n := 0
	evs, pings := readSSE(t, resp.Body, func(sseEvent) {
		n++
		switch n {
		case 1:
			// After the first state, wait for a heartbeat before updating.
			time.Sleep(120 * time.Millisecond)
			a.Bus.Publish("job:"+j.ID, jobs.Update{JobID: j.ID, State: jobs.Running, Progress: 40, Step: "dump"})
		case 2:
			a.Bus.Publish("job:"+j.ID, jobs.Update{JobID: j.ID, State: jobs.Succeeded, Progress: 100})
		}
	})
	if len(evs) != 4 || evs[0].name != "state" || evs[1].name != "state" || evs[2].name != "state" || evs[3].name != "done" || evs[3].data != `"succeeded"` {
		t.Fatalf("events %+v (%v)", evs, rctx.Err())
	}
	var first, running jobs.Update
	json.Unmarshal([]byte(evs[0].data), &first)
	json.Unmarshal([]byte(evs[1].data), &running)
	if first.JobID != j.ID || first.State != jobs.Queued || running.State != jobs.Running || running.Progress != 40 || running.Step != "dump" {
		t.Fatalf("updates %+v %+v", first, running)
	}
	if pings == 0 {
		t.Error("no heartbeat while the job was quiet")
	}
}

// A finished job's stream says so at once and ends; an unknown job is a 404.
func TestJobStreamOfAFinishedOrUnknownJob(t *testing.T) {
	a, srv, admin := jobsPanel(t)
	ctx := context.Background()
	j, _ := a.Jobs.Enqueue(ctx, core.JobBackupCreate, "LIB1", nil, admin)
	setJobState(t, a, j.ID, jobs.Failed)
	b, _ := jobsLogin(t, srv, "op", jobsOpPass)
	resp, err := b.c.Get(srv.URL + "/jobs/" + j.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := readSSE(t, resp.Body, nil)
	resp.Body.Close()
	if len(evs) != 2 || evs[0].name != "state" || !strings.Contains(evs[0].data, `"state":"failed"`) || evs[1].name != "done" || evs[1].data != `"failed"` {
		t.Fatalf("events of a finished job %+v", evs)
	}
	if st, _, _ := b.do("GET", "/jobs/01NOSUCHJOB000000000000000/events", nil); st != http.StatusNotFound {
		t.Fatalf("stream of an unknown job: %d", st)
	}
	if st, _, body := b.do("GET", "/jobs/01NOSUCHJOB000000000000000", nil); st != http.StatusNotFound || !strings.Contains(body, "SQS-J002 Job not found.") {
		t.Fatalf("page of an unknown job: %d", st)
	}
}

// A failed job that is not retried from its page (destructive types, and
// any type for whoever may not retry it) links to the page where its
// operation starts again (docs 09 §3.5).
func TestFailedJobsLinkToWhereTheyStart(t *testing.T) {
	a, srv, admin := jobsPanel(t)
	ctx := context.Background()
	boss, _ := jobsLogin(t, srv, "boss", jobsAdminPass)
	op, _ := jobsLogin(t, srv, "op", jobsOpPass)
	for _, c := range []struct {
		typ, lib string
		params   any
		state    jobs.State
		viewer   *browser
		want     string
	}{
		{core.JobLibraryCreate, "LIB1", nil, jobs.Failed, boss, "/libraries/new"},
		{core.JobRestoreNew, "LIB2", core.RestoreParams{BackupID: "B7"}, jobs.Interrupted, boss, "/backups/B7/restore"},
		{core.JobRestoreNew, "LIB2", nil, jobs.Failed, boss, "/libraries/LIB2"},
		{core.JobRestoreRevert, "LIB3", nil, jobs.Cancelled, boss, "/libraries/LIB3/revert"},
		{core.JobLibraryDelete, "LIB5", nil, jobs.Failed, boss, "/libraries/LIB5"},
		// Retried from its page by an administrator, started again by
		// whoever may not import.
		{core.JobBackupImport, "LIB4", nil, jobs.Failed, op, "/backups/import"},
		{core.JobUpgradeWindow, "", nil, jobs.Failed, op, "/settings?tab=maintenance"},
	} {
		j, err := a.Jobs.Enqueue(ctx, c.typ, c.lib, c.params, admin)
		if err != nil {
			t.Fatal(err)
		}
		setJobState(t, a, j.ID, c.state)
		page := c.viewer.page("/jobs/" + j.ID)
		if !strings.Contains(page, `href="`+c.want+`">Start again</a>`) || strings.Contains(page, "/jobs/"+j.ID+"/retry") {
			t.Errorf("%s %s: no link to %s", c.typ, c.state, c.want)
		}
		if c.typ == core.JobBackupImport {
			if page := boss.page("/jobs/" + j.ID); !strings.Contains(page, `action="/jobs/`+j.ID+`/retry"`) || strings.Contains(page, "Start again") {
				t.Error("the administrator is not offered Retry for a failed import")
			}
		}
	}
	// A queued job offers no way back yet.
	j, _ := a.Jobs.Enqueue(ctx, core.JobLibraryCreate, "LIB6", nil, admin)
	if page := boss.page("/jobs/" + j.ID); strings.Contains(page, "Start again") {
		t.Error("a queued job offers to start again")
	}
}
