// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// The Settings tab of a library saves its name, notes, Resolve version and
// connection limit, refuses invalid values with their codes and leaves the
// library as it was; a library that does not exist is a 404.
func TestLibrarySettingsForm(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	l := fxLibrary(t, a, "apolo", core.StateActive)
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	back := "/libraries/" + l.ID + "?tab=settings"
	save := func(id string, set map[string]string) (string, string) {
		t.Helper()
		f := url.Values{"csrf": {tok}, "name": {" Apolo II "}, "notes": {"Media on /mnt/raid/apolo"}, "resolve": {"21.1.1"}, "connlimit": {"-1"}}
		for k, v := range set {
			f.Set(k, v)
		}
		_, loc, msg := fxPost(boss, "/libraries/"+id+"/settings", f)
		return loc, msg
	}
	if loc, msg := save(l.ID, nil); loc != back || msg != "Changes saved." {
		t.Fatalf("save: %s %q", loc, msg)
	}
	got, _ := a.Svc.Library(ctx, l.ID)
	if got.DisplayName != "Apolo II" || got.Notes != "Media on /mnt/raid/apolo" || got.ResolveVersion != "21.1.1" || got.ConnLimit != -1 {
		t.Fatalf("saved library %+v", got)
	}
	if es := auditEntries(t, a, "library.update"); len(es) != 1 || es[0].Actor.Name != "boss" || es[0].Details["resolve_version"] != "21.1.1" {
		t.Fatalf("library.update audit %+v", es)
	}
	page := boss.page(back)
	for _, want := range []string{`name="name" required maxlength="80" value="Apolo II"`, `value="21.1.1"`, ">Media on /mnt/raid/apolo</textarea>"} {
		if !strings.Contains(page, want) {
			t.Errorf("settings tab lacks %s", want)
		}
	}
	for _, c := range []struct {
		set map[string]string
		msg string
	}{
		{map[string]string{"name": "  "}, "SQS-L003 the display name must be 1 to 80 characters long"},
		{map[string]string{"name": "Apolo\x07"}, "SQS-L003 the display name cannot contain control characters"},
		{map[string]string{"connlimit": "1001"}, "SQS-L012 invalid connection limit (-1 for unlimited, up to 1000)"},
		{map[string]string{"connlimit": "-2"}, "SQS-L012 invalid connection limit (-1 for unlimited, up to 1000)"},
		{map[string]string{"resolve": strings.Repeat("9", 21)}, "SQS-L013 Resolve version too long"},
		{map[string]string{"notes": strings.Repeat("n", 10001)}, "SQS-L014 notes too long"},
	} {
		// Each refused form also carries a new Resolve version, which must
		// not be stored.
		if _, ok := c.set["resolve"]; !ok {
			c.set["resolve"] = "22.0"
		}
		if loc, msg := save(l.ID, c.set); loc != back || msg != c.msg {
			t.Errorf("save %v: %s %q, want %q", c.set, loc, msg, c.msg)
		}
	}
	if got, _ := a.Svc.Library(ctx, l.ID); got.ResolveVersion != "21.1.1" {
		t.Fatalf("a refused form changed the library: %+v", got)
	}
	// A library Resolve has not initialized yet takes a new limit without
	// touching PostgreSQL; an empty field means unlimited.
	w := fxLibrary(t, a, "zeus", core.StateAwaitingInit)
	if _, msg := save(w.ID, map[string]string{"name": "Zeus", "connlimit": "5"}); msg != "Changes saved." {
		t.Fatalf("limit on a library waiting for Resolve: %q", msg)
	}
	if got, _ := a.Svc.Library(ctx, w.ID); got.ConnLimit != 5 {
		t.Fatalf("connection limit %d", got.ConnLimit)
	}
	if _, msg := save(w.ID, map[string]string{"name": "Zeus", "connlimit": ""}); msg != "Changes saved." {
		t.Fatalf("empty limit: %q", msg)
	}
	if got, _ := a.Svc.Library(ctx, w.ID); got.ConnLimit != -1 {
		t.Fatalf("an empty limit left %d", got.ConnLimit)
	}

	if st, _, body := boss.do("POST", "/libraries/01NOSUCHLIBRARY00000000000/settings", url.Values{"csrf": {tok}, "name": {"x"}}); st != http.StatusNotFound || !strings.Contains(body, "SQS-L002 Library not found.") {
		t.Fatalf("unknown library: %d", st)
	}
	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	if st, _, body := op.do("POST", "/libraries/"+l.ID+"/settings", url.Values{"csrf": {opTok}, "name": {"Hijacked"}}); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
		t.Fatalf("operator save: %d", st)
	}
	if got, _ := a.Svc.Library(ctx, l.ID); got.DisplayName != "Apolo II" {
		t.Fatal("an operator renamed the library")
	}
}

