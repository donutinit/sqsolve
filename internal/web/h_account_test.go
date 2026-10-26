// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
)

var (
	totpSecretRe = regexp.MustCompile(`name="secret" value="([A-Z2-7]+)"`)
	recoveryRe   = regexp.MustCompile(`(?s)<pre class="codes" id="codes">(.*?)</pre>`)
	tokenIDRe    = regexp.MustCompile(`action="/account/tokens/([0-9A-Z]{26})/revoke"`)
)

// Enrolling the second factor from the account page: a wrong code sends
// back with its coded notice, the right one shows the recovery codes once,
// and from then on signing in asks for a code (the app's or a recovery
// code) before any page opens. The factor is removed from the same page,
// except by an administrator while the installation requires it.
func TestSecondFactorFromTheAccountPage(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	page := boss.page("/account/totp")
	m := totpSecretRe.FindStringSubmatch(page)
	if m == nil || strings.Contains(page, "required for administrators") {
		t.Fatalf("enrollment page:\n%s", page[:min(len(page), 1200)])
	}
	if st, _, body := boss.do("POST", "/account/totp/start", url.Values{"csrf": {tok}}); st != http.StatusOK || !totpSecretRe.MatchString(body) {
		t.Fatalf("start again: %d", st)
	}
	if _, loc, msg := fxPost(boss, "/account/totp/enable", url.Values{"csrf": {tok}, "secret": {m[1]}, "code": {"12345"}}); loc != "/account" ||
		msg != "SQS-A006 incorrect code. Scan the code again and try once more." {
		t.Fatalf("wrong code: %s %q", loc, msg)
	}
	if u, _ := a.Auth.UserByName(ctx, "boss"); u.TOTPEnabled {
		t.Fatal("a wrong code enabled the second factor")
	}
	code, _ := auth.TOTPCode(m[1], auth.Counter(time.Now()))
	// The secret is shown in groups of four; the form takes it either way.
	grouped := regexp.MustCompile(`(.{4})`).ReplaceAllString(m[1], "$1 ")
	st, _, body := boss.do("POST", "/account/totp/enable", url.Values{"csrf": {tok}, "secret": {grouped}, "code": {code}})
	codes := recoveryRe.FindStringSubmatch(body)
	if st != http.StatusOK || codes == nil {
		t.Fatalf("enable: %d", st)
	}
	recovery := strings.Split(strings.TrimSpace(codes[1]), "\n")
	if len(recovery) != 10 {
		t.Fatalf("%d recovery codes", len(recovery))
	}
	if u, _ := a.Auth.UserByName(ctx, "boss"); !u.TOTPEnabled {
		t.Fatal("second factor not enabled")
	}
	if st, loc, _ := boss.do("GET", "/account/totp", nil); st != http.StatusSeeOther || loc != "/account" {
		t.Fatalf("enrollment page with the factor on: %d %s", st, loc)
	}

	// A new sign-in stops at the code form.
	anon := newBrowser(t, srv)
	if st, loc, _ := anon.do("GET", "/login/code", nil); st != http.StatusSeeOther || loc != "/login" {
		t.Fatalf("code form without a session: %d %s", st, loc)
	}
	if st, loc, _ := anon.do("POST", "/login/code", url.Values{"code": {"x"}}); st != http.StatusSeeOther || loc != "/login" {
		t.Fatalf("code post without a session: %d %s", st, loc)
	}
	b, loc := signIn(t, srv, "boss", jobsAdminPass)
	if loc != "/login/code?next=%2F" {
		t.Fatalf("login with a second factor: %s", loc)
	}
	if st, loc, _ := b.do("GET", "/libraries", nil); st != http.StatusSeeOther || loc != "/login/code" {
		t.Fatalf("a page before the code: %d %s", st, loc)
	}
	form := b.page("/login/code?next=%2Fbackups")
	pending := csrf(t, form)
	if !strings.Contains(form, `name="next" value="/backups"`) {
		t.Fatal("the code form lost next")
	}
	if st, loc, _ := b.do("POST", "/login/code", url.Values{"csrf": {"forged"}, "code": {recovery[0]}, "next": {"/backups"}}); st != http.StatusSeeOther || loc != "/login/code" {
		t.Fatalf("code without the form token: %d %s", st, loc)
	}
	if _, loc, msg := fxPost(b, "/login/code", url.Values{"csrf": {pending}, "code": {"nope nope"}, "next": {"/backups"}}); loc != "/login/code?next=%2Fbackups" || msg != "SQS-A006 incorrect code" {
		t.Fatalf("wrong code: %s %q", loc, msg)
	}
	if es := auditEntries(t, a, "auth.totp"); len(es) != 1 || es[0].Outcome != audit.Denied {
		t.Fatalf("refused code audit %+v", es)
	}
	if _, loc, _ := b.do("POST", "/login/code", url.Values{"csrf": {pending}, "code": {strings.ToUpper(recovery[0])}, "next": {"/backups"}}); loc != "/backups" {
		t.Fatalf("recovery code: %s", loc)
	}
	b.page("/backups")
	if es := auditEntries(t, a, "auth.recovery_code"); len(es) != 1 || es[0].Actor.Name != "boss" {
		t.Fatalf("recovery code audit %+v", es)
	}
	// A recovery code works once; the app's next code works.
	b2, _ := signIn(t, srv, "boss", jobsAdminPass)
	pending2 := csrf(t, b2.page("/login/code"))
	if _, _, msg := fxPost(b2, "/login/code", url.Values{"csrf": {pending2}, "code": {recovery[0]}}); msg != "SQS-A006 incorrect code" {
		t.Fatalf("reused recovery code: %q", msg)
	}
	next, _ := auth.TOTPCode(m[1], auth.Counter(time.Now())+1)
	if _, loc, _ := b2.do("POST", "/login/code", url.Values{"csrf": {pending2}, "code": {next}}); loc != "/" {
		t.Fatalf("app code: %s", loc)
	}

	// Removing it is refused while administrators must have one.
	tok = csrf(t, b.page("/account"))
	st2 := a.Svc.Settings(ctx)
	st2.TOTPRequiredAdmins = true
	if err := a.Svc.SaveSettings(ctx, audit.System, st2); err != nil {
		t.Fatal(err)
	}
	if _, loc, msg := fxPost(b, "/account/totp/disable", url.Values{"csrf": {tok}}); loc != "/account" || msg != "SQS-A040 the second factor is required for administrators on this installation" {
		t.Fatalf("disable while required: %s %q", loc, msg)
	}
	st2.TOTPRequiredAdmins = false
	a.Svc.SaveSettings(ctx, audit.System, st2)
	if _, loc, msg := fxPost(b, "/account/totp/disable", url.Values{"csrf": {tok}}); loc != "/account" || msg != "Second factor disabled." {
		t.Fatalf("disable: %s %q", loc, msg)
	}
	if u, _ := a.Auth.UserByName(ctx, "boss"); u.TOTPEnabled {
		t.Fatal("second factor still enabled")
	}
	if es := auditEntries(t, a, "auth.totp_disable"); len(es) != 1 || es[0].Actor.Name != "boss" {
		t.Fatalf("disable audit %+v", es)
	}
	if _, loc := signIn(t, srv, "boss", jobsAdminPass); loc != "/" {
		t.Fatalf("login after removing the factor: %s", loc)
	}
}

