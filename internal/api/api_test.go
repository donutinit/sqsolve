// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
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

// Audit AUD-008: an empty body decoded as the zero value (maintenance off)
// and a second JSON value after the first was ignored.
func TestDecodeRequiresExactlyOneJSONValue(t *testing.T) {
	type in struct {
		Enabled bool `json:"enabled"`
	}
	for body, want := range map[string]string{
		"":                                  "SQS-C061 invalid JSON body: the body is empty",
		"  \n\t ":                           "SQS-C061 invalid JSON body: the body is empty",
		`{"enabled":true}{"enabled":false}`: "SQS-C061 invalid JSON body: more than one JSON value",
		`{"enabled":true} {}`:               "SQS-C061 invalid JSON body: more than one JSON value",
		`{"enabled":true} x`:                "SQS-C061 invalid JSON body: more than one JSON value",
		`{"enabled":true,"other":1}`:        `SQS-C061 invalid JSON body: json: unknown field "other"`,
		`{"enabled":`:                       "SQS-C061 invalid JSON body: unexpected EOF",
	} {
		r := httptest.NewRequest("POST", "/api/v1/x", strings.NewReader(body))
		var v in
		if err := decode(r, &v); err == nil || err.Error() != want {
			t.Errorf("body %q: want %q, got %v", body, want, err)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/x", strings.NewReader(" {\"enabled\": true}\n"))
	var v in
	if err := decode(r, &v); err != nil || !v.Enabled {
		t.Fatalf("a valid body: %v %+v", err, v)
	}
}

// A body over the limit set by guard is refused with the same code.
func TestDecodeOversizedBody(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/x", strings.NewReader(`{"name":"`+strings.Repeat("a", 2<<20)+`"}`))
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var v struct {
		Name string `json:"name"`
	}
	err := decode(r, &v)
	if err == nil || err.Error() != "SQS-C061 invalid JSON body: http: request body too large" {
		t.Fatalf("want SQS-C061 for an oversized body, got %v", err)
	}
}