// The Schedule tab turns each preset into its cron expression (or the
// activity mode), stores the retention and updates the library's schedule;
// an invalid policy is refused with its code and the stored one stays.
func TestLibraryScheduleForm(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	l := fxLibrary(t, a, "apolo", core.StateActive)
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	back := "/libraries/" + l.ID + "?tab=schedule"
	save := func(set map[string]string) string {
		t.Helper()
		f := url.Values{"csrf": {tok}, "enabled": {"on"}, "preset": {"cron"}, "cron": {"30 1 * * *"}, "timezone": {""},
			"keep_last": {"5"}, "keep_daily": {"7"}, "keep_weekly": {"4"}, "keep_monthly": {"3"}, "min_verified": {"2"}}
		for k, v := range set {
			if v == "-" {
				f.Del(k)
				continue
			}
			f.Set(k, v)
		}
		_, loc, msg := fxPost(boss, "/libraries/"+l.ID+"/policy", f)
		if loc != back {
			t.Fatalf("policy %v: sent to %s", set, loc)
		}
		return msg
	}
	for _, c := range []struct {
		set        map[string]string
		mode, cron string
		check      int
	}{
		{map[string]string{}, core.ModeCron, "30 1 * * *", 15},
		{map[string]string{"preset": "daily", "hour": "5"}, core.ModeCron, "0 5 * * *", 15},
		{map[string]string{"preset": "daily", "hour": "24"}, core.ModeCron, "0 2 * * *", 15},
		{map[string]string{"preset": "workdays"}, core.ModeCron, "0 9-21/4 * * 1-6", 15},
		{map[string]string{"preset": "every4h"}, core.ModeCron, "0 */4 * * *", 15},
		{map[string]string{"preset": "hourly"}, core.ModeCron, "0 * * * *", 15},
		{map[string]string{"preset": "activity", "check_minutes": "30"}, core.ModeActivity, "*/30 * * * *", 30},
	} {
		if msg := save(c.set); msg != "Schedule saved." {
			t.Fatalf("policy %v: %q", c.set, msg)
		}
		p, _ := a.Svc.Policy(ctx, l.ID)
		if p.Mode != c.mode || p.Cron != c.cron || p.CheckMinutes != c.check || !p.Enabled || p.Timezone != "UTC" ||
			p.KeepLast != 5 || p.KeepDaily != 7 || p.KeepWeekly != 4 || p.KeepMonthly != 3 || p.MinVerified != 2 {
			t.Fatalf("policy %v stored %+v", c.set, p)
		}
		if sc, err := a.Sched.Get(ctx, core.ScheduleBackup, l.ID); err != nil || sc.Cron != c.cron || !sc.Enabled {
			t.Fatalf("schedule after %v: %+v %v", c.set, sc, err)
		}
	}
	if es := auditEntries(t, a, "library.policy"); len(es) != 7 || es[0].Actor.Name != "boss" {
		t.Fatalf("library.policy audit: %d entries", len(es))
	}
	// Disabled, with its own time zone: the schedule stops.
	if msg := save(map[string]string{"enabled": "-", "timezone": "Europe/Madrid"}); msg != "Schedule saved." {
		t.Fatalf("disable: %q", msg)
	}
	if p, _ := a.Svc.Policy(ctx, l.ID); p.Enabled || p.Timezone != "Europe/Madrid" {
		t.Fatalf("disabled policy %+v", p)
	}
	if sc, _ := a.Sched.Get(ctx, core.ScheduleBackup, l.ID); sc.Enabled {
		t.Fatal("the schedule of a disabled policy is still enabled")
	}
	if page := boss.page(back); !strings.Contains(page, `value="Europe/Madrid"`) || !strings.Contains(page, `value="30 1 * * *"`) {
		t.Error("the schedule tab does not show the stored policy")
	}
	for _, c := range []struct {
		set map[string]string
		msg string
	}{
		{map[string]string{"cron": "61 * * * *"}, `SQS-J020 invalid schedule expression: "61 * * * *" (use 5 fields: minute hour day month day-of-week)`},
		{map[string]string{"timezone": "Mars/Olympus"}, `SQS-J020 invalid schedule expression: time zone "Mars/Olympus"`},
		{map[string]string{"preset": "activity", "check_minutes": "7"}, "SQS-B090 activity-based mode checks every 5, 10, 15, 30 or 60 minutes"},
		{map[string]string{"keep_last": "1001"}, "SQS-B030 retention values must be between 0 and 1000"},
		{map[string]string{"keep_last": "0", "keep_daily": "0", "keep_weekly": "0", "keep_monthly": "0"}, "SQS-B031 retention must keep at least one backup"},
		{map[string]string{"min_verified": "0"}, "SQS-B032 keep at least one verified backup (1 to 100)"},
	} {
		if msg := save(c.set); msg != c.msg {
			t.Errorf("policy %v: %q, want %q", c.set, msg, c.msg)
		}
	}
	if p, _ := a.Svc.Policy(ctx, l.ID); p.Timezone != "Europe/Madrid" || p.Cron != "30 1 * * *" || p.Enabled {
		t.Fatalf("a refused policy changed the stored one: %+v", p)
	}
	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	if st, _, body := op.do("POST", "/libraries/"+l.ID+"/policy", url.Values{"csrf": {opTok}, "preset": {"hourly"}}); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
		t.Fatalf("operator policy: %d", st)
	}
}

