// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/scheduler"
)

type libRow struct {
	L        *core.Library
	Last     *core.Backup
	Sessions int
	Size     int64
	Track    Track
}

func (s *Server) libraryRows(r *http.Request, opts core.ListOptions) []libRow {
	ctx := r.Context()
	libs, _ := s.A.Svc.Libraries(ctx, opts)
	dbs := map[string]pg.DBInfo{}
	if list, err := s.A.PG.Databases(ctx); err == nil {
		for _, d := range list {
			dbs[d.Name] = d
		}
	}
	rows := make([]libRow, 0, len(libs))
	for _, l := range libs {
		row := libRow{L: l}
		row.Last, _ = s.A.Svc.LatestVerified(ctx, l.ID)
		row.Track = s.track(ctx, l, 14)
		if d, ok := dbs[l.DBName]; ok && l.Live() {
			row.Sessions, row.Size = d.ActiveSessions, d.SizeBytes
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *Server) libraries(w http.ResponseWriter, r *http.Request) {
	show := r.URL.Query().Get("view")
	opts := core.ListOptions{}
	if show == "trash" {
		opts.States = []core.State{core.StateTrashed}
	} else if show == "deleted" {
		opts.States = []core.State{core.StatePurged}
	}
	rows := s.libraryRows(r, opts)
	if show == "" {
		filtered := rows[:0]
		for _, row := range rows {
			if row.L.State != core.StateTrashed {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	s.render(w, r, "libraries", "Libraries", "libraries", map[string]any{"Rows": rows, "Show": show})
}

func (s *Server) libraryNewForm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.render(w, r, "library_new", "New library", "libraries", map[string]any{
		"Name": q.Get("name"), "Slug": q.Get("slug"), "Notes": q.Get("notes"), "Strategy": q.Get("strategy"),
		"Host": s.A.Svc.AdvertisedHost(r.Context()),
	})
}

func (s *Server) libraryNewSubmit(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	name := strings.TrimSpace(r.FormValue("name"))
	slug := strings.TrimSpace(r.FormValue("slug"))
	if slug == "" {
		slug = core.SuggestSlug(name)
	}
	l, j, err := s.A.Svc.CreateLibrary(r.Context(), p.Actor(), core.CreateInput{DisplayName: name, Slug: slug, Strategy: r.FormValue("strategy"), Notes: r.FormValue("notes")})
	if err != nil {
		s.flash(w, "error", humanError(err))
		http.Redirect(w, r, "/libraries/new?name="+urlq(name)+"&slug="+urlq(slug)+"&notes="+urlq(r.FormValue("notes")), http.StatusSeeOther)
		return
	}
	_ = j
	s.ok(w, r, "/libraries/"+l.ID, "Library created. Follow the steps to initialize it from Resolve.")
}

func (s *Server) libraryOr404(w http.ResponseWriter, r *http.Request) *core.Library {
	l, err := s.A.Svc.Library(r.Context(), r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, principalOf(r), http.StatusNotFound, "SQS-L002 Library not found.")
		return nil
	}
	return l
}

func (s *Server) library(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	l := s.libraryOr404(w, r)
	if l == nil {
		return
	}
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "connection"
	}
	data := map[string]any{"L": l, "Tab": tab, "Host": s.A.Svc.AdvertisedHost(ctx), "Port": s.A.Cfg.PGPort}
	data["Settings"] = s.A.Svc.Settings(ctx)
	creds, _ := s.A.Svc.Credentials(ctx, l.ID)
	data["Creds"] = creds
	for _, c := range creds {
		if c.Kind == "owner" {
			data["Owner"] = c
		}
	}
	data["Last"], _ = s.A.Svc.LatestVerified(ctx, l.ID)
	if db, err := s.A.PG.Database(ctx, l.DBName); err == nil && db != nil {
		data["DB"] = db
	}
	running, _ := s.A.Jobs.List(ctx, jobs.Filter{LibraryID: l.ID, States: []jobs.State{jobs.Queued, jobs.Running}, Limit: 10})
	data["Running"] = running
	switch tab {
	case "backups":
		bs, _ := s.A.Svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Limit: 200})
		data["Backups"] = bs
		data["Track"] = s.track(ctx, l, 30)
	case "schedule":
		pol, _ := s.A.Svc.Policy(ctx, l.ID)
		data["Policy"] = pol
		data["CheckOptions"] = []int{5, 10, 15, 30, 60}
		data["Upcoming"], _ = scheduler.Upcoming(pol.Cron, pol.Timezone, time.Now(), 5)
		keep, expire, _ := s.A.Svc.RetentionPreview(ctx, l.ID, pol)
		data["Keep"], data["Expire"] = len(keep), len(expire)
		sc, _ := s.A.Sched.Get(ctx, core.ScheduleBackup, l.ID)
		data["Schedule"] = sc
	case "access":
		data["Rules"], _ = s.A.Svc.NetworkRules(ctx, l.ID)
	case "activity":
		data["Sessions"], _ = s.A.Svc.Sessions(ctx, l.ID)
		data["Events"], _ = s.A.Events.List(ctx, events.Filter{LibraryID: l.ID, Limit: 50})
		data["Jobs"], _ = s.A.Jobs.List(ctx, jobs.Filter{LibraryID: l.ID, Limit: 20})
	}
	if tab == "connection" && l.State == core.StateActive {
		data["Sessions"], _ = s.A.Svc.Sessions(ctx, l.ID)
	}
	if l.State == core.StateAwaitingInit || l.State == core.StateInitExpired {
		data["Misnamed"] = s.A.Svc.MisnamedDatabases(ctx, l)
	}
	s.render(w, r, "library", l.DisplayName, "libraries", data)
}

