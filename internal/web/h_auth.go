// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
)

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	return next
}

func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

func (s *Server) setupForm(w http.ResponseWriter, r *http.Request) {
	if has, _ := s.A.Auth.HasAdmin(r.Context()); has {
		http.Redirect(w, r, "/entrar", http.StatusSeeOther)
		return
	}
	data := map[string]any{
		"Pre":        s.preToken(w, r),
		"Host":       requestHost(r),
		"InstanceID": s.A.InstanceID,
		"Copies":     len(app.SystemCopies(s.A.Cfg)),
		"Form":       r.URL.Query(),
		"TZ":         s.A.Cfg.Location.String(),
	}
	if info, err := s.A.PanelCertInfo(); err == nil {
		data["Fingerprint"] = info.Fingerprint
	}
	s.render(w, r, "setup", "Instalación", "", data)
}

func (s *Server) checkInstallCode(code string) bool {
	want := s.A.InstallCode()
	code = strings.ToUpper(strings.TrimSpace(code))
	return want != "" && subtle.ConstantTimeCompare([]byte(code), []byte(want)) == 1
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	back := func(msg string) {
		s.flash(w, "error", msg)
		q := url.Values{"username": {r.FormValue("username")}, "host": {r.FormValue("host")}, "name": {r.FormValue("name")}}
		http.Redirect(w, r, "/instalar?"+q.Encode(), http.StatusSeeOther)
	}
	if has, _ := s.A.Auth.HasAdmin(ctx); has {
		http.Redirect(w, r, "/entrar", http.StatusSeeOther)
		return
	}
	if !s.checkPre(r) {
		back("El formulario venció; vuelve a intentarlo.")
		return
	}
	if !s.checkInstallCode(r.FormValue("code")) {
		s.A.Audit.Record(ctx, audit.Actor{Type: "anonymous", IP: s.clientIP(r)}, "setup.code", "system", "", audit.Denied, nil)
		time.Sleep(time.Second)
		back("El código de instalación no es correcto. Búscalo en el registro del contenedor sqsolve.")
		return
	}
	if r.FormValue("password") != r.FormValue("password2") {
		back("Las contraseñas no coinciden.")
		return
	}
	u, err := s.A.Auth.CreateUser(ctx, audit.Actor{Type: "setup", Name: "instalación", IP: s.clientIP(r)}, r.FormValue("username"), r.FormValue("display"), r.FormValue("password"), auth.RoleAdmin)
	if err != nil {
		back(humanError(err))
		return
	}
	st := s.A.Svc.Settings(ctx)
	st.AdvertisedHost = strings.TrimSpace(r.FormValue("host"))
	if n := strings.TrimSpace(r.FormValue("name")); n != "" {
		st.InstanceName = n
	}
	switch r.FormValue("preset") {
	case "laboral":
		st.DefaultPolicy.Cron = "0 9-21/4 * * 1-6"
	case "cada4":
		st.DefaultPolicy.Cron = "0 */4 * * *"
	default:
		st.DefaultPolicy.Cron = "0 2 * * *"
	}
	if err := s.A.Svc.SaveSettings(ctx, audit.Actor{Type: "user", ID: u.ID, Name: u.Username}, st); err != nil {
		back(humanError(err))
		return
	}
	s.A.ClearInstallCode()
	token, p, _, err := s.A.Auth.Login(ctx, u.Username, r.FormValue("password"), s.clientIP(r), r.UserAgent())
	if err != nil {
		http.Redirect(w, r, "/entrar", http.StatusSeeOther)
		return
	}
	s.setCookie(w, sessionCookie, token, int(auth.SessionAbsolute.Seconds()))
	s.A.Audit.Record(ctx, p.Actor(), "setup.complete", "system", "", audit.OK, map[string]any{"advertised_host": st.AdvertisedHost})
	http.Redirect(w, r, "/instalar/kit", http.StatusSeeOther)
}

