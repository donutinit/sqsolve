// SPDX-License-Identifier: AGPL-3.0-only

// Package core holds the domain services of sqsolve: libraries, credentials,
// backups, verification, retention and restores. Resolve databases are a
// black box (ADR 0007): only catalog queries, row counts and pg_dump or
// pg_restore touch them.
package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/pgconf"
	"github.com/donutinit/sqsolve/internal/scheduler"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

// Service is the domain service.
type Service struct {
	Cfg        *config.Config
	DB         *store.DB
	PG         *pg.Client
	Exec       *execx.Runner
	Box        *secrets.Box
	Jobs       *jobs.Manager
	Sched      *scheduler.Scheduler
	Events     *events.Recorder
	Audit      *audit.Log
	Log        *slog.Logger
	InstanceID string
	Now        func() time.Time

	activity activityCache
	hbaMu    sync.Mutex
	hbaLive  string // pg_hba.conf this process last got PostgreSQL to load; guarded by hbaMu
	// hbaErr is what HBAProblem returns. It is set with hbaMu held, so the
	// results land in the order the rewrites ran, and read under hbaErrMu
	// alone, so the dashboard does not wait for a rewrite in progress.
	hbaErrMu sync.Mutex
	hbaErr   error
	stableMu sync.Mutex
	stable   map[string]int
	warned   map[string]bool
}

// Settings are the runtime settings editable in the UI.
type Settings struct {
	AdvertisedHost      string         `json:"advertised_host"`
	DefaultPolicy       Policy         `json:"default_policy"`
	InitWindowMinutes   int            `json:"init_window_minutes"`
	TrashDays           int            `json:"trash_days"`
	PrevDays            int            `json:"prev_days"`
	MaintenanceDBAccess bool           `json:"maintenance_db_access"`
	RequireTLS          bool           `json:"require_tls"`
	StaleHours          int            `json:"stale_hours"`
	ImportMaxMB         int            `json:"import_max_mb"`
	ExperimentalKeys    bool           `json:"experimental_resolvedbkey"`
	ExperimentalExports bool           `json:"experimental_exports"`
	TOTPRequiredAdmins  bool           `json:"totp_required_admins"`
	InstanceName        string         `json:"instance_name"`
	Extra               map[string]any `json:"extra,omitempty"`
}

// DefaultSettings for a new installation.
func DefaultSettings(tz string) Settings {
	return Settings{
		DefaultPolicy: Policy{
			Enabled: true, Mode: ModeActivity, CheckMinutes: 15, Cron: ActivityCron(15), Timezone: tz,
			KeepLast: 10, KeepDaily: 14, KeepWeekly: 8, KeepMonthly: 12, MinVerified: 1,
		},
		InitWindowMinutes:   60,
		TrashDays:           30,
		PrevDays:            7,
		MaintenanceDBAccess: true,
		StaleHours:          48,
		ImportMaxMB:         20480,
		InstanceName:        "sqsolve",
	}
}

const settingsKey = "runtime"

// MaxRetentionDays caps the trash and previous-state periods: ten years, far
// below what overflows a time.Duration.
const MaxRetentionDays = 3650

// MaxStaleHours caps the hours without a backup before the warning (ten
// years) and MaxImportMB the maximum upload (1 TiB), far below what
// overflows the duration and the byte count computed from them.
const (
	MaxStaleHours = MaxRetentionDays * 24
	MaxImportMB   = 1 << 20
)

// Settings returns the runtime settings with defaults filled in.
func (s *Service) Settings(ctx context.Context) Settings {
	st, _ := s.storedSettings(ctx)
	return st
}

