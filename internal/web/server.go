// SPDX-License-Identifier: AGPL-3.0-only

// Package web serves the administration panel: server-rendered HTML, plain
// forms with the post/redirect/get pattern and a small script for live job
// progress (Server-Sent Events). No third-party JavaScript.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/auth"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	sessionCookie = "sqsolve_session"
	preCookie     = "sqsolve_pre"
	flashCookie   = "sqsolve_flash"
)

// Server is the web panel.
type Server struct {
	A     *app.App
	log   *slog.Logger
	pages map[string]*template.Template
	mux   *http.ServeMux
}

// New builds the handler. api registers the JSON API routes.
func New(a *app.App, api func(*http.ServeMux)) (*Server, error) {
	s := &Server{A: a, log: a.Log, mux: http.NewServeMux()}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.routes()
	if api != nil {
		api(s.mux)
	}
	return s, nil
}

func (s *Server) parseTemplates() error {
	s.pages = map[string]*template.Template{}
	base, err := template.New("base").Funcs(funcs(s.A)).ParseFS(templatesFS, "templates/layout.html", "templates/partials.html")
	if err != nil {
		return err
	}
	entries, err := fs.Glob(templatesFS, "templates/*.html")
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := strings.TrimSuffix(strings.TrimPrefix(e, "templates/"), ".html")
		if name == "layout" || name == "partials" {
			continue
		}
		t, err := template.Must(base.Clone()).ParseFS(templatesFS, e)
		if err != nil {
			return fmt.Errorf("plantilla %s: %w", name, err)
		}
		s.pages[name] = t
	}
	return nil
}

// ServeHTTP applies the common middleware.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'; object-src 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	rw := &statusWriter{ResponseWriter: w, status: 200}
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("pánico en una petición", "path", r.URL.Path, "panic", p)
			http.Error(rw, "Error interno (SQS-X999)", http.StatusInternalServerError)
		}
		if !strings.HasPrefix(r.URL.Path, "/static/") && r.URL.Path != "/healthz" {
			s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rw.status, "ms", time.Since(start).Milliseconds(), "ip", s.clientIP(r))
		}
	}()
	if r.Method == http.MethodPost && !strings.HasPrefix(r.URL.Path, "/api/") {
		limit := int64(1 << 20)
		if r.URL.Path == "/respaldos/importar" {
			limit = int64(s.A.Svc.Settings(r.Context()).ImportMaxMB) << 20
		}
		r.Body = http.MaxBytesReader(rw, r.Body, limit)
	}
	s.mux.ServeHTTP(rw, r)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// clientIP honours X-Forwarded-For only from trusted proxies.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	for _, p := range s.A.Cfg.TrustedProxies {
		if p.Contains(addr) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				return strings.TrimSpace(parts[len(parts)-1])
			}
		}
	}
	return addr.String()
}

func (s *Server) secure() bool { return !s.A.Cfg.InsecureHTTP }

func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteStrictMode})
}

// principal resolves the session; nil if not logged in.
func (s *Server) principal(r *http.Request, allowPending bool) (*auth.Principal, string) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, ""
	}
	p, err := s.A.Auth.Session(r.Context(), c.Value, s.clientIP(r), allowPending)
	if err != nil {
		return nil, c.Value
	}
	return p, c.Value
}

type ctxKey int

const (
	principalKey ctxKey = iota + 1
	multipartKey
)

func isMultipart(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data")
}

// multipartOf returns the reader left by page after checking the CSRF part.
func multipartOf(r *http.Request) *multipart.Reader {
	mr, _ := r.Context().Value(multipartKey).(*multipart.Reader)
	return mr
}

func withPrincipal(r *http.Request, p *auth.Principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalKey, p))
}

func principalOf(r *http.Request) *auth.Principal {
	p, _ := r.Context().Value(principalKey).(*auth.Principal)
	return p
}

