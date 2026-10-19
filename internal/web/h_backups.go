// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/jobs"
)

func urlq(s string) string { return url.QueryEscape(s) }

type backupRow struct {
	B *core.Backup
	L *core.Library
}

func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	f := core.BackupFilter{LibraryID: q.Get("biblioteca"), Limit: 300}
	if st := q.Get("estado"); st != "" {
		f.Statuses = []string{st}
	}
	bs, _ := s.A.Svc.Backups(ctx, f)
	libs, _ := s.A.Svc.Libraries(ctx, core.ListOptions{IncludePurged: true})
	byID := map[string]*core.Library{}
	for _, l := range libs {
		byID[l.ID] = l
	}
	rows := make([]backupRow, 0, len(bs))
	var total int64
	for _, b := range bs {
		rows = append(rows, backupRow{b, byID[b.LibraryID]})
		if b.Status == core.BackupOK {
			total += b.SizeBytes
		}
	}
	imports, _ := s.A.Svc.ImportCandidates()
	s.render(w, r, "backups", "Respaldos", "respaldos", map[string]any{"Rows": rows, "Libraries": libs, "Filter": f, "Total": total, "Imports": len(imports)})
}

func (s *Server) backupOr404(w http.ResponseWriter, r *http.Request) (*core.Backup, *core.Library) {
	b, err := s.A.Svc.Backup(r.Context(), r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, principalOf(r), http.StatusNotFound, "SQS-B001 Respaldo no encontrado.")
		return nil, nil
	}
	l, _ := s.A.Svc.Library(r.Context(), b.LibraryID)
	return b, l
}

func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	b, l := s.backupOr404(w, r)
	if b == nil {
		return
	}
	data := map[string]any{"B": b, "L": l}
	if m, err := core.ReadManifest(strings.TrimSuffix(s.A.Svc.AbsPath(b), ".backup") + ".manifest.json"); err == nil {
		data["M"] = m
	}
	if b.JobID != "" {
		data["Job"], _ = s.A.Jobs.Get(r.Context(), b.JobID)
	}
	s.render(w, r, "backup", "Respaldo", "respaldos", data)
}

func (s *Server) backupVerify(w http.ResponseWriter, r *http.Request) {
	b, _ := s.backupOr404(w, r)
	if b == nil {
		return
	}
	j, err := s.A.Jobs.Enqueue(r.Context(), core.JobBackupVerify, b.LibraryID, core.VerifyParams{BackupID: b.ID}, principalOf(r).Actor(), jobs.Priority(60))
	if err != nil {
		s.fail(w, r, "/respaldos/"+b.ID, err)
		return
	}
	s.ok(w, r, "/tareas/"+j.ID, "")
}

func (s *Server) backupPin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pin := r.FormValue("pin") == "1"
	if err := s.A.Svc.SetPinned(r.Context(), principalOf(r).Actor(), id, pin); err != nil {
		s.fail(w, r, "/respaldos/"+id, err)
		return
	}
	msg := "Respaldo fijado: la retención no lo borrará."
	if !pin {
		msg = "Respaldo suelto: la retención lo trata como los demás."
	}
	s.ok(w, r, "/respaldos/"+id, msg)
}

func (s *Server) backupDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := s.A.Svc.Backup(r.Context(), id)
	if err != nil {
		s.fail(w, r, "/respaldos", err)
		return
	}
	if err := s.A.Svc.DeleteBackup(r.Context(), principalOf(r).Actor(), id); err != nil {
		s.fail(w, r, "/respaldos/"+id, err)
		return
	}
	s.ok(w, r, "/respaldos?biblioteca="+b.LibraryID, "Respaldo borrado.")
}

