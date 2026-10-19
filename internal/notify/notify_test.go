// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

func TestWebhookSignedAndDeduplicated(t *testing.T) {
	ctx := context.Background()
	db, _ := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"), "")
	box, _ := secrets.NewBox(secrets.RandomBytes(32))
	n := New(db, box, audit.New(db), slog.New(slog.NewTextHandler(io.Discard, nil)))
	var mu sync.Mutex
	var got []Payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts, _ := strconv.ParseInt(r.Header.Get("X-Sqsolve-Timestamp"), 10, 64)
		if r.Header.Get("X-Sqsolve-Signature") != Sign("s3cret", ts, body) {
			w.WriteHeader(401)
			return
		}
		var p Payload
		json.Unmarshal(body, &p)
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}))
	defer srv.Close()
	ch := &Channel{Kind: Webhook, Name: "test", Enabled: true, MinSeverity: events.Error, Pattern: "backup.*", Config: map[string]string{"url": srv.URL, "secret": "s3cret"}}
	if err := n.Save(ctx, audit.System, ch); err != nil {
		t.Fatal(err)
	}
	if err := n.Test(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	// Secrets are kept when the field comes back empty.
	ch.Config["secret"] = ""
	n.Save(ctx, audit.System, ch)
	saved, _ := n.Channel(ctx, ch.ID)
	if saved.Config["secret"] != "s3cret" {
		t.Fatal("secret lost on update")
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go n.Run(cctx)
	n.Handle(events.Event{ID: "1", Type: "backup.failed", Severity: events.Error, Message: "a", TS: time.Now()})
	n.Handle(events.Event{ID: "2", Type: "backup.failed", Severity: events.Error, Message: "b", TS: time.Now()})
	n.Handle(events.Event{ID: "3", Type: "backup.succeeded", Severity: events.Info, Message: "c", TS: time.Now()})
	n.Handle(events.Event{ID: "4", Type: "library.created", Severity: events.Error, Message: "d", TS: time.Now()})
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	// 1 test + 1 backup.failed (the second is held, the info and the other pattern filtered).
	if len(got) != 2 || got[1].ID != "1" {
		t.Fatalf("deliveries %+v", got)
	}
}

func TestValidate(t *testing.T) {
	bad := []*Channel{
		{Kind: Webhook, Name: "x", MinSeverity: events.Error, Config: map[string]string{"url": "ftp://x"}},
		{Kind: SMTP, Name: "x", MinSeverity: events.Error, Config: map[string]string{"host": "h"}},
		{Kind: "pigeon", Name: "x", MinSeverity: events.Error, Config: map[string]string{}},
		{Kind: Ntfy, Name: "", MinSeverity: events.Error, Config: map[string]string{"url": "https://ntfy.sh/x"}},
	}
	for _, c := range bad {
		if Validate(c) == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	if !matches("backup.*, storage.low", "storage.low") || matches("backup.*", "library.created") || !matches("*", "x.y") {
		t.Fatal("pattern matching")
	}
}
