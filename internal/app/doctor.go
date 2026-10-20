// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/pg"
)

// Check is one doctor result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
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

// Doctor runs the checks of docs 13 §7.
func Doctor(ctx context.Context, a *App) []Check {
	var out []Check
	add := func(name, status, detail, hint string) { out = append(out, Check{name, status, detail, hint}) }
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
	for _, f := range []string{masterKeyPath(c), adminPassPath(c)} {
		st, err := os.Stat(f)
		switch {
		case err != nil:
			add("Secret "+filepath.Base(f), "fail", err.Error(), "run sqsolve init")
		case st.Mode().Perm()&0o077 != 0:
			add("Secret "+filepath.Base(f), "fail", fmt.Sprintf("permissions %o", st.Mode().Perm()), "they must be 0400")
		default:
			add("Secret "+filepath.Base(f), "ok", "present and protected", "")
		}
	}
	if err := a.PG.Ping(ctx); err != nil {
		add("PostgreSQL", "fail", err.Error(), "check the postgres container and the shared socket volume")
	} else {
		info, _ := a.PG.Server(ctx)
		detail := fmt.Sprintf("version %s, %d of %d connections", info.Version, info.Connections, info.MaxConnections)
		status := "ok"
		if !info.DataChecksums {
			detail += "; no page checksums"
			status = "warn"
		}
		add("PostgreSQL", status, detail, "")
		if conn, err := a.PG.Superuser(ctx, "postgres"); err != nil {
			add("Superuser over the socket", "fail", err.Error(), "the password in "+c.SuperuserPasswordFile()+" does not match the cluster")
		} else {
			conn.Close(ctx)
			add("Superuser over the socket", "ok", "reachable only through the local socket", "")
		}
		if errs, err := a.PG.HBAErrors(ctx); err != nil {
			add("pg_hba.conf", "warn", "could not validate: "+err.Error(), "")
		} else if len(errs) > 0 {
			add("pg_hba.conf", "fail", strings.Join(errs, "; "), "use Maintenance > Regenerate pg_hba.conf")
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
