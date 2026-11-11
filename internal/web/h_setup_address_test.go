// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The address the workstations use is checked before the administrator is
// created. It used to be refused only when the settings were saved: by then
// the account existed and the installation code was spent, so the setup
// ended on the login page with none of its settings stored. An address with
// a port (or the IPv6 address the panel was opened with) was enough.
func TestSetupChecksTheAddressBeforeCreatingTheAdministrator(t *testing.T) {
	ctx := context.Background()
	a, srv := sqliteApp(t)
	a.PrepareInstallCode(ctx)
	code := a.InstallCode()
	b := newBrowser(t, srv)
	form := url.Values{"csrf": {csrf(t, b.page("/setup"))}, "code": {code}, "username": {"diego"}, "password": {jobsAdminPass},
		"password2": {jobsAdminPass}, "host": {"192.168.1.10:5432"}, "name": {"Studio"}, "preset": {"daily"}}
	st, loc, _ := b.do("POST", "/setup", form)
	if st != http.StatusSeeOther || !strings.HasPrefix(loc, "/setup?") {
		t.Fatalf("an address with a port: %d %s", st, loc)
	}
	if f := flashOf(b); !strings.Contains(f, "SQS-C030 invalid advertised address: type only the IP or the name, without a port") {
		t.Fatalf("notice %q", f)
	}
	if has, err := a.Auth.HasAdmin(ctx); err != nil || has {
		t.Fatalf("the administrator was created before the address was checked: %v %v", has, err)
	}
	if a.InstallCode() != code {
		t.Fatal("a refused setup spent the installation code")
	}
	form.Set("host", "192.168.1.10")
	if _, loc, _ := b.do("POST", "/setup", form); loc != "/setup/kit" {
		t.Fatalf("setup with a valid address: %s", loc)
	}
	if got := a.Svc.Settings(ctx); got.AdvertisedHost != "192.168.1.10" || got.InstanceName != "Studio" || got.DefaultPolicy.Cron != "0 2 * * *" {
		t.Fatalf("settings of the setup were not stored: %+v", got)
	}
}
