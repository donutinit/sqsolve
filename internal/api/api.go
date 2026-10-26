// SPDX-License-Identifier: AGPL-3.0-only

// Package api serves the JSON API under /api/v1 (docs 10). It calls the same
// services and the same authorization policy as the web panel.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/health"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// API holds the dependencies.
type API struct {
	A *app.App
}

// Problem is an RFC 9457 error body.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Code     string `json:"code,omitempty"`
	Instance string `json:"instance,omitempty"`
}

var codeRe = regexp.MustCompile(`SQS-[A-Z][0-9]{3}`)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func problem(w http.ResponseWriter, r *http.Request, status int, err error) {
	msg := err.Error()
	code := codeRe.FindString(msg)
	typ := "about:blank"
	if code != "" {
		typ = "/docs/errors/" + code
	}
	title := http.StatusText(status)
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	if errors.Is(err, auth.ErrBusy) {
		w.Header().Set("Retry-After", "5")
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(Problem{Type: typ, Title: title, Status: status, Detail: msg, Code: code, Instance: r.URL.Path})
}

// statusFor maps service errors to HTTP status codes.
func statusFor(err error) int {
	switch {
	case errors.Is(err, core.ErrLibraryNotFound), errors.Is(err, core.ErrBackupNotFound), errors.Is(err, jobs.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, jobs.ErrNotCancellable), errors.Is(err, core.ErrBackupPath), errors.Is(err, jobs.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, audit.ErrUnavailable):
		return http.StatusServiceUnavailable
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "SQS-B012"):
		return http.StatusInsufficientStorage
	case strings.Contains(msg, "SQS-P001"), errors.Is(err, auth.ErrBusy):
		return http.StatusServiceUnavailable
	}
	return http.StatusUnprocessableEntity
}

// clientIP resolves the caller's address with the same rules as the panel:
// X-Forwarded-For only from SQSOLVE_TRUSTED_PROXIES (auth.ClientIP).
func (a *API) clientIP(r *http.Request) string {
	return auth.ClientIP(r, a.A.Cfg.TrustedProxies)
}

// principal resolves a bearer token or a session cookie. With a cookie,
// mutating requests need the X-CSRF-Token header.
func (a *API) principal(r *http.Request) (*auth.Principal, error) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return a.A.Auth.TokenPrincipal(r.Context(), strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")), a.clientIP(r))
	}
	c, err := r.Cookie("sqsolve_session")
	if err != nil {
		return nil, auth.ErrNoSession
	}
	p, err := a.A.Auth.Session(r.Context(), c.Value, a.clientIP(r), false)
	if err != nil {
		return nil, err
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !a.A.Auth.CheckCSRF(c.Value, r.Header.Get("X-CSRF-Token")) {
		return nil, errors.New("SQS-A031 missing X-CSRF-Token header")
	}
	return p, nil
}

type handler func(w http.ResponseWriter, r *http.Request, p *auth.Principal)

func (a *API) guard(action auth.Action, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := a.principal(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sqsolve"`)
			problem(w, r, http.StatusUnauthorized, err)
			return
		}
		// The panel's enrollment requirement also holds for the same browser
		// session here; tokens are separate credentials and not affected.
		if auth.MustEnrollTOTP(p, func() bool { return a.A.Svc.Settings(r.Context()).TOTPRequiredAdmins }) {
			problem(w, r, http.StatusForbidden, auth.ErrEnrollTOTP)
			return
		}
		if !p.Can(action) {
			problem(w, r, http.StatusForbidden, errors.New("SQS-A030 the role or the token scopes do not allow this action"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		h(w, r, p)
	}
}

// decode reads a body that holds exactly one JSON value: an empty body is
// not the zero value, and nothing may follow the value.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if errors.Is(err, io.EOF) {
		err = errors.New("the body is empty")
	} else if err == nil && !errors.Is(dec.Decode(&json.RawMessage{}), io.EOF) {
		err = errors.New("more than one JSON value")
	}
	if err != nil {
		return fmt.Errorf("SQS-C061 invalid JSON body: %w", err)
	}
	return nil
}

