// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/pgconf"
)

var auditActor = audit.Actor{Type: "user", ID: "U1", Name: "diego"}

// servePanel starts a.HTTPServer on a free local port and returns its address.
func servePanel(t *testing.T, a *App) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := a.HTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "panel") }))
	go func() {
		if srv.TLSConfig == nil {
			srv.Serve(ln)
		} else {
			srv.ServeTLS(ln, "", "")
		}
	}()
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// peerName is the common name of the certificate the panel presents.
func peerName(t *testing.T, addr string) (string, error) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].Subject.CommonName, nil
}

// The panel serves the certificate in use at each handshake: a certificate
// installed by the administrator is served without a restart, a pair that
// does not match is refused and leaves the served one in place, and without
// any certificate no TLS connection is accepted.
func TestPanelServesTheCurrentCertificate(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()
	addr := servePanel(t, a)
	if _, err := peerName(t, addr); err == nil {
		t.Fatal("a TLS connection without a certificate")
	}
	if err := a.LoadPanelCert(); err != nil {
		t.Fatal(err)
	}
	if cn, err := peerName(t, addr); err != nil || cn != "sqsolve" {
		t.Fatalf("self-signed certificate: %q %v", cn, err)
	}
	cp, kp, _ := pgconf.SelfSigned("studio.example", []string{"localhost"}, 400*24*time.Hour)
	if err := a.InstallPanelCert(cp, kp); err != nil {
		t.Fatal(err)
	}
	if cn, err := peerName(t, addr); err != nil || cn != "studio.example" {
		t.Fatalf("installed certificate: %q %v", cn, err)
	}
	_, otherKey, _ := pgconf.SelfSigned("other.example", []string{"localhost"}, time.Hour)
	if err := a.InstallPanelCert(cp, otherKey); err == nil || !strings.HasPrefix(err.Error(), "SQS-C021 the certificate and the key do not match: ") {
		t.Fatalf("mismatched pair: %v", err)
	}
	if cn, _ := peerName(t, addr); cn != "studio.example" {
		t.Fatalf("a refused pair changed the served certificate to %q", cn)
	}
	if info, err := a.PanelCertInfo(); err != nil || !strings.Contains(info.Subject, "studio.example") {
		t.Fatalf("a refused pair changed the files: %+v %v", info, err)
	}
	// TLS 1.1 and older are refused.
	if conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS11}); err == nil {
		conn.Close()
		t.Fatal("a TLS 1.1 connection was accepted")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	res, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "panel" {
		t.Fatalf("body %q", body)
	}
}

// A certificate that cannot be written is not installed: the served one and
// the files stay as they were.
func TestInstallPanelCertWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the folder mode")
	}
	ctx := context.Background()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()
	if err := a.LoadPanelCert(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(panelCertPath(cfg))
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	cp, kp, _ := pgconf.SelfSigned("studio.example", []string{"localhost"}, 400*24*time.Hour)
	if err := a.InstallPanelCert(cp, kp); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("install into a read-only folder: %v", err)
	}
	if certOf(t, a).Subject.CommonName != "sqsolve" {
		t.Fatal("a certificate that was not written is served")
	}
}

// With SQSOLVE_INSECURE_HTTP the panel speaks plain HTTP.
func TestPanelWithoutTLS(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	cfg.InsecureHTTP = true
	a := openApp(t, ctx, cfg)
	defer a.Close()
	addr := servePanel(t, a)
	res, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "panel" {
		t.Fatalf("body %q", body)
	}
}

// Links in notifications point at the address workstations use, with the
// panel's scheme and port; without an advertised address there is no link.
func TestNotificationLinks(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()
	if got := a.BaseURL(); got != "" {
		t.Fatalf("a link without an advertised address: %q", got)
	}
	for _, c := range []struct {
		host, listen string
		insecure     bool
		want         string
	}{
		{"10.0.0.5", ":9432", false, "https://10.0.0.5:9432"},
		{"10.0.0.5", "0.0.0.0:8443", false, "https://10.0.0.5:8443"},
		{"10.0.0.5", "", false, "https://10.0.0.5:9432"},
		{"10.0.0.5", ":8080", true, "http://10.0.0.5:8080"},
		{"fd00::5", ":9432", false, "https://[fd00::5]:9432"},
	} {
		cfg.AdvertisedHost, cfg.Listen, cfg.InsecureHTTP = c.host, c.listen, c.insecure
		if got := a.BaseURL(); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
	// The address saved in Settings wins over the configured one.
	cfg.Listen, cfg.InsecureHTTP = ":9432", false
	st := a.Svc.Settings(ctx)
	st.AdvertisedHost = "nas.local"
	if err := a.Svc.SaveSettings(ctx, auditActor, st); err != nil {
		t.Fatal(err)
	}
	if got := a.BaseURL(); got != "https://nas.local:9432" {
		t.Fatalf("with an address in Settings: %q", got)
	}
}
