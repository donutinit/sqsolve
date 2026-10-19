// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/drp"
)

// Experimental export of projects and grades (ADR 0019, docs 22 §7).

// SchemaDTO tells the client whether the library's schema was verified.
type SchemaDTO struct {
	Fingerprint string `json:"fingerprint"`
	Verified    bool   `json:"verified"`
	Resolve     string `json:"resolve,omitempty"`
}

// ProjectDTO is a project as listed for export.
type ProjectDTO struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	Modified *time.Time `json:"modified,omitempty"`
}

// StillDTO is a gallery still as listed for export.
type StillDTO struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	RecTC   string `json:"rec_tc,omitempty"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Created string `json:"created,omitempty"`
}

// AlbumDTO is a gallery album with its stills.
type AlbumDTO struct {
	ViewID      string     `json:"view_id"`
	Name        string     `json:"name"`
	ProjectID   string     `json:"project_id,omitempty"`
	ProjectName string     `json:"project_name,omitempty"`
	Stills      []StillDTO `json:"stills"`
}

func schemaDTO(s *drp.Session) SchemaDTO {
	return SchemaDTO{Fingerprint: s.Fingerprint, Verified: s.KnownOK, Resolve: s.Known.Resolve}
}

func (a *API) openExport(w http.ResponseWriter, r *http.Request) (*core.Library, *drp.Session) {
	l := a.lib(w, r)
	if l == nil {
		return nil, nil
	}
	s, err := a.A.Svc.OpenExport(r.Context(), l)
	if err != nil {
		problem(w, r, core.ExportStatus(err), err)
		return nil, nil
	}
	return l, s
}

func (a *API) projects(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	_, s := a.openExport(w, r)
	if s == nil {
		return
	}
	defer s.Close(r.Context())
	projects, err := s.Projects(r.Context())
	if err != nil {
		problem(w, r, core.ExportStatus(err), err)
		return
	}
	out := make([]ProjectDTO, 0, len(projects))
	for _, pr := range projects {
		d := ProjectDTO{ID: pr.ID, Name: pr.Name, Path: pr.Path}
		if !pr.Modified.IsZero() {
			m := pr.Modified.UTC()
			d.Modified = &m
		}
		out = append(out, d)
	}
	writeJSON(w, 200, map[string]any{"items": out, "schema": schemaDTO(s), "experimental": true})
}

func (a *API) grades(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	_, s := a.openExport(w, r)
	if s == nil {
		return
	}
	defer s.Close(r.Context())
	albums, err := s.Albums(r.Context())
	if err != nil {
		problem(w, r, core.ExportStatus(err), err)
		return
	}
	out := make([]AlbumDTO, 0, len(albums))
	for _, al := range albums {
		d := AlbumDTO{ViewID: al.ViewID, Name: al.Name, ProjectID: al.ProjectID, ProjectName: al.ProjectName, Stills: []StillDTO{}}
		for _, st := range al.Stills {
			d.Stills = append(d.Stills, StillDTO{ID: st.ID, Label: st.Label, RecTC: st.RecTC, Width: st.Width, Height: st.Height, Created: st.Created})
		}
		out = append(out, d)
	}
	writeJSON(w, 200, map[string]any{"albums": out, "schema": schemaDTO(s), "experimental": true})
}

func (a *API) exportProject(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	a.exportDownload(w, r, p, "project", r.PathValue("pid"))
}

func (a *API) exportStill(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	a.exportDownload(w, r, p, "still", r.PathValue("sid"))
}

func (a *API) exportAlbum(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	a.exportDownload(w, r, p, "album", r.PathValue("vid"))
}

func (a *API) exportDownload(w http.ResponseWriter, r *http.Request, p *auth.Principal, kind, id string) {
	ctx := r.Context()
	l, s := a.openExport(w, r)
	if s == nil {
		return
	}
	defer s.Close(ctx)
	var buf bytes.Buffer
	var info *drp.ExportInfo
	var err error
	var name string
	switch kind {
	case "project":
		if info, err = s.ExportProject(ctx, id, &buf); err == nil {
			name = l.Slug + "_" + drp.SafeName(info.Name, "proyecto") + ".drp"
		}
	case "still":
		if info, err = s.ExportStill(ctx, id, &buf); err == nil {
			name = drp.StillFileName(info.Name, strings.ToLower(id))
		}
	default:
		if info, err = s.ExportAlbum(ctx, id, &buf); err == nil {
			name = l.Slug + "_" + drp.SafeName(info.Name, "album") + ".zip"
		}
	}
	if err != nil {
		problem(w, r, core.ExportStatus(err), err)
		return
	}
	a.A.Svc.AuditExport(ctx, p.Actor(), l, s, kind, strings.ToLower(id), info)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ToLower(id)+`"`)
	w.Header().Set("X-Sqsolve-Name", name)
	w.Header().Set("X-Sqsolve-Experimental", "true")
	if len(info.Notes) > 0 {
		w.Header().Set("X-Sqsolve-Notes", strings.Join(info.Notes, " | "))
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.Bytes())
}
