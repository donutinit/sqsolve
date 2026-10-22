// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/api"
	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/notify"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/web"
)

const (
	jobsAdminPass = "tungsten light 3200K"
	jobsOpPass    = "daylight 5600K!"
)

// jobsPanel builds a real App and router with SQLite only (no PostgreSQL,
// no background workers: queued jobs stay queued), with an administrator
// "boss" and an operator "op".
func jobsPanel(t *testing.T) (*app.App, *httptest.Server, audit.Actor) {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	cfg := &config.Config{AppDir: filepath.Join(base, "app"), BackupDir: filepath.Join(base, "backups"), PGDir: filepath.Join(base, "pg"),
		PGDataDir: filepath.Join(base, "pg", "data"), PGSocket: filepath.Join(base, "sock"), PGPort: 1, PGListen: "127.0.0.1",
		PGMaxConnections: 50, PGMemoryMB: 256, UID: os.Getuid(), GID: os.Getgid(), PGUID: os.Getuid(), PGGID: os.Getgid(),
		LogLevel: "warn", LogFormat: "text", Workers: 1, HeavyJobs: 1, Location: time.UTC, InsecureHTTP: true, Listen: "127.0.0.1:0"}
	os.MkdirAll(filepath.Dir(cfg.SuperuserPasswordFile()), 0o700)
	if err := os.WriteFile(cfg.SuperuserPasswordFile(), []byte("x\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := app.Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	adm, err := a.Auth.CreateUser(ctx, audit.System, "boss", "", jobsAdminPass, auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Auth.CreateUser(ctx, audit.System, "op", "", jobsOpPass, auth.RoleOperator); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(web.SetSSEHeartbeat(50 * time.Millisecond))
	h, err := web.New(a, (&api.API{A: a}).Register)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return a, srv, audit.Actor{Type: "user", ID: adm.ID, Name: "boss"}
}

func jobsLogin(t *testing.T, srv *httptest.Server, user, pass string) (*browser, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	b := &browser{t: t, base: srv.URL, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	pre := csrf(t, b.page("/login"))
	if _, loc, _ := b.do("POST", "/login", url.Values{"csrf": {pre}, "username": {user}, "password": {pass}}); loc != "/" {
		t.Fatalf("login %s: %s", user, loc)
	}
	return b, csrf(t, b.page("/account"))
}

func setJobState(t *testing.T, a *app.App, id string, st jobs.State) {
	t.Helper()
	if _, err := a.DB.ExecContext(context.Background(), `UPDATE jobs SET state = ?, finished_at = ? WHERE id = ?`, string(st), store.NowT(), id); err != nil {
		t.Fatal(err)
	}
}

func auditEntries(t *testing.T, a *app.App, action string) []audit.Entry {
	t.Helper()
	all, err := a.Audit.List(context.Background(), 0, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.Entry
	for _, e := range all {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func jobCount(t *testing.T, a *app.App) int {
	t.Helper()
	list, err := a.Jobs.List(context.Background(), jobs.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

// Audit finding C2: an operator cancelled an administrator's queued restore
// and replayed it, skipping its confirmation.
func TestOperatorCannotCancelOrRetryAdminJobs(t *testing.T) {
	a, srv, admin := jobsPanel(t)
	ctx := context.Background()
	j, err := a.Jobs.Enqueue(ctx, core.JobRestoreReplace, "LIB1", core.RestoreParams{BackupID: "B1", TerminateSessions: true}, admin)
	if err != nil {
		t.Fatal(err)
	}
	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	st, _, body := op.do("POST", "/jobs/"+j.ID+"/cancel", url.Values{"csrf": {opTok}})
	if st != http.StatusForbidden || !strings.Contains(body, "SQS-A030") {
		t.Fatalf("operator cancel: %d", st)
	}
	if g, _ := a.Jobs.Get(ctx, j.ID); g.State != jobs.Queued {
		t.Fatalf("state after a refused cancel: %s", g.State)
	}
	if es := auditEntries(t, a, "job.cancel"); len(es) != 1 || es[0].Outcome != audit.Denied || es[0].Actor.Name != "op" || es[0].TargetID != j.ID {
		t.Fatalf("cancel audit %+v", es)
	}
	// The administrator may cancel it.
	boss, bossTok := jobsLogin(t, srv, "boss", jobsAdminPass)
	if _, loc, _ := boss.do("POST", "/jobs/"+j.ID+"/cancel", url.Values{"csrf": {bossTok}}); loc != "/jobs/"+j.ID {
		t.Fatalf("admin cancel: %s", loc)
	}
	if g, _ := a.Jobs.Get(ctx, j.ID); g.State != jobs.Cancelled {
		t.Fatalf("state after the admin cancel: %s", g.State)
	}
	before := jobCount(t, a)
	// The operator cannot replay it.
	st, _, body = op.do("POST", "/jobs/"+j.ID+"/retry", url.Values{"csrf": {opTok}})
	if st != http.StatusForbidden || !strings.Contains(body, "SQS-A030") || jobCount(t, a) != before {
		t.Fatalf("operator retry: %d", st)
	}
	if es := auditEntries(t, a, "job.retry"); len(es) != 1 || es[0].Outcome != audit.Denied || es[0].Actor.Name != "op" {
		t.Fatalf("retry audit %+v", es)
	}
	// Nobody replays a destructive job: it starts again from its page.
	_, loc, _ := boss.do("POST", "/jobs/"+j.ID+"/retry", url.Values{"csrf": {bossTok}})
	if loc != "/jobs/"+j.ID || jobCount(t, a) != before {
		t.Fatalf("admin retry of a destructive job: %s", loc)
	}
	if page := boss.page(loc); !strings.Contains(page, "SQS-J030") || !strings.Contains(page, "start the operation again") {
		t.Fatal("the refusal does not say why")
	}
	// The job page offers no retry, but the way back to the operation.
	for _, b := range []*browser{op, boss} {
		page := b.page("/jobs/" + j.ID)
		if strings.Contains(page, "/jobs/"+j.ID+"/retry") || !strings.Contains(page, `href="/backups/B1/restore"`) {
			t.Fatalf("job page actions:\n%s", page)
		}
	}
}

func TestRetryOfAnIdempotentJobIsAudited(t *testing.T) {
	a, srv, admin := jobsPanel(t)
	ctx := context.Background()
	j, err := a.Jobs.Enqueue(ctx, core.JobBackupCreate, "LIB1", core.BackupParams{Kind: core.KindManual}, admin)
	if err != nil {
		t.Fatal(err)
	}
	setJobState(t, a, j.ID, jobs.Failed)
	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	if page := op.page("/jobs/" + j.ID); !strings.Contains(page, `action="/jobs/`+j.ID+`/retry"`) {
		t.Fatal("no retry button for a failed backup")
	}
	_, loc, _ := op.do("POST", "/jobs/"+j.ID+"/retry", url.Values{"csrf": {opTok}})
	nj, err := a.Jobs.Get(ctx, strings.TrimPrefix(loc, "/jobs/"))
	if err != nil {
		t.Fatalf("retry refused: %s", loc)
	}
	if nj.ID == j.ID || nj.Type != core.JobBackupCreate || nj.Attempt != 2 || nj.CreatedBy.Name != "op" || string(nj.Params) != string(j.Params) {
		t.Fatalf("retried job %+v", nj)
	}
	es := auditEntries(t, a, "job.retry")
	// Written with the new job (jobs.Audited): its result follows when it ends.
	if len(es) != 1 || es[0].Outcome != audit.Accepted || es[0].TargetID != j.ID || es[0].Details["job_id"] != nj.ID || es[0].Details["type"] != j.Type || es[0].Actor.Name != "op" {
		t.Fatalf("retry audit %+v", es)
	}
	// A failed system job: the operator neither retries nor cancels it.
	sj, _ := a.Jobs.Enqueue(ctx, core.JobVacuum, "", nil, admin)
	setJobState(t, a, sj.ID, jobs.Failed)
	page := op.page("/jobs/" + sj.ID)
	if strings.Contains(page, "/jobs/"+sj.ID+"/retry") || !strings.Contains(page, `href="/settings?tab=maintenance"`) {
		t.Fatalf("system job page for an operator:\n%s", page)
	}
	if st, _, _ := op.do("POST", "/jobs/"+sj.ID+"/retry", url.Values{"csrf": {opTok}}); st != http.StatusForbidden {
		t.Fatalf("operator retried a system job: %d", st)
	}
}

func TestAPIJobCancelChecksTheType(t *testing.T) {
	a, srv, admin := jobsPanel(t)
	ctx := context.Background()
	j, _ := a.Jobs.Enqueue(ctx, core.JobLibraryDelete, "LIB1", nil, admin)
	cancel := func(b *browser, tok string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/jobs/"+j.ID+"/cancel", nil)
		req.Header.Set("X-CSRF-Token", tok)
		resp, err := b.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	if st, out := cancel(op, opTok); st != http.StatusForbidden || out["code"] != "SQS-A030" {
		t.Fatalf("operator: %d %v", st, out)
	}
	if g, _ := a.Jobs.Get(ctx, j.ID); g.State != jobs.Queued {
		t.Fatalf("state %s", g.State)
	}
	boss, bossTok := jobsLogin(t, srv, "boss", jobsAdminPass)
	if st, out := cancel(boss, bossTok); st != http.StatusAccepted {
		t.Fatalf("admin: %d %v", st, out)
	}
	es := auditEntries(t, a, "job.cancel")
	if len(es) != 2 || es[0].Outcome != audit.OK || es[1].Outcome != audit.Denied {
		t.Fatalf("cancel audit %+v", es)
	}
}

// Audit finding: the job stream could miss the final update (published
// before it subscribed, or dropped by the bus) and wait forever.
func TestJobStreamEndsWhenTheDatabaseSaysDone(t *testing.T) {
	a, srv, admin := jobsPanel(t)
	ctx := context.Background()
	j, _ := a.Jobs.Enqueue(ctx, core.JobBackupCreate, "LIB1", nil, admin)
	b, _ := jobsLogin(t, srv, "op", jobsOpPass)
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(rctx, "GET", srv.URL+"/jobs/"+j.ID+"/events", nil)
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	var events []string
	updated := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "event: ") {
			continue
		}
		events = append(events, strings.TrimPrefix(line, "event: "))
		if !updated {
			// Finished behind the bus's back.
			setJobState(t, a, j.ID, jobs.Succeeded)
			updated = true
		}
		if strings.HasSuffix(line, "done") {
			break
		}
	}
	if len(events) < 2 || events[len(events)-1] != "done" {
		t.Fatalf("events %v (%v)", events, rctx.Err())
	}
}

// Delivery errors are only shown to whoever manages the channels.
func TestNotificationErrorsOnlyForManagers(t *testing.T) {
	a, srv, admin := jobsPanel(t)
	ctx := context.Background()
	ch := &notify.Channel{Kind: notify.Webhook, Name: "hook", Enabled: true, MinSeverity: events.Error, Pattern: "*", Config: map[string]string{"url": "https://hooks.example/x"}}
	if err := a.Notify.Save(ctx, admin, ch); err != nil {
		t.Fatal(err)
	}
	a.DB.ExecContext(ctx, `UPDATE notification_channels SET last_error = ? WHERE id = ?`, "SQS-N040 could not reach the destination: refused-detail", ch.ID)
	op, _ := jobsLogin(t, srv, "op", jobsOpPass)
	if page := op.page("/settings?tab=notifications"); !strings.Contains(page, "hook") || strings.Contains(page, "refused-detail") {
		t.Fatal("an operator sees the delivery error")
	}
	boss, _ := jobsLogin(t, srv, "boss", jobsAdminPass)
	if page := boss.page("/settings?tab=notifications"); !strings.Contains(page, "refused-detail") {
		t.Fatal("the administrator does not see the delivery error")
	}
}

// docs 08 T-16: saving a channel that points inside the network warns.
func TestChannelSaveWarnsAboutPrivateAddresses(t *testing.T) {
	_, srv, _ := jobsPanel(t)
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	form := url.Values{"csrf": {tok}, "kind": {"webhook"}, "name": {"internal"}, "enabled": {"on"}, "severity": {"error"}, "pattern": {"*"}, "url": {"http://127.0.0.1:9/hook"}}
	_, loc, _ := boss.do("POST", "/settings/notifications", form)
	page := boss.page(loc)
	if !strings.Contains(page, `class="flash warn"`) || !strings.Contains(page, "Channel saved.") || !strings.Contains(page, "SQS-N042") {
		t.Fatal("no warning after saving a private address")
	}
	form.Set("name", "public")
	form.Set("url", "https://203.0.113.9/hook")
	_, loc, _ = boss.do("POST", "/settings/notifications", form)
	if page := boss.page(loc); strings.Contains(page, "SQS-N042") || !strings.Contains(page, "Channel saved.") {
		t.Fatal("a public address got the warning")
	}
}
