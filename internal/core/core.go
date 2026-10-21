// SPDX-License-Identifier: AGPL-3.0-only

// Package core holds the domain services of sqsolve: libraries, credentials,
// backups, verification, retention and restores. Resolve databases are a
// black box (ADR 0007): only catalog queries, row counts and pg_dump or
// pg_restore touch them.
package core

import (
	"context"
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

// Settings returns the runtime settings with defaults filled in.
func (s *Service) Settings(ctx context.Context) Settings {
	def := DefaultSettings(s.Cfg.Location.String())
	cur := def
	ok, err := s.DB.GetSetting(ctx, settingsKey, &cur)
	if err != nil || !ok {
		return def
	}
	if cur.DefaultPolicy.Cron == "" {
		cur.DefaultPolicy = def.DefaultPolicy
	}
	if cur.DefaultPolicy.Adaptive() && cur.DefaultPolicy.CheckMinutes == 0 {
		cur.DefaultPolicy.CheckMinutes = 15
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
	if cur.InstanceName == "" {
		cur.InstanceName = def.InstanceName
	}
	return cur
}

// SaveSettings validates and stores settings.
func (s *Service) SaveSettings(ctx context.Context, by audit.Actor, st Settings) error {
	st.AdvertisedHost = strings.TrimSpace(st.AdvertisedHost)
	if strings.ContainsAny(st.AdvertisedHost, " /\\:\"'<>") {
		return errors.New("SQS-C030 invalid advertised address: type only the IP or the name, without a port")
	}
	if err := st.DefaultPolicy.Validate(); err != nil {
		return err
	}
	if st.InitWindowMinutes < 5 || st.InitWindowMinutes > 24*60 {
		return errors.New("SQS-C031 the initialization window must be between 5 and 1440 minutes")
	}
	if st.TrashDays < 1 || st.PrevDays < 1 || st.TrashDays > MaxRetentionDays || st.PrevDays > MaxRetentionDays {
		return fmt.Errorf("SQS-C032 retention periods must be between 1 and %d days", MaxRetentionDays)
	}
	old := s.Settings(ctx)
	if err := s.DB.SetSetting(ctx, settingsKey, st, by.Name); err != nil {
		return err
	}
	s.Audit.Record(ctx, by, "settings.update", "settings", settingsKey, audit.OK, map[string]any{
		"advertised_host": st.AdvertisedHost, "require_tls": st.RequireTLS, "maintenance_db_access": st.MaintenanceDBAccess,
		"experimental_exports": st.ExperimentalExports,
	})
	if old.RequireTLS != st.RequireTLS || old.MaintenanceDBAccess != st.MaintenanceDBAccess {
		return s.SyncHBA(ctx)
	}
	return nil
}

// SyncHBA regenerates pg_hba.conf from the catalog, validates it and reloads
// PostgreSQL. A failed validation restores the previous file. The file is
// only taken as applied once a reload in this process succeeded, so after a
// failed validation query or reload (or a restart) the next sync validates
// and reloads again even if the file on disk did not change (docs 08 §7).
func (s *Service) SyncHBA(ctx context.Context) error {
	s.hbaMu.Lock()
	defer s.hbaMu.Unlock()
	st := s.Settings(ctx)
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
		pgconf.RestorePreviousHBA(s.Cfg.PGConfDir())
		return fmt.Errorf("SQS-P042 generated pg_hba.conf is not valid: %s", strings.Join(errs, "; "))
	} else if err != nil {
		return err
	}
	if err := s.PG.ReloadConf(ctx); err != nil {
		return fmt.Errorf("SQS-P043 could not reload PostgreSQL: %w", err)
	}
	s.hbaLive = content
	return nil
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
