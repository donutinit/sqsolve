// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

type sink struct {
	mu  sync.Mutex
	got []Payload
	srv *httptest.Server
}

func newSink(t *testing.T) *sink {
	s := &sink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Payload
		json.NewDecoder(r.Body).Decode(&p)
		s.mu.Lock()
		s.got = append(s.got, p)
		s.mu.Unlock()
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sink) payloads() []Payload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Payload(nil), s.got...)
}

// wait returns the payloads once there are n of them, or fails.
func (s *sink) wait(t *testing.T, n int) []Payload {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.payloads(); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("want %d deliveries, have %+v", n, s.payloads())
	return nil
}

func newNotifier(t *testing.T) (*Notifier, *store.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secrets.NewBox(secrets.RandomBytes(32))
	n := New(db, box, audit.New(db), slog.New(slog.NewTextHandler(io.Discard, nil)))
	n.retries = nil
	return n, db
}

func addChannel(t *testing.T, n *Notifier, url string, min events.Severity) *Channel {
	t.Helper()
	ch := &Channel{Kind: Webhook, Name: "hook", Enabled: true, MinSeverity: min, Pattern: "*", Config: map[string]string{"url": url}}
	if err := n.Save(context.Background(), audit.System, ch); err != nil {
		t.Fatal(err)
	}
	return ch
}

func run(t *testing.T, n *Notifier) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func ev(id, typ string, sev events.Severity, lib string, data map[string]any) events.Event {
	return events.Event{ID: id, Type: typ, Severity: sev, LibraryID: lib, Message: "msg " + id, TS: time.Now(), Data: data}
}

func TestEscalationIsAlwaysDelivered(t *testing.T) {
	n, _ := newNotifier(t)
	s := newSink(t)
	addChannel(t, n, s.srv.URL, events.Warning)
	run(t, n)
	n.Handle(ev("1", events.StorageLow, events.Warning, "", nil))
	n.Handle(ev("2", events.StorageLow, events.Warning, "", nil)) // held
	n.Handle(ev("3", events.StorageLow, events.Error, "", nil))   // escalation: sent
	n.Handle(ev("4", events.StorageLow, events.Error, "", nil))   // held
	n.Handle(ev("5", events.StorageLow, events.Warning, "", nil)) // held
	n.Handle(ev("6", events.PostgresDown, events.Critical, "", nil))
	got := s.wait(t, 3)
	time.Sleep(200 * time.Millisecond)
	ids := map[string]bool{}
	for _, p := range s.payloads() {
		ids[p.ID] = true
	}
	if len(s.payloads()) != 3 || !ids["1"] || !ids["3"] || !ids["6"] {
		t.Fatalf("deliveries %+v", got)
	}
}

func TestSystemEventsGroupByJobType(t *testing.T) {
	n, _ := newNotifier(t)
	s := newSink(t)
	addChannel(t, n, s.srv.URL, events.Error)
	run(t, n)
	n.Handle(ev("1", events.JobFailed, events.Error, "", map[string]any{"job_type": "system.vacuum"}))
	n.Handle(ev("2", events.JobFailed, events.Error, "", map[string]any{"job_type": "system.retention"}))
	n.Handle(ev("3", events.JobFailed, events.Error, "", map[string]any{"job_type": "system.vacuum"})) // held
	n.Handle(ev("4", events.JobFailed, events.Error, "LIB1", map[string]any{"job_type": "backup.create"}))
	s.wait(t, 3)
	time.Sleep(200 * time.Millisecond)
	if got := s.payloads(); len(got) != 3 {
		t.Fatalf("deliveries %+v", got)
	}
}

