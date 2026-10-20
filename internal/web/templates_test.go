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
	v := View{Title: "t", Section: "bibliotecas", P: &auth.Principal{User: &auth.User{Username: "admin", Role: auth.RoleAdmin}}, Data: data, Now: time.Now(), Query: url.Values{}}
	var buf bytes.Buffer
	if err := s.pages[name].ExecuteTemplate(&buf, "layout", v); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return buf.String()
}

func TestLibraryExportPageWithStills(t *testing.T) {
	l := &core.Library{ID: "01M5A5X0JZQYHV8GE4H1PK6VG9", Slug: "apolo", DisplayName: "Apolo", State: core.StateActive}
	still := exportStill{Still: drp.Still{ID: "ab12cd", Label: "0001", RecTC: "01:00:00:00"}, Created: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	data := func(can bool) map[string]any {
		return map[string]any{
			"L": l, "Tab": "exportar", "Enabled": true, "CanExport": can,
			"Projects":      []drp.Project{{ID: "p1", Name: "Corto"}},
			"PowerGrades":   []exportAlbum{{Album: drp.Album{ViewID: "v1", Name: "Looks"}, Stills: []exportStill{still}}},
			"ProjectAlbums": []exportAlbum{{Album: drp.Album{ViewID: "v2", ProjectID: "p1", ProjectName: "Corto"}, Stills: []exportStill{still}}},
		}
	}
	link := `href="/bibliotecas/01M5A5X0JZQYHV8GE4H1PK6VG9/exportar/stills/ab12cd"`
	out := renderPage(t, "library_export", data(true))
	if n := strings.Count(out, link); n != 2 {
		t.Errorf("with permission: %d .drx links, want 2 (one per album)", n)
	}
	if !strings.Contains(out, "01:00:00:00") || !strings.Contains(out, "0001") {
		t.Error("still label and timecode missing")
	}
	out = renderPage(t, "library_export", data(false))
	if strings.Contains(out, link) || strings.Contains(out, "/exportar/albumes/") || strings.Contains(out, "/exportar/proyectos/") {
		t.Error("without permission the download links must not appear")
	}
}

// TestLayoutBrand checks the identity (ADR 0020) in both layouts: the "Nodo"
// lockup with its ports and the navigation icons, plus the files the layout
// links to.
func TestLayoutBrand(t *testing.T) {
	l := &core.Library{ID: "01M5A5X0JZQYHV8GE4H1PK6VG9", Slug: "apolo", DisplayName: "Apolo", State: core.StateActive}
	shell := renderPage(t, "library_export", map[string]any{"L": l, "Tab": "exportar", "Enabled": false, "Err": "x"})
	for _, want := range []string{`class="brand" href="/" aria-label="sqsolve, ir al panel"`, `class="in"`, `class="out"`, `class="word"`, `rel="apple-touch-icon" href="/static/apple-touch-icon.png"`} {
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
