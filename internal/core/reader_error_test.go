// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// TestReaderErrorHidesTheConnection: the rule of docs 08 §5 wherever an
// error reaches a reader. A failed connection to PostgreSQL is logged whole,
// in a WARN line named by the caller and with its attributes, and only a
// reader with the diagnostics permission gets the driver's text; any other
// error comes back as it is and is not logged.
func TestReaderErrorHidesTheConnection(t *testing.T) {
	ctx := context.Background()
	var buf bytes.Buffer
	cl := pg.New(t.TempDir(), 5432, secrets.New("admin password"), secrets.New("superuser password"))
	s := &Service{PG: cl, Log: slog.New(slog.NewJSONHandler(&buf, nil))}
	logged := func() []map[string]any {
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
	_, connectErr := cl.ConnectDB(ctx, "postgres")
	err := fmt.Errorf("%w: %w", pg.ErrUnavailable, connectErr)
	if !strings.Contains(err.Error(), cl.SocketDir) || !strings.Contains(err.Error(), "user=sqsolve_admin") {
		t.Fatalf("the driver's text changed: %v", err)
	}

	// A reader without the permission.
	got := s.ReaderError(err, "doctor", false, "path", "/health", "check", "PostgreSQL")
	if got == nil {
		t.Fatal("a hidden error became nil")
	}
	refused(t, "a reader without the permission", got, "SQS-P001 PostgreSQL is not available: "+connectionHidden)
	for _, internal := range []string{cl.SocketDir, ".s.PGSQL", "sqsolve_admin"} {
		if strings.Contains(got.Error(), internal) {
			t.Errorf("the public text names %q: %v", internal, got)
		}
	}
	lines := logged()
	if len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["msg"] != "doctor: the connection to PostgreSQL failed" ||
		lines[0]["path"] != "/health" || lines[0]["check"] != "PostgreSQL" || lines[0]["err"] != err.Error() {
		t.Fatalf("log after a hidden error: %+v", lines)
	}

	// A reader with the permission gets the error itself; the log has it too.
	if got := s.ReaderError(err, "postgres.down", true); got != err {
		t.Errorf("a reader with the permission: %v", got)
	}
	lines = logged()
	if len(lines) != 2 || lines[1]["msg"] != "postgres.down: the connection to PostgreSQL failed" || lines[1]["err"] != err.Error() {
		t.Fatalf("log after a whole error: %+v", lines)
	}
	if _, ok := lines[1]["path"]; ok {
		t.Errorf("a line without attributes has a path: %+v", lines[1])
	}

	// Not a connection error, and no error.
	if got := s.ReaderError(pg.ErrUnavailable, "doctor", false); got != pg.ErrUnavailable {
		t.Errorf("another error was changed: %v", got)
	}
	if got := s.ReaderError(nil, "doctor", false); got != nil {
		t.Errorf("no error became %v", got)
	}
	if n := len(logged()); n != 2 {
		t.Errorf("errors that are not a failed connection were logged: %d lines", n)
	}
}
