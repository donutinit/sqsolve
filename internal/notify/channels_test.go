// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
)

// logBuf is a log destination safe for the notifier's goroutines.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// withLog gives n a log the test can read; call it before Run.
func withLog(n *Notifier) *logBuf {
	l := &logBuf{}
	n.log = slog.New(slog.NewTextHandler(l, nil))
	return l
}

// Each invalid channel is refused with its own code and message.
func TestValidateErrorCodes(t *testing.T) {
	ok := func(kind string, cfg map[string]string) *Channel {
		return &Channel{Kind: kind, Name: "ops", MinSeverity: events.Error, Config: cfg}
	}
	smtp := func(port, security string) *Channel {
		return ok(SMTP, map[string]string{"host": "mail.example.com", "from": "sqsolve@example.com", "to": "ops@example.com", "port": port, "security": security})
	}
	long := ok(Webhook, map[string]string{"url": "https://hooks.example.com/x"})
	long.Name = strings.Repeat("n", 81)
	blank := ok(Webhook, map[string]string{"url": "https://hooks.example.com/x"})
	blank.Name = "   "
	badSev := ok(Webhook, map[string]string{"url": "https://hooks.example.com/x"})
	badSev.MinSeverity = "debug"
	for _, tc := range []struct {
		name string
		c    *Channel
		want string
	}{
		{"blank name", blank, "SQS-N021 the channel needs a name"},
		{"long name", long, "SQS-N021 the channel needs a name"},
		{"severity", badSev, "SQS-N022 invalid severity"},
		{"ftp webhook", ok(Webhook, map[string]string{"url": "ftp://hooks.example.com/x"}), "SQS-N023 invalid URL (use http:// or https://)"},
		{"ntfy without host", ok(Ntfy, map[string]string{"url": "https:///topic"}), "SQS-N023 invalid URL (use http:// or https://)"},
		{"unparsable ntfy", ok(Ntfy, map[string]string{"url": "https://ntfy.example.com/\x7f"}), "SQS-N023 invalid URL (use http:// or https://)"},
		{"smtp without sender", ok(SMTP, map[string]string{"host": "mail.example.com", "to": "ops@example.com"}), "SQS-N024 email needs a server, sender and recipients"},
		{"port zero", smtp("0", ""), "SQS-N025 invalid port"},
		{"port too high", smtp("65536", ""), "SQS-N025 invalid port"},
		{"port not a number", smtp("smtp", ""), "SQS-N025 invalid port"},
		{"security", smtp("587", "ssl"), "SQS-N026 invalid security setting"},
		{"kind", ok("pigeon", map[string]string{}), "SQS-N001 unknown channel kind"},
	} {
		if err := Validate(tc.c); err == nil || err.Error() != tc.want {
			t.Errorf("%s: got %v, want %s", tc.name, err, tc.want)
		}
	}
	for _, sec := range []string{"", "starttls", "tls", "none"} {
		if err := Validate(smtp("465", sec)); err != nil {
			t.Errorf("security %q: %v", sec, err)
		}
	}
	c := ok(Ntfy, map[string]string{"url": "https://ntfy.example.com/topic"})
	if err := Validate(c); err != nil || c.Pattern != "*" {
		t.Fatalf("an empty pattern means every event: %v %q", err, c.Pattern)
	}
}

// An invalid channel is not saved and leaves no audit entry.
func TestSaveRefusesAnInvalidChannel(t *testing.T) {
	ctx := context.Background()
	n, db := newNotifier(t)
	err := n.Save(ctx, audit.System, &Channel{Kind: Webhook, Name: "hook", MinSeverity: events.Error, Config: map[string]string{"url": "nope"}})
	if err == nil || err.Error() != "SQS-N023 invalid URL (use http:// or https://)" {
		t.Fatalf("%v", err)
	}
	if chs, _ := n.Channels(ctx); len(chs) != 0 {
		t.Fatalf("saved %+v", chs)
	}
	if es, _ := audit.New(db).List(ctx, 0, 10, ""); len(es) != 0 {
		t.Fatalf("audit %+v", es)
	}
}

