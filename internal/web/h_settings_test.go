// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/notify"
)

// The General form stores every field it shows; a value the service refuses
// comes back as a coded notice and changes nothing, and a field that is not
// a number keeps its current value. Only whoever may edit the settings sees
// the Save button and gets past the form.
func TestSettingsGeneralForm(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	form := url.Values{"csrf": {tok}, "name": {"  Studio B  "}, "host": {"nas.studio"}, "init_window": {"45"}, "trash_days": {"10"},
		"prev_days": {"3"}, "stale_hours": {"36"}, "import_max_mb": {"2048"}, "maintenance_db": {"on"}, "experimental_exports": {"on"},
		"default_cron": {"*/15 * * * *"}, "keep_last": {"4"}, "keep_daily": {"5"}, "keep_weekly": {"6"}, "keep_monthly": {"7"}}
	if st, loc, msg := fxPost(boss, "/settings/general", form); st != http.StatusSeeOther || loc != "/settings" || msg != "Settings saved." {
		t.Fatalf("save: %d %s %q", st, loc, msg)
	}
	got := a.Svc.Settings(ctx)
	if got.InstanceName != "Studio B" || got.AdvertisedHost != "nas.studio" || got.InitWindowMinutes != 45 || got.TrashDays != 10 || got.PrevDays != 3 ||
		got.StaleHours != 36 || got.ImportMaxMB != 2048 || !got.ExperimentalExports || got.RequireTLS || !got.MaintenanceDBAccess || got.TOTPRequiredAdmins ||
		got.DefaultPolicy.KeepLast != 4 || got.DefaultPolicy.KeepDaily != 5 ||
		got.DefaultPolicy.KeepWeekly != 6 || got.DefaultPolicy.KeepMonthly != 7 {
		t.Fatalf("saved settings %+v", got)
	}
	if es := auditEntries(t, a, "settings.update"); len(es) != 1 || es[0].Actor.Name != "boss" || es[0].Outcome != audit.OK {
		t.Fatalf("settings audit %+v", es)
	}
	page := boss.page("/settings")
	for _, want := range []string{`name="name" value="Studio B"`, `name="host" value="nas.studio"`, `name="init_window" value="45"`,
		`name="experimental_exports" checked`, `name="keep_monthly" value="7"`} {
		if !strings.Contains(page, want) {
			t.Errorf("general tab lacks %s", want)
		}
	}
	saveButton := regexp.MustCompile(`<button[^>]*type="submit"[^>]*>Save</button>`)
	if !saveButton.MatchString(page) {
		t.Error("the general tab has no Save button")
	}

	for _, c := range []struct{ field, value, msg string }{
		{"init_window", "2", "SQS-C031 the initialization window must be between 5 and 1440 minutes"},
		{"trash_days", "4000", "SQS-C032 retention periods must be between 1 and 3650 days"},
		{"prev_days", "0", "SQS-C032 retention periods must be between 1 and 3650 days"},
		{"host", "nas.studio:5432", "SQS-C030 invalid advertised address: type only the IP or the name, without a port"},
		{"keep_last", "0", "SQS-B031 retention must keep at least one backup"},
	} {
		bad := url.Values{}
		for k, v := range form {
			bad[k] = v
		}
		bad.Set("name", "Changed")
		if c.field == "keep_last" {
			bad.Set("keep_daily", "0")
			bad.Set("keep_weekly", "0")
			bad.Set("keep_monthly", "0")
		}
		bad.Set(c.field, c.value)
		if _, loc, msg := fxPost(boss, "/settings/general", bad); loc != "/settings" || msg != c.msg {
			t.Errorf("%s=%q: %s %q", c.field, c.value, loc, msg)
		}
	}
	if got := a.Svc.Settings(ctx); got.InstanceName != "Studio B" {
		t.Fatalf("a refused form changed the settings: %+v", got)
	}
	// A number field that does not parse keeps the stored value.
	form.Set("stale_hours", "a day")
	if _, _, msg := fxPost(boss, "/settings/general", form); msg != "Settings saved." {
		t.Fatalf("save with a non-numeric field: %q", msg)
	}
	if got := a.Svc.Settings(ctx); got.StaleHours != 36 {
		t.Fatalf("non-numeric stale_hours changed the value to %d", got.StaleHours)
	}

	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	if page := op.page("/settings"); !strings.Contains(page, "<fieldset disabled>") || saveButton.MatchString(page) {
		t.Error("an operator gets an editable General form")
	}
	form.Set("csrf", opTok)
	form.Set("name", "Operator was here")
	if st, _, body := op.do("POST", "/settings/general", form); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
		t.Fatalf("operator save: %d", st)
	}
	form.Del("csrf")
	if st, _, body := boss.do("POST", "/settings/general", form); st != http.StatusForbidden || !strings.Contains(body, fxCSRFRefused) {
		t.Fatalf("save without the form token: %d", st)
	}
	if got := a.Svc.Settings(ctx); got.InstanceName != "Studio B" {
		t.Fatalf("a refused request changed the settings: %q", got.InstanceName)
	}
}