// storedSettings is Settings with the error of reading the stored settings,
// when it returns the factory defaults.
func (s *Service) storedSettings(ctx context.Context) (Settings, error) {
	def := DefaultSettings(s.Cfg.Location.String())
	cur := def
	ok, err := s.DB.GetSetting(ctx, settingsKey, &cur)
	if err != nil || !ok {
		return def, err
	}
	// A default policy stored without an expression was written by 1.2.3 or
	// earlier (the "Schedule (cron)" field saved empty in activity mode) and
	// has been read as the factory policy ever since. It still is: the
	// retention numbers stored with it were never in effect nor shown, and
	// every library that becomes active takes the default policy as its own.
	// SaveSettings no longer stores such a policy.
	dp := &cur.DefaultPolicy
	if dp.Cron == "" {
		*dp = def.DefaultPolicy
	}
	if dp.CheckMinutes == 0 {
		dp.CheckMinutes = def.DefaultPolicy.CheckMinutes
	}
	// In activity mode the expression is the check cadence, as SetPolicy
	// stores it for a library; one stored there by 1.2.1 to 1.2.3 never ran.
	if dp.Adaptive() {
		dp.Cron = dp.EffectiveCron()
	}
	if cur.InitWindowMinutes <= 0 {
		cur.InitWindowMinutes = def.InitWindowMinutes
	}
	if cur.TrashDays <= 0 {
		cur.TrashDays = def.TrashDays
	}
	if cur.PrevDays <= 0 {
		cur.PrevDays = def.PrevDays
	}
	// Values stored before SaveSettings capped them must not overflow the
	// durations computed from them.
	cur.TrashDays = min(cur.TrashDays, MaxRetentionDays)
	cur.PrevDays = min(cur.PrevDays, MaxRetentionDays)
	if cur.StaleHours <= 0 {
		cur.StaleHours = def.StaleHours
	}
	if cur.ImportMaxMB <= 0 {
		cur.ImportMaxMB = def.ImportMaxMB
	}
	cur.StaleHours = min(cur.StaleHours, MaxStaleHours)
	cur.ImportMaxMB = min(cur.ImportMaxMB, MaxImportMB)
	if cur.InstanceName == "" {
		cur.InstanceName = def.InstanceName
	}
	return cur, nil
}

// ValidateAdvertisedHost checks the address the workstations use to reach
// the server: an IP or a name, without a port, a scheme or a path.
func ValidateAdvertisedHost(host string) error {
	if strings.ContainsAny(host, " /\\:\"'<>") {
		return errors.New("SQS-C030 invalid advertised address: type only the IP or the name, without a port")
	}
	return nil
}

