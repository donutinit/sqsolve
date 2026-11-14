// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestHealthPageHidesTheDriverText: PostgreSQL stops after the pool opened.
// The Health page shows the driver's text of the failed connection (the
// socket path, the sqsolve_admin role), in the PostgreSQL card and in the
// doctor checks, only to a reader with the diagnostics permission (docs 08
// §5).
func TestHealthPageHidesTheDriverText(t *testing.T) {
	a, srv, c := fxPGPanel(t)
	eve, _ := jobsLogin(t, srv, "eve", fxViewerPass)
	op, _ := jobsLogin(t, srv, "op", jobsOpPass)
	boss, _ := jobsLogin(t, srv, "boss", jobsAdminPass)
	c.Stop()
	// Once the pool holds no connection from before the stop, every read
	// dials the socket and fails with the driver's connection error.
	for i := 0; ; i++ {
		_, err := a.PG.Server(context.Background())
		if errors.As(err, new(*pgconn.ConnectError)) {
			break
		}
		if i == 20 {
			t.Fatalf("the pool still answers with connections from before the stop: %v", err)
		}
	}
	const hidden = "the connection to PostgreSQL failed (the cause is in the sqsolve log)"
	for name, b := range map[string]*browser{"a viewer": eve, "an operator": op} {
		page := b.page("/health")
		if !strings.Contains(page, `<p class="muted small">`+hidden+`</p>`) || !strings.Contains(page, "SQS-P001 PostgreSQL is not available: "+hidden) {
			t.Errorf("%s: the public text is missing from the Health page", name)
		}
		for _, internal := range []string{c.SocketDir, ".s.PGSQL", "sqsolve_admin", "failed to connect"} {
			if strings.Contains(page, internal) {
				t.Errorf("%s: the Health page names %q", name, internal)
			}
		}
	}
	page := boss.page("/health")
	if strings.Contains(page, hidden) || strings.Count(page, c.SocketDir) < 2 || !strings.Contains(page, "SQS-P001 PostgreSQL is not available: failed to connect to") {
		t.Errorf("an administrator does not get the driver's text on the Health page")
	}
}