// Saving creates the channel with a trimmed name and an audit entry, saving
// it again updates it in place, and Delete removes it with its own entry.
func TestSaveUpdateAndDeleteAreAudited(t *testing.T) {
	ctx := context.Background()
	n, db := newNotifier(t)
	ch := &Channel{Kind: Ntfy, Name: "  phone  ", Enabled: true, MinSeverity: events.Warning, Config: map[string]string{"url": "https://ntfy.example.com/topic", "token": "tk_123456"}}
	if err := n.Save(ctx, audit.System, ch); err != nil {
		t.Fatal(err)
	}
	ch.Name, ch.Enabled, ch.MinSeverity, ch.Pattern = "phone 2", false, events.Critical, "postgres.*"
	ch.Config = map[string]string{"url": "https://ntfy.example.com/other"}
	if err := n.Save(ctx, audit.System, ch); err != nil {
		t.Fatal(err)
	}
	all, _ := n.Channels(ctx)
	if len(all) != 1 {
		t.Fatalf("%d channels", len(all))
	}
	got := all[0]
	if got.ID != ch.ID || got.Name != "phone 2" || got.Enabled || got.MinSeverity != events.Critical || got.Pattern != "postgres.*" ||
		got.Config["url"] != "https://ntfy.example.com/other" || got.Config["token"] != "tk_123456" || got.CreatedAt.IsZero() {
		t.Fatalf("updated %+v", got)
	}
	if err := n.Delete(ctx, audit.System, ch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Channel(ctx, ch.ID); err == nil || err.Error() != "SQS-N020 channel not found" {
		t.Fatalf("deleted channel: %v", err)
	}
	es, _ := audit.New(db).List(ctx, 0, 10, ch.ID)
	var actions []string
	for _, e := range es {
		actions = append(actions, e.Action+":"+e.Outcome)
	}
	if strings.Join(actions, ",") != "notify.delete:ok,notify.save:ok,notify.save:ok" {
		t.Fatalf("audit %v", actions)
	}
	if es[1].Details["name"] != "phone 2" || es[1].Details["kind"] != Ntfy {
		t.Fatalf("details %+v", es[1].Details)
	}
	for _, e := range es {
		for _, v := range e.Details {
			if v == "tk_123456" {
				t.Fatal("the token is in the audit log")
			}
		}
	}
}

// A database that refuses the write fails Save and Delete with the cause,
// without an audit entry and without changing the channel.
func TestSaveAndDeleteReportDatabaseErrors(t *testing.T) {
	ctx := context.Background()
	n, db := newNotifier(t)
	ch := addChannel(t, n, "https://hooks.example.com/x", events.Error)
	for _, q := range []string{
		`CREATE TRIGGER no_insert BEFORE INSERT ON notification_channels BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`,
		`CREATE TRIGGER no_update BEFORE UPDATE ON notification_channels BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`,
		`CREATE TRIGGER no_delete BEFORE DELETE ON notification_channels BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := audit.New(db).List(ctx, 0, 100, "")
	if err := n.Save(ctx, audit.System, &Channel{Kind: Webhook, Name: "new", MinSeverity: events.Error, Config: map[string]string{"url": "https://hooks.example.com/y"}}); err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("insert: %v", err)
	}
	ch.Name = "renamed"
	if err := n.Save(ctx, audit.System, ch); err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("update: %v", err)
	}
	if err := n.Delete(ctx, audit.System, ch.ID); err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("delete: %v", err)
	}
	all, _ := n.Channels(ctx)
	if len(all) != 1 || all[0].Name != "hook" {
		t.Fatalf("channels %+v", all)
	}
	if after, _ := audit.New(db).List(ctx, 0, 100, ""); len(after) != len(before) {
		t.Fatalf("audit entries %d, want %d", len(after), len(before))
	}
}

// A channel whose configuration cannot be decrypted (another master key)
// is still listed, with an empty configuration; a row that cannot be read
// fails the list with the cause.
func TestChannelsWithBadRows(t *testing.T) {
	ctx := context.Background()
	n, db := newNotifier(t)
	ch := addChannel(t, n, "https://hooks.example.com/x", events.Error)
	if _, err := db.ExecContext(ctx, `UPDATE notification_channels SET config_enc = 'not sealed' WHERE id = ?`, ch.ID); err != nil {
		t.Fatal(err)
	}
	got, err := n.Channel(ctx, ch.ID)
	if err != nil || len(got.Config) != 0 || got.Name != "hook" {
		t.Fatalf("%+v %v", got, err)
	}
	db.ExecContext(ctx, `UPDATE notification_channels SET enabled = 'maybe' WHERE id = ?`, ch.ID)
	if _, err := n.Channels(ctx); err == nil || !strings.Contains(err.Error(), `name "enabled"`) {
		t.Fatalf("list: %v", err)
	}
	if _, err := n.Channel(ctx, ch.ID); err == nil || !strings.Contains(err.Error(), `name "enabled"`) {
		t.Fatalf("one: %v", err)
	}
	if err := n.Test(ctx, ch.ID); err == nil || !strings.Contains(err.Error(), `name "enabled"`) {
		t.Fatalf("test: %v", err)
	}
	db.Close()
	if _, err := n.Channels(ctx); err == nil || err.Error() != "sql: database is closed" {
		t.Fatalf("closed: %v", err)
	}
}

// Testing a channel that does not exist is SQS-N020.
func TestTestUnknownChannel(t *testing.T) {
	n, _ := newNotifier(t)
	if err := n.Test(context.Background(), "NOPE"); err == nil || err.Error() != "SQS-N020 channel not found" {
		t.Fatalf("%v", err)
	}
}

// A URL without a host gives no address warning: there is nothing to
// resolve.
func TestAddressWarningWithoutHost(t *testing.T) {
	n, _ := newNotifier(t)
	for _, u := range []string{"https:///topic", "://bad", ""} {
		if w := n.AddressWarning(context.Background(), &Channel{Kind: Webhook, Config: map[string]string{"url": u}}); w != "" {
			t.Errorf("%q: %q", u, w)
		}
	}
}
