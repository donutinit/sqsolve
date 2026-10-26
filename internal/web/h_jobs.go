// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// sseHeartbeat is how often a job stream pings and re-reads the job.
var sseHeartbeat = 15 * time.Second

func (s *Server) jobsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	f := jobs.Filter{Limit: 100, LibraryID: q.Get("library"), Type: q.Get("type")}
	if st := q.Get("state"); st != "" {
		f.States = []jobs.State{jobs.State(st)}
	}
	active, _ := s.A.Jobs.List(ctx, jobs.Filter{States: []jobs.State{jobs.Running, jobs.Queued}, Limit: 100})
	history, _ := s.A.Jobs.List(ctx, f)
	libs, _ := s.A.Svc.Libraries(ctx, core.ListOptions{IncludePurged: true})
	names := map[string]string{}
	for _, l := range libs {
		names[l.ID] = l.DisplayName
	}
	s.render(w, r, "jobs", "Jobs", "jobs", map[string]any{"Active": active, "History": history, "Names": names, "State": q.Get("state")})
}

// jobAction is the permission needed to cancel or retry a job of type typ:
// the one that starts the operation. Types without one are for
// administrators.
func (s *Server) jobAction(typ string) auth.Action {
	if a := s.A.Jobs.Action(typ); a != "" {
		return auth.Action(a)
	}
	return auth.ActSettingsEdit
}

// jobStartURL is the page where the operation of j starts again, for jobs
// that are not retried from their own page (destructive types ask for
// their confirmation there).
func jobStartURL(j *jobs.Job) string {
	var p struct {
		BackupID string `json:"backup_id"`
	}
	json.Unmarshal(j.Params, &p)
	switch j.Type {
	case core.JobLibraryCreate:
		return "/libraries/new"
	case core.JobRestoreNew, core.JobRestoreReplace:
		if p.BackupID != "" {
			return "/backups/" + p.BackupID + "/restore"
		}
	case core.JobRestoreRevert:
		if j.LibraryID != "" {
			return "/libraries/" + j.LibraryID + "/revert"
		}
	case core.JobBackupImport:
		return "/backups/import"
	case core.JobUpgradeWindow:
		return "/settings?tab=maintenance"
	}
	if j.LibraryID != "" {
		return "/libraries/" + j.LibraryID
	}
	return "/settings?tab=maintenance"
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalOf(r)
	j, err := s.A.Jobs.Get(ctx, r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, p, http.StatusNotFound, "SQS-J002 Job not found.")
		return
	}
	steps, _ := s.A.Jobs.Steps(ctx, j.ID)
	logs, _ := s.A.Jobs.Logs(ctx, j.ID, 0)
	data := map[string]any{"J": j, "Steps": steps, "Logs": logs}
	if j.LibraryID != "" {
		data["L"], _ = s.A.Svc.Library(ctx, j.LibraryID)
	}
	var res map[string]any
	if json.Unmarshal(j.Result, &res) == nil {
		data["Result"] = res
	}
	// The routes also require job.cancel and backup.create.
	allowed := p.Can(s.jobAction(j.Type))
	canRetry := j.Retryable && allowed && p.Can(auth.ActBackupCreate)
	data["CanCancel"] = j.Cancellable && allowed && p.Can(auth.ActJobCancel)
	data["CanRetry"] = canRetry
	if !canRetry && (j.State == jobs.Failed || j.State == jobs.Cancelled || j.State == jobs.Interrupted) {
		data["StartURL"] = jobStartURL(j)
	}
	s.render(w, r, "job", j.Title, "jobs", data)
}

// jobEvents streams progress with Server-Sent Events.
func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	// Subscribe before reading the job, so an update in between is not lost.
	ch, cancel := s.A.Bus.Subscribe("job:" + id)
	defer cancel()
	j, err := s.A.Jobs.Get(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}
	sendJob := func(j *jobs.Job) bool {
		send("state", jobs.Update{JobID: j.ID, State: j.State, Progress: j.Progress, Step: j.CurrentStep, Error: j.ErrorDetail})
		if j.State.Finished() {
			send("done", j.State)
			return true
		}
		return false
	}
	if sendJob(j) {
		return
	}
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			// The bus drops updates for slow readers: the database has the
			// last word on whether the job finished.
			if cur, err := s.A.Jobs.Get(ctx, id); err == nil && cur.State.Finished() {
				sendJob(cur)
				return
			}
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case m, ok := <-ch:
			if !ok {
				return
			}
			u, _ := m.Data.(jobs.Update)
			send("state", u)
			if u.State.Finished() {
				send("done", u.State)
				return
			}
		}
	}
}

// jobForAction loads the job and checks the permission of its type; on
// refusal it answers 403 and records the attempt.
func (s *Server) jobForAction(w http.ResponseWriter, r *http.Request, action string) (*jobs.Job, bool) {
	ctx := r.Context()
	p := principalOf(r)
	id := r.PathValue("id")
	j, err := s.A.Jobs.Get(ctx, id)
	if err != nil {
		s.fail(w, r, "/jobs", err)
		return nil, false
	}
	if !p.Can(s.jobAction(j.Type)) {
		s.A.Audit.BestEffort(ctx, p.Actor(), action, "job", id, audit.Denied, map[string]any{"type": j.Type})
		s.renderError(w, r, p, http.StatusForbidden, "SQS-A030 Your role does not allow this action.")
		return nil, false
	}
	return j, true
}

func (s *Server) jobCancel(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobForAction(w, r, "job.cancel")
	if !ok {
		return
	}
	ctx := r.Context()
	// The entry goes before the cancellation (ADR 0026).
	if err := s.A.Audit.Do(ctx, principalOf(r).Actor(), "job.cancel", "job", j.ID, map[string]any{"type": j.Type}, func() error {
		return s.A.Jobs.Cancel(ctx, j.ID)
	}); err != nil {
		s.fail(w, r, "/jobs/"+j.ID, err)
		return
	}
	s.ok(w, r, "/jobs/"+j.ID, "Cancellation requested.")
}

func (s *Server) jobRetry(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobForAction(w, r, "job.retry")
	if !ok {
		return
	}
	p := principalOf(r)
	nj, err := s.A.Jobs.Retry(r.Context(), j.ID, p.Actor(), jobs.Audited("job.retry", "job", j.ID, map[string]any{"type": j.Type}))
	if err != nil {
		s.fail(w, r, "/jobs/"+j.ID, err)
		return
	}
	s.ok(w, r, "/jobs/"+nj.ID, "Job queued again.")
}
