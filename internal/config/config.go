// SPDX-License-Identifier: AGPL-3.0-only

// Package config reads the SQSOLVE_* environment variables that configure
// startup. Everything else lives in the settings table and is edited in the UI.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the startup configuration.
type Config struct {
	Listen         string
	HTTPRedirect   string
	AdvertisedHost string
	InsecureHTTP   bool

	AppDir    string // app volume: SQLite, secrets, panel TLS
	BackupDir string // backups volume
	PGDir     string // db volume root (/var/lib/postgresql)
	PGDataDir string // PGDATA inside the db volume
	PGSocket  string
	PGPort    int
	PGListen  string
	PGBinDir  string

	PGMemoryMB       int
	PGMaxConnections int

	UID, GID     int
	PGUID, PGGID int

	LogLevel  string
	LogFormat string

	TrustedProxies []netip.Prefix
	Workers        int
	HeavyJobs      int
	Location       *time.Location
	FixPerms       bool

	// Volume identity (ADR 0023). Each adopt variable lets init accept a
	// volume it would otherwise refuse or leave unmarked; remove it once
	// the volume carries its marker.
	BackupsAdopt bool
	DBAdopt      bool
	AppAdopt     bool
	// PGAcceptSystemID accepts a PostgreSQL cluster whose system identifier
	// differs from the one stored in the control plane. It only matches
	// that exact cluster, so leaving it set accepts nothing else.
	PGAcceptSystemID string
}

// PGConfDir is the directory inside the db volume that sqsolve manages.
func (c *Config) PGConfDir() string { return filepath.Join(c.PGDir, "sqsolve") }

// SecretsDir is where sqsolve keeps its own secrets.
func (c *Config) SecretsDir() string { return filepath.Join(c.AppDir, "secrets") }

// DBPath is the SQLite database of the control plane.
func (c *Config) DBPath() string { return filepath.Join(c.AppDir, "sqsolve.db") }

// SuperuserPasswordFile lives next to the PostgreSQL data because the
// postgres container needs it at first start and it protects that data.
func (c *Config) SuperuserPasswordFile() string {
	return filepath.Join(c.PGConfDir(), "superuser_password")
}

// Error describes an invalid variable.
type Error struct {
	Var string
	Msg string
}

func (e *Error) Error() string { return fmt.Sprintf("SQS-C001 %s: %s", e.Var, e.Msg) }

// Load reads the environment.
func Load() (*Config, error) { return load(os.Getenv) }

func load(get func(string) string) (*Config, error) {
	c := &Config{}
	var errs []error
	str := func(name, def string) string {
		if v := strings.TrimSpace(get(name)); v != "" {
			return v
		}
		return def
	}
	num := func(name string, def, min, max int) int {
		v := strings.TrimSpace(get(name))
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < min || n > max {
			errs = append(errs, &Error{name, fmt.Sprintf("must be an integer between %d and %d", min, max)})
			return def
		}
		return n
	}
	boolean := func(name string) bool {
		switch strings.ToLower(strings.TrimSpace(get(name))) {
		case "1", "true", "yes", "on":
			return true
		}
		return false
	}

	c.Listen = str("SQSOLVE_LISTEN", ":9432")
	c.HTTPRedirect = str("SQSOLVE_HTTP_REDIRECT", "")
	c.AdvertisedHost = str("SQSOLVE_ADVERTISED_HOST", "")
	c.InsecureHTTP = boolean("SQSOLVE_INSECURE_HTTP")
	c.AppDir = str("SQSOLVE_APP_DIR", "/var/lib/sqsolve")
	c.BackupDir = str("SQSOLVE_BACKUP_DIR", "/backups")
	c.PGDir = str("SQSOLVE_PG_DIR", "/var/lib/postgresql")
	c.PGDataDir = str("SQSOLVE_PG_DATA_DIR", filepath.Join(c.PGDir, "18", "docker"))
	c.PGSocket = str("SQSOLVE_PG_SOCKET_DIR", "/var/run/postgresql")
	c.PGPort = num("SQSOLVE_PG_PORT", 5432, 1, 65535)
	c.PGListen = str("SQSOLVE_PG_LISTEN", "*")
	c.PGBinDir = str("SQSOLVE_PG_BIN_DIR", "")
	c.PGMemoryMB = num("SQSOLVE_PG_MEMORY_MB", 2048, 256, 1<<20)
	c.PGMaxConnections = num("SQSOLVE_PG_MAX_CONNECTIONS", 200, 20, 5000)
	c.UID = num("SQSOLVE_UID", 1000, 0, 1<<31-1)
	c.GID = num("SQSOLVE_GID", 1000, 0, 1<<31-1)
	c.PGUID = num("SQSOLVE_PG_UID", 999, 0, 1<<31-1)
	c.PGGID = num("SQSOLVE_PG_GID", 999, 0, 1<<31-1)
	c.LogLevel = str("SQSOLVE_LOG_LEVEL", "info")
	c.LogFormat = str("SQSOLVE_LOG_FORMAT", "json")
	c.Workers = num("SQSOLVE_WORKERS", 2, 1, 16)
	c.HeavyJobs = num("SQSOLVE_HEAVY_JOBS", 1, 1, 8)
	c.FixPerms = boolean("SQSOLVE_FIX_PERMS")
	c.BackupsAdopt = boolean("SQSOLVE_BACKUPS_ADOPT")
	c.DBAdopt = boolean("SQSOLVE_DB_ADOPT")
	c.AppAdopt = boolean("SQSOLVE_APP_ADOPT")
	if v := strings.TrimSpace(get("SQSOLVE_PG_ACCEPT_SYSTEM_ID")); v != "" {
		if _, err := strconv.ParseUint(v, 10, 64); err != nil {
			errs = append(errs, &Error{"SQSOLVE_PG_ACCEPT_SYSTEM_ID", "must be the numeric system identifier shown in the event postgres.cluster_changed"})
		} else {
			c.PGAcceptSystemID = v
		}
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, &Error{"SQSOLVE_LOG_LEVEL", "must be debug, info, warn or error"})
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		errs = append(errs, &Error{"SQSOLVE_LOG_FORMAT", "must be json or text"})
	}
	for _, p := range []struct{ name, val string }{
		{"SQSOLVE_APP_DIR", c.AppDir}, {"SQSOLVE_BACKUP_DIR", c.BackupDir},
		{"SQSOLVE_PG_DIR", c.PGDir}, {"SQSOLVE_PG_SOCKET_DIR", c.PGSocket},
	} {
		if !filepath.IsAbs(p.val) {
			errs = append(errs, &Error{p.name, "must be an absolute path"})
		}
	}
	if v := strings.TrimSpace(get("SQSOLVE_TRUSTED_PROXIES")); v != "" {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			p, err := netip.ParsePrefix(part)
			if err != nil {
				if a, err2 := netip.ParseAddr(part); err2 == nil {
					p = netip.PrefixFrom(a, a.BitLen())
				} else {
					errs = append(errs, &Error{"SQSOLVE_TRUSTED_PROXIES", "invalid CIDR: " + part})
					continue
				}
			}
			c.TrustedProxies = append(c.TrustedProxies, p.Masked())
		}
	}
	tz := str("TZ", "UTC")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		errs = append(errs, &Error{"TZ", "unknown time zone: " + tz})
		loc = time.UTC
	}
	c.Location = loc

	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return c, fmt.Errorf("invalid configuration:\n  %s", strings.Join(msgs, "\n  "))
	}
	return c, nil
}