func (s *Server) backupDownload(w http.ResponseWriter, r *http.Request) {
	b, l := s.backupOr404(w, r)
	if b == nil {
		return
	}
	if b.Status != core.BackupOK {
		s.renderError(w, r, principalOf(r), http.StatusGone, "SQS-B001 El archivo de este respaldo ya no existe.")
		return
	}
	f, err := os.Open(s.A.Svc.AbsPath(b))
	if err != nil {
		s.renderError(w, r, principalOf(r), http.StatusNotFound, "SQS-B001 No se encontró el archivo: "+err.Error())
		return
	}
	defer f.Close()
	slug := "biblioteca"
	if l != nil {
		slug = l.Slug
	}
	s.A.Audit.Record(r.Context(), principalOf(r).Actor(), "backup.download", "backup", b.ID, "ok", nil)
	name := fmt.Sprintf("%s_%s.backup", slug, b.StartedAt.UTC().Format("20060102T150405Z"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Sqsolve-SHA256", b.SHA256)
	http.ServeContent(w, r, name, b.StartedAt, f)
}

func (s *Server) restoreForm(w http.ResponseWriter, r *http.Request) {
	b, l := s.backupOr404(w, r)
	if b == nil {
		return
	}
	data := map[string]any{"B": b, "L": l, "Mode": r.URL.Query().Get("modo")}
	if l != nil {
		for k, v := range s.confirmData(r, l) {
			data[k] = v
		}
		data["B"] = b
		data["SuggestSlug"] = core.SuggestSlug(l.Slug + " copia " + b.StartedAt.In(s.A.Cfg.Location).Format("0102"))
		data["SuggestName"] = l.DisplayName + " (copia del " + b.StartedAt.In(s.A.Cfg.Location).Format("2006-01-02") + ")"
		data["CanReplace"] = l.State == core.StateActive || l.State == core.StateMaintenance
		data["SchemaDiffers"] = l.Fingerprint != "" && b.Fingerprint != "" && l.Fingerprint != b.Fingerprint
	}
	s.render(w, r, "restore", "Restaurar respaldo", "respaldos", data)
}

func (s *Server) restoreSubmit(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id := r.PathValue("id")
	back := "/respaldos/" + id + "/restaurar"
	if r.FormValue("mode") == "reemplazar" {
		if !p.Can("restore.replace") {
			s.renderError(w, r, p, http.StatusForbidden, "SQS-A030 Tu rol no permite reemplazar una biblioteca.")
			return
		}
		if !p.FreshAuth() {
			s.flash(w, "info", "Confirma tu contraseña y vuelve a enviar el formulario.")
			http.Redirect(w, r, "/confirmar?next="+urlq(back+"?modo=reemplazar"), http.StatusSeeOther)
			return
		}
		j, err := s.A.Svc.RestoreReplace(r.Context(), p.Actor(), id, strings.TrimSpace(r.FormValue("confirm")), r.FormValue("terminate") == "on")
		if err != nil {
			s.fail(w, r, back+"?modo=reemplazar", err)
			return
		}
		s.ok(w, r, "/tareas/"+j.ID, "")
		return
	}
	_, j, err := s.A.Svc.RestoreAsNew(r.Context(), p.Actor(), id, strings.TrimSpace(r.FormValue("slug")), r.FormValue("name"))
	if err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, "/tareas/"+j.ID, "")
}

func (s *Server) importForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	files, err := s.A.Svc.ImportCandidates()
	libs, _ := s.A.Svc.Libraries(ctx, core.ListOptions{})
	s.render(w, r, "import", "Importar respaldo", "respaldos", map[string]any{
		"Files": files, "Err": err, "Libraries": libs, "Dir": s.A.Svc.ImportDir(), "MaxMB": s.A.Svc.Settings(ctx).ImportMaxMB,
		"Selected": r.URL.Query().Get("biblioteca"),
	})
}

func (s *Server) importSubmit(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	back := "/respaldos/importar"
	mr := multipartOf(r)
	if mr == nil {
		j, err := s.A.Svc.Import(r.Context(), p.Actor(), r.FormValue("library"), r.FormValue("file"), r.FormValue("delete") == "on")
		if err != nil {
			s.fail(w, r, back, err)
			return
		}
		s.ok(w, r, "/tareas/"+j.ID, "")
		return
	}
	// A multi-gigabyte upload over a slow link outlives the server's
	// ReadTimeout; the size is still capped by ImportMaxMB.
	http.NewResponseController(w).SetReadDeadline(time.Now().Add(12 * time.Hour))
	fields := map[string]string{}
	uploaded := ""
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if uploaded != "" {
				os.Remove(filepath.Join(s.A.Svc.ImportDir(), uploaded))
			}
			s.fail(w, r, back, fmt.Errorf("SQS-B052 la subida se interrumpió: %w", err))
			return
		}
		if part.FormName() == "upload" {
			if part.FileName() == "" || uploaded != "" {
				io.Copy(io.Discard, part)
				continue
			}
			name, err := s.saveUpload(part)
			if err != nil {
				s.fail(w, r, back, err)
				return
			}
			uploaded = name
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(part, 4096))
		fields[part.FormName()] = string(b)
	}
	name, deleteSource := fields["file"], fields["delete"] == "on"
	if uploaded != "" {
		// The upload is only a transfer copy: it never stays in _import.
		name, deleteSource = uploaded, true
	}
	if name == "" {
		s.fail(w, r, back, errors.New("SQS-B050 elige un archivo de la carpeta de importación o súbelo"))
		return
	}
	j, err := s.A.Svc.Import(r.Context(), p.Actor(), fields["library"], name, deleteSource)
	if err != nil {
		if uploaded != "" {
			os.Remove(filepath.Join(s.A.Svc.ImportDir(), uploaded))
		}
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, "/tareas/"+j.ID, "")
}

// saveUpload streams a file part into the import folder of the backups
// volume and returns its name there.
func (s *Server) saveUpload(part *multipart.Part) (string, error) {
	base := filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
	if base == "." || base == "/" || base == ".." || strings.HasPrefix(base, ".") || strings.HasSuffix(base, ".partial") {
		return "", errors.New("SQS-B050 nombre de archivo inválido")
	}
	dir := s.A.Svc.ImportDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, base)
	if _, err := os.Lstat(dst); err == nil {
		return "", fmt.Errorf("SQS-B054 ya hay un archivo llamado %s en la carpeta de importación; impórtalo desde la lista o cámbiale el nombre", base)
	}
	out, err := os.OpenFile(dst+".partial", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("SQS-B052 no se pudo guardar la subida: %w", err)
	}
	if _, err := io.Copy(out, part); err != nil {
		out.Close()
		os.Remove(dst + ".partial")
		return "", fmt.Errorf("SQS-B052 la subida se interrumpió: %s", humanError(err))
	}
	if err := out.Close(); err != nil {
		os.Remove(dst + ".partial")
		return "", err
	}
	if err := fsx.SealFile(dst+".partial", dst); err != nil {
		os.Remove(dst + ".partial")
		return "", err
	}
	return base, nil
}