// SaveSettings validates and stores settings. When "TLS only" or the access
// to the maintenance database changed, or the last rewrite of pg_hba.conf did
// not take effect (HBAProblem), pg_hba.conf is rewritten afterwards under its
// own audit entry (syncHBAAfter): an error returned at that point is the
// rewrite's, and the settings stay stored. Saving the same settings again
// therefore retries a rewrite that failed.
func (s *Service) SaveSettings(ctx context.Context, by audit.Actor, st Settings) error {
	st.AdvertisedHost = strings.TrimSpace(st.AdvertisedHost)
	if err := ValidateAdvertisedHost(st.AdvertisedHost); err != nil {
		return err
	}
	if err := st.DefaultPolicy.Validate(); err != nil {
		return err
	}
	// As SetPolicy does for a library: in activity mode the stored
	// expression is the check cadence. An empty one would be read back as
	// "no default policy", and a typed one would sit there without effect.
	if st.DefaultPolicy.Adaptive() {
		st.DefaultPolicy.Cron = st.DefaultPolicy.EffectiveCron()
	}
	if st.InitWindowMinutes < 5 || st.InitWindowMinutes > 24*60 {
		return errors.New("SQS-C031 the initialization window must be between 5 and 1440 minutes")
	}
	if st.TrashDays < 1 || st.PrevDays < 1 || st.TrashDays > MaxRetentionDays || st.PrevDays > MaxRetentionDays {
		return fmt.Errorf("SQS-C032 retention periods must be between 1 and %d days", MaxRetentionDays)
	}
	if st.StaleHours < 1 || st.StaleHours > MaxStaleHours {
		return fmt.Errorf("SQS-C033 the hours without a backup before the warning must be between 1 and %d", MaxStaleHours)
	}
	if st.ImportMaxMB < 1 || st.ImportMaxMB > MaxImportMB {
		return fmt.Errorf("SQS-C034 the maximum upload must be between 1 and %d MB", MaxImportMB)
	}
	old := s.Settings(ctx)
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.DB.SetSettingTx(ctx, tx, settingsKey, st, by.Name); err != nil {
			return err
		}
		// Every setting as saved (none is a secret), so the entries show
		// what each save changed.
		return s.Audit.RecordTx(ctx, tx, by, "settings.update", "settings", settingsKey, audit.OK, map[string]any{
			"instance_name": st.InstanceName, "advertised_host": st.AdvertisedHost, "default_policy": st.DefaultPolicy,
			"init_window_minutes": st.InitWindowMinutes, "trash_days": st.TrashDays, "prev_days": st.PrevDays,
			"stale_hours": st.StaleHours, "import_max_mb": st.ImportMaxMB, "require_tls": st.RequireTLS,
			"maintenance_db_access": st.MaintenanceDBAccess, "totp_required_admins": st.TOTPRequiredAdmins,
			"experimental_exports": st.ExperimentalExports, "experimental_resolvedbkey": st.ExperimentalKeys,
		})
	})
	if err != nil {
		return err
	}
	if old.RequireTLS != st.RequireTLS || old.MaintenanceDBAccess != st.MaintenanceDBAccess || s.HBAProblem() != nil {
		return s.syncHBAAfter(ctx, by, "settings.update", "settings", settingsKey, map[string]any{
			"require_tls": st.RequireTLS, "maintenance_db_access": st.MaintenanceDBAccess,
		})
	}
	return nil
}

// SyncHBA regenerates pg_hba.conf from the catalog, validates it and reloads
// PostgreSQL. A failed validation restores the previous file, and its error
// says when that failed too. Without the stored settings nothing is written
// (SQS-P044). The file is only taken as applied once a reload in this process
// succeeded, so after a failed validation query or reload (or a restart) the
// next sync validates and reloads again even if the file on disk did not
// change (docs 08 §7).
// Its result is what HBAProblem reports until the next rewrite.
func (s *Service) SyncHBA(ctx context.Context) (err error) {
	s.hbaMu.Lock()
	defer s.hbaMu.Unlock()
	defer func() { s.setHBAProblem(err) }()
	// The factory defaults turn "TLS only" off and open the maintenance
	// database: without the stored settings nothing is rewritten.
	st, err := s.storedSettings(ctx)
	if err != nil {
		return fmt.Errorf("SQS-P044 the stored settings could not be read, so pg_hba.conf was not rewritten: %w", err)
	}
	libs, err := s.Libraries(ctx, ListOptions{})
	if err != nil {
		return err
	}
	var hl []pgconf.HBALibrary
	for _, l := range libs {
		switch l.State {
		case StateAwaitingInit, StateActive, StateMaintenance, StateRestoring:
		default:
			continue
		}
		creds, err := s.Credentials(ctx, l.ID)
		if err != nil {
			return err
		}
		var roles []string
		for _, c := range creds {
			if c.RevokedAt == nil {
				roles = append(roles, c.PGRole)
			}
		}
		rules, err := s.NetworkRules(ctx, l.ID)
		if err != nil {
			return err
		}
		var cidrs []string
		for _, r := range rules {
			cidrs = append(cidrs, r.CIDR)
		}
		hl = append(hl, pgconf.HBALibrary{DB: l.DBName, Roles: roles, CIDRs: cidrs})
	}
	content, err := pgconf.HBA(pgconf.HBAOptions{Libraries: hl, MaintenanceDB: st.MaintenanceDBAccess, RequireTLS: st.RequireTLS})
	if err != nil {
		return err
	}
	changed, err := pgconf.WriteHBA(s.Cfg.PGConfDir(), content)
	if err != nil {
		return fmt.Errorf("SQS-P041 could not write pg_hba.conf: %w", err)
	}
	if !changed && s.hbaLive == content {
		return nil
	}
	// Until a reload succeeds nobody knows which rules are in force.
	s.hbaLive = ""
	if errs, err := s.PG.HBAErrors(ctx); err == nil && len(errs) > 0 {
		err := fmt.Errorf("SQS-P042 generated pg_hba.conf is not valid: %s", strings.Join(errs, "; "))
		if rerr := pgconf.RestorePreviousHBA(s.Cfg.PGConfDir()); rerr != nil {
			return fmt.Errorf("%w. The previous pg_hba.conf could not be put back, so the rejected file stays on disk: %w", err, rerr)
		}
		return err
	} else if err != nil {
		return err
	}
	if err := s.PG.ReloadConf(ctx); err != nil {
		return fmt.Errorf("SQS-P043 could not reload PostgreSQL: %w", err)
	}
	s.hbaLive = content
	return nil
}

