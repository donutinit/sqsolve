// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
)

// request is what a test destination received.
type request struct {
	header http.Header
	path   string
	body   string
}

// recorder is a destination that keeps every request and answers with the
// next status of its script (200 once the script ends).
type recorder struct {
	mu     sync.Mutex
	got    []request
	script []int
	srv    *httptest.Server
}

func newRecorder(t *testing.T, script ...int) *recorder {
	rec := &recorder{script: script}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.got = append(rec.got, request{r.Header.Clone(), r.URL.Path, string(b)})
		status := http.StatusOK
		if len(rec.script) > 0 {
			status, rec.script = rec.script[0], rec.script[1:]
		}
		rec.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (rec *recorder) requests() []request {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]request(nil), rec.got...)
}

func (rec *recorder) wait(t *testing.T, n int) []request {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := rec.requests(); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("want %d requests, have %d", n, len(rec.requests()))
	return nil
}

// waitChannel returns the channel once check accepts it, or fails.
func waitChannel(t *testing.T, n *Notifier, id string, check func(*Channel) bool) *Channel {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := n.Channel(context.Background(), id); err == nil && check(c) {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, _ := n.Channel(context.Background(), id)
	t.Fatalf("channel never reached the state: %+v", c)
	return nil
}

// A webhook carries the event as JSON with the instance name and links to
// the job and the library in the panel, and the delivery headers; without a
// secret there is no signature.
func TestWebhookPayloadAndHeaders(t *testing.T) {
	n, _ := newNotifier(t)
	n.Instance = func() string { return "nas-01" }
	n.BaseURL = func() string { return "https://sqsolve.example" }
	rec := newRecorder(t)
	addChannel(t, n, rec.srv.URL+"/hook", events.Info)
	run(t, n)
	at := time.Date(2026, 10, 25, 8, 30, 0, 0, time.FixedZone("CST", -6*3600))
	n.Handle(events.Event{ID: "EV1", TS: at, Type: events.BackupFailed, Severity: events.Error, LibraryID: "LIB1", JobID: "JOB1", Message: "Backup failed", Data: map[string]any{"code": "SQS-B001"}})
	r := rec.wait(t, 1)[0]
	var p Payload
	if err := json.Unmarshal([]byte(r.body), &p); err != nil {
		t.Fatal(err)
	}
	if p.ID != "EV1" || p.Type != events.BackupFailed || p.Severity != "error" || !p.OccurredAt.Equal(at) || p.OccurredAt.Location() != time.UTC ||
		p.Instance != "nas-01" || p.LibraryID != "LIB1" || p.JobID != "JOB1" || p.Message != "Backup failed" || p.Data["code"] != "SQS-B001" || p.Suppressed != 0 {
		t.Fatalf("payload %+v", p)
	}
	if p.Links["job"] != "https://sqsolve.example/jobs/JOB1" || p.Links["library"] != "https://sqsolve.example/libraries/LIB1" {
		t.Fatalf("links %v", p.Links)
	}
	h := r.header
	ts, err := strconv.ParseInt(h.Get("X-Sqsolve-Timestamp"), 10, 64)
	if r.path != "/hook" || h.Get("Content-Type") != "application/json" || h.Get("User-Agent") != "sqsolve" || h.Get("X-Sqsolve-Event") != events.BackupFailed ||
		len(h.Get("X-Sqsolve-Delivery")) != 26 || err != nil || time.Since(time.Unix(ts, 0)) > time.Minute || h.Get("X-Sqsolve-Signature") != "" {
		t.Fatalf("headers %v", h)
	}
}

// Without a base URL there are no links; a system event has no job or
// library links either.
func TestWebhookLinksOnlyWhatExists(t *testing.T) {
	rec := newRecorder(t)
	noBase, _ := newNotifier(t)
	addChannel(t, noBase, rec.srv.URL, events.Info)
	run(t, noBase)
	noBase.Handle(events.Event{ID: "1", Type: events.BackupFailed, Severity: events.Error, LibraryID: "LIB1", JobID: "JOB1", Message: "m", TS: time.Now()})
	rec.wait(t, 1)
	withBase, _ := newNotifier(t)
	withBase.BaseURL = func() string { return "https://sqsolve.example" }
	addChannel(t, withBase, rec.srv.URL, events.Info)
	run(t, withBase)
	withBase.Handle(ev("2", events.PostgresDown, events.Critical, "", nil))
	got := rec.wait(t, 2)
	for i, r := range got {
		var p Payload
		json.Unmarshal([]byte(r.body), &p)
		if len(p.Links) != 0 || r.body == "" {
			t.Errorf("delivery %d: links %v", i, p.Links)
		}
	}
}

// An ntfy notification is the message as the body, with a title that names
// the instance and the severity, the event type as a tag, a priority that
// grows with the severity, the token as a bearer and a link to the job.
func TestNtfyHeaders(t *testing.T) {
	ctx := context.Background()
	n, _ := newNotifier(t)
	n.Instance = func() string { return "nas-01" }
	n.BaseURL = func() string { return "https://sqsolve.example" }
	rec := newRecorder(t)
	withToken := &Channel{Kind: Ntfy, Name: "phone", Enabled: true, MinSeverity: events.Info, Pattern: "backup.*", Config: map[string]string{"url": rec.srv.URL + "/sqsolve", "token": "tk_secret"}}
	if err := n.Save(ctx, audit.System, withToken); err != nil {
		t.Fatal(err)
	}
	plain := &Channel{Kind: Ntfy, Name: "plain", Enabled: true, MinSeverity: events.Info, Pattern: "postgres.*", Config: map[string]string{"url": rec.srv.URL + "/public"}}
	if err := n.Save(ctx, audit.System, plain); err != nil {
		t.Fatal(err)
	}
	run(t, n)
	n.Handle(events.Event{ID: "1", Type: events.BackupStale, Severity: events.Warning, LibraryID: "LIB1", JobID: "JOB1", Message: "No backup for 3 days", TS: time.Now()})
	r := rec.wait(t, 1)[0]
	h := r.header
	if r.path != "/sqsolve" || r.body != "No backup for 3 days" || h.Get("Title") != "[nas-01] Warning" || h.Get("Tags") != "backup_stale" ||
		h.Get("Priority") != "4" || h.Get("Authorization") != "Bearer tk_secret" || h.Get("Click") != "https://sqsolve.example/jobs/JOB1" {
		t.Fatalf("request %+v", r)
	}
	for i, tc := range []struct {
		sev  events.Severity
		prio string
	}{{events.Info, "3"}, {events.Error, "5"}, {events.Critical, "5"}} {
		n.Handle(events.Event{ID: strconv.Itoa(i + 2), Type: events.PostgresDown, Severity: tc.sev, Message: "PostgreSQL", TS: time.Now()})
		r := rec.wait(t, i+2)[i+1]
		if r.path != "/public" || r.header.Get("Priority") != tc.prio || r.header.Get("Authorization") != "" || r.header.Get("Click") != "" {
			t.Fatalf("%s: %+v", tc.sev, r)
		}
	}
}

// A redirect is not followed: it counts as a failed delivery with its
// status, and the second address never sees the event.
func TestRedirectIsNotFollowed(t *testing.T) {
	ctx := context.Background()
	n, _ := newNotifier(t)
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	ch := addChannel(t, n, redirect.URL, events.Error)
	if err := n.Test(ctx, ch.ID); err == nil || err.Error() != "SQS-N002 the destination responded 302 Found" {
		t.Fatalf("%v", err)
	}
	if followed.Load() != 0 {
		t.Fatal("the redirect was followed")
	}
}

// A failed delivery is retried after each backoff; once it goes through
// the channel records the success and clears the last error.
func TestDeliveryRetriesUntilItSucceeds(t *testing.T) {
	ctx := context.Background()
	n, db := newNotifier(t)
	n.retries = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}
	rec := newRecorder(t, http.StatusServiceUnavailable, http.StatusBadGateway)
	ch := addChannel(t, n, rec.srv.URL, events.Error)
	db.ExecContext(ctx, `UPDATE notification_channels SET last_error = 'old failure' WHERE id = ?`, ch.ID)
	run(t, n)
	n.Handle(ev("1", events.BackupFailed, events.Error, "LIB1", nil))
	got := waitChannel(t, n, ch.ID, func(c *Channel) bool { return c.LastOK != nil })
	if got.LastError != "" || len(rec.requests()) != 3 {
		t.Fatalf("channel %+v after %d attempts", got, len(rec.requests()))
	}
}

// A delivery that keeps failing gives up after the last retry and stores
// the last error with its time.
func TestDeliveryGivesUpAfterTheRetries(t *testing.T) {
	n, _ := newNotifier(t)
	n.retries = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}
	rec := newRecorder(t, 500, 500, 503, 200)
	ch := addChannel(t, n, rec.srv.URL, events.Error)
	run(t, n)
	n.Handle(ev("1", events.BackupFailed, events.Error, "LIB1", nil))
	got := waitChannel(t, n, ch.ID, func(c *Channel) bool { return c.LastError != "" })
	if got.LastOK != nil || !hasSuffix(got.LastError, " SQS-N002 the destination responded 503 Service Unavailable") {
		t.Fatalf("channel %+v", got)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(rec.requests()); n != 3 {
		t.Fatalf("%d attempts, want 3", n)
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix
}
