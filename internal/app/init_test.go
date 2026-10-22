// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	pg := t.TempDir()
	return &config.Config{AppDir: t.TempDir(), BackupDir: t.TempDir(), PGDir: pg, PGDataDir: filepath.Join(pg, "18", "docker"),
		PGSocket: t.TempDir(), PGPort: 5432, PGListen: "*", PGMaxConnections: 50, PGMemoryMB: 256,
		UID: os.Getuid(), GID: os.Getgid(), PGUID: os.Getuid(), PGGID: os.Getgid(), Location: time.UTC}
}

func TestInitPreparesDataDir(t *testing.T) {
	cfg := testConfig(t)
	if err := Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{cfg.PGDataDir: 0o700, filepath.Dir(cfg.PGDataDir): 0o755} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Errorf("%s: mode %04o, want %04o", path, got, want)
		}
	}
	if err := os.Chmod(cfg.PGDataDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(cfg.PGDataDir); st.Mode().Perm() != 0o700 {
		t.Errorf("a second init left PGDATA at %04o", st.Mode().Perm())
	}
}

// A TrueNAS dataset made with the Apps preset gives new folders 0770 from its
// ACL, whatever mode they are created with, and the PostgreSQL user cannot
// get through them to PGDATA.
func TestInitOpensFoldersAboveDataDir(t *testing.T) {
	cfg := testConfig(t)
	parent := filepath.Dir(cfg.PGDataDir)
	if err := os.MkdirAll(parent, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(parent); st.Mode().Perm() != 0o755 {
		t.Errorf("%s left at %04o", parent, st.Mode().Perm())
	}
}

func TestDataDirModeNeedsReachableFolders(t *testing.T) {
	root := t.TempDir()
	pgdata := filepath.Join(root, "18", "docker")
	if err := os.MkdirAll(pgdata, 0o700); err != nil {
		t.Fatal(err)
	}
	os.Chmod(root, 0o755)
	os.Chmod(filepath.Dir(pgdata), 0o770)
	if err := dataDirMode(root, pgdata); err == nil || !strings.Contains(err.Error(), "cannot enter") {
		t.Fatalf("want an unreachable folder error, got %v", err)
	}
	os.Chmod(filepath.Dir(pgdata), 0o755)
	if err := dataDirMode(root, pgdata); err != nil {
		t.Fatal(err)
	}
}

func TestInitRefusesVolumeThatIgnoresChmod(t *testing.T) {
	dataDirCheck = func(string, string) error {
		return errors.New("its folder was left at 0770 and PostgreSQL requires 0700")
	}
	defer func() { dataDirCheck = dataDirMode }()
	cfg := testConfig(t)
	pg := cfg.PGDir
	err := Init(cfg, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "SQS-C054") || !strings.Contains(err.Error(), "Generic preset") {
		t.Fatalf("want SQS-C054 naming the Generic preset, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(pg, "sqsolve", "postgresql.conf")); !os.IsNotExist(err) {
		t.Fatalf("init wrote PostgreSQL config into a volume it refused: %v", err)
	}
}

// Audit AUD-003: init ignored a failed chmod of an existing secret, folder
// or TLS key and reported success with the old mode in place.
func TestInitFailsWhenAModeCannotBeSet(t *testing.T) {
	for _, c := range []struct {
		path func(*config.Config) string
		mode os.FileMode
	}{
		{masterKeyPath, 0o400},
		{adminPassPath, 0o400},
		{instanceIDPath, 0o600},
		{func(c *config.Config) string { return c.SecretsDir() }, 0o700},
		{func(c *config.Config) string { return c.SuperuserPasswordFile() }, 0o440},
		{func(c *config.Config) string { return filepath.Join(c.PGConfDir(), "tls", "server.key") }, 0o600},
		{func(c *config.Config) string { return c.PGConfDir() }, 0o750},
	} {
		cfg := testConfig(t)
		if err := Init(cfg, io.Discard); err != nil {
			t.Fatal(err)
		}
		path := c.path(cfg)
		chmod = func(p string, m os.FileMode) error {
			if p == path {
				return &fs.PathError{Op: "chmod", Path: p, Err: syscall.EPERM}
			}
			return os.Chmod(p, m)
		}
		err := Init(cfg, io.Discard)
		chmod = os.Chmod
		want := fmt.Sprintf("SQS-C085 could not set the mode of %s to %04o: chmod %s: operation not permitted", path, c.mode, path)
		if err == nil || err.Error() != want {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

// The backups volume and the db volume root are not secret: a mode that
// cannot be set there is a warning, so a NAS share does not stop init.
func TestInitWarnsWhenAVolumeRootKeepsItsMode(t *testing.T) {
	cfg := testConfig(t)
	if err := Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	chmod = func(p string, m os.FileMode) error {
		if p == cfg.BackupDir || p == cfg.PGDir {
			return &fs.PathError{Op: "chmod", Path: p, Err: syscall.EPERM}
		}
		return os.Chmod(p, m)
	}
	defer func() { chmod = os.Chmod }()
	var out strings.Builder
	if err := Init(cfg, &out); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{cfg.BackupDir, cfg.PGDir} {
		if !strings.Contains(out.String(), "warning: could not set the mode of "+p) {
			t.Errorf("no warning for %s:\n%s", p, out.String())
		}
	}
}
