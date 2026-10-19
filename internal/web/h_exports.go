// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/drp"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// Experimental export of projects and grades (ADR 0019, docs 22 §7).

type exportStill struct {
	drp.Still
	Created time.Time
}

type exportAlbum struct {
	drp.Album
	Stills []exportStill
}

func (s *Server) exportAlbums(albums []drp.Album) (power, byProject []exportAlbum) {
	for _, a := range albums {
		ea := exportAlbum{Album: a}
		for _, st := range a.Stills {
			es := exportStill{Still: st}
			if t, err := time.ParseInLocation("2006-01-02T15:04:05.000", st.Created, s.A.Cfg.Location); err == nil {
				es.Created = t
			}
			ea.Stills = append(ea.Stills, es)
		}
		if a.ProjectID == "" {
			power = append(power, ea)
		} else {
			byProject = append(byProject, ea)
		}
	}
	return power, byProject
}

func (s *Server) libraryExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	l := s.libraryOr404(w, r)
	if l == nil {
		return
	}
	data := map[string]any{"L": l, "Tab": "exportar", "Enabled": true, "CanExport": principalOf(r).Can(auth.ActLibraryExport)}
	data["Running"], _ = s.A.Jobs.List(ctx, jobs.Filter{LibraryID: l.ID, States: []jobs.State{jobs.Queued, jobs.Running}, Limit: 10})
	sess, err := s.A.Svc.OpenExport(ctx, l)
	if err != nil {
		data["Err"] = humanError(err)
		data["Enabled"] = !errors.Is(err, core.ErrExportsDisabled)
		s.render(w, r, "library_export", l.DisplayName, "bibliotecas", data)
		return
	}
	defer sess.Close(ctx)
	data["Fingerprint"], data["Known"], data["KnownOK"] = sess.Fingerprint, sess.Known, sess.KnownOK
	projects, err := sess.Projects(ctx)
	if err == nil {
		var albums []drp.Album
		if albums, err = sess.Albums(ctx); err == nil {
			data["PowerGrades"], data["ProjectAlbums"] = s.exportAlbums(albums)
		}
	}
	if err != nil {
		data["Err"] = humanError(err)
	}
	data["Projects"] = projects
	s.render(w, r, "library_export", l.DisplayName, "bibliotecas", data)
}

func (s *Server) libraryExportProject(w http.ResponseWriter, r *http.Request) {
	s.exportDownload(w, r, "project", r.PathValue("pid"))
}

func (s *Server) libraryExportStill(w http.ResponseWriter, r *http.Request) {
	s.exportDownload(w, r, "still", r.PathValue("sid"))
}

func (s *Server) libraryExportAlbum(w http.ResponseWriter, r *http.Request) {
	s.exportDownload(w, r, "album", r.PathValue("vid"))
}

func (s *Server) exportDownload(w http.ResponseWriter, r *http.Request, kind, id string) {
	ctx := r.Context()
	l := s.libraryOr404(w, r)
	if l == nil {
		return
	}
	p := principalOf(r)
	sess, err := s.A.Svc.OpenExport(ctx, l)
	if err != nil {
		s.renderError(w, r, p, core.ExportStatus(err), humanError(err))
		return
	}
	defer sess.Close(ctx)
	var buf bytes.Buffer
	var info *drp.ExportInfo
	var name string
	switch kind {
	case "project":
		if info, err = sess.ExportProject(ctx, id, &buf); err == nil {
			name = l.Slug + "_" + drp.SafeName(info.Name, "proyecto") + ".drp"
		}
	case "still":
		if info, err = sess.ExportStill(ctx, id, &buf); err == nil {
			name = drp.StillFileName(info.Name, strings.ToLower(id))
		}
	default:
		if info, err = sess.ExportAlbum(ctx, id, &buf); err == nil {
			name = l.Slug + "_" + drp.SafeName(info.Name, "album") + ".zip"
		}
	}
	if err != nil {
		s.renderError(w, r, p, core.ExportStatus(err), humanError(err))
		return
	}
	s.A.Svc.AuditExport(ctx, p.Actor(), l, sess, kind, strings.ToLower(id), info)
	serveAttachment(w, name, buf.Bytes())
}

// serveAttachment sends bytes as a download with an ASCII name and the
// UTF-8 name (RFC 5987) for browsers that read it.
func serveAttachment(w http.ResponseWriter, name string, data []byte) {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+ascii+`"; filename*=UTF-8''`+url.PathEscape(name))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}
