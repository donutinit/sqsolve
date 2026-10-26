// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// The access forms of a live library against PostgreSQL: the connection
// limit lands on its roles, a person gets a credential that connects, the
// network rules reach pg_hba.conf, a session is closed from the panel, a
// rotated password replaces the old one, a revoked person can no longer
// sign in, maintenance closes the library to the workstations (and its open
// sessions when asked) until it is reopened, and an expired initialization
// window opens again. Each change outside SQLite leaves its "accepted"
// entry and its result.
func TestLibraryAccessWithPostgreSQL(t *testing.T) {
	a, srv, c := fxPGPanel(t)
	ctx := context.Background()
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	l := fxActiveLibrary(t, a, c, boss, tok, "Apolo", "apolo", func(conn *pgx.Conn) {
		if _, err := conn.Exec(ctx, `CREATE TABLE "SM_Project"(id serial primary key, name text)`); err != nil {
			t.Fatal(err)
		}
	})
	base := "/libraries/" + l.ID
	access := base + "?tab=access"
	super, err := c.Connect(ctx, "postgres", c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close(ctx)
	connLimit := func(role string) (n int) {
		t.Helper()
		if err := super.QueryRow(ctx, `SELECT rolconnlimit FROM pg_roles WHERE rolname = $1`, role).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	outcomes := func(action string) string {
		var out []string
		for _, e := range auditEntries(t, a, action) {
			out = append([]string{e.Outcome}, out...)
		}
		return strings.Join(out, ",")
	}

	// Connection limit on a live library: on its roles.
	if _, loc, msg := fxPost(boss, base+"/settings", url.Values{"csrf": {tok}, "name": {"Apolo"}, "connlimit": {"7"}}); loc != base+"?tab=settings" || msg != "Changes saved." {
		t.Fatalf("connection limit: %s %q", loc, msg)
	}
	if n := connLimit("apolo"); n != 7 {
		t.Fatalf("owner role limit %d", n)
	}

	// A person.
	if _, loc, msg := fxPost(boss, base+"/people", url.Values{"csrf": {tok}, "person": {" Ana "}}); loc != access || msg != "Credential created for ana (user apolo__ana)." {
		t.Fatalf("add person: %s %q", loc, msg)
	}
	if _, _, msg := fxPost(boss, base+"/people", url.Values{"csrf": {tok}, "person": {"ana"}}); msg != "SQS-L033 role apolo__ana already exists" {
		t.Fatalf("add the same person twice: %q", msg)
	}
	var memberID string
	creds, _ := a.Svc.Credentials(ctx, l.ID)
	for _, cr := range creds {
		if cr.Kind == "member" {
			memberID = cr.ID
		}
	}
	if memberID == "" || connLimit("apolo__ana") != 7 {
		t.Fatalf("member credential %q, limit %d", memberID, connLimit("apolo__ana"))
	}
	_, _, reveal := boss.do("POST", base+"/password/reveal", url.Values{"csrf": {tok}, "cred": {memberID}})
	pw := regexp.MustCompile(`id="r-pass">([^<]+)<`).FindStringSubmatch(reveal)
	if pw == nil || !strings.Contains(reveal, "apolo__ana") {
		t.Fatal("the member's password is not revealed with its user")
	}
	ana, err := fxConnect(t, c, "apolo__ana", secrets.New(pw[1]), "apolo")
	if err != nil {
		t.Fatalf("the person cannot connect: %v", err)
	}
	if es := auditEntries(t, a, "library.credential_reveal"); len(es) == 0 || es[0].Actor.Name != "boss" || es[0].Details["role"] != "apolo__ana" {
		t.Fatalf("reveal audit %+v", es)
	}

	// Network rules.
	if _, loc, msg := fxPost(boss, base+"/networks", url.Values{"csrf": {tok}, "cidrs": {"127.0.0.1\n10.8.0.0/24, 10.8.0.9/24"}}); loc != access || msg != "Network rules applied." {
		t.Fatalf("networks: %s %q", loc, msg)
	}
	hba, _ := os.ReadFile(filepath.Join(a.Cfg.PGConfDir(), "pg_hba.conf"))
	if !strings.Contains(string(hba), "127.0.0.1/32") || !strings.Contains(string(hba), "10.8.0.0/24") {
		t.Fatalf("pg_hba.conf without the rules:\n%s", hba)
	}
	if rules, _ := a.Svc.NetworkRules(ctx, l.ID); len(rules) != 2 {
		t.Fatalf("rules %+v", rules)
	}
	if page := boss.page(access); !strings.Contains(page, "10.8.0.0/24\n") {
		t.Error("the access tab does not show the rules")
	}
	if got := outcomes("library.network_rules"); got != "accepted,ok" {
		t.Fatalf("network rules audit: %s", got)
	}

	// Ending a session.
	var pid int32
	if err := ana.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if page := boss.page(base + "?tab=activity"); !strings.Contains(page, fmt.Sprintf("/sessions/%d/terminate", pid)) {
		t.Fatal("the activity tab does not list the session")
	}
	terminate := fmt.Sprintf("%s/sessions/%d/terminate", base, pid)
	if _, loc, msg := fxPost(boss, terminate, url.Values{"csrf": {tok}}); loc != base+"?tab=activity" || msg != "Session closed." {
		t.Fatalf("terminate: %s %q", loc, msg)
	}
	if _, err := ana.Exec(ctx, `SELECT 1`); err == nil {
		t.Fatal("the closed session still works")
	}
	ana.Close(ctx)
	if _, _, msg := fxPost(boss, terminate, url.Values{"csrf": {tok}}); msg != "SQS-L016 the session no longer exists or does not belong to this library" {
		t.Fatalf("terminate twice: %q", msg)
	}
	if got := outcomes("library.session_terminate"); got != "accepted,ok,accepted,failed" {
		t.Fatalf("terminate audit: %s", got)
	}

	// Rotating the owner's password.
	oldPW := fxReveal(t, a, l.ID, "")
	if _, loc, msg := fxPost(boss, base+"/password/rotate", url.Values{"csrf": {tok}}); loc != access || msg != "Password changed. Update it on the workstations that use this credential." {
		t.Fatalf("rotate: %s %q", loc, msg)
	}
	if conn, err := c.ConnectTCP(ctx, "apolo", oldPW, "apolo"); err == nil {
		conn.Close(ctx)
		t.Fatal("the old password still works")
	}
	newPW := fxReveal(t, a, l.ID, "")
	owner, err := fxConnect(t, c, "apolo", newPW, "apolo")
	if err != nil {
		t.Fatalf("the new password does not work: %v", err)
	}
	defer owner.Close(ctx)

	// Revoking the person.
	if _, loc, msg := fxPost(boss, base+"/people/"+memberID+"/revoke", url.Values{"csrf": {tok}}); loc != access || msg != "Credential revoked and sessions closed." {
		t.Fatalf("revoke: %s %q", loc, msg)
	}
	if conn, err := c.ConnectTCP(ctx, "apolo__ana", secrets.New(pw[1]), "apolo"); err == nil {
		conn.Close(ctx)
		t.Fatal("a revoked person still connects")
	}
	if got := outcomes("library.member_revoke"); got != "accepted,ok" {
		t.Fatalf("revoke audit: %s", got)
	}

	// Maintenance closes the library and, when asked, its open sessions.
	if _, loc, msg := fxPost(boss, base+"/maintenance", url.Values{"csrf": {tok}, "on": {"1"}, "terminate": {"on"}}); loc != base ||
		msg != "Library in maintenance: it does not accept new connections. 1 sessions were closed." {
		t.Fatalf("maintenance on: %s %q", loc, msg)
	}
	if got, _ := a.Svc.Library(ctx, l.ID); got.State != core.StateMaintenance {
		t.Fatalf("state %s", got.State)
	}
	if _, err := owner.Exec(ctx, `SELECT 1`); err == nil {
		t.Fatal("an open session survived maintenance with terminate")
	}
	if conn, err := c.ConnectTCP(ctx, "apolo", newPW, "apolo"); err == nil {
		conn.Close(ctx)
		t.Fatal("a workstation connects during maintenance")
	}
	if _, _, msg := fxPost(boss, base+"/maintenance", url.Values{"csrf": {tok}, "on": {"1"}}); msg != "SQS-L015 cannot change maintenance in state In maintenance" {
		t.Fatalf("maintenance twice: %q", msg)
	}
	if _, loc, msg := fxPost(boss, base+"/maintenance", url.Values{"csrf": {tok}, "on": {"0"}}); loc != base || msg != "Connections reopened." {
		t.Fatalf("maintenance off: %s %q", loc, msg)
	}
	conn, err := fxConnect(t, c, "apolo", newPW, "apolo")
	if err != nil {
		t.Fatalf("no connection after reopening: %v", err)
	}
	conn.Close(ctx)
	if got := outcomes("library.maintenance"); got != "accepted,ok,accepted,ok" {
		t.Fatalf("maintenance audit: %s", got)
	}

	// An expired initialization window opens again.
	_, loc, _ := boss.do("POST", "/libraries/new", url.Values{"csrf": {tok}, "name": {"Zeus"}, "slug": {"zeus"}, "strategy": {"resolve"}})
	zeus := strings.TrimPrefix(loc, "/libraries/")
	for i := 0; i < 100; i++ {
		if z, _ := a.Svc.Library(ctx, zeus); z != nil && z.State == core.StateAwaitingInit {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	fxSet(t, a, "libraries", zeus, "state = 'init_expired'")
	if _, err := super.Exec(ctx, `ALTER ROLE zeus NOCREATEDB`); err != nil {
		t.Fatal(err)
	}
	if _, loc, msg := fxPost(boss, "/libraries/"+zeus+"/window", url.Values{"csrf": {tok}}); loc != "/libraries/"+zeus || msg != "Initialization window reopened." {
		t.Fatalf("reopen window: %s %q", loc, msg)
	}
	var createdb bool
	super.QueryRow(ctx, `SELECT rolcreatedb FROM pg_roles WHERE rolname = 'zeus'`).Scan(&createdb)
	if z, _ := a.Svc.Library(ctx, zeus); z.State != core.StateAwaitingInit || z.InitWindowExpiresAt == nil || !createdb {
		t.Fatalf("after reopening: %+v createdb=%v", z, createdb)
	}

	// Regenerating pg_hba.conf from Maintenance.
	if _, loc, msg := fxPost(boss, "/settings/maintenance/hba", url.Values{"csrf": {tok}}); loc != "/settings?tab=maintenance" || msg != "pg_hba.conf regenerated and PostgreSQL reloaded." {
		t.Fatalf("hba: %s %q", loc, msg)
	}
	if got := outcomes("maintenance.hba"); got != "accepted,ok" {
		t.Fatalf("hba audit: %s", got)
	}
}
