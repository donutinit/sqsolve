// SPDX-License-Identifier: AGPL-3.0-only

// Package app wires every service together for the server and the CLI.
package app

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/health"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/notify"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/pgconf"
	"github.com/donutinit/sqsolve/internal/scheduler"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

// Build information, set by main.
var (
	Version = "dev"
	Commit  = "unknown"
)

// App holds the services.
type App struct {
	Cfg        *config.Config
	Log        *slog.Logger
	DB         *store.DB
	Box        *secrets.Box
	Audit      *audit.Log
	Bus        *events.Bus
	Events     *events.Recorder
	Jobs       *jobs.Manager
	Sched      *scheduler.Scheduler
	PG         *pg.Client
	Svc        *core.Service
	Auth       *auth.Service
	Notify     *notify.Notifier
	Monitor    *health.Monitor
	InstanceID string
	Started    time.Time

	pgReady         atomic.Bool
	identityProblem atomic.Pointer[string]
	installMu       sync.Mutex
	installCode     string
	cert            atomic.Pointer[tls.Certificate]
	cancel          context.CancelFunc
}

// ReadMasterKey loads the master key file.
func ReadMasterKey(c *config.Config) ([]byte, error) {
	s, err := secrets.ReadFile(masterKeyPath(c))
	if err != nil {
		return nil, fmt.Errorf("SQS-C100 could not read the master key (did you run sqsolve init?): %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s.Reveal()))
	if err != nil {
		return nil, fmt.Errorf("SQS-C011 the master key is not valid base64: %w", err)
	}
	return key, nil
}

// Open builds the application without starting background work.
func Open(ctx context.Context, c *config.Config, log *slog.Logger) (*App, error) {
	a := &App{Cfg: c, Log: log, Started: time.Now()}
	idb, err := os.ReadFile(instanceIDPath(c))
	if err != nil {
		return nil, fmt.Errorf("SQS-C052 %s is missing: run sqsolve init (the sqsolve-init service of the compose file): %w", instanceIDPath(c), err)
	}
	a.InstanceID = strings.TrimSpace(string(idb))
	key, err := ReadMasterKey(c)
	if err != nil {
		return nil, err
	}
	if a.Box, err = secrets.NewBox(key); err != nil {
		return nil, err
	}
	adminPass, err := secrets.ReadFile(adminPassPath(c))
	if err != nil {
		return nil, fmt.Errorf("SQS-C013 sqsolve_admin password: %w", err)
	}
	superPass, err := secrets.ReadFile(c.SuperuserPasswordFile())
	if err != nil {
		return nil, fmt.Errorf("SQS-C014 superuser password: %w", err)
	}
	if a.DB, err = store.Open(ctx, c.DBPath(), stateBackupDir(c)); err != nil {
		return nil, err
	}
	a.Audit = audit.New(a.DB)
	a.Bus = events.NewBus()
	a.Events = events.NewRecorder(a.DB, a.Bus, log)
	a.Jobs = jobs.NewManager(a.DB, a.Events, a.Bus, log, c.Workers, c.HeavyJobs)
	a.Sched = scheduler.New(a.DB, a.Jobs, a.Events, log)
	a.PG = pg.New(c.PGSocket, c.PGPort, adminPass, superPass)
	core.Version = Version
	a.Svc = &core.Service{Cfg: c, DB: a.DB, PG: a.PG, Exec: execx.NewRunner(c.PGBinDir), Box: a.Box, Jobs: a.Jobs, Sched: a.Sched,
		Events: a.Events, Audit: a.Audit, Log: log, InstanceID: a.InstanceID}
	a.Svc.Register()
	a.Auth = auth.New(a.DB, a.Box, a.Audit, a.Events, key)
	a.Notify = notify.New(a.DB, a.Box, a.Audit, log)
	a.Notify.Instance = func() string { return a.Svc.Settings(context.Background()).InstanceName }
	a.Notify.BaseURL = a.BaseURL
	a.Events.Listen(a.Notify.Handle)
	a.Monitor = &health.Monitor{Svc: a.Svc, Jobs: a.Jobs, Events: a.Events, PanelTLS: panelCertPath(c),
		RenewPanelCert: a.RenewPanelCert, Notices: a.identityNotices,
		// Catalog writes of the monitor wait until reconciliation is done.
		Paused: func() bool { return !a.pgReady.Load() }}
	return a, nil
}

// BaseURL is the panel address for links in notifications.
func (a *App) BaseURL() string {
	host := a.Svc.AdvertisedHost(context.Background())
	if host == "" {
		return ""
	}
	_, port, _ := net.SplitHostPort(a.Cfg.Listen)
	scheme := "https"
	if a.Cfg.InsecureHTTP {
		scheme = "http"
	}
	if port == "" {
		port = "9432"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// Close releases resources.
func (a *App) Close() {
	a.PG.Close()
	a.DB.Close()
}

// PGAlive pings PostgreSQL now. Readiness probes use it instead of the
// monitor's last check, which can be seconds old.
func (a *App) PGAlive(ctx context.Context) bool {
	if !a.pgReady.Load() {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return a.PG.Ping(ctx) == nil
}

// PGReady reports whether PostgreSQL was reached and reconciled at startup
// and still answers.
func (a *App) PGReady() bool {
	return a.pgReady.Load() && (a.Monitor == nil || a.Monitor.PostgresUp())
}

// InstallCode returns the one-time setup code while there is no admin.
func (a *App) InstallCode() string {
	a.installMu.Lock()
	defer a.installMu.Unlock()
	return a.installCode
}

// ClearInstallCode removes the setup code after the first admin exists.
func (a *App) ClearInstallCode() {
	a.installMu.Lock()
	a.installCode = ""
	a.installMu.Unlock()
}

// Certificate returns the current panel certificate.
func (a *App) Certificate() (*tls.Certificate, error) {
	if c := a.cert.Load(); c != nil {
		return c, nil
	}
	return nil, errors.New("no certificate")
}

// PanelCertInfo describes the panel certificate.
func (a *App) PanelCertInfo() (pgconf.CertInfo, error) { return pgconf.ReadCert(panelCertPath(a.Cfg)) }

// removeStalePartials deletes what an interrupted control plane copy left
// behind: in the app volume always, in the backups volume only when it
// carries this installation's marker.
func (a *App) removeStalePartials() {
	dirs := []string{stateBackupDir(a.Cfg)}
	if a.Svc.BackupVolumeOK() == nil {
		dirs = append(dirs, filepath.Join(a.Cfg.BackupDir, "_system"))
	}
	for _, d := range dirs {
		if n, err := fsx.RemovePartials(d); err != nil {
			a.Log.Warn("could not remove interrupted copies", "dir", d, "err", err)
		} else if n > 0 {
			a.Log.Info("removed interrupted copies", "dir", d, "files", n)
		}
	}
}

// Background starts the monitor, the PostgreSQL connection, reconciliation
// and the loops. The monitor and the notifier start first, so a PostgreSQL
// that never answers is reported (postgres.down after the startup grace).
func (a *App) Background(ctx context.Context) {
	ctx, a.cancel = context.WithCancel(ctx)
	a.removeStalePartials()
	go a.Notify.Run(ctx)
	go a.Monitor.Run(ctx)
	go func() {
		delay := time.Second
		for {
			err := a.connectPG(ctx)
			if err == nil {
				break
			}
			a.Log.Warn("waiting for PostgreSQL", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay < 15*time.Second {
				delay *= 2
			}
		}
		a.Monitor.CheckPG(ctx)
		if !a.guardCluster(ctx) {
			// A different cluster: no reconciliation, no pg_hba.conf, no
			// jobs until the change is accepted and sqsolve restarts.
			return
		}
		interrupted, err := a.Jobs.MarkInterrupted(ctx)
		if err != nil {
			a.Log.Error("marking interrupted jobs", "err", err)
		}
		for _, n := range a.Svc.Reconcile(ctx, interrupted) {
			a.Log.Info("reconciliation", "note", n)
		}
		if err := a.Svc.EnsureSystemSchedules(ctx); err != nil {
			a.Log.Error("system schedules", "err", err)
		}
		a.pgReady.Store(true)
		a.Jobs.Start(ctx)
		go a.Sched.Run(ctx)
		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					a.Svc.WatchInit(ctx)
				}
			}
		}()
		go func() {
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					a.Auth.PurgeSessions(ctx)
				}
			}
		}()
		a.Log.Info("sqsolve ready", "version", Version)
	}()
}

func (a *App) connectPG(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := a.PG.Bootstrap(cctx); err != nil {
		return err
	}
	return a.PG.Open(cctx)
}

// PrepareInstallCode generates the setup code if there is no admin yet.
func (a *App) PrepareInstallCode(ctx context.Context) {
	has, err := a.Auth.HasAdmin(ctx)
	if err != nil || has {
		return
	}
	code := secrets.GeneratePassword(10).Reveal()
	code = strings.ToUpper(code[:5] + "-" + code[5:])
	a.installMu.Lock()
	a.installCode = code
	a.installMu.Unlock()
	banner := strings.Repeat("=", 64)
	fmt.Fprintf(os.Stderr, "\n%s\n  sqsolve: open the panel and use this installation code:\n\n      %s\n\n  (it only works until the first administrator is created)\n%s\n\n", banner, code, banner)
	a.Log.Info("installation code available in the container output")
}

// Shutdown stops background work gracefully.
func (a *App) Shutdown(timeout time.Duration) {
	a.Jobs.Shutdown(timeout)
	if a.cancel != nil {
		a.cancel()
	}
}

// RecordStart emits the start event and detects upgrades.
func (a *App) RecordStart(ctx context.Context) {
	var last string
	a.DB.GetSetting(ctx, "last_version", &last)
	if last != "" && last != Version {
		a.Events.Emit(ctx, events.Event{Type: events.SystemUpgraded, Message: fmt.Sprintf("sqsolve was upgraded from %s to %s", last, Version)})
		a.Audit.Record(ctx, audit.System, "system.upgrade", "system", "", audit.OK, map[string]any{"from": last, "to": Version})
	}
	a.DB.SetSetting(ctx, "last_version", Version, "system")
	a.Events.Emit(ctx, events.Event{Type: events.SystemStarted, Message: "sqsolve " + Version + " started"})
}

// HTTPServer builds the server around handler.
func (a *App) HTTPServer(handler http.Handler) *http.Server {
	srv := &http.Server{
		Addr:              a.Cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute, // uploads of backups
		WriteTimeout:      0,               // SSE and downloads stream
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(a.Log.Handler(), slog.LevelWarn),
	}
	if !a.Cfg.InsecureHTTP {
		srv.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				return a.Certificate()
			},
		}
	}
	return srv
}
