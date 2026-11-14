// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// pgHidden is what a caller without the diagnostics permission gets of a
// failed connection to PostgreSQL (pg.Public, docs 08 §5).
const pgHidden = "the connection to PostgreSQL failed (the cause is in the sqsolve log)"

// driverText is the start of the driver's text of a failed connection as
// sqsolve_admin.
const driverText = "failed to connect to `user=sqsolve_admin database=postgres`: "

// callers are the four kinds of caller of these tests: two without the
// diagnostics permission and two with it.
func callers(t *testing.T, e *apiEnv) []struct {
	name  string
	e     *apiEnv
	token string
	full  bool
} {
	return []struct {
		name  string
		e     *apiEnv
		token string
		full  bool
	}{
		{"a read token", e, e.read, false},
		{"an operator's session", e.as(t, "op", auth.RoleOperator), "cookie", false},
		{"an admin token", e, e.admin, true},
		{"an administrator's session", e, "cookie", true},
	}
}

// noDriverText fails when a body names the socket or the control role.
func noDriverText(t *testing.T, what, body, socketDir string) {
	t.Helper()
	for _, internal := range []string{socketDir, ".s.PGSQL", "sqsolve_admin", "failed to connect"} {
		if strings.Contains(body, internal) {
			t.Errorf("%s names %q: %s", what, internal, body)
		}
	}
}

// TestAPIProblemHidesTheDriverText: every problem a route answers keeps the
// driver's text of a failed connection to PostgreSQL only for a caller with
// the diagnostics permission; the status comes from the handler.
func TestAPIProblemHidesTheDriverText(t *testing.T) {
	e := apiServer(t)
	cl := pg.New(t.TempDir(), 5432, secrets.New("admin password"), secrets.New("superuser password"))
	_, connectErr := cl.ConnectDB(context.Background(), "postgres")
	a := &API{A: e.a}
	srv := httptest.NewServer(a.guard(auth.ActView, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		problem(w, r, statusFor(connectErr), connectErr)
	}))
	defer srv.Close()
	for _, who := range callers(t, e) {
		c := *who.e
		c.srv = srv
		r := c.call(t, "GET", "/api/v1/libraries/L1/sessions", who.token, "", false)
		if who.full {
			wantProblemPrefix(t, who.name, r, 422, driverText)
			continue
		}
		wantProblem(t, who.name, r, 422, pgHidden)
		noDriverText(t, who.name, r.body, cl.SocketDir)
	}
}

// TestAPIHidesTheDriverTextWhenPostgreSQLStops: PostgreSQL stops after the
// pool opened. GET /system/status and a route that fails on the pool show
// the driver's text of the failed connection (the socket path, the
// sqsolve_admin role) only to a caller with the diagnostics permission
// (docs 08 §5); the status of the route does not change.
func TestAPIHidesTheDriverTextWhenPostgreSQLStops(t *testing.T) {
	e, c := pgServer(t)
	e.seedLibrary(t, "L1", "lib", "Lib", core.StateActive)
	who := callers(t, e)
	c.Stop()
	// Once the pool holds no connection from before the stop, every read
	// dials the socket and fails with the driver's connection error.
	for i := 0; ; i++ {
		_, err := e.a.PG.Server(context.Background())
		if errors.As(err, new(*pgconn.ConnectError)) {
			break
		}
		if i == 20 {
			t.Fatalf("the pool still answers with connections from before the stop: %v", err)
		}
	}
	for _, w := range who {
		var st struct {
			Postgres struct {
				Up    bool   `json:"up"`
				Error string `json:"error"`
			} `json:"postgres"`
		}
		r := w.e.call(t, "GET", "/api/v1/system/status", w.token, "", false)
		decodeInto(t, r, &st)
		sessions := w.e.call(t, "GET", "/api/v1/libraries/L1/sessions", w.token, "", false)
		if w.full {
			if r.status != 200 || st.Postgres.Up || !strings.HasPrefix(st.Postgres.Error, driverText) || !strings.Contains(st.Postgres.Error, c.SocketDir) {
				t.Errorf("%s: status %d %s", w.name, r.status, r.body)
			}
			wantProblemPrefix(t, w.name+": sessions", sessions, 422, driverText)
			continue
		}
		if r.status != 200 || st.Postgres.Up || st.Postgres.Error != pgHidden {
			t.Errorf("%s: status %d %s", w.name, r.status, r.body)
		}
		wantProblem(t, w.name+": sessions", sessions, 422, pgHidden)
		noDriverText(t, w.name+": status", r.body, c.SocketDir)
		noDriverText(t, w.name+": sessions", sessions.body, c.SocketDir)
	}
}
