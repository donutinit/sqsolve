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
	"net/http"
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
			return fmt.Errorf("template %s: %w", name, err)
		}
		s.pages[name] = t
	}
	return nil
}

// setSecurityHeaders sets the headers every response carries (docs 08 §10).
// HSTS only makes sense when the panel itself serves HTTPS.
func setSecurityHeaders(h http.Header, secure bool) {
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'; object-src 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if secure {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

// ServeHTTP applies the common middleware.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	setSecurityHeaders(w.Header(), s.secure())
	rw := &statusWriter{ResponseWriter: w, status: 200}
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("panic in a request", "path", r.URL.Path, "panic", p)
			http.Error(rw, "Internal error (SQS-X999)", http.StatusInternalServerError)
		}
		if !quietPath(r.URL.Path) {
			s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rw.status, "ms", time.Since(start).Milliseconds(), "ip", s.clientIP(r))
		}
	}()
	if r.Method == http.MethodPost && !strings.HasPrefix(r.URL.Path, "/api/") {
		limit := int64(1 << 20)
		if r.URL.Path == "/backups/import" {
			limit = int64(s.A.Svc.Settings(r.Context()).ImportMaxMB) << 20
		}
		r.Body = http.MaxBytesReader(rw, r.Body, limit)
	}
	s.mux.ServeHTTP(rw, r)
}

