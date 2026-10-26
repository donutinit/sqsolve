// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/events"
)

// Flush called while Run still runs (its context was not cancelled) gives
// up at its own deadline and stops the deliveries: a later event fails at
// once instead of going out during the stop.
func TestFlushGivesUpOnARunThatDoesNotStop(t *testing.T) {
	n, _ := newNotifier(t)
	s := newSink(t)
	ch := addChannel(t, n, s.srv.URL, events.Error)
	run(t, n)
	n.Handle(ev("1", events.BackupFailed, events.Error, "LIB1", nil))
	s.wait(t, 1) // Run is running
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	n.Flush(ctx)
	if took := time.Since(start); took < 100*time.Millisecond || took > 3*time.Second {
		t.Fatalf("Flush took %s, want its deadline", took)
	}
	n.Handle(ev("2", events.RestoreFailed, events.Error, "LIB1", nil))
	got := waitChannel(t, n, ch.ID, func(c *Channel) bool { return c.LastError != "" })
	if !strings.Contains(got.LastError, "SQS-N040 could not reach the destination: context canceled") || len(s.payloads()) != 1 {
		t.Fatalf("after the stop: %q, %d deliveries", got.LastError, len(s.payloads()))
	}
}

// A delivery that hangs does not hold the stop: Flush waits until its
// deadline, logs that notifications were left, and cancels the request.
func TestFlushStopsAHangingDelivery(t *testing.T) {
	n, _ := newNotifier(t)
	logs := withLog(n)
	var arrived, cancelled atomic.Int32
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // so the server notices the client going away
		arrived.Add(1)
		select {
		case <-r.Context().Done():
			cancelled.Add(1)
		case <-time.After(10 * time.Second):
		}
	}))
	defer hang.Close()
	ch := addChannel(t, n, hang.URL, events.Error)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx); close(done) }()
	n.Handle(ev("1", events.BackupFailed, events.Error, "LIB1", nil))
	deadline := time.Now().Add(5 * time.Second)
	for arrived.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the delivery never arrived")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	fctx, fcancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer fcancel()
	start := time.Now()
	n.Flush(fctx)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("Flush took %s", took)
	}
	if !strings.Contains(logs.String(), `msg="stopping before every notification was delivered"`) {
		t.Fatalf("log %s", logs.String())
	}
	deadline = time.Now().Add(5 * time.Second)
	for cancelled.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the hanging request was not cancelled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := waitChannel(t, n, ch.ID, func(c *Channel) bool { return c.LastError != "" })
	if !strings.Contains(got.LastError, "SQS-N040 could not reach the destination: context canceled") {
		t.Fatalf("stored error %q", got.LastError)
	}
}

// When the channels cannot be read, the event and the summaries of the
// open windows are dropped with a log line, not a crash.
func TestChannelReadErrorsAreLogged(t *testing.T) {
	n, db := newNotifier(t)
	logs := withLog(n)
	s := newSink(t)
	addChannel(t, n, s.srv.URL, events.Error)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx); close(done) }()
	n.Handle(ev("1", events.BackupFailed, events.Error, "LIB1", nil))
	s.wait(t, 1)
	n.Handle(ev("2", events.BackupFailed, events.Error, "LIB1", nil)) // held in the window
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	if _, err := db.Exec(`ALTER TABLE notification_channels RENAME TO notification_channels_gone`); err != nil {
		t.Fatal(err)
	}
	n.Handle(ev("3", events.RestoreFailed, events.Error, "LIB1", nil))
	fctx, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer fcancel()
	n.Flush(fctx)
	want := `msg="could not read the notification channels" err="SQL logic error: no such table: notification_channels (1)"`
	if c := strings.Count(logs.String(), want); c != 2 {
		t.Fatalf("%d lines %q, want one for the event and one for the summary:\n%s", c, want, logs.String())
	}
	if got := s.payloads(); len(got) != 1 {
		t.Fatalf("deliveries %+v", got)
	}
}
