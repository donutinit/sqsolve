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

func TestKeepsPermissionsOnPOSIXDir(t *testing.T) {
	dir := t.TempDir()
	if err := keepsPermissions(dir); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".sqsolve-perms-*")); len(left) != 0 {
		t.Fatalf("probe left files behind: %v", left)
	}
}

func TestInitRefusesVolumeThatIgnoresChmod(t *testing.T) {
	permProbe = func(string) error { return errors.New("se pidió 0640 y quedó 0740") }
	defer func() { permProbe = keepsPermissions }()
	pg := t.TempDir()
	cfg := &config.Config{AppDir: t.TempDir(), BackupDir: t.TempDir(), PGDir: pg, PGDataDir: filepath.Join(pg, "18", "docker"),
		PGSocket: t.TempDir(), PGPort: 5432, PGListen: "*", PGMaxConnections: 50, PGMemoryMB: 256,
		UID: os.Getuid(), GID: os.Getgid(), PGUID: os.Getuid(), PGGID: os.Getgid(), Location: time.UTC}
	err := Init(cfg, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "SQS-C054") || !strings.Contains(err.Error(), "preset Generic") {
		t.Fatalf("want SQS-C054 naming the Generic preset, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(pg, "sqsolve", "postgresql.conf")); !os.IsNotExist(err) {
		t.Fatalf("init wrote PostgreSQL config into a volume it refused: %v", err)
	}
}
