// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/store"
)

// TestRenderFailureAnswersX998: a page whose template fails with its data
// answers 500 with SQS-X998 in plain text, never half a page, and the cause
// goes to the log.
func TestRenderFailureAnswersX998(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{Location: time.UTC}
	var logs bytes.Buffer
	s := &Server{A: &app.App{Cfg: cfg, Svc: &core.Service{Cfg: cfg, DB: db}}, log: slog.New(slog.NewTextHandler(&logs, nil))}
	if err := s.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	// The error page reads .Data.Message, which this value does not have.
	rec := httptest.NewRecorder()
	s.renderStatus(rec, httptest.NewRequest("GET", "/x", nil), http.StatusOK, "error", "Error", "", struct{}{})
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != "Error rendering the page (SQS-X998)\n" {
		t.Fatalf("answer %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("Content-Type %q, body %q", ct, rec.Body.String())
	}
	for _, want := range []string{"msg=render", "page=error", "Message"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log lacks %q: %s", want, logs.String())
		}
	}
}

// TestPanicAnswersX999: a handler that panics behind the panel's middleware
// gets 500 with SQS-X999 in plain text and the security headers, the process
// goes on, and the panic and its path go to the log with the request line.
func TestPanicAnswersX999(t *testing.T) {
	var logs bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("lens cap on") })
	s := &Server{A: &app.App{Cfg: &config.Config{Location: time.UTC}}, log: slog.New(slog.NewTextHandler(&logs, nil)), mux: mux}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/boom", nil))
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != "Internal error (SQS-X999)\n" {
		t.Fatalf("answer %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") || rec.Header().Get("Content-Security-Policy") == "" ||
		rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("headers %v", rec.Header())
	}
	for _, want := range []string{`msg="panic in a request"`, "path=/boom", `panic="lens cap on"`, "msg=http", "status=500"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log lacks %q: %s", want, logs.String())
		}
	}
}