// quietPath reports the requests that are not logged: static files and the
// health probes, which Docker and uptime monitors call every few seconds.
func quietPath(p string) bool {
	switch p {
	case "/healthz", "/readyz", "/api/v1/system/health":
		return true
	}
	return strings.HasPrefix(p, "/static/")
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

// clientIP honours X-Forwarded-For only from trusted proxies, with the same
// rules as the API (auth.ClientIP).
func (s *Server) clientIP(r *http.Request) string {
	return auth.ClientIP(r, s.A.Cfg.TrustedProxies)
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

// totpEnrollPath is the enrollment page an administrator without a second
// factor is sent to while the installation requires one; enrollmentPaths are
// the only pages such a session may reach (docs 08 §4).
const totpEnrollPath = "/account/totp"

var enrollmentPaths = map[string]bool{totpEnrollPath: true, "/account/totp/start": true, "/account/totp/enable": true, "/logout": true}

// mustEnroll reports whether p is an administrator who has to enable the
// second factor before using the panel.
func (s *Server) mustEnroll(ctx context.Context, p *auth.Principal) bool {
	return auth.MustEnrollTOTP(p, func() bool { return s.A.Svc.Settings(ctx).TOTPRequiredAdmins })
}

// page wraps a handler that needs a logged-in user with the action. An
// administrator without the second factor the installation requires only
// reaches the enrollment pages.
func (s *Server) page(action auth.Action, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if has, err := s.A.Auth.HasAdmin(r.Context()); err == nil && !has {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		p, raw := s.principal(r, false)
		if p == nil {
			if raw != "" {
				if _, err := s.A.Auth.Session(r.Context(), raw, s.clientIP(r), true); err == nil {
					http.Redirect(w, r, "/login/code", http.StatusSeeOther)
					return
				}
			}
			next := r.URL.RequestURI()
			http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
			return
		}
		if !enrollmentPaths[r.URL.Path] && s.mustEnroll(r.Context(), p) {
			http.Redirect(w, r, totpEnrollPath, http.StatusSeeOther)
			return
		}
		if !p.Can(action) {
			s.renderError(w, r, p, http.StatusForbidden, "SQS-A030 Your role does not allow this action.")
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
				s.renderError(w, r, p, http.StatusForbidden, "SQS-A031 The form expired or did not come from this panel. Reload the page and try again.")
				return
			}
		}
		h(w, withPrincipal(r, p))
	}
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
		http.Error(w, "unknown template: "+name, 500)
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
		http.Error(w, "Error rendering the page (SQS-X998)", 500)
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
		return "SQS-C060 The request body is too large"
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

	m.HandleFunc("GET /setup", s.setupForm)
	m.HandleFunc("POST /setup", s.setupSubmit)
	m.HandleFunc("POST /setup/recover", s.setupRecover)
	m.HandleFunc("GET /setup/kit", s.page(auth.ActView, s.setupKit))
	m.HandleFunc("GET /login", s.loginForm)
	m.HandleFunc("POST /login", s.loginSubmit)
	m.HandleFunc("GET /login/code", s.totpForm)
	m.HandleFunc("POST /login/code", s.totpSubmit)
	m.HandleFunc("POST /logout", s.page(auth.ActView, s.logout))

	m.HandleFunc("GET /{$}", s.page(auth.ActView, s.dashboard))
	m.HandleFunc("GET /libraries", s.page(auth.ActView, s.libraries))
	m.HandleFunc("GET /libraries/new", s.page(auth.ActLibraryCreate, s.libraryNewForm))
	m.HandleFunc("POST /libraries/new", s.page(auth.ActLibraryCreate, s.libraryNewSubmit))
	m.HandleFunc("GET /libraries/{id}", s.page(auth.ActView, s.library))
	m.HandleFunc("GET /libraries/{id}/instructions", s.page(auth.ActView, s.libraryInstructions))
	m.HandleFunc("POST /libraries/{id}/settings", s.page(auth.ActLibraryEdit, s.librarySettings))
	m.HandleFunc("POST /libraries/{id}/policy", s.page(auth.ActPolicyEdit, s.libraryPolicy))
	m.HandleFunc("POST /libraries/{id}/backup", s.page(auth.ActBackupCreate, s.libraryBackup))
	m.HandleFunc("POST /libraries/{id}/maintenance", s.page(auth.ActLibraryMaintenance, s.libraryMaintenance))
	m.HandleFunc("POST /libraries/{id}/window", s.page(auth.ActLibraryEdit, s.libraryReopen))
	m.HandleFunc("POST /libraries/{id}/password/reveal", s.page(auth.ActLibraryReveal, s.libraryReveal))
	m.HandleFunc("POST /libraries/{id}/password/rotate", s.page(auth.ActLibraryCredentials, s.libraryRotate))
	m.HandleFunc("POST /libraries/{id}/people", s.page(auth.ActLibraryCredentials, s.libraryAddMember))
	m.HandleFunc("POST /libraries/{id}/people/{cid}/revoke", s.page(auth.ActLibraryCredentials, s.libraryRevokeMember))
	m.HandleFunc("POST /libraries/{id}/networks", s.page(auth.ActLibraryCredentials, s.libraryNetwork))
	m.HandleFunc("POST /libraries/{id}/sessions/{pid}/terminate", s.page(auth.ActSessionTerminate, s.librarySessionTerminate))
	m.HandleFunc("GET /libraries/{id}/delete", s.page(auth.ActLibraryDelete, s.libraryDeleteForm))
	m.HandleFunc("POST /libraries/{id}/delete", s.page(auth.ActLibraryDelete, s.libraryDeleteSubmit))
	m.HandleFunc("POST /libraries/{id}/untrash", s.page(auth.ActLibraryDelete, s.libraryUntrash))
	m.HandleFunc("GET /libraries/{id}/purge", s.page(auth.ActLibraryDelete, s.libraryPurgeForm))
	m.HandleFunc("POST /libraries/{id}/purge", s.page(auth.ActLibraryDelete, s.libraryPurgeSubmit))
	m.HandleFunc("GET /libraries/{id}/revert", s.page(auth.ActRestoreRevert, s.libraryRevertForm))
	m.HandleFunc("POST /libraries/{id}/revert", s.page(auth.ActRestoreRevert, s.libraryRevertSubmit))
	m.HandleFunc("POST /libraries/{id}/recreate", s.page(auth.ActRestoreReplace, s.libraryRecreate))
	m.HandleFunc("GET /libraries/{id}/export", s.page(auth.ActView, s.libraryExport))
	m.HandleFunc("GET /libraries/{id}/export/projects/{pid}", s.page(auth.ActLibraryExport, s.libraryExportProject))
	m.HandleFunc("GET /libraries/{id}/export/stills/{sid}", s.page(auth.ActLibraryExport, s.libraryExportStill))
	m.HandleFunc("GET /libraries/{id}/export/albums/{vid}", s.page(auth.ActLibraryExport, s.libraryExportAlbum))

	m.HandleFunc("GET /backups", s.page(auth.ActView, s.backups))
	m.HandleFunc("GET /backups/import", s.page(auth.ActBackupImport, s.importForm))
	m.HandleFunc("POST /backups/import", s.page(auth.ActBackupImport, s.importSubmit))
	m.HandleFunc("GET /backups/{id}", s.page(auth.ActView, s.backup))
	m.HandleFunc("POST /backups/{id}/verify", s.page(auth.ActBackupVerify, s.backupVerify))
	m.HandleFunc("POST /backups/{id}/pin", s.page(auth.ActBackupPin, s.backupPin))
	m.HandleFunc("POST /backups/{id}/delete", s.page(auth.ActLibraryDelete, s.backupDelete))
	m.HandleFunc("GET /backups/{id}/download", s.page(auth.ActBackupDownload, s.backupDownload))
	m.HandleFunc("GET /backups/{id}/export", s.page(auth.ActView, s.backupExport))
	m.HandleFunc("POST /backups/{id}/export/prepare", s.page(auth.ActLibraryExport, s.backupExportPrepare))
	m.HandleFunc("POST /backups/{id}/export/close", s.page(auth.ActLibraryExport, s.backupExportClose))
	m.HandleFunc("GET /backups/{id}/export/projects/{pid}", s.page(auth.ActLibraryExport, s.backupExportProject))
	m.HandleFunc("GET /backups/{id}/export/stills/{sid}", s.page(auth.ActLibraryExport, s.backupExportStill))
	m.HandleFunc("GET /backups/{id}/export/albums/{vid}", s.page(auth.ActLibraryExport, s.backupExportAlbum))
	m.HandleFunc("GET /backups/{id}/restore", s.page(auth.ActRestoreNew, s.restoreForm))
	m.HandleFunc("POST /backups/{id}/restore", s.page(auth.ActRestoreNew, s.restoreSubmit))

	m.HandleFunc("GET /jobs", s.page(auth.ActView, s.jobsList))
	m.HandleFunc("GET /jobs/{id}", s.page(auth.ActView, s.job))
	m.HandleFunc("GET /jobs/{id}/events", s.page(auth.ActView, s.jobEvents))
	m.HandleFunc("POST /jobs/{id}/cancel", s.page(auth.ActJobCancel, s.jobCancel))
	m.HandleFunc("POST /jobs/{id}/retry", s.page(auth.ActBackupCreate, s.jobRetry))

	m.HandleFunc("GET /activity", s.page(auth.ActView, s.activity))
	m.HandleFunc("GET /activity/audit.jsonl", s.page(auth.ActAuditView, s.auditExport))
	m.HandleFunc("GET /health", s.page(auth.ActView, s.healthPage))

	m.HandleFunc("GET /settings", s.page(auth.ActView, s.settings))
	m.HandleFunc("POST /settings/general", s.page(auth.ActSettingsEdit, s.settingsGeneral))
	m.HandleFunc("POST /settings/certificate", s.page(auth.ActSettingsEdit, s.settingsCert))
	m.HandleFunc("POST /settings/users", s.page(auth.ActUsersManage, s.userCreate))
	m.HandleFunc("POST /settings/users/{id}", s.page(auth.ActUsersManage, s.userUpdate))
	m.HandleFunc("POST /settings/users/{id}/password", s.page(auth.ActUsersManage, s.userPassword))
	m.HandleFunc("POST /settings/users/{id}/totp", s.page(auth.ActUsersManage, s.userDisableTOTP))
	m.HandleFunc("POST /settings/notifications", s.page(auth.ActNotifyManage, s.notifySave))
	m.HandleFunc("POST /settings/notifications/{id}/test", s.page(auth.ActNotifyManage, s.notifyTest))
	m.HandleFunc("POST /settings/notifications/{id}/delete", s.page(auth.ActNotifyManage, s.notifyDelete))
	m.HandleFunc("POST /settings/maintenance/{action}", s.page(auth.ActSettingsEdit, s.maintenanceAction))
	m.HandleFunc("GET /settings/kit", s.page(auth.ActSettingsEdit, s.recoveryKit))
	m.HandleFunc("GET /settings/diagnostics", s.page(auth.ActDiagnostics, s.diagnostics))

	m.HandleFunc("GET /account", s.page(auth.ActView, s.account))
	m.HandleFunc("POST /account/password", s.page(auth.ActView, s.accountPassword))
	m.HandleFunc("GET /account/totp", s.page(auth.ActView, s.accountTOTPBegin))
	m.HandleFunc("POST /account/totp/start", s.page(auth.ActView, s.accountTOTPBegin))
	m.HandleFunc("POST /account/totp/enable", s.page(auth.ActView, s.accountTOTPEnable))
	m.HandleFunc("POST /account/totp/disable", s.page(auth.ActView, s.accountTOTPDisable))
	m.HandleFunc("POST /account/tokens", s.page(auth.ActTokensOwn, s.tokenCreate))
	m.HandleFunc("POST /account/tokens/{id}/revoke", s.page(auth.ActTokensOwn, s.tokenRevoke))
	m.HandleFunc("POST /account/sessions", s.page(auth.ActView, s.logoutEverywhere))
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	var problems []string
	if !s.A.PGAlive(r.Context()) {
		problems = append(problems, "postgresql")
	}
	if err := s.A.Svc.BackupVolumeOK(); err != nil {
		problems = append(problems, "backup volume")
	}
	if len(problems) > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, "not ready: %s\n", strings.Join(problems, ", "))
		return
	}
	w.Write([]byte("ready\n"))
}
