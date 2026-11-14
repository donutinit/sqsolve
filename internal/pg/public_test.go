// SPDX-License-Identifier: AGPL-3.0-only

package pg_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/donutinit/sqsolve/internal/pg"
)

// What pg.Public leaves of a connection error: hiddenText alone, or after
// the message of the experimental export (publicText).
const (
	hiddenText = "the connection to PostgreSQL failed (the cause is in the sqsolve log)"
	publicText = "SQS-E005 the library is not available for export: " + hiddenText
)

// wrapped is an error whose text says nothing of the driver's error it wraps.
type wrapped struct{ err error }

func (e wrapped) Error() string { return "boom" }
func (e wrapped) Unwrap() error { return e.err }

// TestPublicHidesConnectionErrors: the text of a driver's connection error
// (the socket path, the role, the database) is cut from what a reader gets,
// whatever wraps it; any other error comes back whole.
func TestPublicHidesConnectionErrors(t *testing.T) {
	ctx := context.Background()
	cl := noServer(t)
	_, connectErr := cl.ConnectDB(ctx, "apolo")
	if !errors.As(connectErr, new(*pgconn.ConnectError)) {
		t.Fatalf("not a connection error of the driver: %T %v", connectErr, connectErr)
	}
	if s := connectErr.Error(); !strings.Contains(s, cl.SocketDir) || !strings.Contains(s, "user=sqsolve_admin database=apolo") || strings.Contains(s, "admin password") {
		t.Fatalf("the driver's text changed: %s", s)
	}
	sentinel := errors.New("SQS-E005 the library is not available for export")
	socket := &net.OpError{Op: "write", Net: "unix", Addr: &net.UnixAddr{Name: filepath.Join(cl.SocketDir, ".s.PGSQL.5432"), Net: "unix"}, Err: errors.New("broken pipe")}
	cases := []struct {
		name   string
		err    error
		text   string
		hidden bool
	}{
		{"export without a server", fmt.Errorf("%w: %w", sentinel, connectErr), publicText, true},
		{"the driver's error alone", connectErr, hiddenText, true},
		{"a socket that broke in a session", fmt.Errorf("SQS-E005 reading SM_Project: %w", fmt.Errorf("write failed: %w", socket)),
			"SQS-E005 reading SM_Project: write failed: " + hiddenText, true},
		{"a configuration the driver cannot parse", fmt.Errorf("%w: %w", sentinel, pgconn.NewParseConfigError("host="+cl.SocketDir+" port=x", "invalid port", nil)),
			publicText, true},
		{"an error PostgreSQL raised in a session", fmt.Errorf("SQS-E005 reading SM_Project: %w", &pgconn.PgError{Severity: "ERROR", Code: "57014", Message: "canceling statement due to statement timeout"}),
			"SQS-E005 reading SM_Project: ERROR: canceling statement due to statement timeout (SQLSTATE 57014)", false},
		{"not a driver's error", errors.New("SQS-E005 the library is not available for export: it is in state In the trash"),
			"SQS-E005 the library is not available for export: it is in state In the trash", false},
		{"a wrapper that rewrote the text", wrapped{connectErr}, hiddenText, true},
		{"no error", nil, "", false},
	}
	for _, c := range cases {
		text, cut := pg.Public(c.err)
		if text != c.text || cut != c.hidden {
			t.Errorf("%s: %q %v, want %q %v", c.name, text, cut, c.text, c.hidden)
		}
		if !cut {
			continue
		}
		for _, internal := range []string{cl.SocketDir, ".s.PGSQL", "sqsolve_admin", "apolo"} {
			if strings.Contains(text, internal) {
				t.Errorf("%s: the text names %q: %s", c.name, internal, text)
			}
		}
	}
}

// TestSuperuserKeepsTheDriverError: the superuser connection (the doctor's
// check) reports SQS-P001 with the driver's error still in the chain, so
// pg.Public can cut its text for a reader without the diagnostics
// permission (docs 08 §5).
func TestSuperuserKeepsTheDriverError(t *testing.T) {
	cl := noServer(t)
	_, err := cl.Superuser(context.Background(), "postgres")
	if !errors.Is(err, pg.ErrUnavailable) || !errors.As(err, new(*pgconn.ConnectError)) ||
		!strings.HasPrefix(err.Error(), "SQS-P001 PostgreSQL is not available: failed to connect to `user=postgres database=postgres`: ") {
		t.Fatalf("superuser without a server: %v", err)
	}
	text, hidden := pg.Public(err)
	if want := "SQS-P001 PostgreSQL is not available: " + hiddenText; text != want || !hidden {
		t.Fatalf("public text: %q %v, want %q", text, hidden, want)
	}
	if strings.Contains(text, cl.SocketDir) || strings.Contains(text, ".s.PGSQL") {
		t.Fatalf("the public text names the socket: %s", text)
	}
}