// Users are added and edited from the Users tab: each refusal carries its
// code, a disabled account loses its open session at once, the last active
// administrator stays, and only whoever manages users reaches the tab.
func TestUserManagementForms(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	add := func(user, pass, pass2, role string) string {
		t.Helper()
		_, loc, msg := fxPost(boss, "/settings/users", url.Values{"csrf": {tok}, "username": {user}, "display": {"Ana Ruiz"}, "role": {role},
			"password": {pass}, "password2": {pass2}})
		if loc != "/settings?tab=users" {
			t.Fatalf("create %s: sent to %s", user, loc)
		}
		return msg
	}
	for _, c := range []struct{ user, pass, pass2, role, msg string }{
		{"ana", "grading suite 1", "grading suite 2", "viewer", "SQS-A015 the passwords do not match"},
		{"a", "grading suite 1", "grading suite 1", "viewer", "SQS-A011 invalid username: 2 to 32 characters, letters, digits, dot, hyphen or underscore"},
		{"ana", "grading suite 1", "grading suite 1", "root", "SQS-A012 invalid role"},
		{"ana", "short", "short", "viewer", "SQS-A010 weak password: use at least 8 characters"},
		{"ana", "ana grading suite", "ana grading suite", "viewer", "SQS-A010 weak password: cannot contain the username"},
		{"op", "grading suite 1", "grading suite 1", "viewer", "SQS-A013 a user with that name already exists"},
	} {
		if msg := add(c.user, c.pass, c.pass2, c.role); msg != c.msg {
			t.Errorf("create %s %s: %q, want %q", c.user, c.role, msg, c.msg)
		}
	}
	if _, err := a.Auth.UserByName(ctx, "ana"); err == nil {
		t.Fatal("a refused form created the user")
	}
	if msg := add("ana", "grading suite 1", "grading suite 1", "viewer"); msg != "User created." {
		t.Fatalf("create: %q", msg)
	}
	ana, err := a.Auth.UserByName(ctx, "ana")
	if err != nil || ana.Role != auth.RoleViewer || ana.DisplayName != "Ana Ruiz" {
		t.Fatalf("created user %+v %v", ana, err)
	}
	if es := auditEntries(t, a, "user.create"); es[0].Actor.Name != "boss" || es[0].TargetID != ana.ID || es[0].Details["username"] != "ana" || es[0].Outcome != audit.OK {
		t.Fatalf("user.create audit %+v", es[0])
	}
	users := boss.page("/settings?tab=users")
	if !strings.Contains(users, "<strong>ana</strong>") || !strings.Contains(users, `action="/settings/users/`+ana.ID+`"`) {
		t.Fatal("the users tab does not list the new user")
	}

	anaB, _ := jobsLogin(t, srv, "ana", "grading suite 1")
	update := func(id, display, role string, disabled bool) (string, string) {
		t.Helper()
		f := url.Values{"csrf": {tok}, "display": {display}, "role": {role}}
		if disabled {
			f.Set("disabled", "on")
		}
		_, loc, msg := fxPost(boss, "/settings/users/"+id, f)
		return loc, msg
	}
	if loc, msg := update(ana.ID, "Ana R.", "operator", false); loc != "/settings?tab=users" || msg != "User updated." {
		t.Fatalf("update: %s %q", loc, msg)
	}
	if u, _ := a.Auth.UserByID(ctx, ana.ID); u.Role != auth.RoleOperator || u.DisplayName != "Ana R." || u.DisabledAt != nil {
		t.Fatalf("updated user %+v", u)
	}
	if st, _, _ := anaB.do("GET", "/backups", nil); st != http.StatusOK {
		t.Fatalf("the open session lost access after a role change: %d", st)
	}
	if _, msg := update(ana.ID, "Ana R.", "operator", true); msg != "User updated." {
		t.Fatalf("disable: %q", msg)
	}
	if st, loc, _ := anaB.do("GET", "/", nil); st != http.StatusSeeOther || !strings.HasPrefix(loc, "/login") {
		t.Fatalf("a disabled user kept the session: %d %s", st, loc)
	}
	boss2, err := a.Auth.UserByName(ctx, "boss")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, role, msg string }{
		{boss2.ID, "operator", "SQS-A014 at least one active administrator must remain"},
		{ana.ID, "superuser", "SQS-A012 invalid role"},
		{"01NOSUCHUSER0000000000000", "viewer", "SQS-A001 user not found"},
	} {
		if _, msg := update(c.id, "", c.role, false); msg != c.msg {
			t.Errorf("update %s to %s: %q, want %q", c.id, c.role, msg, c.msg)
		}
	}
	if u, _ := a.Auth.UserByID(ctx, boss2.ID); u.Role != auth.RoleAdmin {
		t.Fatal("the last administrator was demoted")
	}
	if _, loc, msg := fxPost(boss, "/settings/users/01NOSUCHUSER0000000000000/totp", url.Values{"csrf": {tok}}); loc != "/settings?tab=users" || msg != "SQS-A001 user not found" {
		t.Fatalf("second factor of an unknown user: %s %q", loc, msg)
	}

	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	if st, _, body := op.do("GET", "/settings?tab=users", nil); st != http.StatusForbidden || !strings.Contains(body, "SQS-A030 Your role does not allow managing users.") {
		t.Fatalf("operator on the users tab: %d", st)
	}
	if strings.Contains(op.page("/settings"), `href="/settings?tab=users"`) {
		t.Error("an operator is offered the users tab")
	}
	if st, _, body := op.do("POST", "/settings/users", url.Values{"csrf": {opTok}, "username": {"mallory"}, "role": {"admin"},
		"password": {"grading suite 1"}, "password2": {"grading suite 1"}}); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
		t.Fatalf("operator create: %d", st)
	}
	if _, err := a.Auth.UserByName(ctx, "mallory"); err == nil {
		t.Fatal("an operator created an administrator")
	}
}