// Register adds the routes to mux.
func (a *API) Register(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/system/health", a.health)
	m.HandleFunc("GET /api/v1/system/status", a.guard(auth.ActView, a.status))
	m.HandleFunc("GET /api/v1/system/version", a.guard(auth.ActView, a.version))
	m.HandleFunc("GET /api/v1/libraries", a.guard(auth.ActView, a.libraries))
	m.HandleFunc("POST /api/v1/libraries", a.guard(auth.ActLibraryCreate, a.libraryCreate))
	m.HandleFunc("GET /api/v1/libraries/{id}", a.guard(auth.ActView, a.library))
	m.HandleFunc("PATCH /api/v1/libraries/{id}", a.guard(auth.ActLibraryEdit, a.libraryPatch))
	m.HandleFunc("DELETE /api/v1/libraries/{id}", a.guard(auth.ActLibraryDelete, a.libraryDelete))
	m.HandleFunc("POST /api/v1/libraries/{id}/maintenance", a.guard(auth.ActLibraryMaintenance, a.maintenance))
	m.HandleFunc("GET /api/v1/libraries/{id}/connection", a.guard(auth.ActView, a.connection))
	m.HandleFunc("POST /api/v1/libraries/{id}/connection/reveal", a.guard(auth.ActLibraryReveal, a.reveal))
	m.HandleFunc("GET /api/v1/libraries/{id}/sessions", a.guard(auth.ActView, a.sessions))
	m.HandleFunc("GET /api/v1/libraries/{id}/backup-policy", a.guard(auth.ActView, a.policyGet))
	m.HandleFunc("PUT /api/v1/libraries/{id}/backup-policy", a.guard(auth.ActPolicyEdit, a.policyPut))
	m.HandleFunc("GET /api/v1/libraries/{id}/backups", a.guard(auth.ActView, a.libraryBackups))
	m.HandleFunc("POST /api/v1/libraries/{id}/backups", a.guard(auth.ActBackupCreate, a.backupCreate))
	m.HandleFunc("GET /api/v1/libraries/{id}/projects", a.guard(auth.ActView, a.projects))
	m.HandleFunc("GET /api/v1/libraries/{id}/projects/{pid}/export", a.guard(auth.ActLibraryExport, a.exportProject))
	m.HandleFunc("GET /api/v1/libraries/{id}/grades", a.guard(auth.ActView, a.grades))
	m.HandleFunc("GET /api/v1/libraries/{id}/grades/{sid}/export", a.guard(auth.ActLibraryExport, a.exportStill))
	m.HandleFunc("GET /api/v1/libraries/{id}/grades/albums/{vid}/export", a.guard(auth.ActLibraryExport, a.exportAlbum))
	m.HandleFunc("GET /api/v1/backups", a.guard(auth.ActView, a.backups))
	m.HandleFunc("GET /api/v1/backups/{id}", a.guard(auth.ActView, a.backup))
	m.HandleFunc("POST /api/v1/backups/{id}/verify", a.guard(auth.ActBackupVerify, a.backupVerify))
	m.HandleFunc("POST /api/v1/backups/{id}/restore", a.guard(auth.ActRestoreNew, a.restore))
	m.HandleFunc("GET /api/v1/backups/{id}/download", a.guard(auth.ActBackupDownload, a.download))
	m.HandleFunc("GET /api/v1/backups/{id}/export", a.guard(auth.ActView, a.backupExportStatus))
	m.HandleFunc("POST /api/v1/backups/{id}/export", a.guard(auth.ActLibraryExport, a.backupExportPrepare))
	m.HandleFunc("DELETE /api/v1/backups/{id}/export", a.guard(auth.ActLibraryExport, a.backupExportClose))
	m.HandleFunc("GET /api/v1/backups/{id}/export/projects/{pid}", a.guard(auth.ActLibraryExport, a.backupExportProject))
	m.HandleFunc("GET /api/v1/backups/{id}/export/grades/{sid}", a.guard(auth.ActLibraryExport, a.backupExportStill))
	m.HandleFunc("GET /api/v1/backups/{id}/export/grades/albums/{vid}", a.guard(auth.ActLibraryExport, a.backupExportAlbum))
	m.HandleFunc("GET /api/v1/jobs", a.guard(auth.ActView, a.jobsList))
	m.HandleFunc("GET /api/v1/jobs/{id}", a.guard(auth.ActView, a.job))
	m.HandleFunc("GET /api/v1/jobs/{id}/logs", a.guard(auth.ActView, a.jobLogs))
	m.HandleFunc("POST /api/v1/jobs/{id}/cancel", a.guard(auth.ActJobCancel, a.jobCancel))
	m.HandleFunc("GET /api/v1/events", a.guard(auth.ActView, a.eventsList))
	m.HandleFunc("GET /api/v1/audit", a.guard(auth.ActAuditView, a.auditList))
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		problem(w, r, http.StatusNotFound, errors.New("SQS-C062 unknown API route"))
	})
}

