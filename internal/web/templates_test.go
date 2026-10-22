// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/drp"
)

// renderPage executes a page through the real layout with an admin principal,
// without an HTTP server: enough to catch template errors that only appear
// with data the end-to-end test cannot produce (stills, albums).
func renderPage(t *testing.T, name string, data map[string]any) string {
	t.Helper()
	s := &Server{A: &app.App{Cfg: &config.Config{Location: time.UTC}}}
	if err := s.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	v := View{Title: "t", Section: "libraries", P: &auth.Principal{User: &auth.User{Username: "admin", Role: auth.RoleAdmin}}, Data: data, Now: time.Now(), Query: url.Values{}}
	var buf bytes.Buffer
	if err := s.pages[name].ExecuteTemplate(&buf, "layout", v); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return buf.String()
}

func TestLibraryExportPageWithStills(t *testing.T) {
	l := &core.Library{ID: "01M5A5X0JZQYHV8GE4H1PK6VG9", Slug: "apolo", DisplayName: "Apolo", State: core.StateActive}
	still := exportStill{Still: drp.Still{ID: "ab12cd", Label: "0001", RecTC: "01:00:00:00"}, Created: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	projects := []drp.Project{{ID: "p1", Name: "Short"}, {ID: "p2", Name: "Long feature"}}
	power := []exportAlbum{{Album: drp.Album{ViewID: "v1", Name: "Looks"}, Stills: []exportStill{still}}}
	byProject := []exportAlbum{{Album: drp.Album{ViewID: "v2", ProjectID: "p1", ProjectName: "Short"}, Stills: []exportStill{still}}}
	data := func(can bool, query string) map[string]any {
		q, _ := url.ParseQuery(query)
		return map[string]any{
			"L": l, "Tab": "export", "Enabled": true, "CanExport": can,
			"Catalog": buildExportCatalog(q, projects, power, byProject),
		}
	}
	link := `href="/libraries/01M5A5X0JZQYHV8GE4H1PK6VG9/export/stills/ab12cd"`
	// The projects view: the view switch with the totals, a search box, the
	// still count of each project linking to its stills, and no .drx links.
	out := renderPage(t, "library_export", data(true, ""))
	for _, want := range []string{`href="/libraries/01M5A5X0JZQYHV8GE4H1PK6VG9/export?view=stills" >Stills <span class="muted small">2</span>`, `Projects <span class="muted small">2</span>`, `name="q"`, `?view=stills&project=p1">1 still</a>`, `Long feature`, `/export/projects/p2"`} {
		if !strings.Contains(out, want) {
			t.Errorf("projects view lacks %s", want)
		}
	}
	if strings.Contains(out, link) || strings.Contains(out, "PowerGrades") {
		t.Error("projects view shows stills")
	}
	// A search narrows the table and says so.
	out = renderPage(t, "library_export", data(true, "q=long"))
	if !strings.Contains(out, "1 of 2 match") || strings.Contains(out, `data-label="Project">Short<`) || !strings.Contains(out, `value="long"`) {
		t.Error("search did not narrow the projects")
	}
	// The stills view: both albums with their .drx links, collapsed.
	out = renderPage(t, "library_export", data(true, "view=stills"))
	if n := strings.Count(out, link); n != 2 {
		t.Errorf("with permission: %d .drx links, want 2 (one per album)", n)
	}
	if !strings.Contains(out, "01:00:00:00") || !strings.Contains(out, "0001") || !strings.Contains(out, `<details class="more" >`) {
		t.Error("still label, timecode or collapsed album missing")
	}
	// Narrowed to one project: its name in the heading, the album expanded
	// as a plain section, no PowerGrades.
	out = renderPage(t, "library_export", data(true, "project=p1"))
	if !strings.Contains(out, "Stills of Short") || strings.Contains(out, "PowerGrades") || strings.Count(out, link) != 1 || !strings.Contains(out, `?view=stills">All stills</a>`) {
		t.Error("project narrowing wrong")
	}
	out = renderPage(t, "library_export", data(false, "view=stills"))
	if strings.Contains(out, link) || strings.Contains(out, "/export/albums/") || strings.Contains(out, "/export/projects/") {
		t.Error("without permission the download links must not appear")
	}
}

// TestLayoutBrand checks the identity (ADR 0020) in both layouts: the "Node"
// lockup with its ports and the navigation icons, plus the files the layout
// links to.
func TestLayoutBrand(t *testing.T) {
	l := &core.Library{ID: "01M5A5X0JZQYHV8GE4H1PK6VG9", Slug: "apolo", DisplayName: "Apolo", State: core.StateActive}
	shell := renderPage(t, "library_export", map[string]any{"L": l, "Tab": "export", "Enabled": false, "Err": "x"})
	for _, want := range []string{`class="brand" href="/" aria-label="sqsolve, go to the dashboard"`, `class="in"`, `class="out"`, `class="word"`, `rel="apple-touch-icon" href="/static/apple-touch-icon.png"`} {
		if !strings.Contains(shell, want) {
			t.Errorf("shell layout lacks %s", want)
		}
	}
	if n := strings.Count(shell, `class="ni"`); n != 7 {
		t.Errorf("navigation icons: %d, want 7", n)
	}
	if n := strings.Count(shell, `class="port out"`); n < 2 {
		t.Errorf("output ports in icons: %d, want at least 2", n)
	}
	s := &Server{A: &app.App{Cfg: &config.Config{Location: time.UTC}}}
	if err := s.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.pages["error"].ExecuteTemplate(&buf, "layout", View{Title: "t", Data: map[string]any{"Message": "m"}, Now: time.Now(), Query: url.Values{}}); err != nil {
		t.Fatal(err)
	}
	if bare := buf.String(); !strings.Contains(bare, `class="brand big"`) || !strings.Contains(bare, `role="img" aria-label="sqsolve"`) {
		t.Error("bare layout lacks the brand lockup with its accessible name")
	}
	for _, f := range []string{"static/favicon.svg", "static/apple-touch-icon.png"} {
		b, err := staticFS.ReadFile(f)
		if err != nil || len(b) == 0 {
			t.Errorf("%s not embedded: %v", f, err)
		}
	}
	if fav, _ := staticFS.ReadFile("static/favicon.svg"); !strings.Contains(string(fav), `rx="5"`) || strings.Contains(string(fav), "<path") {
		t.Error("favicon must be the reduced mark: node and ports, without the signal path")
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := http.Header{}
	setSecurityHeaders(h, true)
	for k, want := range map[string]string{"Strict-Transport-Security": "max-age=31536000", "Cross-Origin-Resource-Policy": "same-origin", "X-Frame-Options": "DENY"} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Error("CSP missing")
	}
	plain := http.Header{}
	setSecurityHeaders(plain, false)
	if plain.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must not be sent over plain HTTP")
	}
}

