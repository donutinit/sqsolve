// SPDX-License-Identifier: AGPL-3.0-only

// Package health watches PostgreSQL, the volumes and certificates, emits
// events when something crosses a threshold and builds the status snapshot
// shown on the dashboard.
package health

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/pgconf"
)

// Volume is the state of one volume.
type Volume struct {
	Name     string
	Path     string
	Total    uint64
	Free     uint64
	UsedPct  float64
	MarkerOK bool
	Err      string
}

// Tone for the UI.
func (v Volume) Tone() string {
	switch {
	case v.Err != "":
		return "bad"
	case v.UsedPct >= 90:
		return "bad"
	case v.UsedPct >= 80:
		return "warn"
	}
	return "ok"
}

// Cert is a certificate state.
type Cert struct {
	Name        string
	NotAfter    time.Time
	Fingerprint string
	Err         string
}

// Attention is something the administrator should look at.
type Attention struct {
	Severity events.Severity
	Message  string
	Link     string
}

// Status is the dashboard snapshot.
type Status struct {
	At          time.Time
	Postgres    pg.ServerInfo
	PostgresUp  bool
	PostgresErr string
	Volumes     []Volume
	Certs       []Cert
	Queued      int
	Running     int
	Failed24h   int
	Libraries   int
	Active      int
	Attention   []Attention
	Databases   []pg.DBInfo
}

// Monitor runs the periodic checks.
type Monitor struct {
	Svc      *core.Service
	Jobs     *jobs.Manager
	Events   *events.Recorder
	PanelTLS string // path of the panel certificate

	mu        sync.Mutex
	pgUp      *bool
	volLevels map[string]int
}

// Run loops until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	fast := time.NewTicker(5 * time.Second)
	slow := time.NewTicker(5 * time.Minute)
	daily := time.NewTicker(24 * time.Hour)
	defer fast.Stop()
	defer slow.Stop()
	defer daily.Stop()
	m.checkPG(ctx)
	m.checkVolumes(ctx)
	m.checkCerts(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			m.checkPG(ctx)
		case <-slow.C:
			m.checkVolumes(ctx)
			if m.isUp() {
				m.Svc.FingerprintAll(ctx)
			}
		case <-daily.C:
			m.checkCerts(ctx)
		}
	}
}

// PostgresUp reports the last check. Before the first one it trusts the
// startup connection.
func (m *Monitor) PostgresUp() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pgUp == nil || *m.pgUp
}

func (m *Monitor) isUp() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pgUp != nil && *m.pgUp
}

func (m *Monitor) checkPG(ctx context.Context) {
	err := m.Svc.PG.Ping(ctx)
	up := err == nil
	m.mu.Lock()
	prev := m.pgUp
	m.pgUp = &up
	m.mu.Unlock()
	if prev == nil {
		return
	}
	if *prev && !up {
		m.Events.Emit(ctx, events.Event{Type: events.PostgresDown, Severity: events.Critical, Message: "PostgreSQL is not responding: " + err.Error()})
	} else if !*prev && up {
		m.Events.Emit(ctx, events.Event{Type: events.PostgresUp, Message: "PostgreSQL is responding again"})
	}
}

// Volumes returns the state of every volume.
func (m *Monitor) Volumes() []Volume {
	cfg := m.Svc.Cfg
	list := []struct{ name, path string }{
		{"Database", filepath.Join(cfg.PGDir)},
		{"Application", cfg.AppDir},
		{"Backups", cfg.BackupDir},
	}
	var out []Volume
	for _, v := range list {
		vol := Volume{Name: v.name, Path: v.path, MarkerOK: true}
		sp, err := fsx.Statfs(v.path)
		if err != nil {
			vol.Err = err.Error()
		} else {
			vol.Total, vol.Free, vol.UsedPct = sp.Total, sp.Free, sp.UsedPercent()
		}
		if v.path == cfg.BackupDir {
			if err := m.Svc.BackupVolumeOK(); err != nil {
				vol.MarkerOK = false
				vol.Err = err.Error()
			}
		}
		out = append(out, vol)
	}
	return out
}

func (m *Monitor) checkVolumes(ctx context.Context) {
	m.mu.Lock()
	if m.volLevels == nil {
		m.volLevels = map[string]int{}
	}
	m.mu.Unlock()
	for _, v := range m.Volumes() {
		level := 0
		switch {
		case v.UsedPct >= 90:
			level = 2
		case v.UsedPct >= 80:
			level = 1
		}
		if !v.MarkerOK {
			level = 3
		}
		m.mu.Lock()
		prev := m.volLevels[v.Name]
		m.volLevels[v.Name] = level
		m.mu.Unlock()
		if level <= prev {
			continue
		}
		switch level {
		case 1:
			m.Events.Emit(ctx, events.Event{Type: events.StorageLow, Severity: events.Warning, Message: fmt.Sprintf("Volume %s is at %.0f %%", v.Name, v.UsedPct)})
		case 2:
			m.Events.Emit(ctx, events.Event{Type: events.StorageLow, Severity: events.Error, Message: fmt.Sprintf("Volume %s is at %.0f %%: free up space", v.Name, v.UsedPct)})
		case 3:
			m.Events.Emit(ctx, events.Event{Type: events.StorageVolumeMissing, Severity: events.Critical, Message: "The backups volume is not available: " + v.Err})
		}
	}
}

