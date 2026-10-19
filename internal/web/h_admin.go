// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/notify"
)

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st := s.A.Monitor.Snapshot(ctx)
	libs, _ := s.A.Svc.Libraries(ctx, core.ListOptions{States: []core.State{core.StateActive, core.StateMaintenance, core.StateAwaitingInit, core.StateInitExpired, core.StateRestoring, core.StateMissing}})
	var tracks []Track
	stations := map[string]bool{}
	for _, l := range libs {
		tracks = append(tracks, s.track(ctx, l, 14))
		if l.Live() {
			if ss, err := s.A.PG.Sessions(ctx, l.DBName); err == nil {
				for _, x := range ss {
					stations[x.ClientAddr] = true
				}
			}
		}
	}
	recent, _ := s.A.Jobs.List(ctx, jobs.Filter{Limit: 6})
	evs, _ := s.A.Events.List(ctx, events.Filter{MinSeverity: events.Warning, Limit: 6})
	s.render(w, r, "dashboard", "Panel", "panel", map[string]any{"S": st, "Tracks": tracks, "Stations": len(stations), "Jobs": recent, "Events": evs, "Host": s.A.Svc.AdvertisedHost(ctx)})
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalOf(r)
	tab := r.URL.Query().Get("tab")
	data := map[string]any{"Tab": tab}
	if tab == "auditoria" {
		if !p.Can(auth.ActAuditView) {
			s.renderError(w, r, p, http.StatusForbidden, "SQS-A030 Tu rol no permite ver la auditoría.")
			return
		}
		data["Audit"], _ = s.A.Audit.List(ctx, 0, 300, "")
		broken, n, err := s.A.Audit.Verify(ctx)
		data["Broken"], data["Count"], data["VerifyErr"] = broken, n, err
	} else {
		sev := events.Severity(r.URL.Query().Get("sev"))
		data["Sev"] = string(sev)
		data["Events"], _ = s.A.Events.List(ctx, events.Filter{MinSeverity: sev, TypePrefix: r.URL.Query().Get("tipo"), Limit: 300})
	}
	s.render(w, r, "activity", "Actividad", "actividad", data)
}