// page wraps a handler that needs a logged-in user with the action.
func (s *Server) page(action auth.Action, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if has, err := s.A.Auth.HasAdmin(r.Context()); err == nil && !has {
			http.Redirect(w, r, "/instalar", http.StatusSeeOther)
			return
		}
		p, raw := s.principal(r, false)
		if p == nil {
			if raw != "" {
				if _, err := s.A.Auth.Session(r.Context(), raw, s.clientIP(r), true); err == nil {
					http.Redirect(w, r, "/entrar/codigo", http.StatusSeeOther)
					return
				}
			}
			next := r.URL.RequestURI()
			http.Redirect(w, r, "/entrar?next="+url.QueryEscape(next), http.StatusSeeOther)
			return
		}
		if !p.Can(action) {
			s.renderError(w, r, p, http.StatusForbidden, "SQS-A030 Tu rol no permite esta acción.")
			return
		}
		if r.Method == http.MethodPost {
			ok := false
			if isMultipart(r) {
				// Multipart bodies are streamed by the handler; parsing them here
				// would spill uploads to /tmp. The token must be the first part.
				if mr, err := r.MultipartReader(); err == nil {
					if part, err := mr.NextPart(); err == nil && part.FormName() == "csrf" {
						b, _ := io.ReadAll(io.LimitReader(part, 512))
						if ok = s.A.Auth.CheckCSRF(raw, string(b)); ok {
							r = r.WithContext(context.WithValue(r.Context(), multipartKey, mr))
						}
					}
				}
			} else {
				ok = s.A.Auth.CheckCSRF(raw, r.FormValue("csrf"))
			}
			if !ok {
				s.renderError(w, r, p, http.StatusForbidden, "SQS-A031 El formulario venció o no viene de este panel. Recarga la página e inténtalo de nuevo.")
				return
			}
		}
		h(w, withPrincipal(r, p))
	}
}

// dangerous requires a recent reauthentication before running h.
func (s *Server) dangerous(action auth.Action, h http.HandlerFunc) http.HandlerFunc {
	return s.page(action, func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r)
		if !p.FreshAuth() {
			next := r.URL.RequestURI()
			if r.Method == http.MethodPost {
				if ref := r.Header.Get("Referer"); ref != "" {
					if u, err := url.Parse(ref); err == nil && u.Host == r.Host {
						next = u.RequestURI()
					}
				}
			}
			s.flash(w, "info", "Confirma tu contraseña para continuar.")
			http.Redirect(w, r, "/confirmar?next="+url.QueryEscape(next), http.StatusSeeOther)
			return
		}
		h(w, r)
	})
}

type flashMsg struct {
	Kind string `json:"k"`
	Msg  string `json:"m"`
}

func (s *Server) flash(w http.ResponseWriter, kind, msg string) {
	b, _ := json.Marshal(flashMsg{kind, msg})
	s.setCookie(w, flashCookie, base64.RawURLEncoding.EncodeToString(b), 60)
}

func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) *flashMsg {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	s.setCookie(w, flashCookie, "", -1)
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	var f flashMsg
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	return &f
}

// View is the data every template receives.
type View struct {
	Title    string
	Section  string
	P        *auth.Principal
	CSRF     string
	Flash    *flashMsg
	Data     any
	Running  int
	Queued   int
	PGReady  bool
	Version  string
	Instance string
	Now      time.Time
	Path     string
	Query    url.Values
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, title, section string, data any) {
	s.renderStatus(w, r, http.StatusOK, name, title, section, data)
}

// renderStatus sets every header (including the cookie that consumes the
// flash) before writing the status line.
func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, status int, name, title, section string, data any) {
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "plantilla desconocida: "+name, 500)
		return
	}
	p := principalOf(r)
	v := View{Title: title, Section: section, P: p, Data: data, Version: app.Version, Now: time.Now(), Path: r.URL.Path, Query: r.URL.Query(),
		PGReady: s.A.PGReady(), Instance: s.A.Svc.Settings(r.Context()).InstanceName}
	if p != nil {
		v.CSRF = p.CSRF
		v.Queued, v.Running, _ = s.A.Jobs.Counts(r.Context())
	}
	v.Flash = s.takeFlash(w, r)
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", v); err != nil {
		s.log.Error("render", "page", name, "err", err)
		http.Error(w, "Error al mostrar la página (SQS-X998)", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, p *auth.Principal, status int, msg string) {
	if p != nil {
		r = withPrincipal(r, p)
	}
	s.renderStatus(w, r, status, "error", "Error", "", map[string]any{"Status": status, "Message": msg})
}

