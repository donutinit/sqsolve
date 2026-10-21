// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/health"
)

// Ops M4: uptime monitors had no unauthenticated signal for stale backups or
// a nearly full volume.
func TestHealthBody(t *testing.T) {
	fresh := health.Freshness{Status: health.StatusOK, Libraries: 2, ThresholdHours: 48, OldestVerifiedAgeHours: 20}
	stale := fresh
	stale.Status, stale.Stale = health.StatusCritical, 1
	vols := func(pct float64) []health.Volume {
		return []health.Volume{{ID: "db", Name: "Database", Path: "/srv/secret/db", UsedPct: 40}, {ID: "backups", Name: "Backups", Path: "/srv/secret/backups", UsedPct: pct}}
	}
	for _, tc := range []struct {
		name          string
		pg, vol       bool
		fresh         health.Freshness
		vols          []health.Volume
		code          int
		status, volSt string
	}{
		{"all good", true, true, fresh, vols(50), 200, "ok", "ok"},
		{"volume nearly full", true, true, fresh, vols(85), 200, "warning", "warning"},
		{"volume full", true, true, fresh, vols(92), 503, "critical", "critical"},
		{"stale backups", true, true, stale, vols(50), 503, "critical", "ok"},
		{"postgres down", false, true, fresh, vols(50), 503, "critical", "ok"},
		{"backups volume not mounted", true, false, fresh, vols(50), 503, "critical", "ok"},
	} {
		code, body := healthBody(tc.pg, tc.vol, false, tc.fresh, tc.vols)
		raw, _ := json.Marshal(body)
		var got struct {
			Ready   bool             `json:"ready"`
			Status  string           `json:"status"`
			Backups health.Freshness `json:"backups"`
			Volumes []struct {
				Name   string  `json:"name"`
				Used   float64 `json:"used_percent"`
				Status string  `json:"status"`
			} `json:"volumes"`
		}
		json.Unmarshal(raw, &got)
		if code != tc.code || got.Status != tc.status || got.Ready != (tc.pg && tc.vol) {
			t.Errorf("%s: %d %+v", tc.name, code, got)
		}
		if len(got.Volumes) != 2 || got.Volumes[1].Name != "backups" || got.Volumes[1].Status != tc.volSt {
			t.Errorf("%s: volumes %+v", tc.name, got.Volumes)
		}
		if got.Backups.Libraries != 2 || got.Backups.ThresholdHours != 48 {
			t.Errorf("%s: backups %+v", tc.name, got.Backups)
		}
		if strings.Contains(string(raw), "/srv/secret") {
			t.Errorf("%s: paths in the unauthenticated output: %s", tc.name, raw)
		}
	}
	if code, body := healthBody(true, true, true, fresh, vols(10)); code != 503 || body["postgres_cluster"] != "changed" {
		t.Errorf("a changed cluster: %d %v", code, body)
	}
}

// The handler, on an installation whose PostgreSQL is not reachable.
func TestHealthHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pg := t.TempDir()
	cfg := &config.Config{AppDir: t.TempDir(), BackupDir: t.TempDir(), PGDir: pg, PGDataDir: filepath.Join(pg, "18", "docker"),
		PGSocket: t.TempDir(), PGPort: 5432, PGListen: "*", PGMaxConnections: 50, PGMemoryMB: 256, Workers: 1, HeavyJobs: 1,
		UID: os.Getuid(), GID: os.Getgid(), PGUID: os.Getuid(), PGGID: os.Getgid(), Location: time.UTC}
	if err := app.Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	mux := http.NewServeMux()
	(&API{A: a}).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/system/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ready", "postgres", "backup_volume", "status", "backups", "volumes", "postgres_cluster"} {
		if _, ok := body[k]; !ok {
			t.Errorf("missing %s: %s", k, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), cfg.AppDir) || strings.Contains(rec.Body.String(), a.InstanceID) {
		t.Fatalf("the unauthenticated output shows paths or the installation: %s", rec.Body.String())
	}
}