// health needs no authentication (uptime monitors, docs 13 §3): it shows
// states and counts, never paths, names or identifiers.
func (a *API) health(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pgOK, volOK := a.A.PGAlive(ctx), a.A.Svc.BackupVolumeOK() == nil
	status, body := healthBody(pgOK, volOK, a.A.PGIdentityProblem() != "", a.A.Monitor.BackupFreshness(ctx), a.A.Monitor.Volumes())
	writeJSON(w, status, body)
}

type volumeHealth struct {
	Name    string  `json:"name"`
	UsedPct float64 `json:"used_percent"`
	Status  string  `json:"status"`
}

// healthBody builds the unauthenticated health: 503 when anything is
// critical (PostgreSQL, the backups volume marker, a changed cluster, stale
// backups or a volume at 90 %).
func healthBody(pgOK, volOK, clusterChanged bool, fresh health.Freshness, vols []health.Volume) (int, map[string]any) {
	overall := health.StatusOK
	worse := func(s string) {
		if s == health.StatusCritical || (s == health.StatusWarning && overall == health.StatusOK) {
			overall = s
		}
	}
	vs := []volumeHealth{}
	for _, v := range vols {
		st := v.Status()
		worse(st)
		vs = append(vs, volumeHealth{v.ID, float64(int64(v.UsedPct*10)) / 10, st})
	}
	worse(fresh.Status)
	cluster := "ok"
	if clusterChanged {
		cluster = "changed"
		worse(health.StatusCritical)
	}
	if !pgOK || !volOK {
		worse(health.StatusCritical)
	}
	code := http.StatusOK
	if overall == health.StatusCritical {
		code = http.StatusServiceUnavailable
	}
	return code, map[string]any{"ready": pgOK && volOK, "postgres": pgOK, "backup_volume": volOK, "status": overall,
		"postgres_cluster": cluster, "backups": fresh, "volumes": vs}
}

func (a *API) status(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	st := a.A.Monitor.Snapshot(r.Context())
	type att struct {
		Severity string `json:"severity"`
		Message  string `json:"message"`
	}
	var attention []att
	for _, x := range st.Attention {
		attention = append(attention, att{string(x.Severity), x.Message})
	}
	type vol struct {
		Name    string  `json:"name"`
		Path    string  `json:"path"`
		Total   uint64  `json:"total_bytes"`
		Free    uint64  `json:"free_bytes"`
		UsedPct float64 `json:"used_percent"`
		Error   string  `json:"error,omitempty"`
	}
	var vols []vol
	for _, v := range st.Volumes {
		vols = append(vols, vol{v.Name, v.Path, v.Total, v.Free, v.UsedPct, v.Err})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"postgres": map[string]any{"up": st.PostgresUp, "version": st.Postgres.Version, "connections": st.Postgres.Connections, "max_connections": st.Postgres.MaxConnections, "error": st.PostgresErr},
		"volumes":  vols, "jobs": map[string]int{"queued": st.Queued, "running": st.Running, "failed_24h": st.Failed24h},
		"libraries": map[string]int{"total": st.Libraries, "active": st.Active}, "attention": attention,
	})
}

func (a *API) version(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	info, _ := a.A.PG.Server(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"sqsolve": app.Version, "commit": app.Commit, "postgres": info.Version, "instance_id": a.A.InstanceID})
}