// Audit AUD-004: an entry written before its action (a queued job, a change
// outside SQLite) reads "Accepted" with a neutral outline badge, not as a
// failure.
func TestAuditLogShowsAcceptedEntries(t *testing.T) {
	entries := []audit.Entry{
		{Seq: 2, TS: time.Now(), Actor: audit.Actor{Type: "user", Name: "diego"}, Action: "library.delete", TargetType: "library", TargetID: "01M5A5X0JZQYHV8GE4H1PK6VG9", Outcome: audit.OK},
		{Seq: 1, TS: time.Now(), Actor: audit.Actor{Type: "user", Name: "diego"}, Action: "library.delete", TargetType: "library", TargetID: "01M5A5X0JZQYHV8GE4H1PK6VG9", Outcome: audit.Accepted},
	}
	page := renderPage(t, "activity", map[string]any{"Tab": "audit", "Audit": entries, "Count": int64(2)})
	if !strings.Contains(page, `<span class="badge info"><span class="dot" aria-hidden="true"></span>Accepted</span>`) {
		t.Fatal("the accepted entry has no neutral badge")
	}
	if !strings.Contains(page, `<span class="badge ok"><span class="dot" aria-hidden="true"></span>OK</span>`) {
		t.Fatal("the result entry has no OK badge")
	}
}