func TestWindowEndSendsASummary(t *testing.T) {
	n, _ := newNotifier(t)
	n.window = 300 * time.Millisecond
	n.sweepEvery = 20 * time.Millisecond
	s := newSink(t)
	addChannel(t, n, s.srv.URL, events.Error)
	run(t, n)
	n.Handle(ev("1", events.BackupFailed, events.Error, "LIB1", nil))
	n.Handle(ev("2", events.BackupFailed, events.Error, "LIB1", nil))
	n.Handle(ev("3", events.BackupFailed, events.Error, "LIB1", nil))
	got := s.wait(t, 2)
	if got[0].ID != "1" || got[0].Suppressed != 0 {
		t.Fatalf("first %+v", got[0])
	}
	sum := got[1]
	if sum.ID != "3" || sum.Suppressed != 2 || sum.LibraryID != "LIB1" || sum.Type != events.BackupFailed || !strings.HasPrefix(sum.Message, "2 more like this") || !strings.Contains(sum.Message, "msg 3") {
		t.Fatalf("summary %+v", sum)
	}
	// After the window a new event goes out at once.
	n.Handle(ev("4", events.BackupFailed, events.Error, "LIB1", nil))
	if got := s.wait(t, 3); got[2].ID != "4" {
		t.Fatalf("after the window %+v", got[2])
	}
}

// The summary carries the highest severity held, not the latest one's.
func TestSummaryHasTheHighestHeldSeverity(t *testing.T) {
	n, _ := newNotifier(t)
	n.window = 300 * time.Millisecond
	n.sweepEvery = 20 * time.Millisecond
	s := newSink(t)
	addChannel(t, n, s.srv.URL, events.Warning)
	run(t, n)
	n.Handle(ev("1", events.StorageLow, events.Critical, "", nil)) // delivered, opens the window
	n.Handle(ev("2", events.StorageLow, events.Error, "", nil))    // held
	n.Handle(ev("3", events.StorageLow, events.Warning, "", nil))  // held, latest
	got := s.wait(t, 2)
	sum := got[1]
	if sum.ID != "3" || sum.Suppressed != 2 || sum.Severity != string(events.Error) {
		t.Fatalf("summary %+v", sum)
	}
}

// A channel in its retry backoff does not hold a graceful stop: Flush makes
// one immediate last attempt and gives up.
func TestFlushDoesNotWaitForRetryBackoff(t *testing.T) {
	n, _ := newNotifier(t)
	n.retries = []time.Duration{time.Hour}
	var calls atomic.Int32
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	ch := addChannel(t, n, dead.URL, events.Error)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx); close(done) }()
	n.Handle(ev("1", events.BackupFailed, events.Error, "LIB1", nil))
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the first attempt never arrived")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // now waiting for the 1 h retry
	cancel()
	<-done
	fctx, fcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer fcancel()
	start := time.Now()
	n.Flush(fctx)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("Flush waited %s for a channel in backoff", took)
	}
	if calls.Load() != 2 {
		t.Fatalf("attempts: %d, want the first one and one last immediate one", calls.Load())
	}
	c, _ := n.Channel(context.Background(), ch.ID)
	if !strings.Contains(c.LastError, "SQS-N002 the destination responded 500 Internal Server Error") {
		t.Fatalf("stored error %q", c.LastError)
	}
}

// A URL that does not even build a request: the error has its code and no
// trace of the URL.
func TestInvalidDestinationURL(t *testing.T) {
	n, _ := newNotifier(t)
	bad := "http://hooks.example/T000/\x7fXSECRETX"
	for _, kind := range []string{Webhook, Ntfy} {
		err := n.send(context.Background(), &Channel{Kind: kind, Config: map[string]string{"url": bad}}, ev("1", events.BackupFailed, events.Error, "", nil), 0)
		if !errors.Is(err, errBadURL) || err.Error() != "SQS-N041 the destination URL is not valid" {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}

func TestFlushDeliversWhatShutdownLeft(t *testing.T) {
	n, _ := newNotifier(t)
	s := newSink(t)
	addChannel(t, n, s.srv.URL, events.Warning)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx); close(done) }()
	n.Handle(ev("1", events.BackupFailed, events.Error, "LIB1", nil))
	s.wait(t, 1)
	n.Handle(ev("2", events.BackupFailed, events.Error, "LIB1", nil)) // held in the window
	time.Sleep(100 * time.Millisecond)
	// The process is stopping: the loop ends first, jobs still emit events.
	cancel()
	<-done
	n.Handle(ev("3", events.JobInterrupted, events.Warning, "LIB1", map[string]any{"job_type": "backup.create"}))
	fctx, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer fcancel()
	n.Flush(fctx)
	got := s.payloads()
	ids := map[string]Payload{}
	for _, p := range got {
		ids[p.ID] = p
	}
	if len(got) != 3 || ids["3"].Type != events.JobInterrupted || ids["2"].Suppressed != 1 {
		t.Fatalf("after flush %+v", got)
	}
}