// syncHBAAfter rewrites pg_hba.conf for a change whose own audit entry is
// already closed (the settings, the addition of a member), under an entry of
// its own (ADR 0026, docs 08 §9): accepted first, and without it the file is
// not rewritten (SQS-X040); then ok, or failed with the error. cause is the
// action of that change, which is not undone when the rewrite fails.
func (s *Service) syncHBAAfter(ctx context.Context, by audit.Actor, cause, targetType, targetID string, details map[string]any) error {
	details["cause"] = cause
	ran := false
	err := s.Audit.Do(ctx, by, "postgres.hba", targetType, targetID, details, func() error {
		ran = true
		return s.SyncHBA(ctx)
	})
	if !ran && err != nil {
		// The change is stored and not in force, as after a failed rewrite.
		// Taken after any rewrite in progress, which may predate the change.
		s.hbaMu.Lock()
		s.setHBAProblem(err)
		s.hbaMu.Unlock()
	}
	return err
}

// HBAProblem returns why the pg_hba.conf in force may not match the catalog
// and the settings: the error of the last rewrite in this process when it
// failed, or when it was not run for want of its audit entry (SQS-X040). It
// is nil after a rewrite that took effect, and before the first one; every
// start rewrites the file (reconciliation, step 5).
func (s *Service) HBAProblem() error {
	s.hbaErrMu.Lock()
	defer s.hbaErrMu.Unlock()
	return s.hbaErr
}

func (s *Service) setHBAProblem(err error) {
	s.hbaErrMu.Lock()
	s.hbaErr = err
	s.hbaErrMu.Unlock()
}

// ReaderError returns the error a reader of the panel, the API, an event or
// a stored result gets for err (docs 08 §5). A failed connection to
// PostgreSQL names the socket path, the sqsolve_admin role and the database:
// the whole text goes to the process log, in a WARN line that starts with
// what and carries attrs, and only a reader with the diagnostics permission
// (full) gets it; everyone else gets the text up to the driver's part
// (pg.Public). Any other error comes back unchanged and is not logged. The
// result of a hidden error no longer wraps the sentinels: take an HTTP status
// from err.
func (s *Service) ReaderError(err error, what string, full bool, attrs ...any) error {
	text, hidden := pg.Public(err)
	if !hidden {
		return err
	}
	s.Log.Warn(what+": the connection to PostgreSQL failed", append(attrs, "err", err)...)
	if full {
		return err
	}
	return errors.New(text)
}

// AdvertisedHost returns the address workstations should use.
func (s *Service) AdvertisedHost(ctx context.Context) string {
	if h := s.Settings(ctx).AdvertisedHost; h != "" {
		return h
	}
	if s.Cfg.AdvertisedHost != "" {
		return s.Cfg.AdvertisedHost
	}
	return ""
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