func (s *Server) auditExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="sqsolve-auditoria-`+time.Now().Format("20060102")+`.jsonl"`)
	s.A.Audit.Export(r.Context(), w, time.Time{}, time.Now().Add(time.Hour))
}

func (s *Server) healthPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st := s.A.Monitor.Snapshot(ctx)
	scheds, _ := s.A.Sched.List(ctx)
	s.render(w, r, "health", "Salud", "salud", map[string]any{"S": st, "Schedules": scheds, "Checks": app.Doctor(ctx, s.A)})
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalOf(r)
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "general"
	}
	data := map[string]any{"Tab": tab, "Settings": s.A.Svc.Settings(ctx), "Cfg": s.A.Cfg, "InstanceID": s.A.InstanceID}
	switch tab {
	case "usuarios":
		if !p.Can(auth.ActUsersManage) {
			s.renderError(w, r, p, http.StatusForbidden, "SQS-A030 Tu rol no permite administrar usuarios.")
			return
		}
		data["Users"], _ = s.A.Auth.Users(ctx)
		data["Roles"] = []auth.Role{auth.RoleAdmin, auth.RoleOperator, auth.RoleViewer}
	case "notificaciones":
		chs, _ := s.A.Notify.Channels(ctx)
		data["Channels"] = chs
		data["Edit"] = r.URL.Query().Get("editar")
		data["NewKind"] = r.URL.Query().Get("nuevo")
	case "mantenimiento":
		data["Copies"] = app.SystemCopies(s.A.Cfg)
		data["Imports"], _ = s.A.Svc.ImportCandidates()
	case "acerca":
		data["Version"], data["Commit"] = app.Version, app.Commit
		if info, err := s.A.PG.Server(ctx); err == nil {
			data["PG"] = info
		}
	}
	if info, err := s.A.PanelCertInfo(); err == nil {
		data["Cert"] = info
	}
	s.render(w, r, "settings", "Ajustes", "ajustes", data)
}

func (s *Server) settingsGeneral(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st := s.A.Svc.Settings(ctx)
	st.InstanceName = strings.TrimSpace(r.FormValue("name"))
	st.AdvertisedHost = strings.TrimSpace(r.FormValue("host"))
	st.InitWindowMinutes = atoi(r.FormValue("init_window"), st.InitWindowMinutes)
	st.TrashDays = atoi(r.FormValue("trash_days"), st.TrashDays)
	st.PrevDays = atoi(r.FormValue("prev_days"), st.PrevDays)
	st.StaleHours = atoi(r.FormValue("stale_hours"), st.StaleHours)
	st.ImportMaxMB = atoi(r.FormValue("import_max_mb"), st.ImportMaxMB)
	st.MaintenanceDBAccess = r.FormValue("maintenance_db") == "on"
	st.RequireTLS = r.FormValue("require_tls") == "on"
	st.TOTPRequiredAdmins = r.FormValue("totp_required") == "on"
	st.ExperimentalExports = r.FormValue("experimental_exports") == "on"
	st.DefaultPolicy.Cron = strings.TrimSpace(r.FormValue("default_cron"))
	st.DefaultPolicy.KeepLast = atoi(r.FormValue("keep_last"), st.DefaultPolicy.KeepLast)
	st.DefaultPolicy.KeepDaily = atoi(r.FormValue("keep_daily"), st.DefaultPolicy.KeepDaily)
	st.DefaultPolicy.KeepWeekly = atoi(r.FormValue("keep_weekly"), st.DefaultPolicy.KeepWeekly)
	st.DefaultPolicy.KeepMonthly = atoi(r.FormValue("keep_monthly"), st.DefaultPolicy.KeepMonthly)
	if err := s.A.Svc.SaveSettings(ctx, principalOf(r).Actor(), st); err != nil {
		s.fail(w, r, "/ajustes", err)
		return
	}
	s.ok(w, r, "/ajustes", "Ajustes guardados.")
}

func readUpload(r *http.Request, field string) ([]byte, error) {
	f, _, err := r.FormFile(field)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 256<<10))
}

func (s *Server) settingsCert(w http.ResponseWriter, r *http.Request) {
	cert, err1 := readUpload(r, "cert")
	key, err2 := readUpload(r, "key")
	if err1 != nil || err2 != nil {
		s.fail(w, r, "/ajustes", errors.New("SQS-C021 sube el certificado y la llave en formato PEM"))
		return
	}
	if err := s.A.InstallPanelCert(cert, key); err != nil {
		s.fail(w, r, "/ajustes", err)
		return
	}
	s.A.Audit.Record(r.Context(), principalOf(r).Actor(), "settings.certificate", "system", "", "ok", nil)
	s.ok(w, r, "/ajustes", "Certificado instalado. Ya se está usando.")
}

func (s *Server) userCreate(w http.ResponseWriter, r *http.Request) {
	back := "/ajustes?tab=usuarios"
	if r.FormValue("password") != r.FormValue("password2") {
		s.fail(w, r, back, errors.New("SQS-A015 las contraseñas no coinciden"))
		return
	}
	_, err := s.A.Auth.CreateUser(r.Context(), principalOf(r).Actor(), r.FormValue("username"), r.FormValue("display"), r.FormValue("password"), auth.Role(r.FormValue("role")))
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "Usuario creado.")
}

func (s *Server) userUpdate(w http.ResponseWriter, r *http.Request) {
	back := "/ajustes?tab=usuarios"
	err := s.A.Auth.UpdateUser(r.Context(), principalOf(r).Actor(), r.PathValue("id"), r.FormValue("display"), auth.Role(r.FormValue("role")), r.FormValue("disabled") == "on")
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "Usuario actualizado.")
}

func (s *Server) userPassword(w http.ResponseWriter, r *http.Request) {
	back := "/ajustes?tab=usuarios"
	if err := s.A.Auth.SetPassword(r.Context(), principalOf(r).Actor(), r.PathValue("id"), r.FormValue("password"), ""); err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "Contraseña cambiada; se cerraron sus sesiones.")
}

func (s *Server) userDisableTOTP(w http.ResponseWriter, r *http.Request) {
	back := "/ajustes?tab=usuarios"
	if err := s.A.Auth.DisableTOTP(r.Context(), principalOf(r).Actor(), r.PathValue("id")); err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "Segundo factor desactivado para ese usuario.")
}

func (s *Server) notifySave(w http.ResponseWriter, r *http.Request) {
	back := "/ajustes?tab=notificaciones"
	c := &notify.Channel{
		ID: r.FormValue("id"), Kind: r.FormValue("kind"), Name: r.FormValue("name"), Enabled: r.FormValue("enabled") == "on",
		MinSeverity: events.Severity(r.FormValue("severity")), Pattern: strings.TrimSpace(r.FormValue("pattern")), Config: map[string]string{},
	}
	for _, k := range []string{"url", "secret", "token", "host", "port", "username", "password", "from", "to", "security"} {
		if v := strings.TrimSpace(r.FormValue(k)); v != "" {
			c.Config[k] = v
		}
	}
	if err := s.A.Notify.Save(r.Context(), principalOf(r).Actor(), c); err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "Canal guardado. Usa Probar para confirmar que llega.")
}

func (s *Server) notifyTest(w http.ResponseWriter, r *http.Request) {
	back := "/ajustes?tab=notificaciones"
	if err := s.A.Notify.Test(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, back, fmt.Errorf("SQS-N030 la prueba falló: %w", err))
		return
	}
	s.ok(w, r, back, "Prueba enviada correctamente.")
}

func (s *Server) notifyDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.A.Notify.Delete(r.Context(), principalOf(r).Actor(), r.PathValue("id")); err != nil {
		s.fail(w, r, "/ajustes?tab=notificaciones", err)
		return
	}
	s.ok(w, r, "/ajustes?tab=notificaciones", "Canal borrado.")
}

func (s *Server) maintenanceAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalOf(r)
	back := "/ajustes?tab=mantenimiento"
	var j *jobs.Job
	var err error
	switch r.PathValue("accion") {
	case "catalogo":
		j, err = s.A.Jobs.Enqueue(ctx, core.JobCatalogRebuild, "", nil, p.Actor())
	case "revisar":
		j, err = s.A.Jobs.Enqueue(ctx, core.JobCatalogCheck, "", nil, p.Actor())
	case "verificar":
		j, err = s.A.Jobs.Enqueue(ctx, core.JobVerifyAll, "", nil, p.Actor())
	case "retencion":
		j, err = s.A.Jobs.Enqueue(ctx, core.JobRetention, "", nil, p.Actor())
	case "optimizar":
		j, err = s.A.Jobs.Enqueue(ctx, core.JobVacuum, "", nil, p.Actor())
	case "control":
		j, err = s.A.Jobs.Enqueue(ctx, core.JobControlBackup, "", nil, p.Actor())
	case "hba":
		err = s.A.Svc.SyncHBA(ctx)
		if err == nil {
			s.ok(w, r, back, "pg_hba.conf regenerado y PostgreSQL recargado.")
			return
		}
	case "actualizacion":
		j, err = s.A.Svc.UpgradeWindow(ctx, p.Actor(), r.FormValue("target"))
	default:
		err = errors.New("acción desconocida")
	}
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.A.Audit.Record(ctx, p.Actor(), "maintenance."+r.PathValue("accion"), "system", "", "ok", nil)
	s.ok(w, r, "/tareas/"+j.ID, "")
}

func (s *Server) recoveryKit(w http.ResponseWriter, r *http.Request) {
	kit, err := s.A.RecoveryKit(r.Context())
	if err != nil {
		s.fail(w, r, "/ajustes?tab=mantenimiento", err)
		return
	}
	s.A.Audit.Record(r.Context(), principalOf(r).Actor(), "settings.recovery_kit", "system", "", "ok", nil)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="sqsolve-recuperacion-`+strings.ToLower(s.A.InstanceID[len(s.A.InstanceID)-6:])+`.txt"`)
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, kit)
}