func TestDeliveryErrorsHideTheURL(t *testing.T) {
	n, _ := newNotifier(t)
	ctx := context.Background()
	// Nothing listens on port 1: the connection is refused.
	secretURL := "http://127.0.0.1:1/services/T000/B000/XSECRETX"
	ch := addChannel(t, n, secretURL, events.Error)
	err := n.Test(ctx, ch.ID)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-N040 ") || strings.Contains(err.Error(), "XSECRETX") {
		t.Fatalf("test error %v", err)
	}
	got, _ := n.Channel(ctx, ch.ID)
	if !strings.Contains(got.LastError, "SQS-N040 could not reach the destination") || strings.Contains(got.LastError, "XSECRETX") || strings.Contains(got.LastError, "/services/") {
		t.Fatalf("stored error %q", got.LastError)
	}
	// The same through the delivery loop, with a status from the server.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer bad.Close()
	ch2 := addChannel(t, n, bad.URL+"/hooks/XSECRET2X", events.Error)
	run(t, n)
	n.Handle(ev("1", events.BackupFailed, events.Error, "LIB1", nil))
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, _ := n.Channel(ctx, ch2.ID)
		if c.LastError != "" {
			if !strings.Contains(c.LastError, "SQS-N002 the destination responded 403 Forbidden") || strings.Contains(c.LastError, "XSECRET2X") {
				t.Fatalf("stored error %q", c.LastError)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no error recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The short error keeps the cause and drops the URL.
	if msg := shortError(&Channel{Config: map[string]string{"url": "https://x.example/t/SECRET"}}, errors.New("Post \"https://x.example/t/SECRET\": EOF")); strings.Contains(msg, "SECRET") || !strings.Contains(msg, "EOF") {
		t.Fatalf("short error %q", msg)
	}
}

func TestAddressWarning(t *testing.T) {
	n, _ := newNotifier(t)
	ctx := context.Background()
	n.lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "nas.lan":
			return []net.IPAddr{{IP: net.ParseIP("192.168.1.20")}}, nil
		case "ntfy.example.com":
			return []net.IPAddr{{IP: net.ParseIP("203.0.113.7")}}, nil
		}
		return nil, errors.New("no such host")
	}
	for _, u := range []string{"http://127.0.0.1:8080/x", "http://10.0.0.5/hook", "https://[fe80::1]/x", "http://localhost/x", "http://169.254.169.254/latest", "https://nas.lan/topic", "http://[::1]:9/x", "http://0.0.0.0/x"} {
		w := n.AddressWarning(ctx, &Channel{Kind: Webhook, Config: map[string]string{"url": u}})
		if !strings.HasPrefix(w, "SQS-N042 ") || !strings.Contains(w, "private") {
			t.Errorf("%s: %q", u, w)
		}
	}
	for _, u := range []string{"https://ntfy.example.com/topic", "https://unknown.example/x", "https://8.8.8.8/x"} {
		if w := n.AddressWarning(ctx, &Channel{Kind: Ntfy, Config: map[string]string{"url": u}}); w != "" {
			t.Errorf("%s: %q", u, w)
		}
	}
	if w := n.AddressWarning(ctx, &Channel{Kind: SMTP, Config: map[string]string{"host": "127.0.0.1"}}); w != "" {
		t.Errorf("smtp: %q", w)
	}
}

func TestNotifierSurvivesAPanic(t *testing.T) {
	n, _ := newNotifier(t)
	s := newSink(t)
	addChannel(t, n, s.srv.URL, events.Error)
	var calls atomic.Int32
	n.Instance = func() string {
		if calls.Add(1) == 1 {
			panic("broken settings")
		}
		return "sqsolve"
	}
	run(t, n)
	n.Handle(ev("1", events.BackupFailed, events.Error, "LIB1", nil))
	time.Sleep(100 * time.Millisecond)
	n.Handle(ev("2", events.RestoreFailed, events.Error, "LIB1", nil))
	if got := s.wait(t, 1); got[0].ID != "2" {
		t.Fatalf("deliveries %+v", got)
	}
}

