// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/health"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// Check is one doctor result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`

	// For ChecksFor: the error whose text ends Detail, and the hint for a
	// reader without the diagnostics permission when Hint names a file.
	err        error
	publicHint string
}

// errCheck is a check whose detail ends with the text of err.
func errCheck(name, status, prefix string, err error, hint string) Check {
	return Check{Name: name, Status: status, Detail: prefix + err.Error(), Hint: hint, err: err}
}

// superuserCheck is the failed check of the superuser connection; its hint
// names the password file.
func superuserCheck(err error, file string) Check {
	c := errCheck("Superuser over the socket", "fail", "", err, "the password in "+file+" does not match the cluster")
	c.publicHint = "the superuser password file does not match the cluster"
	return c
}

// ChecksFor returns the checks of Doctor as the Health page shows them to a
// reader (docs 08 §5): the driver's text of a failed connection to
// PostgreSQL goes to the process log, and only a reader with the diagnostics
// permission (full) gets it and the path of the superuser password file.
// where is the request path. sqsolve doctor and the diagnostic bundle are
// for administrators and show the checks whole.
func (a *App) ChecksFor(checks []Check, where string, full bool) []Check {
	out := append([]Check(nil), checks...)
	for i, c := range out {
		if c.err != nil {
			shown := a.Svc.ReaderError(c.err, "doctor", full, "path", where, "check", c.Name)
			out[i].Detail = strings.TrimSuffix(c.Detail, c.err.Error()) + shown.Error()
		}
		if !full && c.publicHint != "" {
			out[i].Hint = c.publicHint
		}
	}
	return out
}

// Tone for the UI.
func (c Check) Tone() string {
	switch c.Status {
	case "ok":
		return "ok"
	case "warn":
		return "warn"
	}
	return "bad"
}

// clusterCheck compares the PostgreSQL cluster with the one the control
// plane knows (ADR 0023). It works in the CLI too, where the server's guard
// state is not available.
func (a *App) clusterCheck(ctx context.Context) Check {
	const name = "PostgreSQL cluster"
	if p := a.PGIdentityProblem(); p != "" {
		return Check{Name: name, Status: "fail", Detail: p}
	}
	current, err := a.systemIdentifier(ctx)
	if err != nil {
		return errCheck(name, "warn", "could not read the system identifier: ", err, "")
	}
	var stored string
	if _, err := a.DB.GetSetting(ctx, pgSystemIDSetting, &stored); err != nil {
		return Check{Name: name, Status: "warn", Detail: "system identifier " + current + "; the stored one cannot be read: " + err.Error()}
	}
	switch decideIdentity(stored, current, a.Cfg.PGAcceptSystemID) {
	case identityStore:
		return Check{Name: name, Status: "ok", Detail: "system identifier " + current + " (stored at the next start)"}
	case identityAccept:
		return Check{Name: name, Status: "warn", Detail: fmt.Sprintf("system identifier %s differs from %s and SQSOLVE_PG_ACCEPT_SYSTEM_ID accepts it at the next start", current, stored)}
	case identityHold:
		return Check{Name: name, Status: "fail", Detail: clusterChangedMessage(current, stored)}
	}
	return Check{Name: name, Status: "ok", Detail: "system identifier " + current}
}