func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s.A.Audit.Record(ctx, principalOf(r).Actor(), "diagnostics.download", "system", "", "ok", nil)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="sqsolve-diag-`+time.Now().UTC().Format("20060102T150405Z")+`.zip"`)
	z := zip.NewWriter(w)
	defer z.Close()
	add := func(name string, v any) {
		f, err := z.Create(name)
		if err != nil {
			return
		}
		switch t := v.(type) {
		case string:
			io.WriteString(f, t)
		case []byte:
			f.Write(t)
		default:
			b, _ := json.MarshalIndent(v, "", "  ")
			f.Write(b)
		}
	}
	add("version.json", map[string]any{"sqsolve": app.Version, "commit": app.Commit, "instance": s.A.InstanceID, "started": s.A.Started})
	cfg := *s.A.Cfg
	add("config.json", map[string]any{"listen": cfg.Listen, "app_dir": cfg.AppDir, "backup_dir": cfg.BackupDir, "pg_dir": cfg.PGDir,
		"pg_socket": cfg.PGSocket, "pg_port": cfg.PGPort, "uid": cfg.UID, "gid": cfg.GID, "workers": cfg.Workers, "tz": cfg.Location.String(),
		"insecure_http": cfg.InsecureHTTP})
	add("settings.json", s.A.Svc.Settings(ctx))
	add("doctor.json", app.Doctor(ctx, s.A))
	for _, f := range []string{"postgresql.conf", "pg_hba.conf"} {
		if b, err := os.ReadFile(filepath.Join(s.A.Cfg.PGConfDir(), f)); err == nil {
			add("postgresql/"+f, b)
		}
	}
	if dbs, err := s.A.PG.Databases(ctx); err == nil {
		add("postgresql/databases.json", dbs)
	}
	if info, err := s.A.PG.Server(ctx); err == nil {
		add("postgresql/server.json", info)
	}
	if pool, err := s.A.PG.Pool(); err == nil {
		rows, err := pool.Query(ctx, `SELECT rolname, rolsuper, rolcreatedb, rolcreaterole, rolcanlogin, rolconnlimit FROM pg_roles WHERE rolname !~ '^pg_' ORDER BY 1`)
		if err == nil {
			var roles []map[string]any
			for rows.Next() {
				var name string
				var super, cdb, crole, login bool
				var limit int
				if rows.Scan(&name, &super, &cdb, &crole, &login, &limit) == nil {
					roles = append(roles, map[string]any{"name": name, "superuser": super, "createdb": cdb, "createrole": crole, "login": login, "connlimit": limit})
				}
			}
			rows.Close()
			add("postgresql/roles.json", roles)
		}
	}
	libs, _ := s.A.Svc.Libraries(ctx, core.ListOptions{IncludePurged: true})
	add("libraries.json", libs)
	bs, _ := s.A.Svc.Backups(ctx, core.BackupFilter{IncludeDeleted: true, Limit: 2000})
	add("backups.json", bs)
	evs, _ := s.A.Events.List(ctx, events.Filter{Limit: 500})
	add("events.json", evs)
	failed, _ := s.A.Jobs.List(ctx, jobs.Filter{States: []jobs.State{jobs.Failed, jobs.Interrupted}, Limit: 20})
	for _, j := range failed {
		logs, _ := s.A.Jobs.Logs(ctx, j.ID, 0)
		steps, _ := s.A.Jobs.Steps(ctx, j.ID)
		add("jobs/"+j.ID+".json", map[string]any{"job": j, "steps": steps, "logs": logs})
	}
	scheds, _ := s.A.Sched.List(ctx)
	add("schedules.json", scheds)
}
