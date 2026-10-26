// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/store"
)

// The protection timeline on the dashboard marks each backup of the last
// 14 days by its verification (a deleted one still counts, a failed one is
// a cross), shades the periods longer than the stale limit (48 h) without a
// verified backup, and sums it up in words for screen readers. A live
// library without any verified backup is "bad"; one that is not live yet
// is "off".
func TestDashboardProtectionTimeline(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	now := time.Now().UTC()
	day := 24 * time.Hour
	apolo := fxLibrary(t, a, "apolo", core.StateActive)
	fxSet(t, a, "libraries", apolo.ID, "created_at = ?", store.T(now.Add(-20*day)))
	fxBackup(t, a, apolo.ID, core.BackupOK, core.VerifyL2OK, now.Add(-15*day)) // before the window: where coverage starts
	fxBackup(t, a, apolo.ID, core.BackupOK, core.VerifyL1OK, now.Add(-10*day))
	fxBackup(t, a, apolo.ID, core.BackupDeleted, core.VerifyL2OK, now.Add(-8*day-12*time.Hour))
	fxBackup(t, a, apolo.ID, core.BackupFailed, core.VerifyNone, now.Add(-3*day))
	fxBackup(t, a, apolo.ID, core.BackupOK, core.VerifyL2OK, now.Add(-day))
	fxBackup(t, a, apolo.ID, core.BackupOK, core.VerifyNone, now.Add(-2*time.Hour))
	hera := fxLibrary(t, a, "hera", core.StateMaintenance)
	fxSet(t, a, "libraries", hera.ID, "created_at = ?", store.T(now.Add(-3*day)))
	fxLibrary(t, a, "atlas", core.StateAwaitingInit)
	fxLibrary(t, a, "zeus", core.StateTrashed)

	boss, _ := jobsLogin(t, srv, "boss", jobsAdminPass)
	page := boss.page("/")
	// Gaps: from 13 days ago (two days after the backup of 15 days ago) to
	// the one of 10 days ago, 72 h; from 6.5 days ago to the one of a day
	// ago, 132 h.
	for _, want := range []string{
		"<title>apolo: 3 verified and 1 failed backups in 14 days; last verified 24 h ago; 204 hours without a recent verified backup</title>",
		"<title>hera: 0 verified and 0 failed backups in 14 days; last verified no verified backup; 24 hours without a recent verified backup</title>",
		"<title>atlas: 0 verified and 0 failed backups in 14 days; last verified no verified backup</title>",
		`href="/libraries/` + apolo.ID + `?tab=backups"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard lacks %s", want)
		}
	}
	// Each row ends with a badge of its tone and the age of the last
	// verified backup.
	for _, want := range []string{`badge warn">.{0,120}?24 h ago</span>`, `badge bad">.{0,120}?no verified backup</span>`, `badge off">.{0,120}?no verified backup</span>`} {
		if !regexp.MustCompile(want).MatchString(page) {
			t.Errorf("dashboard lacks the badge %s", want)
		}
	}
	if strings.Contains(page, ">zeus<") {
		t.Error("a library in the trash is on the dashboard")
	}
	when := func(d time.Duration) string { return now.Add(-d).Format("2006-01-02 15:04") }
	for _, want := range []string{
		`<line class="mk l1"`, `<title>` + when(10*day) + `: checksum and index OK</title>`,
		`<title>` + when(8*day+12*time.Hour) + `: test restore OK (already deleted by retention)</title>`,
		`<g class="mk failed"><title>` + when(3*day) + `: backup failed</title>`,
		`<title>` + when(day) + `: test restore OK</title>`,
		`<line class="mk unverified"`, `<title>` + when(2*time.Hour) + `: unverified</title>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("apolo's track lacks %s", want)
		}
	}
	if n := strings.Count(page, `<rect class="gap"`); n != 3 {
		t.Errorf("%d shaded gaps, want 3 (two for apolo, one for hera)", n)
	}
	if strings.Contains(page, when(15*day)+":") {
		t.Error("a backup before the window is drawn")
	}
	// The library page draws the same backups over 30 days, the old one too.
	if lib := boss.page("/libraries/" + apolo.ID + "?tab=backups"); !strings.Contains(lib, "<title>"+when(15*day)+": test restore OK</title>") ||
		!strings.Contains(lib, "apolo: 4 verified and 1 failed backups in 30 days; last verified 24 h ago") {
		t.Error("the 30-day track of the library page is wrong")
	}
}