func (s *Server) setupRecover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	back := func(msg string) {
		http.Redirect(w, r, "/instalar?recuperar=1&error="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	if has, _ := s.A.Auth.HasAdmin(ctx); has {
		http.Redirect(w, r, "/entrar", http.StatusSeeOther)
		return
	}
	if !s.checkPre(r) {
		back("El formulario venció; vuelve a intentarlo.")
		return
	}
	if !s.checkInstallCode(r.FormValue("code")) {
		time.Sleep(time.Second)
		back("El código de instalación no es correcto.")
		return
	}
	name, err := app.StageRecovery(s.A.Cfg, r.FormValue("key"))
	if err != nil {
		back(humanError(err))
		return
	}
	s.A.Audit.Record(ctx, audit.Actor{Type: "setup", Name: "recuperación", IP: s.clientIP(r)}, "setup.recover", "system", "", audit.OK, map[string]any{"copy": name})
	s.render(w, r, "recovering", "Recuperando", "", map[string]any{"Copy": name})
	go func() {
		time.Sleep(2 * time.Second)
		s.log.Warn("reinicio para aplicar la recuperación")
		os.Exit(3)
	}()
}

func (s *Server) setupKit(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "kit", "Kit de recuperación", "", nil)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if has, _ := s.A.Auth.HasAdmin(r.Context()); !has {
		http.Redirect(w, r, "/instalar", http.StatusSeeOther)
		return
	}
	if p, _ := s.principal(r, false); p != nil {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", "Entrar", "", map[string]any{"Pre": s.preToken(w, r), "Next": safeNext(r.URL.Query().Get("next")), "User": r.URL.Query().Get("u")})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	fail := func(msg string) {
		s.flash(w, "error", msg)
		http.Redirect(w, r, "/entrar?"+url.Values{"next": {next}, "u": {r.FormValue("username")}}.Encode(), http.StatusSeeOther)
	}
	if !s.checkPre(r) {
		fail("El formulario venció; vuelve a intentarlo.")
		return
	}
	token, _, mfa, err := s.A.Auth.Login(r.Context(), r.FormValue("username"), r.FormValue("password"), s.clientIP(r), r.UserAgent())
	if err != nil {
		fail(humanError(err))
		return
	}
	s.setCookie(w, sessionCookie, token, int(auth.SessionAbsolute.Seconds()))
	if mfa {
		http.Redirect(w, r, "/entrar/codigo?next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) totpForm(w http.ResponseWriter, r *http.Request) {
	p, _ := s.principal(r, true)
	if p == nil {
		http.Redirect(w, r, "/entrar", http.StatusSeeOther)
		return
	}
	s.render(w, r, "totp", "Segundo factor", "", map[string]any{"CSRF": p.CSRF, "Next": safeNext(r.URL.Query().Get("next"))})
}

func (s *Server) totpSubmit(w http.ResponseWriter, r *http.Request) {
	p, raw := s.principal(r, true)
	if p == nil {
		http.Redirect(w, r, "/entrar", http.StatusSeeOther)
		return
	}
	next := safeNext(r.FormValue("next"))
	if !s.A.Auth.CheckCSRF(raw, r.FormValue("csrf")) {
		http.Redirect(w, r, "/entrar/codigo", http.StatusSeeOther)
		return
	}
	if err := s.A.Auth.VerifySecondFactor(r.Context(), p, r.FormValue("code")); err != nil {
		s.flash(w, "error", humanError(err))
		http.Redirect(w, r, "/entrar/codigo?"+url.Values{"next": {next}}.Encode(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.A.Auth.Logout(r.Context(), principalOf(r))
	s.setCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/entrar", http.StatusSeeOther)
}

func (s *Server) logoutEverywhere(w http.ResponseWriter, r *http.Request) {
	s.A.Auth.LogoutEverywhere(r.Context(), principalOf(r))
	s.ok(w, r, "/cuenta", "Se cerraron tus otras sesiones.")
}

func (s *Server) reauthForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "reauth", "Confirmar identidad", "", map[string]any{"Next": safeNext(r.URL.Query().Get("next"))})
}

func (s *Server) reauthSubmit(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	if err := s.A.Auth.Reauth(r.Context(), principalOf(r), r.FormValue("password"), r.FormValue("code")); err != nil {
		s.flash(w, "error", humanError(err))
		http.Redirect(w, r, "/confirmar?next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	tokens, _ := s.A.Auth.Tokens(r.Context(), p.User.ID)
	s.render(w, r, "account", "Mi cuenta", "cuenta", map[string]any{"Tokens": tokens, "Scopes": []string{"read", "backup", "restore", "admin"}})
}

func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if r.FormValue("password") != r.FormValue("password2") {
		s.fail(w, r, "/cuenta", errors.New("SQS-A015 las contraseñas no coinciden"))
		return
	}
	if err := s.A.Auth.SetPassword(r.Context(), p.Actor(), p.User.ID, r.FormValue("password"), p.SessionID); err != nil {
		s.fail(w, r, "/cuenta", err)
		return
	}
	s.ok(w, r, "/cuenta", "Contraseña cambiada. Se cerraron tus otras sesiones.")
}

func (s *Server) accountTOTPBegin(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	secret, uri := s.A.Auth.BeginTOTP(p.User)
	s.render(w, r, "totp_setup", "Activar segundo factor", "cuenta", map[string]any{"Secret": secret, "URI": uri, "Grouped": group4(secret)})
}

func group4(s string) string {
	var b strings.Builder
	for i, c := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func (s *Server) accountTOTPEnable(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	secret := strings.ReplaceAll(r.FormValue("secret"), " ", "")
	codes, err := s.A.Auth.EnableTOTP(r.Context(), p, secret, r.FormValue("code"))
	if err != nil {
		s.flash(w, "error", humanError(err)+". Vuelve a escanear el código e inténtalo de nuevo.")
		http.Redirect(w, r, "/cuenta", http.StatusSeeOther)
		return
	}
	s.render(w, r, "recovery_codes", "Códigos de recuperación", "cuenta", map[string]any{"Codes": codes})
}

func (s *Server) accountTOTPDisable(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if s.A.Svc.Settings(r.Context()).TOTPRequiredAdmins && p.User.Role == auth.RoleAdmin {
		s.fail(w, r, "/cuenta", errors.New("SQS-A040 el segundo factor es obligatorio para administradores en esta instalación"))
		return
	}
	if err := s.A.Auth.DisableTOTP(r.Context(), p.Actor(), p.User.ID); err != nil {
		s.fail(w, r, "/cuenta", err)
		return
	}
	s.ok(w, r, "/cuenta", "Segundo factor desactivado.")
}

func (s *Server) tokenCreate(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	r.ParseForm()
	days := 365
	switch r.FormValue("days") {
	case "30":
		days = 30
	case "90":
		days = 90
	}
	raw, _, err := s.A.Auth.CreateToken(r.Context(), p, r.FormValue("name"), r.Form["scope"], time.Duration(days)*24*time.Hour)
	if err != nil {
		s.fail(w, r, "/cuenta", err)
		return
	}
	s.render(w, r, "token_created", "Token creado", "cuenta", map[string]any{"Token": raw})
}

func (s *Server) tokenRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.A.Auth.RevokeToken(r.Context(), principalOf(r), r.PathValue("id")); err != nil {
		s.fail(w, r, "/cuenta", err)
		return
	}
	s.ok(w, r, "/cuenta", "Token revocado.")
}

var _ = context.Background
