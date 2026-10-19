// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestInitRefusesVolumeThatIgnoresChmod(t *testing.T) {
	dataDirCheck = func(string) error { return errors.New("su carpeta quedó en 0770 y PostgreSQL exige 0700") }
	defer func() { dataDirCheck = dataDirMode }()
	cfg := testConfig(t)
	pg := cfg.PGDir
	err := Init(cfg, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "SQS-C054") || !strings.Contains(err.Error(), "preset Generic") {
		t.Fatalf("want SQS-C054 naming the Generic preset, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(pg, "sqsolve", "postgresql.conf")); !os.IsNotExist(err) {
		t.Fatalf("init wrote PostgreSQL config into a volume it refused: %v", err)
	}
}