// Final review: the lock of one account, a burst from one address and the
// expiry of one certificate are not repeats of the lock of another account,
// a burst from another address or the expiry of the other certificate, so
// they are not held in the same window.
func TestAuthAndCertEventsGroupByWhatTheyAreAbout(t *testing.T) {
	n, _ := newNotifier(t)
	s := newSink(t)
	addChannel(t, n, s.srv.URL, events.Warning)
	run(t, n)
	n.Handle(ev("1", events.AuthLocked, events.Critical, "", map[string]any{"username": "ana", "ip": "192.0.2.1"}))
	n.Handle(ev("2", events.AuthLocked, events.Critical, "", map[string]any{"username": "bea", "ip": "192.0.2.1"}))
	n.Handle(ev("3", events.AuthBruteforce, events.Warning, "", map[string]any{"username": "ana", "ip": "192.0.2.1"}))
	n.Handle(ev("4", events.AuthBruteforce, events.Warning, "", map[string]any{"username": "ana", "ip": "198.51.100.7"}))
	n.Handle(ev("5", events.TLSExpiring, events.Error, "", map[string]any{"cert": "Panel web"}))
	n.Handle(ev("6", events.TLSExpiring, events.Error, "", map[string]any{"cert": "PostgreSQL"}))
	n.Handle(ev("7", events.AuthBruteforce, events.Warning, "", map[string]any{"username": "ana", "ip": "192.0.2.1"})) // a repeat: held
	s.wait(t, 6)
	time.Sleep(200 * time.Millisecond)
	ids := map[string]bool{}
	for _, p := range s.payloads() {
		ids[p.ID] = true
	}
	if len(s.payloads()) != 6 || ids["7"] {
		t.Fatalf("deliveries %v", ids)
	}
}

// postgres.up closes the window of postgres.down, even on a channel that
// does not send info events: a second outage after the operator was told
// "responding again" is delivered, not held until the window ends.
func TestPostgresUpClosesTheDownWindow(t *testing.T) {
	for _, min := range []events.Severity{events.Info, events.Error} {
		n, _ := newNotifier(t)
		s := newSink(t)
		addChannel(t, n, s.srv.URL, min)
		run(t, n)
		n.Handle(ev("1", events.PostgresDown, events.Critical, "", nil))
		n.Handle(ev("2", events.PostgresUp, events.Info, "", nil))
		n.Handle(ev("3", events.PostgresDown, events.Critical, "", nil))
		want := 2
		if min == events.Info {
			want = 3
		}
		s.wait(t, want)
		time.Sleep(200 * time.Millisecond)
		got := s.payloads()
		ids := map[string]bool{}
		for _, p := range got {
			ids[p.ID] = true
		}
		if len(got) != want || !ids["3"] {
			t.Fatalf("channel from %s: deliveries %+v", min, got)
		}
	}
}

// The summary of the postgres.down events held in the window goes out before
// postgres.up, so "not responding" never arrives after "responding again".
func TestPostgresUpComesAfterTheDownSummary(t *testing.T) {
	n, _ := newNotifier(t)
	s := newSink(t)
	addChannel(t, n, s.srv.URL, events.Info)
	run(t, n)
	n.Handle(ev("1", events.PostgresDown, events.Critical, "", nil))
	s.wait(t, 1)
	n.Handle(ev("2", events.PostgresDown, events.Critical, "", nil)) // held
	n.Handle(ev("3", events.PostgresUp, events.Info, "", nil))
	s.wait(t, 3)
	time.Sleep(200 * time.Millisecond)
	got := s.payloads()
	if len(got) != 3 || got[1].ID != "2" || got[1].Suppressed != 1 || got[2].ID != "3" {
		t.Fatalf("want the summary of the held down before the up: %+v", got)
	}
}
