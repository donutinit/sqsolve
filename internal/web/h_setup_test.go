// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
)

// Recovering an installation from the setup screen needs the form token,
// the installation code and a master key that opens a control plane copy:
// any of them wrong sends back to the recovery form, stages nothing and
// keeps the installation code. Once an administrator exists, the form is
// closed.
func TestSetupRecoveryRefusals(t *testing.T) {
	ctx := context.Background()
	a, srv := sqliteApp(t)
	a.PrepareInstallCode(ctx)
	code := a.InstallCode()
	sys := filepath.Join(a.Cfg.BackupDir, "_system")
	os.MkdirAll(sys, 0o700)
	if err := os.WriteFile(filepath.Join(sys, "sqsolve-state-20261020T120000Z.db"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t, srv)
	if page := b.page("/setup"); !strings.Contains(page, `href="/setup?recover=1"`) {
		t.Fatal("the setup screen does not offer the recovery with a copy on the volume")
	}
	page := b.page("/setup?recover=1")
	pre := csrf(t, page)
	if !strings.Contains(page, `action="/setup/recover"`) || strings.Contains(page, "There are no control plane copies") {
		t.Fatal("the recovery form is missing")
	}
	key32 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for _, form := range []url.Values{
		{"csrf": {"forged-token-forged-token"}, "code": {code}, "key": {key32}},
		{"csrf": {pre}, "code": {"WRONG-CODE"}, "key": {key32}},
		{"csrf": {pre}, "code": {code}, "key": {"not the key"}},
		{"csrf": {pre}, "code": {code}, "key": {key32}},
	} {
		st, loc, _ := b.do("POST", "/setup/recover", form)
		if st != http.StatusSeeOther || !strings.HasPrefix(loc, "/setup?recover=1&") {
			t.Errorf("recover %v: %d %s", form, st, loc)
		}
	}
	if a.InstallCode() != code {
		t.Fatal("a failed recovery spent the installation code")
	}
	if _, err := os.Stat(a.Cfg.DBPath() + ".recover"); err == nil {
		t.Fatal("a failed recovery staged a database")
	}
	if es := auditEntries(t, a, "setup.recover"); len(es) != 0 {
		t.Fatalf("a failed recovery was recorded as done: %+v", es)
	}
	if _, err := a.Auth.CreateUser(ctx, audit.System, "boss", "", jobsAdminPass, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if st, loc, _ := b.do("POST", "/setup/recover", url.Values{"csrf": {pre}, "code": {code}, "key": {key32}}); st != http.StatusSeeOther || loc != "/login" {
		t.Fatalf("recover with an administrator: %d %s", st, loc)
	}
}