// fail shows an error from a service as a flash and redirects back.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, back string, err error) {
	s.flash(w, "error", humanError(err))
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) ok(w http.ResponseWriter, r *http.Request, to, msg string) {
	if msg != "" {
		s.flash(w, "ok", msg)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func humanError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return "SQS-C060 El envío es demasiado grande"
	}
	if len(msg) > 600 {
		msg = msg[:600] + "..."
	}
	return msg
}

func randomToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// preToken is a double-submit token for forms before login.
func (s *Server) preToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(preCookie); err == nil && len(c.Value) >= 20 {
		return c.Value
	}
	t := randomToken()
	s.setCookie(w, preCookie, t, 3600)
	return t
}

func (s *Server) checkPre(r *http.Request) bool {
	c, err := r.Cookie(preCookie)
	return err == nil && len(c.Value) >= 20 && c.Value == r.FormValue("csrf")
}

func (s *Server) staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	fsrv := http.FileServer(http.FS(sub))
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fsrv.ServeHTTP(w, r)
	}))
}

func (s *Server) routes() {
	m := s.mux
	m.Handle("GET /static/", s.staticHandler())
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	m.HandleFunc("GET /readyz", s.readyz)
	m.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.svg", http.StatusMovedPermanently)
	})

	m.HandleFunc("GET /instalar", s.setupForm)
	m.HandleFunc("POST /instalar", s.setupSubmit)
	m.HandleFunc("POST /instalar/recuperar", s.setupRecover)
	m.HandleFunc("GET /instalar/kit", s.page(auth.ActView, s.setupKit))
	m.HandleFunc("GET /entrar", s.loginForm)
	m.HandleFunc("POST /entrar", s.loginSubmit)
	m.HandleFunc("GET /entrar/codigo", s.totpForm)
	m.HandleFunc("POST /entrar/codigo", s.totpSubmit)
	m.HandleFunc("POST /salir", s.page(auth.ActView, s.logout))
	m.HandleFunc("GET /confirmar", s.page(auth.ActView, s.reauthForm))
	m.HandleFunc("POST /confirmar", s.page(auth.ActView, s.reauthSubmit))

	m.HandleFunc("GET /{$}", s.page(auth.ActView, s.dashboard))
	m.HandleFunc("GET /bibliotecas", s.page(auth.ActView, s.libraries))
	m.HandleFunc("GET /bibliotecas/nueva", s.page(auth.ActLibraryCreate, s.libraryNewForm))
	m.HandleFunc("POST /bibliotecas/nueva", s.page(auth.ActLibraryCreate, s.libraryNewSubmit))
	m.HandleFunc("GET /bibliotecas/{id}", s.page(auth.ActView, s.library))
	m.HandleFunc("GET /bibliotecas/{id}/instrucciones", s.page(auth.ActView, s.libraryInstructions))
	m.HandleFunc("POST /bibliotecas/{id}/ajustes", s.page(auth.ActLibraryEdit, s.librarySettings))
	m.HandleFunc("POST /bibliotecas/{id}/politica", s.page(auth.ActPolicyEdit, s.libraryPolicy))
	m.HandleFunc("POST /bibliotecas/{id}/respaldar", s.page(auth.ActBackupCreate, s.libraryBackup))
	m.HandleFunc("POST /bibliotecas/{id}/mantenimiento", s.page(auth.ActLibraryMaintenance, s.libraryMaintenance))
	m.HandleFunc("POST /bibliotecas/{id}/ventana", s.page(auth.ActLibraryEdit, s.libraryReopen))
	m.HandleFunc("POST /bibliotecas/{id}/contrasena/ver", s.dangerous(auth.ActLibraryReveal, s.libraryReveal))
	m.HandleFunc("POST /bibliotecas/{id}/contrasena/rotar", s.dangerous(auth.ActLibraryCredentials, s.libraryRotate))
	m.HandleFunc("POST /bibliotecas/{id}/personas", s.dangerous(auth.ActLibraryCredentials, s.libraryAddMember))
	m.HandleFunc("POST /bibliotecas/{id}/personas/{cid}/revocar", s.dangerous(auth.ActLibraryCredentials, s.libraryRevokeMember))
	m.HandleFunc("POST /bibliotecas/{id}/redes", s.dangerous(auth.ActLibraryCredentials, s.libraryNetwork))
	m.HandleFunc("POST /bibliotecas/{id}/sesiones/{pid}/cerrar", s.page(auth.ActSessionTerminate, s.librarySessionTerminate))
	m.HandleFunc("GET /bibliotecas/{id}/eliminar", s.dangerous(auth.ActLibraryDelete, s.libraryDeleteForm))
	m.HandleFunc("POST /bibliotecas/{id}/eliminar", s.dangerous(auth.ActLibraryDelete, s.libraryDeleteSubmit))
	m.HandleFunc("POST /bibliotecas/{id}/recuperar", s.page(auth.ActLibraryDelete, s.libraryUntrash))
	m.HandleFunc("GET /bibliotecas/{id}/purgar", s.dangerous(auth.ActLibraryDelete, s.libraryPurgeForm))
	m.HandleFunc("POST /bibliotecas/{id}/purgar", s.dangerous(auth.ActLibraryDelete, s.libraryPurgeSubmit))
	m.HandleFunc("GET /bibliotecas/{id}/revertir", s.dangerous(auth.ActRestoreRevert, s.libraryRevertForm))
	m.HandleFunc("POST /bibliotecas/{id}/revertir", s.dangerous(auth.ActRestoreRevert, s.libraryRevertSubmit))
	m.HandleFunc("POST /bibliotecas/{id}/recrear", s.page(auth.ActRestoreReplace, s.libraryRecreate))
	m.HandleFunc("GET /bibliotecas/{id}/exportar", s.page(auth.ActView, s.libraryExport))
	m.HandleFunc("GET /bibliotecas/{id}/exportar/proyectos/{pid}", s.page(auth.ActLibraryExport, s.libraryExportProject))
	m.HandleFunc("GET /bibliotecas/{id}/exportar/stills/{sid}", s.page(auth.ActLibraryExport, s.libraryExportStill))
	m.HandleFunc("GET /bibliotecas/{id}/exportar/albumes/{vid}", s.page(auth.ActLibraryExport, s.libraryExportAlbum))

	m.HandleFunc("GET /respaldos", s.page(auth.ActView, s.backups))
	m.HandleFunc("GET /respaldos/importar", s.page(auth.ActBackupImport, s.importForm))
	m.HandleFunc("POST /respaldos/importar", s.page(auth.ActBackupImport, s.importSubmit))
	m.HandleFunc("GET /respaldos/{id}", s.page(auth.ActView, s.backup))
	m.HandleFunc("POST /respaldos/{id}/verificar", s.page(auth.ActBackupVerify, s.backupVerify))
	m.HandleFunc("POST /respaldos/{id}/fijar", s.page(auth.ActBackupPin, s.backupPin))
	m.HandleFunc("POST /respaldos/{id}/borrar", s.dangerous(auth.ActLibraryDelete, s.backupDelete))
	m.HandleFunc("GET /respaldos/{id}/descargar", s.page(auth.ActBackupDownload, s.backupDownload))
	m.HandleFunc("GET /respaldos/{id}/restaurar", s.page(auth.ActRestoreNew, s.restoreForm))
	m.HandleFunc("POST /respaldos/{id}/restaurar", s.page(auth.ActRestoreNew, s.restoreSubmit))

	m.HandleFunc("GET /tareas", s.page(auth.ActView, s.jobsList))
	m.HandleFunc("GET /tareas/{id}", s.page(auth.ActView, s.job))
	m.HandleFunc("GET /tareas/{id}/eventos", s.page(auth.ActView, s.jobEvents))
	m.HandleFunc("POST /tareas/{id}/cancelar", s.page(auth.ActJobCancel, s.jobCancel))
	m.HandleFunc("POST /tareas/{id}/reintentar", s.page(auth.ActBackupCreate, s.jobRetry))

	m.HandleFunc("GET /actividad", s.page(auth.ActView, s.activity))
	m.HandleFunc("GET /actividad/auditoria.jsonl", s.page(auth.ActAuditView, s.auditExport))
	m.HandleFunc("GET /salud", s.page(auth.ActView, s.healthPage))

	m.HandleFunc("GET /ajustes", s.page(auth.ActView, s.settings))
	m.HandleFunc("POST /ajustes/general", s.page(auth.ActSettingsEdit, s.settingsGeneral))
	m.HandleFunc("POST /ajustes/certificado", s.dangerous(auth.ActSettingsEdit, s.settingsCert))
	m.HandleFunc("POST /ajustes/usuarios", s.dangerous(auth.ActUsersManage, s.userCreate))
	m.HandleFunc("POST /ajustes/usuarios/{id}", s.dangerous(auth.ActUsersManage, s.userUpdate))
	m.HandleFunc("POST /ajustes/usuarios/{id}/contrasena", s.dangerous(auth.ActUsersManage, s.userPassword))
	m.HandleFunc("POST /ajustes/usuarios/{id}/totp", s.dangerous(auth.ActUsersManage, s.userDisableTOTP))
	m.HandleFunc("POST /ajustes/notificaciones", s.page(auth.ActNotifyManage, s.notifySave))
	m.HandleFunc("POST /ajustes/notificaciones/{id}/probar", s.page(auth.ActNotifyManage, s.notifyTest))
	m.HandleFunc("POST /ajustes/notificaciones/{id}/borrar", s.page(auth.ActNotifyManage, s.notifyDelete))
	m.HandleFunc("POST /ajustes/mantenimiento/{accion}", s.page(auth.ActSettingsEdit, s.maintenanceAction))
	m.HandleFunc("GET /ajustes/kit", s.dangerous(auth.ActSettingsEdit, s.recoveryKit))
	m.HandleFunc("GET /ajustes/diagnostico", s.page(auth.ActDiagnostics, s.diagnostics))

	m.HandleFunc("GET /cuenta", s.page(auth.ActView, s.account))
	m.HandleFunc("POST /cuenta/contrasena", s.dangerous(auth.ActView, s.accountPassword))
	m.HandleFunc("POST /cuenta/totp/iniciar", s.dangerous(auth.ActView, s.accountTOTPBegin))
	m.HandleFunc("POST /cuenta/totp/activar", s.page(auth.ActView, s.accountTOTPEnable))
	m.HandleFunc("POST /cuenta/totp/quitar", s.dangerous(auth.ActView, s.accountTOTPDisable))
	m.HandleFunc("POST /cuenta/tokens", s.dangerous(auth.ActTokensOwn, s.tokenCreate))
	m.HandleFunc("POST /cuenta/tokens/{id}/revocar", s.page(auth.ActTokensOwn, s.tokenRevoke))
	m.HandleFunc("POST /cuenta/sesiones", s.page(auth.ActView, s.logoutEverywhere))
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	var problems []string
	if !s.A.PGAlive(r.Context()) {
		problems = append(problems, "postgresql")
	}
	if err := s.A.Svc.BackupVolumeOK(); err != nil {
		problems = append(problems, "volumen de respaldos")
	}
	if len(problems) > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, "no listo: %s\n", strings.Join(problems, ", "))
		return
	}
	w.Write([]byte("listo\n"))
}