// Doctor runs the checks of docs 13 §7.
func Doctor(ctx context.Context, a *App) []Check {
	var out []Check
	add := func(name, status, detail, hint string) {
		out = append(out, Check{Name: name, Status: status, Detail: detail, Hint: hint})
	}
	c := a.Cfg

	for _, v := range []struct{ name, path string }{{"Application volume", c.AppDir}, {"Backups volume", c.BackupDir}, {"Database volume", c.PGConfDir()}} {
		probe := filepath.Join(v.path, ".sqsolve-probe")
		if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
			add(v.name, "fail", "cannot write to "+v.path+": "+err.Error(), "check that the volume is mounted and that SQSOLVE_UID matches user: in the compose file")
		} else {
			os.Remove(probe)
			sp, _ := fsx.Statfs(v.path)
			status := "ok"
			if sp.UsedPercent() >= 90 {
				status = "fail"
			} else if sp.UsedPercent() >= 80 {
				status = "warn"
			}
			add(v.name, status, fmt.Sprintf("%s free of %s (%.0f %% used)", core.HumanBytes(int64(sp.Free)), core.HumanBytes(int64(sp.Total)), sp.UsedPercent()), "")
		}
	}
	if err := a.Svc.BackupVolumeOK(); err != nil {
		add("Backups volume marker", "fail", err.Error(), "mount the right volume or run sqsolve init again")
	} else {
		add("Backups volume marker", "ok", "belongs to this installation ("+a.InstanceID+")", "")
	}
	// The same codes as init: SQS-S011 (db) and SQS-S010 (app) for another
	// installation, SQS-S019 for swapped mounts.
	for _, v := range []struct{ name, dir, volume, foreign string }{
		{"Database volume marker", c.PGDir, fsx.VolumeDB, msgDBForeign},
		{"Application volume marker", c.AppDir, fsx.VolumeApp, msgAppForeign},
	} {
		m, err := fsx.ReadMarker(v.dir)
		switch {
		case errors.Is(err, fsx.ErrNoMarker):
			add(v.name, "warn", "the volume has no marker yet", "run sqsolve init (the sqsolve-init service) so it marks the volume")
		case err != nil:
			add(v.name, "fail", err.Error(), "mount the right volume (docs/adr/0023-volume-identity.md)")
		case m.InstanceID != a.InstanceID:
			add(v.name, "fail", fmt.Sprintf(v.foreign, v.dir, m.Volume, m.InstanceID, a.InstanceID), "mount the right volume")
		case m.Volume != v.volume:
			add(v.name, "fail", fsx.SwappedError(v.dir, v.volume, m).Error(), "")
		default:
			add(v.name, "ok", "belongs to this installation ("+a.InstanceID+")", "")
		}
	}
	// The rule startup applies (secrets.ReadFile), so both agree.
	for _, f := range []string{masterKeyPath(c), adminPassPath(c)} {
		_, err := secrets.ReadFile(f)
		switch {
		case errors.Is(err, secrets.ErrPermissions):
			add("Secret "+filepath.Base(f), "fail", err.Error(), "they must be 0400")
		case err != nil:
			add("Secret "+filepath.Base(f), "fail", err.Error(), "run sqsolve init")
		default:
			add("Secret "+filepath.Base(f), "ok", "present and protected", "")
		}
	}
	if err := a.PG.Ping(ctx); err != nil {
		out = append(out, errCheck("PostgreSQL", "fail", "", err, "check the postgres container and the shared socket volume"))
	} else {
		info, _ := a.PG.Server(ctx)
		detail := fmt.Sprintf("version %s, %d of %d connections", info.Version, info.Connections, info.MaxConnections)
		status := "ok"
		if !info.DataChecksums {
			detail += "; no page checksums"
			status = "warn"
		}
		add("PostgreSQL", status, detail, "")
		dump := a.Monitor.PGDumpVersion(ctx)
		switch sev, msg := health.CompareVersions(info.VersionNum, dump); {
		case dump == "":
			add("PostgreSQL version", "warn", "the bundled pg_dump did not report its version", "check SQSOLVE_PG_BIN_DIR")
		case sev == events.Critical:
			add("PostgreSQL version", "fail", msg, "")
		case sev == events.Warning:
			add("PostgreSQL version", "warn", msg, "")
		default:
			add("PostgreSQL version", "ok", fmt.Sprintf("server %s, %s", info.Version, strings.TrimSpace(dump)), "")
		}
		out = append(out, a.clusterCheck(ctx))
		if conn, err := a.PG.Superuser(ctx, "postgres"); err != nil {
			out = append(out, superuserCheck(err, c.SuperuserPasswordFile()))
		} else {
			conn.Close(ctx)
			add("Superuser over the socket", "ok", "reachable only through the local socket", "")
		}
		if errs, err := a.PG.HBAErrors(ctx); err != nil {
			out = append(out, errCheck("pg_hba.conf", "warn", "could not validate: ", err, ""))
		} else if len(errs) > 0 {
			add("pg_hba.conf", "fail", strings.Join(errs, "; "), "use Maintenance > Regenerate pg_hba.conf")
		} else if herr := a.Svc.HBAProblem(); herr != nil {
			out = append(out, errCheck("pg_hba.conf", "fail", "valid, but the last rewrite did not take effect: ", herr, "save Settings, General again or use Maintenance > Regenerate pg_hba.conf"))
		} else {
			add("pg_hba.conf", "ok", "valid", "")
		}
		// Catalog against the server.
		libs, _ := a.Svc.Libraries(ctx, core.ListOptions{})
		dbs, _ := a.PG.Databases(ctx)
		known := map[string]bool{"postgres": true}
		for _, l := range libs {
			known[l.DBName] = true
			if l.TrashDB != "" {
				known[l.TrashDB] = true
			}
			if l.PrevDB != "" {
				known[l.PrevDB] = true
			}
		}
		var orphans []string
		for _, d := range dbs {
			if !known[d.Name] && !pg.IsAuxiliary(d.Name) {
				orphans = append(orphans, d.Name)
			}
		}
		if len(orphans) > 0 {
			add("Databases outside the catalog", "warn", strings.Join(orphans, ", "), "they may be libraries created by hand; they are not backed up")
		} else {
			add("Databases outside the catalog", "ok", "none", "")
		}
	}
	if broken, n, err := a.Audit.Verify(ctx); err != nil {
		add("Audit log", "warn", err.Error(), "")
	} else if broken != 0 {
		add("Audit log", "fail", fmt.Sprintf("chain broken at entry %d", broken), "someone modified the sqsolve database")
	} else {
		add("Audit log", "ok", fmt.Sprintf("%d intact entries", n), "")
	}
	if err := a.DB.IntegrityCheck(ctx); err != nil {
		add("SQLite", "fail", err.Error(), "restore the most recent _system copy")
	} else {
		add("SQLite", "ok", "integrity OK", "")
	}
	return out
}
