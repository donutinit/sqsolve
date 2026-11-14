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

// A volume whose ACL keeps a wider mode accepts chmod and leaves it as it
// was (TrueNAS datasets with inherited NFSv4 entries showed master.key at
// 0700 after init printed "done", and sqsolve then refused to start with
// SQS-C010). init checks the mode it gets and stops with SQS-C085.
func TestInitFailsWhenAnACLKeepsAWiderMode(t *testing.T) {
	cfg := testConfig(t)
	if err := Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	path := masterKeyPath(cfg)
	chmod = func(p string, m os.FileMode) error {
		if p == path {
			return os.Chmod(p, 0o700)
		}
		return os.Chmod(p, m)
	}
	defer func() { chmod = os.Chmod }()
	err := Init(cfg, io.Discard)
	want := fmt.Sprintf("SQS-C085 could not set the mode of %s to 0400: it stays 0700; an ACL on the volume probably grants more (docs/12-deployment.md, \"Permissions and ACLs\")", path)
	if err == nil || err.Error() != want {
		t.Fatalf("want %q, got %v", want, err)
	}
}

// Up to 1.2.3, init checked the mode of a file that was already there,
// not of one it had just written. A volume that does not keep the mode
// passed the run that wrote the secrets and failed only the next one,
// after sqsolve had refused to start with SQS-C010.
func TestInitFailsWhenANewFileDoesNotKeepItsMode(t *testing.T) {
	for _, c := range []struct {
		path   func(*config.Config) string
		mode   os.FileMode
		refuse bool // chmod fails, instead of leaving a wider mode
	}{
		{instanceIDPath, 0o600, false},
		{masterKeyPath, 0o400, false},
		{masterKeyPath, 0o400, true},
		{adminPassPath, 0o400, false},
		{func(c *config.Config) string { return c.SuperuserPasswordFile() }, 0o440, false},
		{func(c *config.Config) string { return filepath.Join(c.PGConfDir(), "pg_hba.conf") }, 0o640, false},
		{func(c *config.Config) string { return filepath.Join(c.PGConfDir(), "pg_ident.conf") }, 0o640, false},
	} {
		// No earlier Init: the file is written by the run under test.
		cfg := testConfig(t)
		path := c.path(cfg)
		cause := "it stays 0700; an ACL on the volume probably grants more (docs/12-deployment.md, \"Permissions and ACLs\")"
		if c.refuse {
			cause = "chmod " + path + ": operation not permitted"
		}
		chmod = func(p string, m os.FileMode) error {
			switch {
			case p != path:
				return os.Chmod(p, m)
			case c.refuse:
				return &fs.PathError{Op: "chmod", Path: p, Err: syscall.EPERM}
			}
			return os.Chmod(p, 0o700)
		}
		out, err := initOut(t, cfg)
		chmod = os.Chmod
		want := fmt.Sprintf("SQS-C085 could not set the mode of %s to %04o: %s", path, c.mode, cause)
		if err == nil || err.Error() != want {
			t.Errorf("want %q, got %v", want, err)
			continue
		}
		if strings.Contains(out, "sqsolve init: done") {
			t.Errorf("%s: init reported success:\n%s", path, out)
		}
		// The run stopped at instance_id, before any secret was generated.
		if path == instanceIDPath(cfg) && exists(masterKeyPath(cfg)) {
			t.Errorf("init generated the master key after %s did not keep its mode", path)
		}
		// The file stays. The next run, on a volume that keeps the mode,
		// checks that same file and does not write another.
		before, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("the file init wrote is gone: %v", err)
			continue
		}
		if err := Init(cfg, io.Discard); err != nil {
			t.Errorf("%s: the next run: %v", path, err)
			continue
		}
		st, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s after the next run: %v", path, err)
			continue
		}
		if st.Mode().Perm() != c.mode {
			t.Errorf("%s: mode %04o after the next run, want %04o", path, st.Mode().Perm(), c.mode)
		}
		if after, _ := os.ReadFile(path); string(after) != string(before) {
			t.Errorf("%s: the next run wrote the file again", path)
		}
	}
}

// A folder sqsolve needs and cannot create stops init with its code and the
// path: one of the application volume (a file sits where the folder goes) and
// the backups volume of a new installation (its parent cannot be written).
func TestInitNamesTheFolderItCannotCreate(t *testing.T) {
	cfg := established(t)
	tls := filepath.Join(cfg.AppDir, "tls")
	if err := os.RemoveAll(tls); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tls, []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := initOut(t, cfg); err == nil || !strings.HasPrefix(err.Error(), "SQS-C050 could not create "+tls+": ") {
		t.Fatalf("a file where the tls folder goes: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes into a folder without write permission")
	}
	fresh := testConfig(t)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })
	fresh.BackupDir = filepath.Join(parent, "backups")
	if _, err := initOut(t, fresh); err == nil || !strings.HasPrefix(err.Error(), "SQS-C051 backups volume: ") || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("a backups folder that cannot be created: %v", err)
	}
}

// A file init cannot write stops it with its code and the path, keeping the
// cause: a secret written for the first time and postgresql.conf, which
// every run writes again. A folder where the .partial file goes makes the
// write fail, also as root.
func TestInitNamesTheFileItCannotWrite(t *testing.T) {
	for _, path := range []func(*config.Config) string{
		masterKeyPath,
		func(c *config.Config) string { return filepath.Join(c.PGConfDir(), "postgresql.conf") },
	} {
		cfg := established(t)
		p := path(cfg)
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(p+".partial", 0o700); err != nil {
			t.Fatal(err)
		}
		out, err := initOut(t, cfg)
		want := fmt.Sprintf("SQS-C055 could not write %s: open %s.partial: is a directory", p, p)
		if err == nil || err.Error() != want || !errors.Is(err, syscall.EISDIR) {
			t.Errorf("want %q, got %v", want, err)
		}
		if strings.Contains(out, "sqsolve init: done") {
			t.Errorf("%s: init reported success:\n%s", p, out)
		}
	}
}