// API tokens from the account page: each refusal carries its code, the
// token is shown once and works on the API until it is revoked, and a user
// who does not manage users revokes only their own tokens.
func TestAPITokensFromTheAccountPage(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	fxViewer(t, a)
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	for _, c := range []struct {
		form url.Values
		msg  string
	}{
		{url.Values{"name": {" "}, "scope": {"read"}}, "SQS-A020 the token needs a name of up to 80 characters"},
		{url.Values{"name": {strings.Repeat("n", 81)}, "scope": {"read"}}, "SQS-A020 the token needs a name of up to 80 characters"},
		{url.Values{"name": {"ci"}}, "SQS-A022 choose at least one scope"},
		{url.Values{"name": {"ci"}, "scope": {"read", "root"}}, "SQS-A021 unknown scope: root"},
	} {
		c.form.Set("csrf", tok)
		if _, loc, msg := fxPost(boss, "/account/tokens", c.form); loc != "/account" || msg != c.msg {
			t.Errorf("create %v: %s %q, want %q", c.form, loc, msg, c.msg)
		}
	}
	if list, _ := a.Auth.Tokens(ctx, ""); len(list) != 0 {
		t.Fatalf("refused forms created %d tokens", len(list))
	}
	st, _, page := boss.do("POST", "/account/tokens", url.Values{"csrf": {tok}, "name": {"nightly ci"}, "scope": {"read", "backup"}, "days": {"90"}})
	raw := regexp.MustCompile(`id="tok">([^<]+)<`).FindStringSubmatch(page)
	if st != http.StatusOK || raw == nil || !strings.HasPrefix(raw[1], "sqs_") {
		t.Fatalf("create: %d", st)
	}
	list, _ := a.Auth.Tokens(ctx, "")
	if len(list) != 1 || list[0].Name != "nightly ci" || strings.Join(list[0].Scopes, ",") != "read,backup" ||
		list[0].ExpiresAt.Sub(list[0].CreatedAt).Round(time.Hour) != 90*24*time.Hour {
		t.Fatalf("created token %+v", list)
	}
	if es := auditEntries(t, a, "token.create"); len(es) != 1 || es[0].Actor.Name != "boss" || es[0].TargetID != list[0].ID {
		t.Fatalf("token.create audit %+v", es)
	}
	account := boss.page("/account")
	if !strings.Contains(account, "nightly ci") || strings.Contains(account, raw[1]) {
		t.Fatal("the account page does not list the token, or shows its secret again")
	}
	api := func(token string) int {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/jobs", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := api(raw[1]); code != http.StatusOK {
		t.Fatalf("API with the new token: %d", code)
	}

	// The viewer has tokens of her own and cannot revoke boss's.
	eve, eveTok := jobsLogin(t, srv, "eve", fxViewerPass)
	_, _, evePage := eve.do("POST", "/account/tokens", url.Values{"csrf": {eveTok}, "name": {"dashboard"}, "scope": {"read"}, "days": {"30"}})
	if !strings.Contains(evePage, `id="tok">sqs_`) {
		t.Fatal("a viewer could not create a token")
	}
	eveIDs := tokenIDRe.FindStringSubmatch(eve.page("/account"))
	if eveIDs == nil {
		t.Fatal("the viewer's token is not listed")
	}
	if _, loc, msg := fxPost(eve, "/account/tokens/"+list[0].ID+"/revoke", url.Values{"csrf": {eveTok}}); loc != "/account" || msg != "SQS-A023 token not found" {
		t.Fatalf("viewer revoking boss's token: %s %q", loc, msg)
	}
	if code := api(raw[1]); code != http.StatusOK {
		t.Fatalf("boss's token after the refused revoke: %d", code)
	}
	if _, _, msg := fxPost(eve, "/account/tokens/"+eveIDs[1]+"/revoke", url.Values{"csrf": {eveTok}}); msg != "Token revoked." {
		t.Fatalf("viewer revoking her token: %q", msg)
	}

	if _, loc, msg := fxPost(boss, "/account/tokens/"+list[0].ID+"/revoke", url.Values{"csrf": {tok}}); loc != "/account" || msg != "Token revoked." {
		t.Fatalf("revoke: %s %q", loc, msg)
	}
	if code := api(raw[1]); code != http.StatusUnauthorized {
		t.Fatalf("API with a revoked token: %d", code)
	}
	if _, _, msg := fxPost(boss, "/account/tokens/"+list[0].ID+"/revoke", url.Values{"csrf": {tok}}); msg != "SQS-A023 token not found" {
		t.Fatalf("revoke twice: %q", msg)
	}
	if es := auditEntries(t, a, "token.revoke"); len(es) != 2 || es[0].Actor.Name != "boss" || es[1].Actor.Name != "eve" {
		t.Fatalf("token.revoke audit %+v", es)
	}
	if page := boss.page("/account"); !strings.Contains(page, ">Revoked<") {
		t.Fatal("the revoked token is not marked")
	}
}
