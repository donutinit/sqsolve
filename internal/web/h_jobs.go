// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
)

func (s *Server) jobsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	f := jobs.Filter{Limit: 100, LibraryID: q.Get("biblioteca"), Type: q.Get("tipo")}
	if st := q.Get("estado"); st != "" {
		f.States = []jobs.State{jobs.State(st)}
	}
	active, _ := s.A.Jobs.List(ctx, jobs.Filter{States: []jobs.State{jobs.Running, jobs.Queued}, Limit: 100})
	history, _ := s.A.Jobs.List(ctx, f)
	libs, _ := s.A.Svc.Libraries(ctx, core.ListOptions{IncludePurged: true})
	names := map[string]string{}
	for _, l := range libs {
		names[l.ID] = l.DisplayName
	}
	s.render(w, r, "jobs", "Tareas", "tareas", map[string]any{"Active": active, "History": history, "Names": names, "State": q.Get("estado")})
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	j, err := s.A.Jobs.Get(ctx, r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, principalOf(r), http.StatusNotFound, "SQS-J002 Tarea no encontrada.")
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
	s.render(w, r, "job", j.Title, "tareas", data)
}

// jobEvents streams progress with Server-Sent Events.
func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	j, err := s.A.Jobs.Get(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming no soportado", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, cancel := s.A.Bus.Subscribe("job:" + id)
	defer cancel()
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}
	send("state", jobs.Update{JobID: j.ID, State: j.State, Progress: j.Progress, Step: j.CurrentStep, Error: j.ErrorDetail})
	if j.State.Finished() {
		send("done", j.State)
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
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

func (s *Server) jobCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.A.Jobs.Cancel(r.Context(), id); err != nil {
		s.fail(w, r, "/tareas/"+id, err)
		return
	}
	s.A.Audit.Record(r.Context(), principalOf(r).Actor(), "job.cancel", "job", id, "ok", nil)
	s.ok(w, r, "/tareas/"+id, "Se pidió cancelar la tarea.")
}

func (s *Server) jobRetry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	nj, err := s.A.Jobs.Retry(r.Context(), id, principalOf(r).Actor())
	if err != nil {
		s.fail(w, r, "/tareas/"+id, err)
		return
	}
	s.ok(w, r, "/tareas/"+nj.ID, "Tarea encolada de nuevo.")
}
