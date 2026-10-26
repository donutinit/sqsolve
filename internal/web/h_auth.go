// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"cmp"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
)

// safeNext returns next when it is a path on this server and "/" otherwise
// (audit M4). Browsers drop tabs and line breaks from a Location and treat a
// backslash as a slash, so "/<TAB>/host" or "/\host" would leave the origin:
// any control character, whitespace or backslash is refused, before and
// after decoding, and the parsed URL must have no scheme or host and a path
// that starts with exactly one slash.
func safeNext(next string) string {
	if next == "" || len(next) > 2048 || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	for _, r := range next {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '\\' {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil ||
		!strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	for _, r := range u.Path {
		if unicode.IsControl(r) || r == '\\' {
			return "/"
		}
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
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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
	s.render(w, r, "setup", "Setup", "", data)
}

// errSetupDone stops a setup whose installation already has an
// administrator.
var errSetupDone = errors.New("the installation already has an administrator")

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	back := func(msg string) {
		s.flash(w, "error", msg)
		q := url.Values{"username": {r.FormValue("username")}, "host": {r.FormValue("host")}, "name": {r.FormValue("name")}}
		http.Redirect(w, r, "/setup?"+q.Encode(), http.StatusSeeOther)
	}
	if has, _ := s.A.Auth.HasAdmin(ctx); has {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !s.checkPre(r) {
		back("The form expired; try again.")
		return
	}
	if r.FormValue("password") != r.FormValue("password2") {
		back("The passwords do not match.")
		return
	}
	// The code is held while the administrator is created and spent with
	// it: concurrent setups with the same code create one administrator.
	var u *auth.User
	matched, err := s.A.UseInstallCode(r.FormValue("code"), func() error {
		if has, err := s.A.Auth.HasAdmin(ctx); err != nil || has {
			return cmp.Or(err, errSetupDone)
		}
		var err error
		u, err = s.A.Auth.CreateUser(ctx, audit.Actor{Type: "setup", Name: "setup", IP: s.clientIP(r)}, r.FormValue("username"), r.FormValue("display"), r.FormValue("password"), auth.RoleAdmin)
		return err
	})
	switch {
	case !matched:
		s.A.Audit.BestEffort(ctx, audit.Actor{Type: "anonymous", IP: s.clientIP(r)}, "setup.code", "system", "", audit.Denied, nil)
		time.Sleep(time.Second)
		back("The installation code is not correct. Look for it in the sqsolve container log.")
		return
	case errors.Is(err, errSetupDone):
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	case err != nil:
		back(humanError(err))
		return
	}
	st := s.A.Svc.Settings(ctx)
	st.AdvertisedHost = strings.TrimSpace(r.FormValue("host"))
	if n := strings.TrimSpace(r.FormValue("name")); n != "" {
		st.InstanceName = n
	}
	st.DefaultPolicy.Mode, st.DefaultPolicy.CheckMinutes = core.ModeCron, 15
	switch r.FormValue("preset") {
	case "workdays":
		st.DefaultPolicy.Cron = "0 9-21/4 * * 1-6"
	case "every4h":
		st.DefaultPolicy.Cron = "0 */4 * * *"
	case "daily":
		st.DefaultPolicy.Cron = "0 2 * * *"
	default:
		st.DefaultPolicy.Mode, st.DefaultPolicy.Cron = core.ModeActivity, core.ActivityCron(15)
	}
	if err := s.A.Svc.SaveSettings(ctx, audit.Actor{Type: "user", ID: u.ID, Name: u.Username}, st); err != nil {
		back(humanError(err))
		return
	}
	token, p, _, err := s.A.Auth.Login(ctx, u.Username, r.FormValue("password"), s.clientIP(r), r.UserAgent())
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.setCookie(w, sessionCookie, token, int(auth.SessionAbsolute.Seconds()))
	s.A.Audit.BestEffort(ctx, p.Actor(), "setup.complete", "system", "", audit.OK, map[string]any{"advertised_host": st.AdvertisedHost})
	http.Redirect(w, r, "/setup/kit", http.StatusSeeOther)
}

func (s *Server) setupRecover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	back := func(msg string) {
		s.flash(w, "error", msg)
		http.Redirect(w, r, "/setup?recover=1", http.StatusSeeOther)
	}
	if has, _ := s.A.Auth.HasAdmin(ctx); has {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !s.checkPre(r) {
		back("The form expired; try again.")
		return
	}
	var name string
	matched, err := s.A.UseInstallCode(r.FormValue("code"), func() error {
		if has, err := s.A.Auth.HasAdmin(ctx); err != nil || has {
			return cmp.Or(err, errSetupDone)
		}
		var err error
		name, err = app.StageRecovery(s.A.Cfg, r.FormValue("key"))
		return err
	})
	switch {
	case !matched:
		time.Sleep(time.Second)
		back("The installation code is not correct.")
		return
	case errors.Is(err, errSetupDone):
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	case err != nil:
		back(humanError(err))
		return
	}
	s.A.Audit.BestEffort(ctx, audit.Actor{Type: "setup", Name: "recovery", IP: s.clientIP(r)}, "setup.recover", "system", "", audit.OK, map[string]any{"copy": name})
	s.render(w, r, "recovering", "Recovering", "", map[string]any{"Copy": name})
	go func() {
		time.Sleep(2 * time.Second)
		s.log.Warn("restarting to apply the recovery")
		os.Exit(3)
	}()
}

func (s *Server) setupKit(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "kit", "Recovery kit", "", nil)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if has, _ := s.A.Auth.HasAdmin(r.Context()); !has {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if p, _ := s.principal(r, false); p != nil {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", "Sign in", "", map[string]any{"Pre": s.preToken(w, r), "Next": safeNext(r.URL.Query().Get("next")), "User": r.URL.Query().Get("u")})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	fail := func(msg string) {
		s.flash(w, "error", msg)
		http.Redirect(w, r, "/login?"+url.Values{"next": {next}, "u": {r.FormValue("username")}}.Encode(), http.StatusSeeOther)
	}
	if !s.checkPre(r) {
		fail("The form expired; try again.")
		return
	}
	token, p, mfa, err := s.A.Auth.Login(r.Context(), r.FormValue("username"), r.FormValue("password"), s.clientIP(r), r.UserAgent())
	if errors.Is(err, auth.ErrBusy) {
		s.loginBusy(w, r, next, err)
		return
	}
	if err != nil {
		fail(humanError(err))
		return
	}
	s.setCookie(w, sessionCookie, token, int(auth.SessionAbsolute.Seconds()))
	if mfa {
		http.Redirect(w, r, "/login/code?next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	if s.mustEnroll(r.Context(), p) {
		http.Redirect(w, r, totpEnrollPath, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// loginBusy answers 503 with Retry-After when every password hashing slot is
// taken (auth.ErrBusy): nothing was checked, so the form comes back as it was
// with the coded message instead of a redirect.
func (s *Server) loginBusy(w http.ResponseWriter, r *http.Request, next string, err error) {
	w.Header().Set("Retry-After", "5")
	s.renderStatus(w, r, http.StatusServiceUnavailable, "login", "Sign in", "",
		map[string]any{"Pre": s.preToken(w, r), "Next": next, "User": r.FormValue("username"), "Error": humanError(err)})
}

func (s *Server) totpForm(w http.ResponseWriter, r *http.Request) {
	p, _ := s.principal(r, true)
	if p == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, "totp", "Second factor", "", map[string]any{"CSRF": p.CSRF, "Next": safeNext(r.URL.Query().Get("next"))})
}

func (s *Server) totpSubmit(w http.ResponseWriter, r *http.Request) {
	p, raw := s.principal(r, true)
	if p == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	next := safeNext(r.FormValue("next"))
	if !s.A.Auth.CheckCSRF(raw, r.FormValue("csrf")) {
		http.Redirect(w, r, "/login/code", http.StatusSeeOther)
		return
	}
	if err := s.A.Auth.VerifySecondFactor(r.Context(), p, r.FormValue("code")); err != nil {
		s.flash(w, "error", humanError(err))
		http.Redirect(w, r, "/login/code?"+url.Values{"next": {next}}.Encode(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.A.Auth.Logout(r.Context(), principalOf(r))
	s.setCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) logoutEverywhere(w http.ResponseWriter, r *http.Request) {
	s.A.Auth.LogoutEverywhere(r.Context(), principalOf(r))
	s.ok(w, r, "/account", "Your other sessions were closed.")
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	tokens, _ := s.A.Auth.Tokens(r.Context(), p.User.ID)
	s.render(w, r, "account", "My account", "account", map[string]any{"Tokens": tokens, "Scopes": []string{"read", "backup", "restore", "export", "admin"}})
}

func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if r.FormValue("password") != r.FormValue("password2") {
		s.fail(w, r, "/account", errors.New("SQS-A015 the passwords do not match"))
		return
	}
	if err := s.A.Auth.SetPassword(r.Context(), p.Actor(), p.User.ID, r.FormValue("password"), p.SessionID); err != nil {
		s.fail(w, r, "/account", err)
		return
	}
	s.ok(w, r, "/account", "Password changed. Your other sessions were closed.")
}

// accountTOTPBegin shows a new secret to enroll. GET /account/totp is where
// an administrator without the required second factor is sent.
func (s *Server) accountTOTPBegin(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p.User.TOTPEnabled {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	secret, uri := s.A.Auth.BeginTOTP(p.User)
	s.render(w, r, "totp_setup", "Enable second factor", "account", map[string]any{"Secret": secret, "URI": uri, "Grouped": group4(secret),
		"Required": s.mustEnroll(r.Context(), p)})
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
		s.flash(w, "error", humanError(err)+". Scan the code again and try once more.")
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	s.render(w, r, "recovery_codes", "Recovery codes", "account", map[string]any{"Codes": codes})
}

func (s *Server) accountTOTPDisable(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if s.A.Svc.Settings(r.Context()).TOTPRequiredAdmins && p.User.Role == auth.RoleAdmin {
		s.fail(w, r, "/account", auth.ErrTOTPRequired)
		return
	}
	if err := s.A.Auth.DisableTOTP(r.Context(), p.Actor(), p.User.ID); err != nil {
		s.fail(w, r, "/account", err)
		return
	}
	s.ok(w, r, "/account", "Second factor disabled.")
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
		s.fail(w, r, "/account", err)
		return
	}
	s.render(w, r, "token_created", "Token created", "account", map[string]any{"Token": raw})
}

func (s *Server) tokenRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.A.Auth.RevokeToken(r.Context(), principalOf(r), r.PathValue("id")); err != nil {
		s.fail(w, r, "/account", err)
		return
	}
	s.ok(w, r, "/account", "Token revoked.")
}

var _ = context.Background
