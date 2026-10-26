// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/fsx"
)

// /readyz names what is not ready (PostgreSQL, the backups volume) with a
// 503, while /healthz only says the process answers.
func TestReadinessNamesWhatIsMissing(t *testing.T) {
	a, srv := sqliteApp(t)
	b := newBrowser(t, srv)
	if st, _, body := b.do("GET", "/healthz", nil); st != http.StatusOK || body != "ok\n" {
		t.Fatalf("healthz: %d %q", st, body)
	}
	if st, _, body := b.do("GET", "/readyz", nil); st != http.StatusServiceUnavailable || body != "not ready: postgresql\n" {
		t.Fatalf("readyz without PostgreSQL: %d %q", st, body)
	}
	if err := os.Remove(filepath.Join(a.Cfg.BackupDir, fsx.MarkerName)); err != nil {
		t.Fatal(err)
	}
	if st, _, body := b.do("GET", "/readyz", nil); st != http.StatusServiceUnavailable || body != "not ready: postgresql, backup volume\n" {
		t.Fatalf("readyz without the volume marker: %d %q", st, body)
	}
}

// Static files are served from the binary with an hour of caching and the
// same security headers as the pages; /favicon.ico points to the SVG.
func TestStaticFilesAreCached(t *testing.T) {
	_, srv := sqliteApp(t)
	b := newBrowser(t, srv)
	resp, err := b.c.Get(srv.URL + "/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "public, max-age=3600" ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("app.js: %d %v", resp.StatusCode, resp.Header)
	}
	if st, _, _ := b.do("GET", "/static/missing.js", nil); st != http.StatusNotFound {
		t.Fatalf("missing static file: %d", st)
	}
	if st, loc, _ := b.do("GET", "/favicon.ico", nil); st != http.StatusMovedPermanently || loc != "/static/favicon.svg" {
		t.Fatalf("favicon: %d %s", st, loc)
	}
}