// LibraryDTO is the public shape of a library.
type LibraryDTO struct {
	ID             string     `json:"id"`
	Slug           string     `json:"slug"`
	DisplayName    string     `json:"display_name"`
	State          string     `json:"state"`
	StateLabel     string     `json:"state_label"`
	Database       string     `json:"database"`
	OwnerRole      string     `json:"owner_role"`
	ResolveVersion string     `json:"resolve_version,omitempty"`
	Fingerprint    string     `json:"schema_fingerprint,omitempty"`
	Tables         int        `json:"schema_tables"`
	ConnLimit      int        `json:"connection_limit"`
	Notes          string     `json:"notes,omitempty"`
	InitExpiresAt  *time.Time `json:"init_window_expires_at,omitempty"`
	PrevExpiresAt  *time.Time `json:"previous_state_expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func libDTO(l *core.Library) LibraryDTO {
	return LibraryDTO{l.ID, l.Slug, l.DisplayName, string(l.State), l.State.Label(), l.DBName, l.OwnerRole, l.ResolveVersion, l.Fingerprint,
		l.TableCount, l.ConnLimit, l.Notes, l.InitWindowExpiresAt, l.PrevExpiresAt, l.CreatedAt, l.UpdatedAt}
}

// BackupDTO is the public shape of a backup.
type BackupDTO struct {
	ID            string     `json:"id"`
	LibraryID     string     `json:"library_id"`
	JobID         string     `json:"job_id,omitempty"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	VerifyStatus  string     `json:"verify_status"`
	VerifyDetail  string     `json:"verify_detail,omitempty"`
	VerifiedAt    *time.Time `json:"verified_at,omitempty"`
	SizeBytes     int64      `json:"size_bytes"`
	SHA256        string     `json:"sha256,omitempty"`
	PGDumpVersion string     `json:"pg_dump_version,omitempty"`
	Fingerprint   string     `json:"schema_fingerprint,omitempty"`
	Pinned        bool       `json:"pinned"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	Error         string     `json:"error,omitempty"`
}

func backupDTO(b *core.Backup) BackupDTO {
	return BackupDTO{b.ID, b.LibraryID, b.JobID, b.Kind, b.Status, b.VerifyStatus, b.VerifyDetail, b.VerifiedAt, b.SizeBytes, b.SHA256,
		b.PGDumpVersion, b.Fingerprint, b.Pinned, b.StartedAt, b.FinishedAt, b.Error}
}

func (a *API) libraries(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	opts := core.ListOptions{}
	if st := r.URL.Query().Get("state"); st != "" {
		opts.States = []core.State{core.State(st)}
	}
	libs, err := a.A.Svc.Libraries(r.Context(), opts)
	if err != nil {
		problem(w, r, 500, err)
		return
	}
	out := make([]LibraryDTO, 0, len(libs))
	for _, l := range libs {
		out = append(out, libDTO(l))
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *API) libraryCreate(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var in struct {
		DisplayName string `json:"display_name"`
		Slug        string `json:"slug"`
		Strategy    string `json:"strategy"`
		Notes       string `json:"notes"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, r, 400, err)
		return
	}
	if in.Slug == "" {
		in.Slug = core.SuggestSlug(in.DisplayName)
	}
	l, j, err := a.A.Svc.CreateLibrary(r.Context(), p.Actor(), core.CreateInput{DisplayName: in.DisplayName, Slug: in.Slug, Strategy: in.Strategy, Notes: in.Notes})
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+j.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"library": libDTO(l), "job": j})
}

func (a *API) lib(w http.ResponseWriter, r *http.Request) *core.Library {
	l, err := a.A.Svc.Library(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, r, statusFor(err), err)
		return nil
	}
	return l
}

func (a *API) library(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	if l := a.lib(w, r); l != nil {
		writeJSON(w, 200, libDTO(l))
	}
}

func (a *API) libraryPatch(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	l := a.lib(w, r)
	if l == nil {
		return
	}
	in := struct {
		DisplayName    *string `json:"display_name"`
		Notes          *string `json:"notes"`
		ResolveVersion *string `json:"resolve_version"`
		ConnLimit      *int    `json:"connection_limit"`
	}{}
	if err := decode(r, &in); err != nil {
		problem(w, r, 400, err)
		return
	}
	u := core.UpdateInput{DisplayName: l.DisplayName, Notes: l.Notes, ResolveVersion: l.ResolveVersion, ConnLimit: l.ConnLimit}
	if in.DisplayName != nil {
		u.DisplayName = *in.DisplayName
	}
	if in.Notes != nil {
		u.Notes = *in.Notes
	}
	if in.ResolveVersion != nil {
		u.ResolveVersion = *in.ResolveVersion
	}
	if in.ConnLimit != nil {
		u.ConnLimit = *in.ConnLimit
	}
	if err := a.A.Svc.UpdateLibrary(r.Context(), p.Actor(), l.ID, u); err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	l, _ = a.A.Svc.Library(r.Context(), l.ID)
	writeJSON(w, 200, libDTO(l))
}

