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
	"github.com/donutinit/sqsolve/internal/audit"
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
	s.render(w, r, "dashboard", "Dashboard", "dashboard", map[string]any{"S": st, "Tracks": tracks, "Stations": len(stations), "Jobs": recent, "Events": evs, "Host": s.A.Svc.AdvertisedHost(ctx)})
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalOf(r)
	tab := r.URL.Query().Get("tab")
	data := map[string]any{"Tab": tab}
	if tab == "audit" {
		if !p.Can(auth.ActAuditView) {
			s.renderError(w, r, p, http.StatusForbidden, "SQS-A030 Your role does not allow viewing the audit log.")
			return
		}
		data["Audit"], _ = s.A.Audit.List(ctx, 0, 300, "")
		broken, n, err := s.A.Audit.Verify(ctx)
		data["Broken"], data["Count"], data["VerifyErr"] = broken, n, err
	} else {
		sev := events.Severity(r.URL.Query().Get("sev"))
		data["Sev"] = string(sev)
		data["Events"], _ = s.A.Events.List(ctx, events.Filter{MinSeverity: sev, TypePrefix: r.URL.Query().Get("type"), Limit: 300})
	}
	s.render(w, r, "activity", "Activity", "activity", data)
}

func (s *Server) auditExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="sqsolve-audit-`+time.Now().Format("20060102")+`.jsonl"`)
	s.A.Audit.Export(r.Context(), w, time.Time{}, time.Now().Add(time.Hour))
}

func (s *Server) healthPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st := s.A.Monitor.Snapshot(ctx)
	scheds, _ := s.A.Sched.List(ctx)
	s.render(w, r, "health", "Health", "health", map[string]any{"S": st, "Schedules": scheds, "Checks": app.Doctor(ctx, s.A)})
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
	case "users":
		if !p.Can(auth.ActUsersManage) {
			s.renderError(w, r, p, http.StatusForbidden, "SQS-A030 Your role does not allow managing users.")
			return
		}
		data["Users"], _ = s.A.Auth.Users(ctx)
		data["Roles"] = []auth.Role{auth.RoleAdmin, auth.RoleOperator, auth.RoleViewer}
	case "notifications":
		chs, _ := s.A.Notify.Channels(ctx)
		data["Channels"] = chs
		data["Edit"] = r.URL.Query().Get("edit")
		data["NewKind"] = r.URL.Query().Get("new")
	case "maintenance":
		data["Copies"] = app.SystemCopies(s.A.Cfg)
		data["Imports"], _ = s.A.Svc.ImportCandidates()
	case "about":
		data["Version"], data["Commit"] = app.Version, app.Commit
		if info, err := s.A.PG.Server(ctx); err == nil {
			data["PG"] = info
		}
	}
	if info, err := s.A.PanelCertInfo(); err == nil {
		data["Cert"] = info
	}
	s.render(w, r, "settings", "Settings", "settings", data)
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
		s.fail(w, r, "/settings", err)
		return
	}
	s.ok(w, r, "/settings", "Settings saved.")
}

// maxPEM bounds each uploaded certificate or key file.
const maxPEM = 256 << 10

// readPEMParts reads the "cert" and "key" file parts from the multipart
// stream that page() left after checking the CSRF part: parsing the form
// again with FormFile would find the body already consumed (audit Low).
func readPEMParts(r *http.Request) (cert, key []byte, err error) {
	mr := multipartOf(r)
	if mr == nil {
		return nil, nil, errors.New("not a multipart form")
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		var dst *[]byte
		switch part.FormName() {
		case "cert":
			dst = &cert
		case "key":
			dst = &key
		default:
			io.Copy(io.Discard, part)
			continue
		}
		b, err := io.ReadAll(io.LimitReader(part, maxPEM+1))
		if err != nil {
			return nil, nil, err
		}
		if len(b) > maxPEM {
			return nil, nil, errors.New("file too large")
		}
		*dst = b
	}
	if len(cert) == 0 || len(key) == 0 {
		return nil, nil, errors.New("missing file")
	}
	return cert, key, nil
}

func (s *Server) settingsCert(w http.ResponseWriter, r *http.Request) {
	cert, key, err := readPEMParts(r)
	if err != nil {
		s.fail(w, r, "/settings", errors.New("SQS-C021 upload the certificate and the key in PEM format"))
		return
	}
	// The entry goes before the files are replaced (ADR 0026).
	if err := s.A.Audit.Do(r.Context(), principalOf(r).Actor(), "settings.certificate", "system", "", nil, func() error {
		return s.A.InstallPanelCert(cert, key)
	}); err != nil {
		s.fail(w, r, "/settings", err)
		return
	}
	s.ok(w, r, "/settings", "Certificate installed. It is already in use.")
}

func (s *Server) userCreate(w http.ResponseWriter, r *http.Request) {
	back := "/settings?tab=users"
	if r.FormValue("password") != r.FormValue("password2") {
		s.fail(w, r, back, errors.New("SQS-A015 the passwords do not match"))
		return
	}
	_, err := s.A.Auth.CreateUser(r.Context(), principalOf(r).Actor(), r.FormValue("username"), r.FormValue("display"), r.FormValue("password"), auth.Role(r.FormValue("role")))
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "User created.")
}

func (s *Server) userUpdate(w http.ResponseWriter, r *http.Request) {
	back := "/settings?tab=users"
	err := s.A.Auth.UpdateUser(r.Context(), principalOf(r).Actor(), r.PathValue("id"), r.FormValue("display"), auth.Role(r.FormValue("role")), r.FormValue("disabled") == "on")
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "User updated.")
}

func (s *Server) userPassword(w http.ResponseWriter, r *http.Request) {
	back := "/settings?tab=users"
	if err := s.A.Auth.SetPassword(r.Context(), principalOf(r).Actor(), r.PathValue("id"), r.FormValue("password"), ""); err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "Password changed; their sessions were closed.")
}

// userDisableTOTP removes another user's second factor. While the
// installation requires it for administrators, an administrator's cannot be
// removed from the panel; a lost one is reset from the container console
// (`sqsolve admin disable-totp`), and that account is then sent to enroll
// again at its next request.
func (s *Server) userDisableTOTP(w http.ResponseWriter, r *http.Request) {
	back := "/settings?tab=users"
	target, err := s.A.Auth.UserByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	if target.Role == auth.RoleAdmin && s.A.Svc.Settings(r.Context()).TOTPRequiredAdmins {
		s.fail(w, r, back, auth.ErrTOTPRequired)
		return
	}
	if err := s.A.Auth.DisableTOTP(r.Context(), principalOf(r).Actor(), target.ID); err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "Second factor disabled for that user.")
}

func (s *Server) notifySave(w http.ResponseWriter, r *http.Request) {
	back := "/settings?tab=notifications"
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
	if warn := s.A.Notify.AddressWarning(r.Context(), c); warn != "" {
		s.flash(w, "warn", "Channel saved. "+warn)
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	s.ok(w, r, back, "Channel saved. Use Test to confirm it arrives.")
}

func (s *Server) notifyTest(w http.ResponseWriter, r *http.Request) {
	back := "/settings?tab=notifications"
	if err := s.A.Notify.Test(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, back, fmt.Errorf("SQS-N030 the test failed: %w", err))
		return
	}
	s.ok(w, r, back, "Test sent successfully.")
}

func (s *Server) notifyDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.A.Notify.Delete(r.Context(), principalOf(r).Actor(), r.PathValue("id")); err != nil {
		s.fail(w, r, "/settings?tab=notifications", err)
		return
	}
	s.ok(w, r, "/settings?tab=notifications", "Channel deleted.")
}

func (s *Server) maintenanceAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalOf(r)
	back := "/settings?tab=maintenance"
	var j *jobs.Job
	var err error
	action := r.PathValue("action")
	// The entry of a job is written with the job (jobs.Audited).
	enqueue := func(typ string) (*jobs.Job, error) {
		return s.A.Jobs.Enqueue(ctx, typ, "", nil, p.Actor(), jobs.Audited("maintenance."+action, "system", "", nil))
	}
	switch action {
	case "catalog":
		j, err = enqueue(core.JobCatalogRebuild)
	case "check":
		j, err = enqueue(core.JobCatalogCheck)
	case "verify":
		j, err = enqueue(core.JobVerifyAll)
	case "retention":
		j, err = enqueue(core.JobRetention)
	case "optimize":
		j, err = enqueue(core.JobVacuum)
	case "control":
		j, err = enqueue(core.JobControlBackup)
	case "hba":
		err = s.A.Audit.Do(ctx, p.Actor(), "maintenance.hba", "system", "", nil, func() error { return s.A.Svc.SyncHBA(ctx) })
		if err == nil {
			s.ok(w, r, back, "pg_hba.conf regenerated and PostgreSQL reloaded.")
			return
		}
	case "upgrade":
		j, err = s.A.Svc.UpgradeWindow(ctx, p.Actor(), r.FormValue("target"))
	default:
		err = errors.New("unknown action")
	}
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, "/jobs/"+j.ID, "")
}

func (s *Server) recoveryKit(w http.ResponseWriter, r *http.Request) {
	kit, err := s.A.RecoveryKit(r.Context())
	if err != nil {
		s.fail(w, r, "/settings?tab=maintenance", err)
		return
	}
	if err := s.A.Audit.Require(r.Context(), principalOf(r).Actor(), "settings.recovery_kit", "system", "", audit.OK, nil); err != nil {
		s.fail(w, r, "/settings?tab=maintenance", err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="sqsolve-recovery-`+strings.ToLower(s.A.InstanceID[len(s.A.InstanceID)-6:])+`.txt"`)
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, kit)
}

func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.A.Audit.Require(ctx, principalOf(r).Actor(), "diagnostics.download", "system", "", audit.OK, nil); err != nil {
		s.fail(w, r, "/settings?tab=maintenance", err)
		return
	}
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