// Delete, recover from the trash, purge, revert and recreate are typed
// confirmations that queue their job with its "accepted" audit entry: a
// wrong identifier or state is refused with its code where the form is,
// nothing is queued without the entry, and none of them is open to an
// operator.
func TestLibraryDangerZoneQueuesAuditedJobs(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	active := fxLibrary(t, a, "apolo", core.StateActive)
	trashed := fxLibrary(t, a, "zeus", core.StateTrashed)
	withPrev := fxLibrary(t, a, "hera", core.StateActive)
	fxSet(t, a, "libraries", withPrev.ID, "prev_db_name = ?", "hera__prev_202610201200")
	missing := fxLibrary(t, a, "atlas", core.StateMissing)
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	post := func(lib *core.Library, action, confirm string, extra ...string) (string, string) {
		t.Helper()
		f := url.Values{"csrf": {tok}, "confirm": {confirm}}
		for i := 0; i+1 < len(extra); i += 2 {
			f.Set(extra[i], extra[i+1])
		}
		_, loc, msg := fxPost(boss, "/libraries/"+lib.ID+"/"+action, f)
		return loc, msg
	}
	base := func(lib *core.Library) string { return "/libraries/" + lib.ID }

	// The confirmation pages show the consequences and ask for the slug.
	if page := boss.page(base(active) + "/delete"); !strings.Contains(page, `data-confirm-match="apolo"`) {
		t.Error("the delete page does not ask for the identifier")
	}
	if page := boss.page(base(trashed) + "/purge"); !strings.Contains(page, `data-confirm-match="zeus"`) {
		t.Error("the purge page does not ask for the identifier")
	}
	if page := boss.page(base(withPrev) + "/revert"); !strings.Contains(page, `data-confirm-match="hera"`) {
		t.Error("the revert page does not ask for the identifier")
	}
	if st, loc, _ := boss.do("GET", base(active)+"/revert", nil); st != http.StatusSeeOther || loc != base(active) || flashOf(boss) != "SQS-R030 there is no previous state to recover" {
		t.Fatalf("revert page without a previous state: %d %s %q", st, loc, flashOf(boss))
	}

	for _, c := range []struct {
		lib             *core.Library
		action, confirm string
		loc, msg        string
	}{
		{active, "delete", "Apolo", base(active) + "/delete", "SQS-L040 type the library identifier to confirm"},
		{trashed, "delete", "zeus", base(trashed) + "/delete", "SQS-L041 cannot delete a library in state In the trash"},
		{active, "untrash", "", base(active), "SQS-L042 the library is not in the trash"},
		{active, "purge", "apolo", base(active) + "/purge", "SQS-L044 only libraries in the trash can be purged"},
		{trashed, "purge", "zeuss", base(trashed) + "/purge", "SQS-L040 type the library identifier to confirm"},
		{withPrev, "revert", "apolo", base(withPrev) + "/revert", "SQS-R002 type the library identifier to confirm"},
		{active, "revert", "apolo", base(active) + "/revert", "SQS-R030 there is no previous state to recover"},
		{active, "recreate", "apolo", base(active), "SQS-R040 only libraries with no database on the server can be recreated"},
		{missing, "recreate", "Atlas", base(missing), "SQS-R002 type the library identifier to confirm"},
		{missing, "recreate", "atlas", base(missing), "SQS-R041 the library has no verified backups"},
	} {
		if loc, msg := post(c.lib, c.action, c.confirm); loc != c.loc || msg != c.msg {
			t.Errorf("%s %s %q: %s %q, want %s %q", c.action, c.lib.Slug, c.confirm, loc, msg, c.loc, c.msg)
		}
	}
	// A revert of a library in the trash is refused even with a previous state.
	fxSet(t, a, "libraries", trashed.ID, "prev_db_name = ?", "zeus__prev_202610201200")
	if _, msg := post(trashed, "revert", "zeus"); msg != "SQS-R051 cannot revert a library in state In the trash" {
		t.Errorf("revert in the trash: %q", msg)
	}
	if n := jobCount(t, a); n != 0 {
		t.Fatalf("refused forms queued %d jobs", n)
	}

	old := time.Now().Add(-48 * time.Hour)
	verified := fxBackup(t, a, missing.ID, core.BackupOK, core.VerifyL2OK, old)
	older := fxBackup(t, a, missing.ID, core.BackupOK, core.VerifyL1OK, old.Add(-24*time.Hour))
	ok := []struct {
		lib             *core.Library
		action, confirm string
		typ, audit      string
		extra           []string
	}{
		{active, "delete", " apolo ", core.JobLibraryDelete, "library.delete", nil},
		{trashed, "untrash", "", core.JobLibraryUntrash, "library.untrash", nil},
		{withPrev, "revert", "hera", core.JobRestoreRevert, "restore.revert", nil},
		{missing, "recreate", "atlas", core.JobRestoreRecreate, "restore.recreate", nil},
	}
	fxAuditDown(t, a)
	for _, c := range ok {
		if _, msg := post(c.lib, c.action, c.confirm, c.extra...); msg != fxAuditDownErr {
			t.Errorf("%s without its audit entry: %q", c.action, msg)
		}
	}
	if _, msg := post(trashed, "purge", "zeus"); msg != fxAuditDownErr {
		t.Errorf("purge without its audit entry: %q", msg)
	}
	if n := jobCount(t, a); n != 0 {
		t.Fatalf("%d jobs queued without their audit entry", n)
	}
	fxAuditUp(t, a)
	for _, c := range ok {
		st, loc, _ := boss.do("POST", "/libraries/"+c.lib.ID+"/"+c.action, url.Values{"csrf": {tok}, "confirm": {c.confirm}})
		if st != http.StatusSeeOther {
			t.Fatalf("%s: %d", c.action, st)
		}
		j := fxQueuedJob(t, a, loc, c.typ, c.lib.ID, c.audit, "boss")
		if c.action == "recreate" && !strings.Contains(string(j.Params), `"backup_id":"`+verified+`"`) {
			t.Errorf("recreate without a choice did not take the newest verified backup: %s", j.Params)
		}
	}
	// Recreate from a backup chosen in the form (on a fresh library: one
	// recreate is already queued for atlas).
	other := fxLibrary(t, a, "eos", core.StateMissing)
	fxSet(t, a, "backups", older, "library_id = ?", other.ID)
	_, loc, _ := boss.do("POST", "/libraries/"+other.ID+"/recreate", url.Values{"csrf": {tok}, "confirm": {"eos"}, "backup": {older}})
	if j := fxQueuedJob(t, a, loc, core.JobRestoreRecreate, other.ID, "restore.recreate", "boss"); !strings.Contains(string(j.Params), older) {
		t.Errorf("recreate ignored the chosen backup: %s", j.Params)
	}
	// Purge needs the library in the trash: zeus is still there (its
	// recovery job is only queued).
	_, loc, _ = boss.do("POST", "/libraries/"+trashed.ID+"/purge", url.Values{"csrf": {tok}, "confirm": {"zeus"}})
	fxQueuedJob(t, a, loc, core.JobLibraryPurge, trashed.ID, "library.purge", "boss")

	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	before := jobCount(t, a)
	for _, p := range []string{"/delete", "/untrash", "/purge", "/revert", "/recreate"} {
		if st, _, body := op.do("POST", base(trashed)+p, url.Values{"csrf": {opTok}, "confirm": {"zeus"}}); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
			t.Errorf("operator %s: %d", p, st)
		}
	}
	for _, p := range []string{"/delete", "/purge", "/revert"} {
		if st, _, _ := op.do("GET", base(trashed)+p, nil); st != http.StatusForbidden {
			t.Errorf("operator GET %s: %d", p, st)
		}
	}
	if n := jobCount(t, a); n != before {
		t.Fatalf("an operator queued %d jobs", n-before)
	}
}

