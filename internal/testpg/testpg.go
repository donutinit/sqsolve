// SPDX-License-Identifier: AGPL-3.0-only

// Package testpg starts a throwaway PostgreSQL cluster for integration tests,
// configured exactly like production (sqsolve's postgresql.conf and
// pg_hba.conf, SCRAM, builtin collation). It only ever signals the server
// process it started.
package testpg

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/donutinit/sqsolve/internal/pgconf"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// Cluster is a running test server.
type Cluster struct {
	Base      string // parent of ConfDir, like the db volume root
	BinDir    string
	DataDir   string
	ConfDir   string
	SocketDir string
	Port      int
	SuperPass secrets.String
	AdminPass secrets.String
	LogPath   string
	cmd       *exec.Cmd
}

// BinDir finds initdb and postgres.
func BinDir() string {
	cands := []string{os.Getenv("SQSOLVE_TEST_PG_BIN")}
	matches, _ := filepath.Glob("/usr/lib/postgresql/*/bin")
	for i := len(matches) - 1; i >= 0; i-- {
		cands = append(cands, matches[i])
	}
	cands = append(cands, "/usr/pgsql-18/bin", "/usr/bin")
	for _, d := range cands {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "initdb")); err == nil {
			if _, err := os.Stat(filepath.Join(d, "postgres")); err == nil {
				return d
			}
		}
	}
	return ""
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func baseDir(t testing.TB) string {
	if d, err := os.UserCacheDir(); err == nil {
		dir := filepath.Join(d, "sqsolve-test")
		if os.MkdirAll(dir, 0o700) == nil {
			tmp, err := os.MkdirTemp(dir, "pg-")
			if err == nil {
				t.Cleanup(func() { os.RemoveAll(tmp) })
				return tmp
			}
		}
	}
	return t.TempDir()
}

// Start starts a cluster or skips the test when PostgreSQL is not installed
// (unless SQSOLVE_REQUIRE_PG=1).
func Start(t testing.TB) *Cluster {
	t.Helper()
	bin := BinDir()
	if bin == "" {
		if os.Getenv("SQSOLVE_REQUIRE_PG") == "1" {
			t.Fatal("PostgreSQL no está instalado y SQSOLVE_REQUIRE_PG=1")
		}
		t.Skip("PostgreSQL no está instalado; define SQSOLVE_TEST_PG_BIN")
	}
	base := baseDir(t)
	sock, err := os.MkdirTemp("/tmp", "sqs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sock) })
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	c := &Cluster{
		Base:      base,
		BinDir:    bin,
		DataDir:   filepath.Join(base, "data"),
		ConfDir:   filepath.Join(base, "sqsolve"),
		SocketDir: sock,
		Port:      port,
		SuperPass: secrets.GeneratePassword(24),
		AdminPass: secrets.GeneratePassword(24),
		LogPath:   filepath.Join(base, "postgres.log"),
	}
	os.MkdirAll(c.ConfDir, 0o700)
	pw := filepath.Join(base, "pw")
	os.WriteFile(pw, []byte(c.SuperPass.Reveal()+"\n"), 0o600)
	out, err := exec.Command(filepath.Join(bin, "initdb"), "-D", c.DataDir, "-U", "postgres", "--pwfile="+pw,
		"--auth-local=scram-sha-256", "--auth-host=reject", "--encoding=UTF8",
		"--locale-provider=builtin", "--builtin-locale=C.UTF-8", "--no-sync").CombinedOutput()
	if err != nil {
		t.Fatalf("initdb: %v\n%s", err, out)
	}
	os.Remove(pw)
	conf := pgconf.PostgresConf(pgconf.Params{
		DataDir: c.DataDir, ConfDir: c.ConfDir, SocketDir: c.SocketDir, Port: c.Port,
		Listen: "127.0.0.1", MaxConnections: 50, MemoryMB: 256,
	})
	conf += "fsync = off\nfull_page_writes = off\n"
	os.WriteFile(filepath.Join(c.ConfDir, "postgresql.conf"), []byte(conf), 0o600)
	hba, _ := pgconf.HBA(pgconf.HBAOptions{})
	os.WriteFile(filepath.Join(c.ConfDir, "pg_hba.conf"), []byte(hba), 0o600)
	os.WriteFile(filepath.Join(c.ConfDir, "pg_ident.conf"), nil, 0o600)

	logf, err := os.Create(c.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(bin, "postgres"), "-c", "config_file="+filepath.Join(c.ConfDir, "postgresql.conf"))
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c.cmd = cmd
	t.Cleanup(func() {
		c.Stop()
		logf.Close()
	})
	deadline := time.Now().Add(20 * time.Second)
	for {
		conn, err := c.Connect(context.Background(), "postgres", c.SuperPass, "postgres")
		if err == nil {
			conn.Close(context.Background())
			break
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(c.LogPath)
			t.Fatalf("PostgreSQL no arrancó: %v\n%s", err, log)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return c
}

// Connect opens a connection through the socket.
func (c *Cluster) Connect(ctx context.Context, user string, pass secrets.String, db string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d sslmode=disable", c.SocketDir, c.Port))
	if err != nil {
		return nil, err
	}
	cfg.User, cfg.Password, cfg.Database = user, pass.Reveal(), db
	return pgx.ConnectConfig(ctx, cfg)
}

// ConnectTCP opens a connection over 127.0.0.1, like a workstation would.
func (c *Cluster) ConnectTCP(ctx context.Context, user string, pass secrets.String, db string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=127.0.0.1 port=%d sslmode=disable connect_timeout=5", c.Port))
	if err != nil {
		return nil, err
	}
	cfg.User, cfg.Password, cfg.Database = user, pass.Reveal(), db
	return pgx.ConnectConfig(ctx, cfg)
}

// Log returns the server log.
func (c *Cluster) Log() string {
	b, _ := os.ReadFile(c.LogPath)
	return string(b)
}

// Stop shuts the server down (fast mode). Only our own child is signalled.
func (c *Cluster) Stop() {
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}
	done := make(chan struct{})
	go func() { c.cmd.Wait(); close(done) }()
	c.cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		c.cmd.Process.Kill()
		<-done
	}
	c.cmd = nil
}

// Restart stops and starts the same cluster again.
func (c *Cluster) Restart(t testing.TB) {
	c.Stop()
	logf, err := os.OpenFile(c.LogPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(c.BinDir, "postgres"), "-c", "config_file="+filepath.Join(c.ConfDir, "postgresql.conf"))
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c.cmd = cmd
	deadline := time.Now().Add(20 * time.Second)
	for {
		conn, err := c.Connect(context.Background(), "postgres", c.SuperPass, "postgres")
		if err == nil {
			conn.Close(context.Background())
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("PostgreSQL no rearrancó: %v\n%s", err, c.Log())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Contains is a helper for log assertions.
func Contains(s, sub string) bool { return strings.Contains(s, sub) }