func (s *Server) libraryInstructions(w http.ResponseWriter, r *http.Request) {
	l := s.libraryOr404(w, r)
	if l == nil {
		return
	}
	conn, err := s.A.Svc.Connection(r.Context(), l.ID, "")
	if err != nil {
		s.renderError(w, r, principalOf(r), 500, humanError(err))
		return
	}
	s.render(w, r, "instructions", "Connect "+l.DisplayName, "libraries", map[string]any{"L": l, "C": conn})
}

func (s *Server) librarySettings(w http.ResponseWriter, r *http.Request) {
	l := s.libraryOr404(w, r)
	if l == nil {
		return
	}
	limit, err := strconv.Atoi(strings.TrimSpace(r.FormValue("connlimit")))
	if err != nil {
		limit = -1
	}
	err = s.A.Svc.UpdateLibrary(r.Context(), principalOf(r).Actor(), l.ID, core.UpdateInput{
		DisplayName: r.FormValue("name"), Notes: r.FormValue("notes"), ResolveVersion: r.FormValue("resolve"), ConnLimit: limit,
	})
	back := "/libraries/" + l.ID + "?tab=settings"
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "Changes saved.")
}

func atoi(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

func (s *Server) libraryPolicy(w http.ResponseWriter, r *http.Request) {
	l := s.libraryOr404(w, r)
	if l == nil {
		return
	}
	back := "/libraries/" + l.ID + "?tab=schedule"
	cron := strings.TrimSpace(r.FormValue("cron"))
	mode, check := core.ModeCron, 15
	switch r.FormValue("preset") {
	case "activity":
		mode, check = core.ModeActivity, atoi(r.FormValue("check_minutes"), 15)
		cron = core.ActivityCron(check)
	case "daily":
		hh := atoi(r.FormValue("hour"), 2)
		if hh < 0 || hh > 23 {
			hh = 2
		}
		cron = fmt.Sprintf("0 %d * * *", hh)
	case "workdays":
		cron = "0 9-21/4 * * 1-6"
	case "every4h":
		cron = "0 */4 * * *"
	case "hourly":
		cron = "0 * * * *"
	}
	p := core.Policy{
		Enabled: r.FormValue("enabled") == "on", Mode: mode, CheckMinutes: check, Cron: cron, Timezone: s.A.Cfg.Location.String(),
		KeepLast: atoi(r.FormValue("keep_last"), 10), KeepDaily: atoi(r.FormValue("keep_daily"), 14),
		KeepWeekly: atoi(r.FormValue("keep_weekly"), 8), KeepMonthly: atoi(r.FormValue("keep_monthly"), 12),
		MinVerified: atoi(r.FormValue("min_verified"), 1),
	}
	if tz := strings.TrimSpace(r.FormValue("timezone")); tz != "" {
		p.Timezone = tz
	}
	if err := s.A.Svc.SetPolicy(r.Context(), principalOf(r).Actor(), l.ID, p); err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "Schedule saved.")
}