func (a *API) libraryDelete(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var in struct {
		Confirm string `json:"confirm"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, r, 400, err)
		return
	}
	j, err := a.A.Svc.Trash(r.Context(), p.Actor(), r.PathValue("id"), in.Confirm)
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func (a *API) maintenance(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var in struct {
		Enabled           bool `json:"enabled"`
		TerminateSessions bool `json:"terminate_sessions"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, r, 400, err)
		return
	}
	n, err := a.A.Svc.SetMaintenance(r.Context(), p.Actor(), r.PathValue("id"), in.Enabled, in.TerminateSessions)
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	writeJSON(w, 200, map[string]any{"maintenance": in.Enabled, "terminated_sessions": n})
}

func (a *API) connection(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	c, err := a.A.Svc.Connection(r.Context(), r.PathValue("id"), r.URL.Query().Get("credential"))
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	writeJSON(w, 200, map[string]any{"library_name": c.LibraryName, "host": c.Host, "port": c.Port, "database": c.Database, "user": c.User})
}

func (a *API) reveal(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	id := r.PathValue("id")
	cred := r.URL.Query().Get("credential")
	pass, err := a.A.Svc.RevealPassword(r.Context(), p.Actor(), id, cred)
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	c, _ := a.A.Svc.Connection(r.Context(), id, cred)
	writeJSON(w, 200, map[string]any{"host": c.Host, "port": c.Port, "database": c.Database, "user": c.User, "password": pass.Reveal()})
}

