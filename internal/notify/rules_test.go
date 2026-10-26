// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
)

// Each channel gets the events of its types at or above its severity: a
// disabled channel gets nothing, and blank items of a type list are
// ignored.
func TestRulesByTypeAndSeverity(t *testing.T) {
	ctx := context.Background()
	n, _ := newNotifier(t)
	restores := newSink(t)
	critical := newSink(t)
	disabled := newSink(t)
	for _, c := range []*Channel{
		{Kind: Webhook, Name: "restores", Enabled: true, MinSeverity: events.Warning, Pattern: "restore.*, ,backup.failed,", Config: map[string]string{"url": restores.srv.URL}},
		{Kind: Webhook, Name: "critical", Enabled: true, MinSeverity: events.Critical, Pattern: "*", Config: map[string]string{"url": critical.srv.URL}},
		{Kind: Webhook, Name: "disabled", Enabled: false, MinSeverity: events.Info, Pattern: "*", Config: map[string]string{"url": disabled.srv.URL}},
	} {
		if err := n.Save(ctx, audit.System, c); err != nil {
			t.Fatal(err)
		}
	}
	run(t, n)
	n.Handle(ev("1", events.RestoreFailed, events.Error, "LIB1", nil))
	n.Handle(ev("2", events.RestoreSucceeded, events.Info, "LIB1", nil)) // below warning
	n.Handle(ev("3", events.BackupFailed, events.Error, "LIB2", nil))
	n.Handle(ev("4", events.BackupFailed+"x", events.Error, "LIB2", nil)) // not in the list
	n.Handle(ev("5", events.PostgresDown, events.Critical, "", nil))
	n.Handle(ev("6", events.StorageLow, events.Error, "", nil)) // below critical
	restores.wait(t, 2)
	critical.wait(t, 1)
	time.Sleep(200 * time.Millisecond)
	// Deliveries of different events run side by side: compare as sets.
	ids := func(ps []Payload) map[string]bool {
		out := map[string]bool{}
		for _, p := range ps {
			out[p.ID] = true
		}
		return out
	}
	if ps := restores.payloads(); len(ps) != 2 || !ids(ps)["1"] || !ids(ps)["3"] {
		t.Errorf("restores channel got %+v", ps)
	}
	if ps := critical.payloads(); len(ps) != 1 || !ids(ps)["5"] {
		t.Errorf("critical channel got %+v", ps)
	}
	if got := disabled.payloads(); len(got) != 0 {
		t.Errorf("disabled channel got %+v", got)
	}
}

// A window that expired before the sweep saw it is closed by the next
// event of its key: the summary of what it held goes out first, then the
// new event opens a new window.
func TestExpiredWindowIsClosedByTheNextEvent(t *testing.T) {
	n, _ := newNotifier(t)
	n.window = 300 * time.Millisecond
	n.sweepEvery = time.Hour
	s := newSink(t)
	addChannel(t, n, s.srv.URL, events.Error)
	run(t, n)
	n.Handle(ev("1", events.BackupFailed, events.Error, "LIB1", nil))
	n.Handle(ev("2", events.BackupFailed, events.Error, "LIB1", nil)) // held
	time.Sleep(500 * time.Millisecond)
	n.Handle(ev("3", events.BackupFailed, events.Error, "LIB1", nil))
	got := s.wait(t, 3)
	if got[0].ID != "1" || got[1].ID != "2" || got[1].Suppressed != 1 || got[2].ID != "3" || got[2].Suppressed != 0 {
		t.Fatalf("deliveries %+v", got)
	}
	// The window ended without held events: the next one opens a new window
	// and goes out alone.
	time.Sleep(500 * time.Millisecond)
	n.Handle(ev("4", events.BackupFailed, events.Error, "LIB1", nil))
	if got := s.wait(t, 4); len(got) != 4 || got[3].ID != "4" || got[3].Suppressed != 0 {
		t.Fatalf("deliveries %+v", got)
	}
}

// Handle never blocks: with the queue full the extra events are dropped
// with a warning, and Flush later delivers what was queued (grouped, the
// first event and a summary of the others).
func TestFullQueueDropsWithAWarning(t *testing.T) {
	n, _ := newNotifier(t)
	logs := withLog(n)
	s := newSink(t)
	addChannel(t, n, s.srv.URL, events.Error)
	for i := 1; i <= 260; i++ {
		n.Handle(ev(strconv.Itoa(i), events.BackupFailed, events.Error, "LIB1", nil))
	}
	if c := strings.Count(logs.String(), `msg="notification queue full; dropping an event" type=backup.failed`); c != 4 {
		t.Fatalf("%d drops logged, want 4:\n%s", c, logs.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n.Flush(ctx)
	got := s.payloads()
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "256" || got[1].Suppressed != 255 {
		t.Fatalf("deliveries %+v", got)
	}
}