func (s *Server) libraryBackup(w http.ResponseWriter, r *http.Request) {
	j, err := s.A.Svc.BackupNow(r.Context(), principalOf(r).Actor(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "/libraries/"+r.PathValue("id"), err)
		return
	}
	s.ok(w, r, "/jobs/"+j.ID, "")
}

func (s *Server) libraryMaintenance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	on := r.FormValue("on") == "1"
	n, err := s.A.Svc.SetMaintenance(r.Context(), principalOf(r).Actor(), id, on, r.FormValue("terminate") == "on")
	back := "/libraries/" + id
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	msg := "Connections reopened."
	if on {
		msg = "Library in maintenance: it does not accept new connections."
		if n > 0 {
			msg += fmt.Sprintf(" %d sessions were closed.", n)
		}
	}
	s.ok(w, r, back, msg)
}

func (s *Server) libraryReopen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.A.Svc.ReopenInitWindow(r.Context(), principalOf(r).Actor(), id); err != nil {
		s.fail(w, r, "/libraries/"+id, err)
		return
	}
	s.ok(w, r, "/libraries/"+id, "Initialization window reopened.")
}

func (s *Server) libraryReveal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	l := s.libraryOr404(w, r)
	if l == nil {
		return
	}
	cid := r.FormValue("cred")
	pass, err := s.A.Svc.RevealPassword(ctx, principalOf(r).Actor(), l.ID, cid)
	if err != nil {
		s.fail(w, r, "/libraries/"+l.ID, err)
		return
	}
	conn, err := s.A.Svc.Connection(ctx, l.ID, cid)
	if err != nil {
		s.fail(w, r, "/libraries/"+l.ID, err)
		return
	}
	s.render(w, r, "reveal", "Password of "+l.DisplayName, "libraries", map[string]any{"L": l, "C": conn, "Password": pass.Reveal()})
}

func (s *Server) libraryRotate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.A.Svc.RotatePassword(r.Context(), principalOf(r).Actor(), id, r.FormValue("cred")); err != nil {
		s.fail(w, r, "/libraries/"+id+"?tab=access", err)
		return
	}
	s.ok(w, r, "/libraries/"+id+"?tab=access", "Password changed. Update it on the workstations that use this credential.")
}

func (s *Server) libraryAddMember(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := s.A.Svc.AddMember(r.Context(), principalOf(r).Actor(), id, r.FormValue("person"))
	if err != nil {
		s.fail(w, r, "/libraries/"+id+"?tab=access", err)
		return
	}
	s.ok(w, r, "/libraries/"+id+"?tab=access", "Credential created for "+c.PersonLabel+" (user "+c.PGRole+").")
}

func (s *Server) libraryRevokeMember(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.A.Svc.RevokeMember(r.Context(), principalOf(r).Actor(), id, r.PathValue("cid")); err != nil {
		s.fail(w, r, "/libraries/"+id+"?tab=access", err)
		return
	}
	s.ok(w, r, "/libraries/"+id+"?tab=access", "Credential revoked and sessions closed.")
}

func (s *Server) libraryNetwork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cidrs := strings.FieldsFunc(r.FormValue("cidrs"), func(r rune) bool { return r == '\n' || r == ',' || r == ' ' || r == '\r' })
	if err := s.A.Svc.SetNetworkRules(r.Context(), principalOf(r).Actor(), id, cidrs); err != nil {
		s.fail(w, r, "/libraries/"+id+"?tab=access", err)
		return
	}
	s.ok(w, r, "/libraries/"+id+"?tab=access", "Network rules applied.")
}

