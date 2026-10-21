// SPDX-License-Identifier: AGPL-3.0-only

package health

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

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
