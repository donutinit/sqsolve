// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/config"
)

// The API ignored SQSOLVE_TRUSTED_PROXIES, so behind a reverse proxy every
// API caller (and its audit entries) carried the proxy's address.
func TestClientIPHonoursTrustedProxies(t *testing.T) {
	a := &API{A: &app.App{Cfg: &config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}}}
	r := httptest.NewRequest("GET", "/api/v1/jobs", nil)
	r.RemoteAddr = "10.1.2.3:4000"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.7")
	if got := a.clientIP(r); got != "198.51.100.7" {
		t.Fatalf("behind a trusted proxy: %s", got)
	}
	r.RemoteAddr = "203.0.113.5:4000"
	if got := a.clientIP(r); got != "203.0.113.5" {
		t.Fatalf("from an untrusted peer: %s", got)
	}
}

func TestBusyIsServiceUnavailable(t *testing.T) {
	if got := statusFor(auth.ErrBusy); got != http.StatusServiceUnavailable {
		t.Fatalf("status for SQS-A050: %d", got)
	}
	w := httptest.NewRecorder()
	problem(w, httptest.NewRequest("POST", "/api/v1/x", nil), statusFor(auth.ErrBusy), auth.ErrBusy)
	var p Problem
	json.NewDecoder(w.Body).Decode(&p)
	if w.Code != 503 || w.Header().Get("Retry-After") != "5" || p.Code != "SQS-A050" || p.Detail != auth.ErrBusy.Error() {
		t.Fatalf("busy problem: %d %q %+v", w.Code, w.Header().Get("Retry-After"), p)
	}
	// The type points at the error page of docs 10 §1.
	if p.Type != "/docs/errors/SQS-A050" {
		t.Fatalf("problem type %q", p.Type)
	}
}