func (s *Server) librarySessionTerminate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pid, _ := strconv.Atoi(r.PathValue("pid"))
	if err := s.A.Svc.TerminateSession(r.Context(), principalOf(r).Actor(), id, int32(pid)); err != nil {
		s.fail(w, r, "/libraries/"+id+"?tab=activity", err)
		return
	}
	s.ok(w, r, "/libraries/"+id+"?tab=activity", "Session closed.")
}

func (s *Server) confirmData(r *http.Request, l *core.Library) map[string]any {
	ctx := r.Context()
	data := map[string]any{"L": l}
	data["Sessions"], _ = s.A.Svc.Sessions(ctx, l.ID)
	data["Last"], _ = s.A.Svc.LatestVerified(ctx, l.ID)
	if db, err := s.A.PG.Database(ctx, l.DBName); err == nil {
		data["DB"] = db
	}
	data["Settings"] = s.A.Svc.Settings(ctx)
	return data
}

func (s *Server) libraryDeleteForm(w http.ResponseWriter, r *http.Request) {
	l := s.libraryOr404(w, r)
	if l == nil {
		return
	}
	s.render(w, r, "library_delete", "Delete "+l.DisplayName, "libraries", s.confirmData(r, l))
}

func (s *Server) libraryDeleteSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, err := s.A.Svc.Trash(r.Context(), principalOf(r).Actor(), id, strings.TrimSpace(r.FormValue("confirm")))
	if err != nil {
		s.fail(w, r, "/libraries/"+id+"/delete", err)
		return
	}
	s.ok(w, r, "/jobs/"+j.ID, "")
}

func (s *Server) libraryUntrash(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, err := s.A.Svc.RestoreFromTrash(r.Context(), principalOf(r).Actor(), id)
	if err != nil {
		s.fail(w, r, "/libraries/"+id, err)
		return
	}
	s.ok(w, r, "/jobs/"+j.ID, "")
}

func (s *Server) libraryPurgeForm(w http.ResponseWriter, r *http.Request) {
	l := s.libraryOr404(w, r)
	if l == nil {
		return
	}
	s.render(w, r, "library_purge", "Purge "+l.DisplayName, "libraries", s.confirmData(r, l))
}

func (s *Server) libraryPurgeSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, err := s.A.Svc.Purge(r.Context(), principalOf(r).Actor(), id, strings.TrimSpace(r.FormValue("confirm")))
	if err != nil {
		s.fail(w, r, "/libraries/"+id+"/purge", err)
		return
	}
	s.ok(w, r, "/jobs/"+j.ID, "")
}

func (s *Server) libraryRevertForm(w http.ResponseWriter, r *http.Request) {
	l := s.libraryOr404(w, r)
	if l == nil {
		return
	}
	if l.PrevDB == "" {
		s.fail(w, r, "/libraries/"+l.ID, errors.New("SQS-R030 there is no previous state to recover"))
		return
	}
	s.render(w, r, "library_revert", "Revert "+l.DisplayName, "libraries", s.confirmData(r, l))
}

func (s *Server) libraryRevertSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, err := s.A.Svc.Revert(r.Context(), principalOf(r).Actor(), id, strings.TrimSpace(r.FormValue("confirm")))
	if err != nil {
		s.fail(w, r, "/libraries/"+id+"/revert", err)
		return
	}
	s.ok(w, r, "/jobs/"+j.ID, "")
}

func (s *Server) libraryRecreate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, err := s.A.Svc.Recreate(r.Context(), principalOf(r).Actor(), id, r.FormValue("backup"), strings.TrimSpace(r.FormValue("confirm")))
	if err != nil {
		s.fail(w, r, "/libraries/"+id, err)
		return
	}
	s.ok(w, r, "/jobs/"+j.ID, "")
}
