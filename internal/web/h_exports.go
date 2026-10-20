// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"context"
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
	data := map[string]any{"L": l, "Tab": "export", "Enabled": true, "CanExport": principalOf(r).Can(auth.ActLibraryExport)}
	data["Running"], _ = s.A.Jobs.List(ctx, jobs.Filter{LibraryID: l.ID, States: []jobs.State{jobs.Queued, jobs.Running}, Limit: 10})
	sess, err := s.A.Svc.OpenExport(ctx, l)
	if err != nil {
		data["Err"] = humanError(err)
		data["Enabled"] = !errors.Is(err, core.ErrExportsDisabled)
		s.render(w, r, "library_export", l.DisplayName, "libraries", data)
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
	s.render(w, r, "library_export", l.DisplayName, "libraries", data)
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
	name, data, info, err := buildExport(ctx, sess, l.Slug, kind, id)
	if err != nil {
		s.renderError(w, r, p, core.ExportStatus(err), humanError(err))
		return
	}
	s.A.Svc.AuditExport(ctx, p.Actor(), l, sess, kind, strings.ToLower(id), info)
	serveAttachment(w, name, data)
}

// buildExport produces one download (project, still or album) from an open
// session; slug prefixes the file names.
func buildExport(ctx context.Context, sess *drp.Session, slug, kind, id string) (name string, data []byte, info *drp.ExportInfo, err error) {
	var buf bytes.Buffer
	switch kind {
	case "project":
		if info, err = sess.ExportProject(ctx, id, &buf); err == nil {
			name = slug + "_" + drp.SafeName(info.Name, "project") + ".drp"
		}
	case "still":
		if info, err = sess.ExportStill(ctx, id, &buf); err == nil {
			name = drp.StillFileName(info.Name, strings.ToLower(id))
		}
	default:
		if info, err = sess.ExportAlbum(ctx, id, &buf); err == nil {
			name = slug + "_" + drp.SafeName(info.Name, "album") + ".zip"
		}
	}
	return name, buf.Bytes(), info, err
}

// Export from a backup (docs 22 §7): the same catalog and downloads, read
// from the temporary database of a prepared workspace.

func (s *Server) backupExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b, l := s.backupOr404(w, r)
	if b == nil {
		return
	}
	data := map[string]any{"B": b, "L": l, "Enabled": true, "CanExport": principalOf(r).Can(auth.ActLibraryExport),
		"Projects": []drp.Project{}, "PowerGrades": []exportAlbum{}, "ProjectAlbums": []exportAlbum{}}
	data["Workspace"], _ = s.A.Svc.BackupExportWorkspace(ctx, b.ID)
	if ws, _ := data["Workspace"].(*core.ExportWorkspace); ws != nil && ws.JobID != "" {
		data["Job"], _ = s.A.Jobs.Get(ctx, ws.JobID)
	}
	sess, ws, err := s.A.Svc.OpenBackupExport(ctx, b.ID)
	data["Live"] = ws.Live(time.Now())
	if err != nil {
		if !errors.Is(err, core.ErrExportNotReady) {
			data["Err"] = humanError(err)
			data["Enabled"] = !errors.Is(err, core.ErrExportsDisabled)
		}
		s.render(w, r, "backup_export", "Export from backup", "backups", data)
		return
	}
	defer sess.Close(ctx)
	data["Ready"] = true
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
	if projects != nil {
		data["Projects"] = projects
	}
	s.render(w, r, "backup_export", "Export from backup", "backups", data)
}

func (s *Server) backupExportPrepare(w http.ResponseWriter, r *http.Request) {
	b, _ := s.backupOr404(w, r)
	if b == nil {
		return
	}
	back := "/backups/" + b.ID + "/export"
	_, j, err := s.A.Svc.PrepareBackupExport(r.Context(), principalOf(r).Actor(), b.ID)
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	if j != nil && !j.State.Finished() {
		s.ok(w, r, "/jobs/"+j.ID, "Preparing the backup for export; come back to this page when it finishes.")
		return
	}
	s.ok(w, r, back, "")
}

func (s *Server) backupExportClose(w http.ResponseWriter, r *http.Request) {
	b, _ := s.backupOr404(w, r)
	if b == nil {
		return
	}
	if err := s.A.Svc.CloseBackupExport(r.Context(), principalOf(r).Actor(), b.ID); err != nil {
		s.fail(w, r, "/backups/"+b.ID+"/export", err)
		return
	}
	s.ok(w, r, "/backups/"+b.ID, "The export was closed and the temporary database was deleted.")
}

func (s *Server) backupExportProject(w http.ResponseWriter, r *http.Request) {
	s.backupExportDownload(w, r, "project", r.PathValue("pid"))
}

func (s *Server) backupExportStill(w http.ResponseWriter, r *http.Request) {
	s.backupExportDownload(w, r, "still", r.PathValue("sid"))
}

func (s *Server) backupExportAlbum(w http.ResponseWriter, r *http.Request) {
	s.backupExportDownload(w, r, "album", r.PathValue("vid"))
}

func (s *Server) backupExportDownload(w http.ResponseWriter, r *http.Request, kind, id string) {
	ctx := r.Context()
	b, l := s.backupOr404(w, r)
	if b == nil {
		return
	}
	p := principalOf(r)
	sess, _, err := s.A.Svc.OpenBackupExport(ctx, b.ID)
	if err != nil {
		s.renderError(w, r, p, core.ExportStatus(err), humanError(err))
		return
	}
	defer sess.Close(ctx)
	slug := "backup"
	if l != nil {
		slug = l.Slug
	}
	name, data, info, err := buildExport(ctx, sess, slug+"_"+b.StartedAt.UTC().Format("20060102T1504"), kind, id)
	if err != nil {
		s.renderError(w, r, p, core.ExportStatus(err), humanError(err))
		return
	}
	s.A.Svc.AuditBackupExport(ctx, p.Actor(), b, sess, kind, strings.ToLower(id), info)
	serveAttachment(w, name, data)
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
