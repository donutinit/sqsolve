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
	Commit  = "desconocido"
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

	pgReady     atomic.Bool
	installMu   sync.Mutex
	installCode string
	cert        atomic.Pointer[tls.Certificate]
	cancel      context.CancelFunc
}

// ReadMasterKey loads the master key file.
func ReadMasterKey(c *config.Config) ([]byte, error) {
	s, err := secrets.ReadFile(masterKeyPath(c))
	if err != nil {
		return nil, fmt.Errorf("SQS-C010 no se pudo leer la clave maestra (¿corriste sqsolve init?): %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s.Reveal()))
	if err != nil {
		return nil, fmt.Errorf("SQS-C011 la clave maestra no es base64 válido: %w", err)
	}
	return key, nil
}

// Open builds the application without starting background work.
func Open(ctx context.Context, c *config.Config, log *slog.Logger) (*App, error) {
	a := &App{Cfg: c, Log: log, Started: time.Now()}
	idb, err := os.ReadFile(instanceIDPath(c))
	if err != nil {
		return nil, fmt.Errorf("SQS-C052 falta %s: corre sqsolve init (el servicio sqsolve-init del compose): %w", instanceIDPath(c), err)
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
		return nil, fmt.Errorf("SQS-C013 contraseña de sqsolve_admin: %w", err)
	}
	superPass, err := secrets.ReadFile(c.SuperuserPasswordFile())
	if err != nil {
		return nil, fmt.Errorf("SQS-C014 contraseña del superusuario: %w", err)
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
	a.Monitor = &health.Monitor{Svc: a.Svc, Jobs: a.Jobs, Events: a.Events, PanelTLS: panelCertPath(c)}
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
	return nil, errors.New("sin certificado")
}

// LoadPanelCert loads (or creates) the panel certificate.
func (a *App) LoadPanelCert() error {
	crt, key := panelCertPath(a.Cfg), panelKeyPath(a.Cfg)
	if _, err := os.Stat(crt); errors.Is(err, os.ErrNotExist) {
		host, _ := os.Hostname()
		hosts := []string{"localhost", "127.0.0.1", host}
		if h := a.Svc.AdvertisedHost(context.Background()); h != "" {
			hosts = append(hosts, h)
		}
		cp, kp, err := pgconf.SelfSigned("sqsolve", hosts, 2*365*24*time.Hour)
		if err != nil {
			return err
		}
		os.MkdirAll(filepath.Dir(crt), 0o700)
		if err := fsx.WriteFileAtomic(key, kp, 0o600); err != nil {
			return err
		}
		if err := fsx.WriteFileAtomic(crt, cp, 0o644); err != nil {
			return err
		}
	}
	pair, err := tls.LoadX509KeyPair(crt, key)
	if err != nil {
		return fmt.Errorf("SQS-C020 certificado del panel: %w", err)
	}
	a.cert.Store(&pair)
	return nil
}

// InstallPanelCert replaces the panel certificate with one provided by the
// administrator.
func (a *App) InstallPanelCert(certPEM, keyPEM []byte) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("SQS-C021 el certificado y la llave no corresponden: %w", err)
	}
	if err := fsx.WriteFileAtomic(panelKeyPath(a.Cfg), keyPEM, 0o600); err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(panelCertPath(a.Cfg), certPEM, 0o644); err != nil {
		return err
	}
	a.cert.Store(&pair)
	return nil
}

// PanelCertInfo describes the panel certificate.
func (a *App) PanelCertInfo() (pgconf.CertInfo, error) { return pgconf.ReadCert(panelCertPath(a.Cfg)) }

// Background starts PostgreSQL connection, reconciliation and loops.
func (a *App) Background(ctx context.Context) {
	ctx, a.cancel = context.WithCancel(ctx)
	go a.Notify.Run(ctx)
	go func() {
		delay := time.Second
		for {
			err := a.connectPG(ctx)
			if err == nil {
				break
			}
			a.Log.Warn("esperando a PostgreSQL", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay < 15*time.Second {
				delay *= 2
			}
		}
		interrupted, err := a.Jobs.MarkInterrupted(ctx)
		if err != nil {
			a.Log.Error("marcando tareas interrumpidas", "err", err)
		}
		for _, n := range a.Svc.Reconcile(ctx, interrupted) {
			a.Log.Info("reconciliación", "nota", n)
		}
		if err := a.Svc.EnsureSystemSchedules(ctx); err != nil {
			a.Log.Error("programaciones del sistema", "err", err)
		}
		a.pgReady.Store(true)
		a.Jobs.Start(ctx)
		go a.Sched.Run(ctx)
		go a.Monitor.Run(ctx)
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
		a.Log.Info("sqsolve listo", "version", Version)
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
	fmt.Fprintf(os.Stderr, "\n%s\n  sqsolve: abre el panel y usa este código de instalación:\n\n      %s\n\n  (solo sirve hasta crear el primer administrador)\n%s\n\n", banner, code, banner)
	a.Log.Info("código de instalación disponible en la salida del contenedor")
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
		a.Events.Emit(ctx, events.Event{Type: events.SystemUpgraded, Message: fmt.Sprintf("sqsolve se actualizó de %s a %s", last, Version)})
		a.Audit.Record(ctx, audit.System, "system.upgrade", "system", "", audit.OK, map[string]any{"from": last, "to": Version})
	}
	a.DB.SetSetting(ctx, "last_version", Version, "sistema")
	a.Events.Emit(ctx, events.Event{Type: events.SystemStarted, Message: "sqsolve " + Version + " arrancó"})
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