// A notification channel is tested against its destination, keeps its
// secret when edited with the field empty and is deleted from the panel;
// every refusal carries its code and only notify.manage reaches the forms.
func TestNotificationChannelForms(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	var mu sync.Mutex
	status, got := http.StatusNoContent, []*http.Request{}
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r)
		w.WriteHeader(status)
	}))
	defer hook.Close()
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	form := url.Values{"csrf": {tok}, "kind": {"webhook"}, "name": {"studio hook"}, "enabled": {"on"}, "severity": {"warning"}, "pattern": {" backup.* "},
		"url": {hook.URL + "/in"}, "secret": {"hmac-secret-123"}}
	if _, loc, msg := fxPost(boss, "/settings/notifications", form); loc != "/settings?tab=notifications" || !strings.HasPrefix(msg, "Channel saved. SQS-N042 ") {
		t.Fatalf("save: %s %q", loc, msg)
	}
	chs, _ := a.Notify.Channels(ctx)
	if len(chs) != 1 || chs[0].Pattern != "backup.*" || chs[0].MinSeverity != events.Warning || chs[0].Config["secret"] != "hmac-secret-123" {
		t.Fatalf("saved channels %+v", chs)
	}
	id := chs[0].ID

	if _, loc, msg := fxPost(boss, "/settings/notifications/"+id+"/test", url.Values{"csrf": {tok}}); loc != "/settings?tab=notifications" || msg != "Test sent successfully." {
		t.Fatalf("test: %s %q", loc, msg)
	}
	mu.Lock()
	if len(got) != 1 || got[0].Method != "POST" || got[0].URL.Path != "/in" || got[0].Header.Get("X-Sqsolve-Event") != "system.test" || got[0].Header.Get("X-Sqsolve-Signature") == "" {
		t.Fatalf("the destination got %d requests", len(got))
	}
	status = http.StatusInternalServerError
	mu.Unlock()
	if _, _, msg := fxPost(boss, "/settings/notifications/"+id+"/test", url.Values{"csrf": {tok}}); msg != "SQS-N030 the test failed: SQS-N002 the destination responded 500 Internal Server Error" {
		t.Fatalf("failed test: %q", msg)
	}
	if page := boss.page("/settings?tab=notifications"); !strings.Contains(page, "SQS-N002 the destination responded 500 Internal Server Error") {
		t.Error("the channel does not show its last error")
	}
	if _, _, msg := fxPost(boss, "/settings/notifications/01NOSUCHCHANNEL00000000000/test", url.Values{"csrf": {tok}}); msg != "SQS-N030 the test failed: SQS-N020 channel not found" {
		t.Fatalf("test of an unknown channel: %q", msg)
	}

	// Editing with the secret field empty keeps the stored secret.
	if page := boss.page("/settings?tab=notifications&edit=" + id); !strings.Contains(page, `name="id" value="`+id+`"`) || strings.Contains(page, "hmac-secret-123") {
		t.Fatal("the edit form is missing the channel or shows its secret")
	}
	edit := url.Values{"csrf": {tok}, "id": {id}, "kind": {"webhook"}, "name": {"renamed hook"}, "severity": {"error"}, "pattern": {"*"}, "url": {hook.URL + "/in"}}
	fxPost(boss, "/settings/notifications", edit)
	if ch, _ := a.Notify.Channel(ctx, id); ch.Name != "renamed hook" || ch.Enabled || ch.Config["secret"] != "hmac-secret-123" {
		t.Fatalf("edited channel %+v", ch)
	}

	for _, c := range []struct {
		set map[string]string
		msg string
	}{
		{map[string]string{"name": " "}, "SQS-N021 the channel needs a name"},
		{map[string]string{"severity": "loud"}, "SQS-N022 invalid severity"},
		{map[string]string{"url": "ftp://files.example/x"}, "SQS-N023 invalid URL (use http:// or https://)"},
		{map[string]string{"kind": "smtp", "host": "mail.example", "to": "ops@example.com"}, "SQS-N024 email needs a server, sender and recipients"},
		{map[string]string{"kind": "smtp", "host": "mail.example", "from": "a@example.com", "to": "ops@example.com", "port": "99999"}, "SQS-N025 invalid port"},
		{map[string]string{"kind": "pager"}, "SQS-N001 unknown channel kind"},
	} {
		bad := url.Values{"csrf": {tok}, "kind": {"webhook"}, "name": {"x"}, "severity": {"error"}, "url": {"https://hooks.example/x"}}
		for k, v := range c.set {
			bad.Set(k, v)
		}
		if _, loc, msg := fxPost(boss, "/settings/notifications", bad); loc != "/settings?tab=notifications" || msg != c.msg {
			t.Errorf("save %v: %s %q, want %q", c.set, loc, msg, c.msg)
		}
	}
	if chs, _ := a.Notify.Channels(ctx); len(chs) != 1 {
		t.Fatalf("refused forms saved channels: %d", len(chs))
	}

	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	for _, p := range []string{"/settings/notifications/" + id + "/test", "/settings/notifications/" + id + "/delete"} {
		if st, _, body := op.do("POST", p, url.Values{"csrf": {opTok}}); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
			t.Errorf("operator %s: %d", p, st)
		}
	}
	if page := op.page("/settings?tab=notifications"); strings.Contains(page, "/delete") || strings.Contains(page, "New channel") {
		t.Error("an operator is offered the channel forms")
	}
	if _, loc, msg := fxPost(boss, "/settings/notifications/"+id+"/delete", url.Values{"csrf": {tok}}); loc != "/settings?tab=notifications" || msg != "Channel deleted." {
		t.Fatalf("delete: %s %q", loc, msg)
	}
	if chs, _ := a.Notify.Channels(ctx); len(chs) != 0 {
		t.Fatal("the channel is still there")
	}
	if es := auditEntries(t, a, "notify.delete"); len(es) != 1 || es[0].TargetID != id || es[0].Actor.Name != "boss" {
		t.Fatalf("notify.delete audit %+v", es)
	}
	if page := boss.page("/settings?tab=notifications"); !strings.Contains(page, "No channels. sqsolve sends nothing outside the server until you add one.") {
		t.Error("the empty state is missing")
	}
}