// The access forms (maintenance, initialization window, passwords, people,
// networks, sessions) check what they can in the catalog before touching
// PostgreSQL and refuse with their codes; when the change would run, its
// "accepted" entry is written first, so with the audit log down nothing
// changes (docs 08 §9). None of them is open to an operator.
func TestLibraryAccessFormsBeforePostgreSQL(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	active := fxLibrary(t, a, "apolo", core.StateActive)
	waiting := fxLibrary(t, a, "zeus", core.StateAwaitingInit)
	expired := fxLibrary(t, a, "hera", core.StateInitExpired)
	member, revoked := ulid.New(), ulid.New()
	now := store.NowT()
	for _, c := range []struct{ id, role, revokedAt string }{{member, "apolo_ana", ""}, {revoked, "apolo_bob", now}} {
		var rev any
		if c.revokedAt != "" {
			rev = c.revokedAt
		}
		if _, err := a.DB.ExecContext(ctx, `INSERT INTO library_credentials(id, library_id, kind, pg_role, person_label, password_enc, created_at, revoked_at) VALUES (?, ?, 'member', ?, ?, 'sealed', ?, ?)`,
			c.id, active.ID, c.role, strings.TrimPrefix(c.role, "apolo_"), now, rev); err != nil {
			t.Fatal(err)
		}
	}
	creds, _ := a.Svc.Credentials(ctx, active.ID)
	owner := ""
	for _, c := range creds {
		if c.Kind == "owner" {
			owner = c.ID
		}
	}
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	base := func(l *core.Library) string { return "/libraries/" + l.ID }
	access := func(l *core.Library) string { return base(l) + "?tab=access" }
	cases := []struct {
		path string
		form url.Values
		loc  string
		msg  string
	}{
		{base(waiting) + "/maintenance", url.Values{"on": {"1"}}, base(waiting), "SQS-L015 cannot change maintenance in state Waiting for Resolve"},
		{base(active) + "/maintenance", url.Values{"on": {"0"}}, base(active), "SQS-L015 cannot change maintenance in state Active"},
		{base(active) + "/window", nil, base(active), "SQS-L011 the library is not waiting for initialization"},
		{base(active) + "/password/rotate", url.Values{"cred": {"01NOSUCHCREDENTIAL00000000"}}, access(active), "SQS-L030 credential not found"},
		{base(active) + "/password/rotate", url.Values{"cred": {revoked}}, access(active), "SQS-L031 the credential is revoked"},
		{base(waiting) + "/people", url.Values{"person": {"ana"}}, access(waiting), "SQS-L032 people can only be added to active libraries"},
		{base(active) + "/people", url.Values{"person": {" Ana! "}}, access(active), "SQS-L001 invalid identifier: a person name uses 2 to 20 lowercase letters or digits, starting with a letter"},
		{base(active) + "/people/" + owner + "/revoke", nil, access(active), "SQS-L034 the primary credential cannot be revoked; rotate its password"},
		{base(active) + "/people/01NOSUCHCREDENTIAL00000000/revoke", nil, access(active), "SQS-L030 credential not found"},
		{base(active) + "/networks", url.Values{"cidrs": {"10.0.0.0/8\r\nnonsense"}}, access(active), `SQS-L020 invalid network: "nonsense"`},
	}
	for _, c := range cases {
		f := url.Values{"csrf": {tok}}
		for k, v := range c.form {
			f[k] = v
		}
		if _, loc, msg := fxPost(boss, c.path, f); loc != c.loc || msg != c.msg {
			t.Errorf("%s: %s %q, want %s %q", c.path, loc, msg, c.loc, c.msg)
		}
	}
	if len(auditEntries(t, a, "library.maintenance"))+len(auditEntries(t, a, "library.credential_rotate"))+len(auditEntries(t, a, "library.network_rules")) != 0 {
		t.Fatal("refused forms wrote audit entries")
	}

	fxAuditDown(t, a)
	for _, c := range []struct {
		path string
		form url.Values
		loc  string
	}{
		{base(active) + "/maintenance", url.Values{"on": {"1"}, "terminate": {"on"}}, base(active)},
		{base(expired) + "/window", nil, base(expired)},
		{base(active) + "/password/rotate", url.Values{"cred": {owner}}, access(active)},
		{base(active) + "/people/" + member + "/revoke", nil, access(active)},
		{base(active) + "/networks", url.Values{"cidrs": {"192.168.1.0/24, 10.8.0.7"}}, access(active)},
		{base(active) + "/sessions/4242/terminate", nil, base(active) + "?tab=activity"},
	} {
		f := url.Values{"csrf": {tok}}
		for k, v := range c.form {
			f[k] = v
		}
		if _, loc, msg := fxPost(boss, c.path, f); loc != c.loc || msg != fxAuditDownErr {
			t.Errorf("%s without its audit entry: %s %q", c.path, loc, msg)
		}
	}
	fxAuditUp(t, a)
	if l, _ := a.Svc.Library(ctx, active.ID); l.State != core.StateActive {
		t.Errorf("maintenance applied without its audit entry: %s", l.State)
	}
	if l, _ := a.Svc.Library(ctx, expired.ID); l.State != core.StateInitExpired {
		t.Errorf("window reopened without its audit entry: %s", l.State)
	}
	if rules, _ := a.Svc.NetworkRules(ctx, active.ID); len(rules) != 0 {
		t.Errorf("network rules stored without their audit entry: %+v", rules)
	}
	creds, _ = a.Svc.Credentials(ctx, active.ID)
	for _, c := range creds {
		if c.ID == member && c.RevokedAt != nil {
			t.Error("member revoked without its audit entry")
		}
		if c.ID == owner && c.RotatedAt != nil {
			t.Error("password rotated without its audit entry")
		}
	}
	for _, action := range []string{"library.maintenance", "library.init_window", "library.credential_rotate", "library.member_revoke", "library.network_rules", "library.session_terminate"} {
		if es := auditEntries(t, a, action); len(es) != 0 {
			t.Errorf("%s: %d entries", action, len(es))
		}
	}

	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	for _, p := range []string{"/maintenance", "/window", "/password/rotate", "/password/reveal", "/people", "/people/" + member + "/revoke", "/networks", "/sessions/1/terminate"} {
		if st, _, body := op.do("POST", base(active)+p, url.Values{"csrf": {opTok}, "on": {"1"}, "person": {"eve"}, "cidrs": {"0.0.0.0/0"}}); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
			t.Errorf("operator %s: %d", p, st)
		}
	}
	if page := op.page(access(active)); strings.Contains(page, "/password/rotate") || strings.Contains(page, `action="`+base(active)+`/people"`) {
		t.Error("an operator is offered the access forms")
	}
}

