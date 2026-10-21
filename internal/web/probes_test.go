// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/config"
)

// Ops M1: health probes every few seconds filled the log with info lines.
func TestProbesAreNotLogged(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{A: &app.App{Cfg: &config.Config{Location: time.UTC, InsecureHTTP: true}},
		log: slog.New(slog.NewTextHandler(&buf, nil)), mux: http.NewServeMux()}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	for _, p := range []string{"/healthz", "/readyz", "/api/v1/system/health", "/static/app.js"} {
		s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	if buf.Len() != 0 {
		t.Fatalf("probes were logged:\n%s", buf.String())
	}
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/libraries", nil))
	if !strings.Contains(buf.String(), "path=/libraries") {
		t.Fatalf("a page request is still logged: %q", buf.String())
	}
}
