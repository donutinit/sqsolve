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

	for _, v := range []struct{ name, path string }{{"Volumen de la aplicación", c.AppDir}, {"Volumen de respaldos", c.BackupDir}, {"Volumen de la base de datos", c.PGConfDir()}} {
		probe := filepath.Join(v.path, ".sqsolve-probe")
		if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
			add(v.name, "fail", "no se puede escribir en "+v.path+": "+err.Error(), "revisa que el volumen esté montado y que SQSOLVE_UID coincida con user: del compose")
		} else {
			os.Remove(probe)
			sp, _ := fsx.Statfs(v.path)
			status := "ok"
			if sp.UsedPercent() >= 90 {
				status = "fail"
			} else if sp.UsedPercent() >= 80 {
				status = "warn"
			}
			add(v.name, status, fmt.Sprintf("%s libres de %s (%.0f %% usado)", core.HumanBytes(int64(sp.Free)), core.HumanBytes(int64(sp.Total)), sp.UsedPercent()), "")
		}
	}
	if err := a.Svc.BackupVolumeOK(); err != nil {
		add("Marcador del volumen de respaldos", "fail", err.Error(), "monta el volumen correcto o vuelve a correr sqsolve init")
	} else {
		add("Marcador del volumen de respaldos", "ok", "pertenece a esta instalación ("+a.InstanceID+")", "")
	}
	for _, f := range []string{masterKeyPath(c), adminPassPath(c)} {
		st, err := os.Stat(f)
		switch {
		case err != nil:
			add("Secreto "+filepath.Base(f), "fail", err.Error(), "corre sqsolve init")
		case st.Mode().Perm()&0o077 != 0:
			add("Secreto "+filepath.Base(f), "fail", fmt.Sprintf("permisos %o", st.Mode().Perm()), "deben ser 0400")
		default:
			add("Secreto "+filepath.Base(f), "ok", "presente y protegido", "")
		}
	}
	if err := a.PG.Ping(ctx); err != nil {
		add("PostgreSQL", "fail", err.Error(), "revisa el contenedor postgres y el volumen compartido del socket")
	} else {
		info, _ := a.PG.Server(ctx)
		detail := fmt.Sprintf("versión %s, %d de %d conexiones", info.Version, info.Connections, info.MaxConnections)
		status := "ok"
		if !info.DataChecksums {
			detail += "; sin sumas de verificación de páginas"
			status = "warn"
		}
		add("PostgreSQL", status, detail, "")
		if conn, err := a.PG.Superuser(ctx, "postgres"); err != nil {
			add("Superusuario por socket", "fail", err.Error(), "la contraseña en "+c.SuperuserPasswordFile()+" no coincide con la del clúster")
		} else {
			conn.Close(ctx)
			add("Superusuario por socket", "ok", "accesible solo por el socket local", "")
		}
		if errs, err := a.PG.HBAErrors(ctx); err != nil {
			add("pg_hba.conf", "warn", "no se pudo validar: "+err.Error(), "")
		} else if len(errs) > 0 {
			add("pg_hba.conf", "fail", strings.Join(errs, "; "), "usa Mantenimiento > Regenerar pg_hba.conf")
		} else {
			add("pg_hba.conf", "ok", "válido", "")
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
			add("Bases fuera del catálogo", "warn", strings.Join(orphans, ", "), "pueden ser bibliotecas creadas a mano; no se respaldan")
		} else {
			add("Bases fuera del catálogo", "ok", "ninguna", "")
		}
	}
	if broken, n, err := a.Audit.Verify(ctx); err != nil {
		add("Auditoría", "warn", err.Error(), "")
	} else if broken != 0 {
		add("Auditoría", "fail", fmt.Sprintf("cadena rota en la entrada %d", broken), "alguien modificó la base de sqsolve")
	} else {
		add("Auditoría", "ok", fmt.Sprintf("%d entradas íntegras", n), "")
	}
	if err := a.DB.IntegrityCheck(ctx); err != nil {
		add("SQLite", "fail", err.Error(), "restaura la copia de _system más reciente")
	} else {
		add("SQLite", "ok", "integridad correcta", "")
	}
	return out
}
