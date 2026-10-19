// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pgconf generates the PostgreSQL configuration that sqsolve owns:
// postgresql.conf (written by sqsolve init) and pg_hba.conf (rewritten by
// sqsolve serve whenever libraries or credentials change).
package pgconf

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/pg"
)

// Params for postgresql.conf.
type Params struct {
	DataDir        string
	ConfDir        string
	SocketDir      string
	Port           int
	Listen         string // listen_addresses; "*" in containers
	MaxConnections int
	MemoryMB       int
	SSL            bool
	LogStatements  bool // laboratory only
}

// PostgresConf renders postgresql.conf.
func PostgresConf(p Params) string {
	shared := p.MemoryMB / 4
	if shared > 2048 {
		shared = 2048
	}
	if shared < 32 {
		shared = 32
	}
	eff := p.MemoryMB * 3 / 4
	listen := quote(p.Listen)
	var b strings.Builder
	w := func(k, v string) { fmt.Fprintf(&b, "%-28s = %s\n", k, v) }
	b.WriteString("# Generado por sqsolve init. Los cambios a mano se pierden en el siguiente arranque.\n")
	b.WriteString("# Ajusta los valores con las variables SQSOLVE_PG_* del compose.\n\n")
	w("data_directory", quote(p.DataDir))
	w("hba_file", quote(filepath.Join(p.ConfDir, "pg_hba.conf")))
	w("ident_file", quote(filepath.Join(p.ConfDir, "pg_ident.conf")))
	w("listen_addresses", listen)
	w("port", fmt.Sprint(p.Port))
	w("unix_socket_directories", quote(p.SocketDir))
	w("unix_socket_permissions", "0777")
	w("max_connections", fmt.Sprint(p.MaxConnections))
	w("superuser_reserved_connections", "5")
	w("password_encryption", "scram-sha-256")
	if p.SSL {
		w("ssl", "on")
		w("ssl_cert_file", quote(filepath.Join(p.ConfDir, "tls", "server.crt")))
		w("ssl_key_file", quote(filepath.Join(p.ConfDir, "tls", "server.key")))
		w("ssl_min_protocol_version", "'TLSv1.2'")
	} else {
		w("ssl", "off")
	}
	w("shared_buffers", fmt.Sprintf("%dMB", shared))
	w("effective_cache_size", fmt.Sprintf("%dMB", eff))
	w("maintenance_work_mem", "128MB")
	w("work_mem", "8MB")
	w("wal_level", "replica")
	w("max_wal_size", "2GB")
	w("checkpoint_timeout", "10min")
	w("tcp_keepalives_idle", "60")
	w("tcp_keepalives_interval", "10")
	w("tcp_keepalives_count", "6")
	w("log_destination", "'stderr'")
	w("logging_collector", "off")
	w("log_line_prefix", "'%m [%p] %u@%d %r '")
	w("log_connections", "on")
	w("log_disconnections", "on")
	w("log_lock_waits", "on")
	w("log_timezone", "'UTC'")
	w("timezone", "'UTC'")
	w("datestyle", "'iso, mdy'")
	w("lc_messages", "'C.UTF-8'")
	w("lc_monetary", "'C.UTF-8'")
	w("lc_numeric", "'C.UTF-8'")
	w("lc_time", "'C.UTF-8'")
	w("default_text_search_config", "'pg_catalog.simple'")
	if p.LogStatements {
		w("log_statement", "'all'")
	}
	return b.String()
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// HBALibrary describes the network access of one library.
type HBALibrary struct {
	DB    string
	Roles []string
	CIDRs []string // empty means any address
}

// HBAOptions shape pg_hba.conf.
type HBAOptions struct {
	Libraries     []HBALibrary
	MaintenanceDB bool // let library roles reach the "postgres" database
	RequireTLS    bool
}

// HBA renders pg_hba.conf. Administrative roles are rejected from the network
// before any library rule, and roles are always listed by name.
func HBA(o HBAOptions) (string, error) {
	var b strings.Builder
	b.WriteString("# Generado por sqsolve. Los cambios a mano se pierden.\n")
	b.WriteString("# TYPE  DATABASE  USER  ADDRESS  METHOD\n")
	line := func(f ...string) { b.WriteString(strings.Join(f, "\t") + "\n") }
	line("local", "all", pg.Superuser, "scram-sha-256")
	line("local", "all", pg.AdminRole, "scram-sha-256")
	line("local", "all", "all", "reject")
	admins := pg.Superuser + "," + pg.AdminRole
	line("host", "all", admins, "all", "reject")
	hostType := "host"
	if o.RequireTLS {
		hostType = "hostssl"
	}
	libs := append([]HBALibrary(nil), o.Libraries...)
	sort.Slice(libs, func(i, j int) bool { return libs[i].DB < libs[j].DB })
	for _, l := range libs {
		if err := pg.ValidateIdent(l.DB); err != nil {
			return "", err
		}
		if len(l.Roles) == 0 {
			continue
		}
		roles := append([]string(nil), l.Roles...)
		sort.Strings(roles)
		for _, r := range roles {
			if err := pg.ValidateIdent(r); err != nil {
				return "", err
			}
			if r == pg.Superuser || r == pg.AdminRole {
				return "", fmt.Errorf("SQS-P040 rol administrativo en una regla de biblioteca")
			}
		}
		cidrs := l.CIDRs
		if len(cidrs) == 0 {
			cidrs = []string{"all"}
		}
		b.WriteString("# biblioteca " + l.DB + "\n")
		for _, c := range cidrs {
			if c != "all" {
				norm, err := pg.ParseCIDR(c)
				if err != nil {
					return "", err
				}
				c = norm
			}
			line(hostType, l.DB, strings.Join(roles, ","), c, "scram-sha-256")
			if o.MaintenanceDB {
				line(hostType, "postgres", strings.Join(roles, ","), c, "scram-sha-256")
			}
		}
	}
	line("host", "all", "all", "all", "reject")
	return b.String(), nil
}

// WriteHBA writes pg_hba.conf keeping the previous version as .prev.
func WriteHBA(confDir, content string) (changed bool, err error) {
	path := filepath.Join(confDir, "pg_hba.conf")
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, []byte(content)) {
		return false, nil
	}
	if err == nil {
		if err := fsx.WriteFileAtomic(path+".prev", old, 0o640); err != nil {
			return false, err
		}
	}
	return true, fsx.WriteFileAtomic(path, []byte(content), 0o640)
}

// RestorePreviousHBA puts back pg_hba.conf.prev.
func RestorePreviousHBA(confDir string) error {
	path := filepath.Join(confDir, "pg_hba.conf")
	old, err := os.ReadFile(path + ".prev")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(path, old, 0o640)
}

// SelfSigned creates an ECDSA P-256 certificate and key.
func SelfSigned(commonName string, hosts []string, validFor time.Duration) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"sqsolve"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else if h != "" {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), nil
}

// CertInfo summarizes a PEM certificate.
type CertInfo struct {
	NotAfter    time.Time
	Fingerprint string // SHA-256, colon separated
	Subject     string
}

// ReadCert parses the first certificate of a PEM file.
func ReadCert(path string) (CertInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return CertInfo{}, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return CertInfo{}, fmt.Errorf("SQS-C020 certificado ilegible: %s", path)
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return CertInfo{}, err
	}
	sum := sha256.Sum256(c.Raw)
	hexs := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, 32)
	for i := 0; i < len(hexs); i += 2 {
		parts = append(parts, hexs[i:i+2])
	}
	return CertInfo{NotAfter: c.NotAfter, Fingerprint: strings.Join(parts, ":"), Subject: c.Subject.CommonName}, nil
}
