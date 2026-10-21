// SPDX-License-Identifier: AGPL-3.0-only

// Package health watches PostgreSQL, the volumes and certificates, emits
// events when something crosses a threshold and builds the status snapshot
// shown on the dashboard.
package health

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/pgconf"
)

// Status values of the unauthenticated health (docs 13 §3).
const (
	StatusOK       = "ok"
	StatusWarning  = "warning"
	StatusCritical = "critical"
)

// DefaultStartupGrace is how long PostgreSQL may take to answer after
// sqsolve starts before postgres.down is emitted.
const DefaultStartupGrace = 5 * time.Minute

// freshnessTTL bounds how often the unauthenticated health reads the catalog,
// and freshnessTimeout how long one reading may take.
const (
	freshnessTTL     = 30 * time.Second
	freshnessTimeout = 10 * time.Second
)

// statfs measures a volume; tests replace it.
var statfs = fsx.Statfs

// Volume is the state of one volume.
type Volume struct {
	ID       string // db, app or backups, as in the volume markers
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
	switch v.Status() {
	case StatusCritical:
		return "bad"
	case StatusWarning:
		return "warn"
	}
	return "ok"
}

// Status of the volume: critical at 90 % or when it cannot be read.
func (v Volume) Status() string {
	switch {
	case v.Err != "", v.UsedPct >= 90:
		return StatusCritical
	case v.UsedPct >= 80:
		return StatusWarning
	}
	return StatusOK
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

// Freshness summarizes how recent the verified backups are, without names.
type Freshness struct {
	Status                 string  `json:"status"`
	Libraries              int     `json:"libraries"`
	Stale                  int     `json:"stale"`
	OldestVerifiedAgeHours float64 `json:"oldest_verified_age_hours"`
	ThresholdHours         int     `json:"stale_after_hours"`
}

// Monitor runs the periodic checks.
type Monitor struct {
	Svc      *core.Service
	Jobs     *jobs.Manager
	Events   *events.Recorder
	PanelTLS string // path of the panel certificate

	// Ping checks PostgreSQL; nil uses Svc.PG.Ping.
	Ping func(context.Context) error
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
	// StartupGrace is how long PostgreSQL may stay unreachable after the
	// first check before postgres.down is emitted (0: DefaultStartupGrace).
	StartupGrace time.Duration
	// CheckEvery is the interval of the PostgreSQL check (0: 5 seconds).
	CheckEvery time.Duration
	// RenewPanelCert renews the self-generated panel certificate when it is
	// about to expire; the daily check calls it first.
	RenewPanelCert func(context.Context) error
	// Notices adds conditions owned by other packages (a changed PostgreSQL
	// cluster) to the attention list.
	Notices func() []Attention
	// Paused holds the checks that write to the catalog.
	Paused func() bool

	mu        sync.Mutex
	checkMu   sync.Mutex
	pgUp      *bool
	started   time.Time
	alerted   bool
	volLevels map[string]int

	freshMu     sync.Mutex
	freshAt     time.Time
	fresh       Freshness
	freshFlight *freshFlight
	freshFn     func(context.Context, time.Time) (Freshness, error) // tests

	dumpOnce    sync.Once
	dumpVersion string
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Run loops until ctx ends. It starts with sqsolve, before PostgreSQL is
// reachable, so a server that never comes up is still reported.
func (m *Monitor) Run(ctx context.Context) {
	every := m.CheckEvery
	if every <= 0 {
		every = 5 * time.Second
	}
	fast := time.NewTicker(every)
	slow := time.NewTicker(5 * time.Minute)
	daily := time.NewTicker(24 * time.Hour)
	defer fast.Stop()
	defer slow.Stop()
	defer daily.Stop()
	m.CheckPG(ctx)
	m.checkVolumes(ctx)
	m.checkCerts(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			m.CheckPG(ctx)
		case <-slow.C:
			m.checkVolumes(ctx)
			if m.isUp() && (m.Paused == nil || !m.Paused()) {
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

func (m *Monitor) ping(ctx context.Context) error {
	if m.Ping != nil {
		return m.Ping(ctx)
	}
	return m.Svc.PG.Ping(ctx)
}

// CheckPG pings PostgreSQL and emits postgres.down on the first failed check
// after it was up, or once it has not answered for StartupGrace since the
// monitor started; postgres.up follows only an emitted postgres.down.
func (m *Monitor) CheckPG(ctx context.Context) {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	err := m.ping(ctx)
	up := err == nil
	now := m.now()
	grace := m.StartupGrace
	if grace <= 0 {
		grace = DefaultStartupGrace
	}
	m.mu.Lock()
	if m.started.IsZero() {
		m.started = now
	}
	prev := m.pgUp
	m.pgUp = &up
	seenUp := prev != nil && *prev
	emitDown, emitUp := false, false
	switch {
	case up && m.alerted:
		m.alerted, emitUp = false, true
	case !up && !m.alerted && seenUp:
		m.alerted, emitDown = true, true
	case !up && !m.alerted && now.Sub(m.started) >= grace:
		m.alerted, emitDown = true, true
	}
	m.mu.Unlock()
	switch {
	case emitDown && seenUp:
		m.Events.Emit(ctx, events.Event{Type: events.PostgresDown, Severity: events.Critical, Message: "PostgreSQL is not responding: " + err.Error()})
	case emitDown:
		m.Events.Emit(ctx, events.Event{Type: events.PostgresDown, Severity: events.Critical,
			Message: fmt.Sprintf("PostgreSQL has not answered in the %.0f minutes since sqsolve started: %v", grace.Minutes(), err)})
	case emitUp:
		m.Events.Emit(ctx, events.Event{Type: events.PostgresUp, Message: "PostgreSQL is responding again"})
	}
}

// Volumes returns the state of every volume.
func (m *Monitor) Volumes() []Volume {
	cfg := m.Svc.Cfg
	list := []struct{ id, name, path string }{
		{"db", "Database", filepath.Join(cfg.PGDir)},
		{"app", "Application", cfg.AppDir},
		{"backups", "Backups", cfg.BackupDir},
	}
	var out []Volume
	for _, v := range list {
		vol := Volume{ID: v.id, Name: v.name, Path: v.path, MarkerOK: true}
		sp, err := statfs(v.path)
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
		if !v.MarkerOK || v.Err != "" {
			// No marker on the backups volume, or a volume that cannot be
			// measured: critical, as /api/v1/system/health reports it.
			level = 3
		}
		m.mu.Lock()
		prev := m.volLevels[v.Name]
		m.volLevels[v.Name] = level
		m.mu.Unlock()
		if level <= prev {
			continue
		}
		// The volume in Data keeps the notifications of two volumes apart.
		data := map[string]any{"volume": v.ID}
		switch level {
		case 1:
			m.Events.Emit(ctx, events.Event{Type: events.StorageLow, Severity: events.Warning, Data: data,
				Message: fmt.Sprintf("Volume %s is at %.0f %%", v.Name, v.UsedPct)})
		case 2:
			m.Events.Emit(ctx, events.Event{Type: events.StorageLow, Severity: events.Error, Data: data,
				Message: fmt.Sprintf("Volume %s is at %.0f %%: free up space", v.Name, v.UsedPct)})
		case 3:
			msg := "The backups volume is not available: " + v.Err
			if v.MarkerOK {
				msg = fmt.Sprintf("Volume %s cannot be read: %s", v.Name, v.Err)
			}
			m.Events.Emit(ctx, events.Event{Type: events.StorageVolumeMissing, Severity: events.Critical, Data: data, Message: msg})
		}
	}
}

func (m *Monitor) pgCertPath() string {
	if m.Svc == nil || m.Svc.Cfg == nil {
		return ""
	}
	return filepath.Join(m.Svc.Cfg.PGConfDir(), "tls", "server.crt")
}

// Names of the certificates, also the "cert" of their tls.expiring events
// (it keeps the notifications of the two certificates apart).
const (
	panelCertName = "Panel web"
	pgCertName    = "PostgreSQL"
)

// Certs returns the certificates in use.
func (m *Monitor) Certs() []Cert {
	var out []Cert
	for _, c := range []struct{ name, path string }{
		{panelCertName, m.PanelTLS},
		{pgCertName, m.pgCertPath()},
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

// certSeverity is the severity of an expiry warning: Error in the last 14
// days (new notification channels send Error and above), Warning before.
func certSeverity(left time.Duration) events.Severity {
	switch {
	case left < 14*24*time.Hour:
		return events.Error
	case left < 30*24*time.Hour:
		return events.Warning
	}
	return ""
}

func (m *Monitor) checkCerts(ctx context.Context) {
	if m.RenewPanelCert != nil {
		if err := m.RenewPanelCert(ctx); err != nil {
			m.Events.Emit(ctx, events.Event{Type: events.TLSExpiring, Severity: events.Error, Message: err.Error(),
				Data: map[string]any{"cert": panelCertName}})
		}
	}
	for _, c := range m.Certs() {
		if c.Err != "" {
			continue
		}
		left := c.NotAfter.Sub(m.now())
		sev := certSeverity(left)
		if sev == "" {
			continue
		}
		msg := fmt.Sprintf("The certificate of %s expires on %s", c.Name, c.NotAfter.Format("2006-01-02"))
		if left <= 0 {
			msg = fmt.Sprintf("The certificate of %s expired on %s", c.Name, c.NotAfter.Format("2006-01-02"))
		}
		m.Events.Emit(ctx, events.Event{Type: events.TLSExpiring, Severity: sev, Message: msg, Data: map[string]any{"cert": c.Name}})
	}
}

var dumpVersionRe = regexp.MustCompile(`\)\s+(\d+)(?:\.(\d+))?`)

// CompareVersions compares the server (server_version_num) with the output
// of the bundled "pg_dump --version". A different major version is
// critical; an older server minor version is a warning. It returns an empty
// severity when they agree or when either version is unknown.
func CompareVersions(serverNum int, pgDump string) (events.Severity, string) {
	mt := dumpVersionRe.FindStringSubmatch(pgDump)
	if serverNum <= 0 || mt == nil {
		return "", ""
	}
	dumpMajor, _ := strconv.Atoi(mt[1])
	dumpMinor, _ := strconv.Atoi(mt[2])
	srvMajor, srvMinor := serverNum/10000, serverNum%10000
	switch {
	case srvMajor != dumpMajor:
		return events.Critical, fmt.Sprintf("PostgreSQL %d.%d does not match pg_dump %d.%d bundled with sqsolve: backups and restores can fail. "+
			"Use the postgres image of the compose file of this sqsolve version", srvMajor, srvMinor, dumpMajor, dumpMinor)
	case srvMinor < dumpMinor:
		return events.Warning, fmt.Sprintf("PostgreSQL %d.%d is older than the bundled pg_dump %d.%d: update the postgres image digest in your compose file",
			srvMajor, srvMinor, dumpMajor, dumpMinor)
	}
	return "", ""
}

// PGDumpVersion returns the output of the bundled "pg_dump --version", read
// once.
func (m *Monitor) PGDumpVersion(ctx context.Context) string {
	m.dumpOnce.Do(func() {
		if m.Svc == nil || m.Svc.Exec == nil {
			return
		}
		// Read once for the whole process: a cancelled request must not
		// leave it empty.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if res, err := m.Svc.Exec.Run(cctx, execx.Cmd{Name: "pg_dump", Args: []string{"--version"}}); err == nil {
			m.dumpVersion = res.Stdout
		}
	})
	return m.dumpVersion
}

// volumeAttention is the attention line of a volume, if it needs one.
func volumeAttention(v Volume) (Attention, bool) {
	switch v.Tone() {
	case "bad":
		msg := fmt.Sprintf("Volume %s is at %.0f %%", v.Name, v.UsedPct)
		if v.Err != "" {
			msg = "Volume " + v.Name + ": " + v.Err
		}
		return Attention{events.Error, msg, "/health"}, true
	case "warn":
		return Attention{events.Warning, fmt.Sprintf("Volume %s is at %.0f %%", v.Name, v.UsedPct), "/health"}, true
	}
	return Attention{}, false
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
	if m.Notices != nil {
		st.Attention = append(st.Attention, m.Notices()...)
	}
	if st.PostgresUp {
		if sev, msg := CompareVersions(st.Postgres.VersionNum, m.PGDumpVersion(ctx)); sev != "" {
			st.Attention = append(st.Attention, Attention{sev, msg, "/health"})
		}
	}
	st.Volumes = m.Volumes()
	for _, v := range st.Volumes {
		if a, ok := volumeAttention(v); ok {
			st.Attention = append(st.Attention, a)
		}
	}
	st.Certs = m.Certs()
	for _, c := range st.Certs {
		if c.Err == "" {
			if sev := certSeverity(time.Until(c.NotAfter)); sev != "" {
				st.Attention = append(st.Attention, Attention{sev, "The certificate of " + c.Name + " expires soon", "/health"})
			}
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

// BackupFreshness reports, for the active libraries with an enabled backup
// schedule, the age of the oldest last verified backup against the stale
// threshold housekeeping uses (core.CheckStale): a library younger than the
// threshold or unchanged since its last verified backup (ADR 0022) is not
// stale. The endpoint that shows it needs no authentication, so a good
// result is cached for freshnessTTL and concurrent probes share one
// computation; while a refresh runs, probes get the previous good result.
// The computation does not depend on the probe's context (a probe that
// gives up must not turn into a failed read) and is bounded by
// freshnessTimeout. A failed computation is critical for that request and
// is not cached.
func (m *Monitor) BackupFreshness(ctx context.Context) Freshness {
	now := m.now()
	m.freshMu.Lock()
	cached := !m.freshAt.IsZero()
	if age := now.Sub(m.freshAt); cached && age >= 0 && age < freshnessTTL {
		f := m.fresh
		m.freshMu.Unlock()
		return f
	}
	if fl := m.freshFlight; fl != nil {
		if cached {
			f := m.fresh
			m.freshMu.Unlock()
			return f
		}
		m.freshMu.Unlock()
		<-fl.done
		return fl.f
	}
	// Waiters see critical unless the computation finishes well.
	fl := &freshFlight{done: make(chan struct{}), f: Freshness{Status: StatusCritical}}
	m.freshFlight = fl
	m.freshMu.Unlock()
	ok := false
	// Deferred so a panic in the computation never leaves the flight set.
	defer func() {
		m.freshMu.Lock()
		m.freshFlight = nil
		if ok {
			m.fresh, m.freshAt = fl.f, now
		}
		m.freshMu.Unlock()
		close(fl.done)
	}()

	compute := m.computeFreshness
	if m.freshFn != nil {
		compute = m.freshFn
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), freshnessTimeout)
	defer cancel()
	f, err := compute(cctx, now)
	fl.f, ok = f, err == nil
	return f
}

// freshFlight is a freshness computation in progress.
type freshFlight struct {
	done chan struct{}
	f    Freshness
}

// computeFreshness reads the catalog. On error the result is critical.
func (m *Monitor) computeFreshness(ctx context.Context, now time.Time) (Freshness, error) {
	hours := m.Svc.Settings(ctx).StaleHours
	threshold := time.Duration(hours) * time.Hour
	f := Freshness{Status: StatusOK, ThresholdHours: hours}
	failed := func(err error) (Freshness, error) {
		f.Status = StatusCritical
		return f, err
	}
	libs, err := m.Svc.Libraries(ctx, core.ListOptions{States: []core.State{core.StateActive, core.StateMaintenance}})
	if err != nil {
		return failed(err)
	}
	var oldest time.Duration
	for _, l := range libs {
		pol, err := m.Svc.Policy(ctx, l.ID)
		if err != nil {
			return failed(err)
		}
		if !pol.Enabled {
			continue
		}
		f.Libraries++
		// The rule housekeeping uses for backup.stale (core.Staleness).
		b, stale, err := m.Svc.Staleness(ctx, l, now, threshold)
		if err != nil {
			return failed(err)
		}
		if b != nil && now.Sub(b.StartedAt) > oldest {
			oldest = now.Sub(b.StartedAt)
		}
		if stale {
			f.Stale++
		}
	}
	f.OldestVerifiedAgeHours = float64(int64(oldest.Hours()*10)) / 10
	if f.Stale > 0 {
		f.Status = StatusCritical
	}
	return f, nil
}