// Certs returns the certificates in use.
func (m *Monitor) Certs() []Cert {
	var out []Cert
	for _, c := range []struct{ name, path string }{
		{"Panel web", m.PanelTLS},
		{"PostgreSQL", filepath.Join(m.Svc.Cfg.PGConfDir(), "tls", "server.crt")},
	} {
		if c.path == "" {
			continue
		}
		info, err := pgconf.ReadCert(c.path)
		cert := Cert{Name: c.name, NotAfter: info.NotAfter, Fingerprint: info.Fingerprint}
		if err != nil {
			cert.Err = err.Error()
		}
		out = append(out, cert)
	}
	return out
}

func (m *Monitor) checkCerts(ctx context.Context) {
	for _, c := range m.Certs() {
		if c.Err == "" && time.Until(c.NotAfter) < 30*24*time.Hour {
			m.Events.Emit(ctx, events.Event{Type: events.TLSExpiring, Severity: events.Warning,
				Message: fmt.Sprintf("The certificate of %s expires on %s", c.Name, c.NotAfter.Format("2006-01-02"))})
		}
	}
}

// Snapshot builds the dashboard status.
func (m *Monitor) Snapshot(ctx context.Context) Status {
	st := Status{At: time.Now()}
	if info, err := m.Svc.PG.Server(ctx); err == nil {
		st.Postgres, st.PostgresUp = info, true
		st.Databases, _ = m.Svc.PG.Databases(ctx)
	} else {
		st.PostgresErr = err.Error()
		st.Attention = append(st.Attention, Attention{events.Critical, "PostgreSQL is not responding: workstations cannot open projects", "/health"})
	}
	st.Volumes = m.Volumes()
	for _, v := range st.Volumes {
		switch v.Tone() {
		case "bad":
			msg := fmt.Sprintf("Volumen %s al %.0f %%", v.Name, v.UsedPct)
			if v.Err != "" {
				msg = "Volumen " + v.Name + ": " + v.Err
			}
			st.Attention = append(st.Attention, Attention{events.Error, msg, "/health"})
		case "warn":
			st.Attention = append(st.Attention, Attention{events.Warning, fmt.Sprintf("Volumen %s al %.0f %%", v.Name, v.UsedPct), "/health"})
		}
	}
	st.Certs = m.Certs()
	for _, c := range st.Certs {
		if c.Err == "" && time.Until(c.NotAfter) < 30*24*time.Hour {
			st.Attention = append(st.Attention, Attention{events.Warning, "The certificate of " + c.Name + " expires soon", "/health"})
		}
	}
	st.Queued, st.Running, _ = m.Jobs.Counts(ctx)
	st.Failed24h, _ = m.Jobs.FailedSince(ctx, time.Now().Add(-24*time.Hour))
	if st.Failed24h > 0 {
		st.Attention = append(st.Attention, Attention{events.Error, fmt.Sprintf("%d jobs failed in the last 24 hours", st.Failed24h), "/jobs?state=failed"})
	}
	libs, _ := m.Svc.Libraries(ctx, core.ListOptions{})
	stale := time.Duration(m.Svc.Settings(ctx).StaleHours) * time.Hour
	for _, l := range libs {
		st.Libraries++
		switch l.State {
		case core.StateActive, core.StateMaintenance:
			st.Active++
			if time.Since(l.CreatedAt) > stale {
				if b, _ := m.Svc.LatestVerified(ctx, l.ID); b == nil || time.Since(b.StartedAt) > stale {
					st.Attention = append(st.Attention, Attention{events.Warning, fmt.Sprintf("%s has no recent verified backup", l.DisplayName), "/libraries/" + l.ID + "?tab=backups"})
				}
			}
		case core.StateAwaitingInit:
			st.Attention = append(st.Attention, Attention{events.Info, l.DisplayName + " is waiting for Resolve to initialize it", "/libraries/" + l.ID})
		case core.StateInitExpired:
			st.Attention = append(st.Attention, Attention{events.Warning, "Initialization expired for " + l.DisplayName, "/libraries/" + l.ID})
		case core.StateMissing:
			st.Attention = append(st.Attention, Attention{events.Critical, l.DisplayName + " has no database on the server: recreate it from a backup", "/libraries/" + l.ID})
		}
	}
	for _, d := range st.Databases {
		if d.CollationStale {
			st.Attention = append(st.Attention, Attention{events.Warning, "The collation version of " + d.Name + " changed; consider reindexing", "/health"})
		}
	}
	if st.PostgresUp && st.Postgres.MaxConnections > 0 && st.Postgres.Connections*100/st.Postgres.MaxConnections >= 80 {
		st.Attention = append(st.Attention, Attention{events.Warning, fmt.Sprintf("PostgreSQL is using %d of %d connections", st.Postgres.Connections, st.Postgres.MaxConnections), "/health"})
	}
	return st
}
