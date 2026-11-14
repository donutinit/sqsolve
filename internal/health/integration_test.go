// SPDX-License-Identifier: AGPL-3.0-only

package health

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/testpg"
)

// Ops L3: the dashboard compares the server with the bundled pg_dump.
func TestSnapshotComparesPostgresWithPgDump(t *testing.T) {
	c := testpg.Start(t)
	ctx := context.Background()
	client := pg.New(c.SocketDir, c.Port, c.AdminPass, c.SuperPass)
	if err := client.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	db := newDB(t)
	rec := recorder(db)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{Location: time.UTC, AppDir: t.TempDir(), BackupDir: t.TempDir(), PGDir: c.Base}
	svc := &core.Service{Cfg: cfg, DB: db, PG: client, Exec: execx.NewRunner(c.BinDir), Events: rec, Log: log}
	m := &Monitor{Svc: svc, Jobs: jobs.NewManager(db, rec, events.NewBus(), log, 1, 1), Events: rec}

	// The real pg_dump next to the test server: the same version.
	st := m.Snapshot(ctx)
	for _, a := range st.Attention {
		if strings.Contains(a.Message, "pg_dump") {
			t.Fatalf("same versions reported: %+v", a)
		}
	}
	if !strings.Contains(m.PGDumpVersion(ctx), "pg_dump (PostgreSQL)") {
		t.Fatalf("pg_dump --version: %q", m.PGDumpVersion(ctx))
	}

	// Another major version of pg_dump.
	m2 := &Monitor{Svc: svc, Jobs: m.Jobs, Events: rec}
	m2.dumpOnce.Do(func() { m2.dumpVersion = "pg_dump (PostgreSQL) 99.0" })
	found := false
	for _, a := range m2.Snapshot(ctx).Attention {
		found = found || (a.Severity == events.Critical && strings.Contains(a.Message, "pg_dump 99.0"))
	}
	if !found {
		t.Fatal("a different major version is not shown as critical")
	}
}

// TestSnapshotHidesTheDriverText: PostgreSQL stops after the pool opened.
// The error of the Health page holds the public text unless ForReader gives
// it whole to a reader with the diagnostics permission, and postgres.down
// carries the public text of the ping; the whole error is in the log
// (docs 08 §5).
func TestSnapshotHidesTheDriverText(t *testing.T) {
	c := testpg.Start(t)
	ctx := context.Background()
	client := pg.New(c.SocketDir, c.Port, c.AdminPass, c.SuperPass)
	if err := client.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	db := newDB(t)
	rec := recorder(db)
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	cfg := &config.Config{Location: time.UTC, AppDir: t.TempDir(), BackupDir: t.TempDir(), PGDir: c.Base}
	svc := &core.Service{Cfg: cfg, DB: db, PG: client, Exec: execx.NewRunner(c.BinDir), Events: rec, Log: log}
	m := &Monitor{Svc: svc, Jobs: jobs.NewManager(db, rec, events.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)), 1, 1), Events: rec}
	m.CheckPG(ctx)
	c.Stop()
	// Once the pool holds no connection from before the stop, every read
	// dials the socket and fails with the driver's connection error.
	for i := 0; ; i++ {
		_, err := client.Server(ctx)
		if errors.As(err, new(*pgconn.ConnectError)) {
			break
		}
		if i == 20 {
			t.Fatalf("the pool still answers with connections from before the stop: %v", err)
		}
	}

	st := m.Snapshot(ctx)
	if st.PostgresUp || st.PostgresErr != hiddenText {
		t.Fatalf("snapshot of a stopped server: up %v, %q", st.PostgresUp, st.PostgresErr)
	}
	if got := m.ForReader(st, "/health", false).PostgresErr; got != hiddenText {
		t.Errorf("a reader without the permission: %q", got)
	}
	whole := m.ForReader(st, "/health", true).PostgresErr
	if !strings.HasPrefix(whole, "failed to connect to `user=sqsolve_admin database=postgres`: ") || !strings.Contains(whole, c.SocketDir) {
		t.Errorf("a reader with the permission: %q", whole)
	}

	m.CheckPG(ctx)
	evs, _ := rec.List(ctx, events.Filter{TypePrefix: "postgres."})
	if want := "PostgreSQL is not responding: SQS-P001 PostgreSQL is not available: " + hiddenText; len(evs) != 1 || evs[0].Message != want {
		t.Fatalf("postgres.down: %+v, want %q", evs, want)
	}
	var msgs []string
	for _, ln := range jsonLines(t, &buf) {
		if e, _ := ln["err"].(string); ln["level"] == "WARN" && strings.Contains(e, c.SocketDir) {
			msgs = append(msgs, ln["msg"].(string))
		}
	}
	if want := []string{"health: the connection to PostgreSQL failed", "health: the connection to PostgreSQL failed",
		"postgres.down: the connection to PostgreSQL failed"}; strings.Join(msgs, "|") != strings.Join(want, "|") {
		t.Errorf("log lines with the whole error: %q, want %q", msgs, want)
	}
}
