// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
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