func (a *API) sessions(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	ss, err := a.A.Svc.Sessions(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	type sdto struct {
		PID         int32     `json:"pid"`
		User        string    `json:"user"`
		ClientAddr  string    `json:"client_addr"`
		Application string    `json:"application"`
		Started     time.Time `json:"started_at"`
		State       string    `json:"state"`
		SSL         bool      `json:"ssl"`
		TLSVersion  string    `json:"tls_version,omitempty"`
	}
	out := []sdto{}
	for _, s := range ss {
		out = append(out, sdto{s.PID, s.User, s.ClientAddr, s.Application, s.Started, s.State, s.SSL, s.TLSVersion})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *API) policyGet(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	if _, err := a.A.Svc.Library(r.Context(), r.PathValue("id")); err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	pol, err := a.A.Svc.Policy(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	writeJSON(w, 200, pol)
}

func (a *API) policyPut(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var pol core.Policy
	if err := decode(r, &pol); err != nil {
		problem(w, r, 400, err)
		return
	}
	if err := a.A.Svc.SetPolicy(r.Context(), p.Actor(), r.PathValue("id"), pol); err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	writeJSON(w, 200, pol)
}

func (a *API) listBackups(w http.ResponseWriter, r *http.Request, libraryID string) {
	f := core.BackupFilter{LibraryID: libraryID, Limit: 200, Before: r.URL.Query().Get("cursor")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 500 {
		f.Limit = n
	}
	if st := r.URL.Query().Get("status"); st != "" {
		f.Statuses = []string{st}
	}
	bs, err := a.A.Svc.Backups(r.Context(), f)
	if err != nil {
		problem(w, r, 500, err)
		return
	}
	out := make([]BackupDTO, 0, len(bs))
	for _, b := range bs {
		out = append(out, backupDTO(b))
	}
	resp := map[string]any{"items": out}
	if len(bs) == f.Limit {
		resp["next_cursor"] = bs[len(bs)-1].ID
	}
	writeJSON(w, 200, resp)
}

func (a *API) libraryBackups(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	a.listBackups(w, r, r.PathValue("id"))
}

func (a *API) backups(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	a.listBackups(w, r, r.URL.Query().Get("library_id"))
}

func (a *API) backupCreate(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	j, err := a.A.Svc.BackupNow(r.Context(), p.Actor(), r.PathValue("id"))
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+j.ID)
	writeJSON(w, http.StatusAccepted, j)
}

func (a *API) backup(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	b, err := a.A.Svc.Backup(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	writeJSON(w, 200, backupDTO(b))
}

func (a *API) backupVerify(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	b, err := a.A.Svc.Backup(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	j, err := a.A.Jobs.Enqueue(r.Context(), core.JobBackupVerify, b.LibraryID, core.VerifyParams{BackupID: b.ID}, p.Actor(),
		jobs.Audited("backup.verify", "backup", b.ID, nil))
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func (a *API) restore(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var in struct {
		Mode              string `json:"mode"`
		Slug              string `json:"slug"`
		DisplayName       string `json:"display_name"`
		Confirm           string `json:"confirm"`
		TerminateSessions bool   `json:"terminate_sessions"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, r, 400, err)
		return
	}
	id := r.PathValue("id")
	switch in.Mode {
	case "replace":
		if !p.Can(auth.ActRestoreReplace) {
			problem(w, r, 403, errors.New("SQS-A030 replacing requires the restore or admin scope"))
			return
		}
		j, err := a.A.Svc.RestoreReplace(r.Context(), p.Actor(), id, in.Confirm, in.TerminateSessions)
		if err != nil {
			problem(w, r, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, j)
	case "new", "":
		l, j, err := a.A.Svc.RestoreAsNew(r.Context(), p.Actor(), id, in.Slug, in.DisplayName)
		if err != nil {
			problem(w, r, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"library": libDTO(l), "job": j})
	default:
		problem(w, r, 400, errors.New("SQS-C063 mode must be new or replace"))
	}
}

func (a *API) download(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	b, err := a.A.Svc.Backup(r.Context(), r.PathValue("id"))
	if err != nil || b.Status != core.BackupOK {
		problem(w, r, 404, core.ErrBackupNotFound)
		return
	}
	abs, err := a.A.Svc.AbsPath(b)
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		problem(w, r, 404, err)
		return
	}
	defer f.Close()
	if err := a.A.Audit.Require(r.Context(), p.Actor(), "backup.download", "backup", b.ID, audit.OK, nil); err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+b.ID+`.backup"`)
	w.Header().Set("X-Sqsolve-SHA256", b.SHA256)
	http.ServeContent(w, r, b.ID+".backup", b.StartedAt, f)
}

func (a *API) jobsList(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	q := r.URL.Query()
	f := jobs.Filter{Type: q.Get("type"), LibraryID: q.Get("library_id"), Before: q.Get("cursor"), Limit: 50}
	if st := q.Get("state"); st != "" {
		f.States = []jobs.State{jobs.State(st)}
	}
	list, err := a.A.Jobs.List(r.Context(), f)
	if err != nil {
		problem(w, r, 500, err)
		return
	}
	resp := map[string]any{"items": list}
	if len(list) == f.Limit {
		resp["next_cursor"] = list[len(list)-1].ID
	}
	writeJSON(w, 200, resp)
}

func (a *API) job(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	j, err := a.A.Jobs.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	steps, _ := a.A.Jobs.Steps(r.Context(), j.ID)
	writeJSON(w, 200, map[string]any{"job": j, "steps": steps})
}

func (a *API) jobLogs(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	logs, err := a.A.Jobs.Logs(r.Context(), r.PathValue("id"), after)
	if err != nil {
		problem(w, r, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": logs})
}

// jobCancel also needs the permission of the job's type (the one that
// starts the operation); types without one are for administrators.
func (a *API) jobCancel(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	ctx := r.Context()
	j, err := a.A.Jobs.Get(ctx, r.PathValue("id"))
	if err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	action := auth.Action(a.A.Jobs.Action(j.Type))
	if action == "" {
		action = auth.ActSettingsEdit
	}
	if !p.Can(action) {
		a.A.Audit.BestEffort(ctx, p.Actor(), "job.cancel", "job", j.ID, audit.Denied, map[string]any{"type": j.Type})
		problem(w, r, http.StatusForbidden, errors.New("SQS-A030 the role or the token scopes do not allow this action"))
		return
	}
	// The entry goes before the cancellation (ADR 0026).
	if err := a.A.Audit.Do(ctx, p.Actor(), "job.cancel", "job", j.ID, map[string]any{"type": j.Type}, func() error {
		return a.A.Jobs.Cancel(ctx, j.ID)
	}); err != nil {
		problem(w, r, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancel_requested"})
}

func (a *API) eventsList(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	q := r.URL.Query()
	evs, err := a.A.Events.List(r.Context(), events.Filter{LibraryID: q.Get("library_id"), MinSeverity: events.Severity(q.Get("min_severity")),
		TypePrefix: q.Get("type"), Before: q.Get("cursor"), Limit: 100})
	if err != nil {
		problem(w, r, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": evs})
}

func (a *API) auditList(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	list, err := a.A.Audit.List(r.Context(), before, 200, r.URL.Query().Get("target_id"))
	if err != nil {
		problem(w, r, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}
