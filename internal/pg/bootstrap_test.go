// SPDX-License-Identifier: AGPL-3.0-only

package pg_test

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// lockedServer is a client of a server on a Unix socket that accepts every
// connection without a password and refuses every statement with the lock
// timeout, as PostgreSQL does behind a lock someone else holds.
func lockedServer(t *testing.T) *pg.Client {
	t.Helper()
	dir := t.TempDir()
	l, err := net.Listen("unix", filepath.Join(dir, ".s.PGSQL.5432"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go refuseStatements(c)
		}
	}()
	return pg.New(dir, 5432, secrets.New("fake admin password"), secrets.New("fake superuser password"))
}

func refuseStatements(c net.Conn) {
	defer c.Close()
	b := pgproto3.NewBackend(c, c)
	if _, err := b.ReceiveStartupMessage(); err != nil {
		return
	}
	b.Send(&pgproto3.AuthenticationOk{})
	b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if b.Flush() != nil {
		return
	}
	for {
		msg, err := b.Receive()
		if err != nil {
			return
		}
		switch msg.(type) {
		case *pgproto3.Sync, *pgproto3.Query:
			b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "55P03", Message: "canceling statement due to lock timeout"})
			b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if b.Flush() != nil {
				return
			}
		case *pgproto3.Terminate:
			return
		}
	}
}

// TestBootstrapReadBehindALock: the first statement of Bootstrap, the read
// of whether sqsolve_admin exists, fails like the ones after it: SQS-P050
// inside SQS-P002, keeping the PostgreSQL error.
func TestBootstrapReadBehindALock(t *testing.T) {
	err := lockedServer(t).Bootstrap(context.Background())
	want := "SQS-P002 preparing sqsolve_admin: " + pg.ErrLockTimeout.Error() + ": ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)"
	if err == nil || err.Error() != want || !errors.Is(err, pg.ErrLockTimeout) || !pg.IsCode(err, "55P03") {
		t.Fatalf("got %v\nwant %s", err, want)
	}
}
