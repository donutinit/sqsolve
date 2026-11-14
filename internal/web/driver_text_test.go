// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// TestFailHidesTheDriverText: the notice of an action that failed on the
// pool (closing an export, for an operator) keeps the driver's text of a
// failed connection to PostgreSQL only for a reader with the diagnostics
// permission, and the whole text goes to the process log (docs 08 §5).
func TestFailHidesTheDriverText(t *testing.T) {
	cl := pg.New(t.TempDir(), 5432, secrets.New("admin password"), secrets.New("superuser password"))
	_, connectErr := cl.ConnectDB(context.Background(), "postgres")
	var buf bytes.Buffer
	cfg := &config.Config{Location: time.UTC, InsecureHTTP: true}
	s := &Server{A: &app.App{Cfg: cfg, Svc: &core.Service{Cfg: cfg, Log: slog.New(slog.NewJSONHandler(&buf, nil))}}}
	const hidden = "the connection to PostgreSQL failed (the cause is in the sqsolve log)"
	for _, c := range []struct {
		role auth.Role
		want string
	}{{auth.RoleViewer, hidden}, {auth.RoleOperator, hidden}, {auth.RoleAdmin, connectErr.Error()}} {
		r := withPrincipal(httptest.NewRequest("POST", "/backups/B1/export/close", nil), &auth.Principal{User: &auth.User{Role: c.role}})
		w := httptest.NewRecorder()
		s.fail(w, r, "/backups/B1/export", connectErr)
		var f flashMsg
		for _, ck := range w.Result().Cookies() {
			if ck.Name == flashCookie {
				raw, _ := base64.RawURLEncoding.DecodeString(ck.Value)
				json.Unmarshal(raw, &f)
			}
		}
		if f.Kind != "error" || f.Msg != c.want || w.Header().Get("Location") != "/backups/B1/export" {
			t.Errorf("%s: notice %+v, location %q, want %q", c.role, f, w.Header().Get("Location"), c.want)
		}
		if c.want == hidden && (strings.Contains(f.Msg, cl.SocketDir) || strings.Contains(f.Msg, "sqsolve_admin")) {
			t.Errorf("%s: the notice names the socket or the role: %s", c.role, f.Msg)
		}
	}
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ln map[string]any
		if err := json.Unmarshal([]byte(raw), &ln); err != nil {
			t.Fatalf("log line %q: %v", raw, err)
		}
		lines = append(lines, ln)
	}
	if len(lines) != 3 {
		t.Fatalf("want one log line per request: %+v", lines)
	}
	for _, ln := range lines {
		if ln["level"] != "WARN" || ln["msg"] != "request: the connection to PostgreSQL failed" || ln["path"] != "/backups/B1/export/close" ||
			ln["err"] != connectErr.Error() {
			t.Errorf("log line: %+v", ln)
		}
	}
}