// The maintenance buttons queue their system job together with its
// "accepted" audit entry, and without that entry nothing is queued (docs 08
// §9). The upgrade window asks for its target version; the rest is for
// administrators only.
func TestMaintenanceActionsQueueAuditedJobs(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	actions := map[string]string{"catalog": core.JobCatalogRebuild, "check": core.JobCatalogCheck, "verify": core.JobVerifyAll,
		"retention": core.JobRetention, "optimize": core.JobVacuum, "control": core.JobControlBackup}
	for action, typ := range actions {
		st, loc, _ := boss.do("POST", "/settings/maintenance/"+action, url.Values{"csrf": {tok}})
		if st != http.StatusSeeOther {
			t.Fatalf("%s: %d", action, st)
		}
		fxQueuedJob(t, a, loc, typ, "", "maintenance."+action, "boss")
	}
	if _, loc, msg := fxPost(boss, "/settings/maintenance/upgrade", url.Values{"csrf": {tok}, "target": {"  "}}); loc != "/settings?tab=maintenance" || msg != "SQS-C040 type the target Resolve version" {
		t.Fatalf("upgrade without a target: %s %q", loc, msg)
	}
	if _, loc, msg := fxPost(boss, "/settings/maintenance/reboot", url.Values{"csrf": {tok}}); loc != "/settings?tab=maintenance" || msg != "SQS-C041 unknown maintenance action" {
		t.Fatalf("unknown action: %s %q", loc, msg)
	}
	_, loc, _ := boss.do("POST", "/settings/maintenance/upgrade", url.Values{"csrf": {tok}, "target": {"21.2"}})
	j := fxQueuedJob(t, a, loc, core.JobUpgradeWindow, "", "system.upgrade_window", "boss")
	if !strings.Contains(string(j.Params), `"target":"21.2"`) {
		t.Fatalf("upgrade params %s", j.Params)
	}
	if page := boss.page(loc); !strings.Contains(page, j.Title) {
		t.Fatal("the job page of the upgrade window does not render")
	}

	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	if page := op.page("/settings?tab=maintenance"); strings.Contains(page, `action="/settings/maintenance/`) || strings.Contains(page, `href="/settings/kit"`) {
		t.Error("an operator is offered the maintenance actions")
	}
	before := jobCount(t, a)
	for _, action := range []string{"verify", "upgrade", "hba"} {
		if st, _, body := op.do("POST", "/settings/maintenance/"+action, url.Values{"csrf": {opTok}, "target": {"21.2"}}); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
			t.Errorf("operator %s: %d", action, st)
		}
	}
	fxAuditDown(t, a)
	for action := range actions {
		if _, loc, msg := fxPost(boss, "/settings/maintenance/"+action, url.Values{"csrf": {tok}}); loc != "/settings?tab=maintenance" || msg != fxAuditDownErr {
			t.Errorf("%s without its audit entry: %s %q", action, loc, msg)
		}
	}
	if _, _, msg := fxPost(boss, "/settings/maintenance/upgrade", url.Values{"csrf": {tok}, "target": {"21.2"}}); msg != fxAuditDownErr {
		t.Errorf("upgrade without its audit entry: %q", msg)
	}
	hba := filepath.Join(a.Cfg.PGConfDir(), "pg_hba.conf")
	old, _ := os.ReadFile(hba)
	if _, _, msg := fxPost(boss, "/settings/maintenance/hba", url.Values{"csrf": {tok}}); msg != fxAuditDownErr {
		t.Errorf("hba without its audit entry: %q", msg)
	}
	if now, _ := os.ReadFile(hba); !bytes.Equal(now, old) {
		t.Error("pg_hba.conf was rewritten without its audit entry")
	}
	if n := jobCount(t, a); n != before {
		t.Fatalf("%d jobs queued without their audit entry or by an operator", n-before)
	}
}

