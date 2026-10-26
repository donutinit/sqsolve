// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/donutinit/sqsolve/internal/api"
	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/testpg"
	"github.com/donutinit/sqsolve/internal/web"
)

// fxPGPanel builds the panel on a throwaway PostgreSQL cluster with the
// background workers running, and the users of jobsPanel ("boss", "op")
// plus the viewer "eve".
func fxPGPanel(t *testing.T) (*app.App, *httptest.Server, *testpg.Cluster) {
	t.Helper()
	c := testpg.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := &config.Config{AppDir: t.TempDir(), BackupDir: t.TempDir(), PGDir: c.Base, PGDataDir: c.DataDir, PGSocket: c.SocketDir,
		PGPort: c.Port, PGListen: "127.0.0.1", PGBinDir: c.BinDir, PGMaxConnections: 50, PGMemoryMB: 256,
		UID: os.Getuid(), GID: os.Getgid(), PGUID: os.Getuid(), PGGID: os.Getgid(), LogLevel: "warn", LogFormat: "text",
		Workers: 2, HeavyJobs: 1, Location: time.UTC, InsecureHTTP: true, Listen: "127.0.0.1:0"}
	if err := os.WriteFile(cfg.SuperuserPasswordFile(), []byte(c.SuperPass.Reveal()+"\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := app.Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	a.EnsureKeyCheck(ctx)
	for _, u := range []struct {
		name, pass string
		role       auth.Role
	}{{"boss", jobsAdminPass, auth.RoleAdmin}, {"op", jobsOpPass, auth.RoleOperator}, {"eve", fxViewerPass, auth.RoleViewer}} {
		if _, err := a.Auth.CreateUser(ctx, audit.System, u.name, "", u.pass, u.role); err != nil {
			t.Fatal(err)
		}
	}
	h, err := web.New(a, (&api.API{A: a}).Register)
	if err != nil {
		t.Fatal(err)
	}
	a.Background(ctx)
	t.Cleanup(func() { a.Shutdown(5 * time.Second) })
	for i := 0; i < 100 && !a.PGReady(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !a.PGReady() {
		t.Fatalf("PostgreSQL not ready\n%s", c.Log())
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return a, srv, c
}

// fxConnect opens a workstation connection (TCP, like Resolve) as role,
// retrying while PostgreSQL applies a pg_hba.conf reload.
func fxConnect(t *testing.T, c *testpg.Cluster, role string, pass secrets.String, db string) (*pgx.Conn, error) {
	t.Helper()
	var conn *pgx.Conn
	var err error
	for i := 0; i < 30; i++ {
		if conn, err = c.ConnectTCP(context.Background(), role, pass, db); err == nil {
			return conn, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, err
}

// fxReveal returns the password of a credential of a library ("" for the
// owner).
func fxReveal(t *testing.T, a *app.App, libraryID, credID string) secrets.String {
	t.Helper()
	pw, err := a.Svc.RevealPassword(context.Background(), audit.System, libraryID, credID)
	if err != nil {
		t.Fatal(err)
	}
	return pw
}

// fxActiveLibrary creates a library from the panel and initializes it the
// way Resolve does: its owner creates the database, then init creates the
// tables, and sqsolve sees it become active.
func fxActiveLibrary(t *testing.T, a *app.App, c *testpg.Cluster, b *browser, tok, name, slug string, init func(*pgx.Conn)) *core.Library {
	t.Helper()
	ctx := context.Background()
	_, loc, _ := b.do("POST", "/libraries/new", url.Values{"csrf": {tok}, "name": {name}, "slug": {slug}, "strategy": {"resolve"}})
	id := strings.TrimPrefix(loc, "/libraries/")
	var l *core.Library
	for i := 0; i < 100; i++ {
		if l, _ = a.Svc.Library(ctx, id); l != nil && l.State == core.StateAwaitingInit {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if l == nil || l.State != core.StateAwaitingInit {
		t.Fatalf("library %s not waiting for Resolve: %+v (%s)", slug, l, loc)
	}
	pw := fxReveal(t, a, id, "")
	conn, err := fxConnect(t, c, l.OwnerRole, pw, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{l.DBName}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	if conn, err = c.ConnectTCP(ctx, l.OwnerRole, pw, l.DBName); err != nil {
		t.Fatal(err)
	}
	init(conn)
	conn.Close(ctx)
	for i := 0; i < 50; i++ {
		a.Svc.WatchInit(ctx)
		if l, _ = a.Svc.Library(ctx, id); l.State == core.StateActive {
			return l
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("library %s not active: %s", slug, l.State)
	return nil
}
