// SPDX-License-Identifier: AGPL-3.0-only

package health

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// What a reader without the diagnostics permission gets of a failed
// connection to PostgreSQL (pg.Public, docs 08 §5).
const hiddenText = "the connection to PostgreSQL failed (the cause is in the sqsolve log)"

// jsonLines decodes the JSON log lines in buf.
func jsonLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var ln map[string]any
		if err := json.Unmarshal([]byte(raw), &ln); err != nil {
			t.Fatalf("log line %q: %v", raw, err)
		}
		out = append(out, ln)
	}
	return out
}

// noInternals fails when text names the socket or the control role.
func noInternals(t *testing.T, what, text, socketDir string) {
	t.Helper()
	for _, internal := range []string{socketDir, ".s.PGSQL", "sqsolve_admin", "failed to connect"} {
		if strings.Contains(text, internal) {
			t.Errorf("%s names %q: %s", what, internal, text)
		}
	}
}

// TestPostgresDownHidesTheDriverText: postgres.down is stored for every
// viewer and sent to the notification channels, so its message ends where
// the driver's text of a failed connection begins; the whole error goes to
// the process log, one line per alert (docs 08 §5).
func TestPostgresDownHidesTheDriverText(t *testing.T) {
	ctx := context.Background()
	rec := recorder(newDB(t))
	cl := pg.New(t.TempDir(), 5432, secrets.New("admin password"), secrets.New("superuser password"))
	_, connectErr := cl.ConnectDB(ctx, "postgres")
	down := fmt.Errorf("%w: %w", pg.ErrUnavailable, connectErr)
	var buf bytes.Buffer
	c := &clock{t: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	var pingErr error
	m := &Monitor{Svc: &core.Service{Log: slog.New(slog.NewJSONHandler(&buf, nil))}, Events: rec, Now: c.now,
		StartupGrace: 5 * time.Minute, Ping: func(context.Context) error { return pingErr }}
	const public = "SQS-P001 PostgreSQL is not available: " + hiddenText

	// It never answered since the start.
	pingErr = down
	m.CheckPG(ctx)
	c.add(5 * time.Minute)
	m.CheckPG(ctx)
	evs := eventsOf(t, rec, "postgres.")
	if want := "PostgreSQL has not answered in the 5 minutes since sqsolve started: " + public; len(evs) != 1 || evs[0].Message != want {
		t.Fatalf("postgres.down at startup: %+v, want %q", evs, want)
	}

	// It answered, then stopped.
	pingErr = nil
	m.CheckPG(ctx)
	pingErr = down
	m.CheckPG(ctx)
	evs = eventsOf(t, rec, "postgres.")
	if want := "PostgreSQL is not responding: " + public; len(evs) != 3 || evs[0].Type != events.PostgresDown || evs[0].Message != want {
		t.Fatalf("postgres.down after it answered: %+v, want %q", evs, want)
	}
	for _, e := range evs {
		noInternals(t, "the message of "+e.Type, e.Message, cl.SocketDir)
	}
	lines := jsonLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("want one log line per alert: %+v", lines)
	}
	for _, ln := range lines {
		if ln["level"] != "WARN" || ln["msg"] != "postgres.down: the connection to PostgreSQL failed" || ln["err"] != down.Error() {
			t.Errorf("log line: %+v", ln)
		}
	}
}

// TestForReaderHidesTheDriverText: the error of PostgreSQL on the Health
// page and in GET /api/v1/system/status keeps the driver's text only for a
// reader with the diagnostics permission, and the whole text goes to the
// process log for every reader (docs 08 §5).
func TestForReaderHidesTheDriverText(t *testing.T) {
	ctx := context.Background()
	cl := pg.New(t.TempDir(), 5432, secrets.New("admin password"), secrets.New("superuser password"))
	_, connectErr := cl.ConnectDB(ctx, "postgres")
	var buf bytes.Buffer
	m := &Monitor{Svc: &core.Service{Log: slog.New(slog.NewJSONHandler(&buf, nil))}}
	st := Status{PostgresErr: hiddenText, pgErr: connectErr}

	got := m.ForReader(st, "/health", false).PostgresErr
	if got != hiddenText {
		t.Errorf("a reader without the permission: %q", got)
	}
	noInternals(t, "the public text", got, cl.SocketDir)
	if got := m.ForReader(st, "/api/v1/system/status", true).PostgresErr; got != connectErr.Error() {
		t.Errorf("a reader with the permission: %q", got)
	}
	lines := jsonLines(t, &buf)
	if len(lines) != 2 || lines[0]["msg"] != "health: the connection to PostgreSQL failed" || lines[0]["path"] != "/health" ||
		lines[0]["err"] != connectErr.Error() || lines[1]["path"] != "/api/v1/system/status" || lines[1]["err"] != connectErr.Error() {
		t.Fatalf("log: %+v", lines)
	}

	// Without the pool there is no driver's text: nothing changes.
	plain := Status{PostgresErr: pg.ErrUnavailable.Error(), pgErr: pg.ErrUnavailable}
	if got := m.ForReader(plain, "/health", false).PostgresErr; got != "SQS-P001 PostgreSQL is not available" {
		t.Errorf("a server never reached: %q", got)
	}
	if n := len(jsonLines(t, &buf)); n != 2 {
		t.Errorf("an error without the driver's text was logged: %d lines", n)
	}
}