// Regenerating pg_hba.conf is a change outside SQLite: its "accepted" entry
// comes first and its result follows; without PostgreSQL the result is
// "failed" with the error, and the notice says it.
func TestMaintenanceHBAWritesItsResult(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	_, loc, msg := fxPost(boss, "/settings/maintenance/hba", url.Values{"csrf": {tok}})
	if loc != "/settings?tab=maintenance" || !strings.HasPrefix(msg, "SQS-P001 PostgreSQL is not available") {
		t.Fatalf("hba without PostgreSQL: %s %q", loc, msg)
	}
	es := auditEntries(t, a, "maintenance.hba")
	if len(es) != 2 || es[1].Outcome != audit.Accepted || es[0].Outcome != audit.Failed || es[0].Details["error"] != msg {
		t.Fatalf("hba audit %+v", es)
	}
	if b, err := os.ReadFile(filepath.Join(a.Cfg.PGConfDir(), "pg_hba.conf")); err != nil || !strings.Contains(string(b), "sqsolve") {
		t.Fatalf("pg_hba.conf not written: %v", err)
	}
}

// The recovery kit and the diagnostic bundle are downloads for
// administrators, each with its audit entry; the bundle carries what its
// card promises and none of the secrets (master key, notification secrets).
func TestRecoveryKitAndDiagnostics(t *testing.T) {
	a, srv, admin := jobsPanel(t)
	ctx := context.Background()
	ch := &notify.Channel{Kind: notify.Webhook, Name: "hook", Enabled: true, MinSeverity: events.Error, Pattern: "*",
		Config: map[string]string{"url": "https://hooks.example/x", "secret": "hook-secret-xyz"}}
	if err := a.Notify.Save(ctx, admin, ch); err != nil {
		t.Fatal(err)
	}
	failed, _ := a.Jobs.Enqueue(ctx, core.JobVacuum, "", nil, admin)
	setJobState(t, a, failed.ID, jobs.Failed)
	key, err := app.ReadMasterKey(a.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(key)
	boss, _ := jobsLogin(t, srv, "boss", jobsAdminPass)

	resp, err := boss.c.Get(srv.URL + "/settings/kit")
	if err != nil {
		t.Fatal(err)
	}
	kit, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	wantName := `attachment; filename="sqsolve-recovery-` + strings.ToLower(a.InstanceID[len(a.InstanceID)-6:]) + `.txt"`
	if resp.StatusCode != 200 || resp.Header.Get("Content-Disposition") != wantName || resp.Header.Get("Cache-Control") != "no-store" ||
		!strings.Contains(string(kit), "MASTER KEY") || !strings.Contains(string(kit), keyB64) || !strings.Contains(string(kit), a.InstanceID) {
		t.Fatalf("kit: %d %v", resp.StatusCode, resp.Header)
	}
	if es := auditEntries(t, a, "settings.recovery_kit"); len(es) != 1 || es[0].Outcome != audit.OK || es[0].Actor.Name != "boss" {
		t.Fatalf("kit audit %+v", es)
	}

	resp, err = boss.c.Get(srv.URL + "/settings/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	bundle, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), `attachment; filename="sqsolve-diag-`) {
		t.Fatalf("diagnostics: %d %v", resp.StatusCode, resp.Header)
	}
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
		for _, secret := range []string{keyB64, "hook-secret-xyz", jobsAdminPass} {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s carries a secret", f.Name)
			}
		}
	}
	for _, name := range []string{"version.json", "config.json", "settings.json", "doctor.json", "libraries.json", "backups.json", "events.json",
		"schedules.json", "jobs/" + failed.ID + ".json"} {
		if _, ok := files[name]; !ok {
			t.Errorf("bundle lacks %s", name)
		}
	}
	var version map[string]any
	if json.Unmarshal([]byte(files["version.json"]), &version) != nil || version["instance"] != a.InstanceID {
		t.Errorf("version.json: %s", files["version.json"])
	}
	if es := auditEntries(t, a, "diagnostics.download"); len(es) != 1 || es[0].Outcome != audit.OK || es[0].Actor.Name != "boss" {
		t.Fatalf("diagnostics audit %+v", es)
	}

	op, _ := jobsLogin(t, srv, "op", jobsOpPass)
	for _, p := range []string{"/settings/kit", "/settings/diagnostics"} {
		if st, _, body := op.do("GET", p, nil); st != http.StatusForbidden || strings.Contains(body, keyB64) || !strings.Contains(body, fxRoleRefused) {
			t.Errorf("operator %s: %d", p, st)
		}
	}
}
