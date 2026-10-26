// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/pgconf"
)

// Panel certificate. sqsolve generates a self-signed one (subject and
// organization "sqsolve", two years) and renews it when fewer than 30 days
// remain, at startup and in the daily certificate check. A certificate
// uploaded by the administrator is never replaced. Key and certificate are
// always written as a pair (fsx.WritePair).

const (
	panelCertValidity    = 2 * 365 * 24 * time.Hour
	panelCertRenewBefore = 30 * 24 * time.Hour
)

// selfGenerated reports whether c is the certificate sqsolve generates for
// the panel: self-signed with subject and organization "sqsolve".
func selfGenerated(c *x509.Certificate) bool {
	return c.Subject.CommonName == "sqsolve" && len(c.Subject.Organization) == 1 && c.Subject.Organization[0] == "sqsolve" &&
		bytes.Equal(c.RawSubject, c.RawIssuer)
}

func (a *App) panelHosts() []string {
	host, _ := os.Hostname()
	hosts := []string{"localhost", "127.0.0.1", host}
	if h := a.Svc.AdvertisedHost(context.Background()); h != "" {
		hosts = append(hosts, h)
	}
	return hosts
}

// generatePanelCert writes a new self-signed pair, key first.
func (a *App) generatePanelCert() (tls.Certificate, error) {
	crt, key := panelCertPath(a.Cfg), panelKeyPath(a.Cfg)
	cp, kp, err := pgconf.SelfSigned("sqsolve", a.panelHosts(), panelCertValidity)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.MkdirAll(filepath.Dir(crt), 0o700); err != nil {
		return tls.Certificate{}, err
	}
	if err := fsx.WritePair(key, kp, 0o600, crt, cp, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(cp, kp)
}

// LoadPanelCert loads the panel certificate at startup. It completes a write
// interrupted between key and certificate, generates a self-signed pair when
// there is none or when the files on disk do not belong together (keeping
// them aside as .replaced-<time>), and renews the self-generated one when it
// is about to expire.
func (a *App) LoadPanelCert() error {
	crt, key := panelCertPath(a.Cfg), panelKeyPath(a.Cfg)
	if _, err := fsx.RecoverPair(key, crt); err != nil {
		return fmt.Errorf("SQS-C020 panel certificate: %w", err)
	}
	cb, cerr := os.ReadFile(crt)
	kb, kerr := os.ReadFile(key)
	for _, e := range []error{cerr, kerr} {
		// A file that exists but cannot be read (owner, mode) is not
		// replaced: that is a permissions problem to fix.
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return fmt.Errorf("SQS-C020 panel certificate: %w", e)
		}
	}
	var pair tls.Certificate
	var err error
	if cerr == nil && kerr == nil {
		pair, err = tls.X509KeyPair(cb, kb)
	}
	if cerr != nil || kerr != nil || err != nil {
		if cerr == nil || kerr == nil {
			stamp := time.Now().UTC().Format("20060102T150405Z")
			for _, p := range []string{crt, key} {
				if exists(p) {
					os.Rename(p, p+".replaced-"+stamp)
				}
			}
			a.Log.Warn("SQS-C083 the panel certificate and its key did not belong together; a new self-signed certificate was generated and the old files kept aside", "err", err)
		}
		if pair, err = a.generatePanelCert(); err != nil {
			return fmt.Errorf("SQS-C020 panel certificate: %w", err)
		}
	}
	a.cert.Store(&pair)
	return a.RenewPanelCert(context.Background())
}

// RenewPanelCert renews the self-generated certificate in use when fewer
// than 30 days remain and swaps it without a restart.
func (a *App) RenewPanelCert(ctx context.Context) error {
	cur := a.cert.Load()
	if cur == nil || len(cur.Certificate) == 0 {
		return nil
	}
	x, err := x509.ParseCertificate(cur.Certificate[0])
	if err != nil || !selfGenerated(x) || time.Until(x.NotAfter) >= panelCertRenewBefore {
		return nil
	}
	pair, err := a.generatePanelCert()
	if err != nil {
		return fmt.Errorf("SQS-C082 could not renew the panel certificate, which expires on %s: %w", x.NotAfter.Format("2006-01-02"), err)
	}
	a.cert.Store(&pair)
	a.Audit.BestEffort(ctx, audit.System, "tls.panel_renewed", "system", "", audit.OK, map[string]any{"previous_not_after": x.NotAfter.UTC().Format(time.RFC3339)})
	a.Log.Info("panel certificate renewed", "previous_not_after", x.NotAfter)
	return nil
}

// InstallPanelCert replaces the panel certificate with one provided by the
// administrator.
func (a *App) InstallPanelCert(certPEM, keyPEM []byte) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("SQS-C021 the certificate and the key do not match: %w", err)
	}
	if err := fsx.WritePair(panelKeyPath(a.Cfg), keyPEM, 0o600, panelCertPath(a.Cfg), certPEM, 0o644); err != nil {
		return err
	}
	a.cert.Store(&pair)
	return nil
}