// The pages of a library that does not exist answer 404 with the code, and
// the connection instructions of a library without its owner credential
// say what is missing instead of rendering empty values.
func TestLibraryPagesOfMissingRecords(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	for _, p := range []string{"", "/instructions", "/delete", "/purge", "/revert", "/export"} {
		if st, _, body := boss.do("GET", "/libraries/01NOSUCHLIBRARY00000000000"+p, nil); st != http.StatusNotFound || !strings.Contains(body, "SQS-L002 Library not found.") {
			t.Errorf("GET %s of an unknown library: %d", p, st)
		}
	}
	for _, p := range []string{"/policy", "/password/reveal"} {
		if st, _, body := boss.do("POST", "/libraries/01NOSUCHLIBRARY00000000000"+p, url.Values{"csrf": {tok}}); st != http.StatusNotFound || !strings.Contains(body, "SQS-L002 Library not found.") {
			t.Errorf("POST %s of an unknown library: %d", p, st)
		}
	}
	l := fxLibrary(t, a, "apolo", core.StateActive)
	if _, err := a.DB.Exec(`DELETE FROM library_credentials WHERE library_id = ?`, l.ID); err != nil {
		t.Fatal(err)
	}
	if st, _, body := boss.do("GET", "/libraries/"+l.ID+"/instructions", nil); st != http.StatusInternalServerError || !strings.Contains(body, "SQS-L030 credential not found") {
		t.Fatalf("instructions without a credential: %d", st)
	}
	if _, loc, msg := fxPost(boss, "/libraries/"+l.ID+"/password/reveal", url.Values{"csrf": {tok}}); loc != "/libraries/"+l.ID || msg != "SQS-L030 credential not found" {
		t.Fatalf("reveal without a credential: %s %q", loc, msg)
	}
}
