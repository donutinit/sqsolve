// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/notify"
)

// A graceful stop ends a running job as interrupted (not cancelled) and
// still delivers the notification it emits while stopping.
func TestStopGracefullyInterruptsJobsThenNotifies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := t.TempDir()
	cfg := &config.Config{AppDir: filepath.Join(base, "app"), BackupDir: filepath.Join(base, "backups"), PGDir: filepath.Join(base, "pg"),
		PGDataDir: filepath.Join(base, "pg", "data"), PGSocket: filepath.Join(base, "sock"), PGPort: 1, PGListen: "127.0.0.1",
		PGMaxConnections: 50, PGMemoryMB: 256, UID: os.Getuid(), GID: os.Getgid(), PGUID: os.Getuid(), PGGID: os.Getgid(),
		LogLevel: "warn", LogFormat: "text", Workers: 1, HeavyJobs: 1, Location: time.UTC, InsecureHTTP: true, Listen: "127.0.0.1:0"}
	os.MkdirAll(filepath.Dir(cfg.SuperuserPasswordFile()), 0o700)
	if err := os.WriteFile(cfg.SuperuserPasswordFile(), []byte("x\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := app.Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	var mu sync.Mutex
	var got []notify.Payload
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p notify.Payload
		json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}))
	defer sink.Close()
	ch := &notify.Channel{Kind: notify.Webhook, Name: "hook", Enabled: true, MinSeverity: events.Warning, Pattern: "job.*", Config: map[string]string{"url": sink.URL}}
	if err := a.Notify.Save(ctx, audit.System, ch); err != nil {
		t.Fatal(err)
	}
	// The notifier loop runs on the signal context, which ends first.
	sigCtx, signal := context.WithCancel(ctx)
	runDone := make(chan struct{})
	go func() { a.Notify.Run(sigCtx); close(runDone) }()

	started := make(chan struct{})
	a.Jobs.Register(jobs.Spec{Type: "test.dump", Cancellable: true, Run: func(ctx context.Context, r *jobs.Run) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	a.Jobs.Start(ctx)
	j, err := a.Jobs.Enqueue(ctx, "test.dump", "", nil, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	signal()
	<-runDone
	stopGracefully(&http.Server{}, a)

	g, _ := a.Jobs.Get(ctx, j.ID)
	if g.State != jobs.Interrupted || g.ErrorCode != "SQS-J010" {
		t.Fatalf("job after the stop: %s %s", g.State, g.ErrorDetail)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Type != events.JobInterrupted || got[0].JobID != j.ID {
		t.Fatalf("notifications after the stop: %+v", got)
	}
}
