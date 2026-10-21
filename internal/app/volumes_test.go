// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/pgconf"
)

// Volume identity (ADR 0023): which volumes init marks, adopts or refuses.

func initOut(t *testing.T, cfg *config.Config) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Init(cfg, &out)
	return out.String(), err
}

func mustInit(t *testing.T, cfg *config.Config) string {
	t.Helper()
	out, err := initOut(t, cfg)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return out
}

func marker(t *testing.T, dir string) *fsx.Marker {
	t.Helper()
	m, err := fsx.ReadMarker(dir)
	if err != nil {
		t.Fatalf("marker of %s: %v", dir, err)
	}
	return m
}

func instanceOf(t *testing.T, cfg *config.Config) string {
	t.Helper()
	b, err := os.ReadFile(instanceIDPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// established simulates an installation whose server already ran: the
// control plane exists and PostgreSQL initialized its cluster.
func established(t *testing.T) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	mustInit(t, cfg)
	touch(t, cfg.DBPath())
	touch(t, filepath.Join(cfg.PGDataDir, "PG_VERSION"))
	return cfg
}

// newDB points the config at another empty db volume.
func newDB(t *testing.T, cfg *config.Config) {
	cfg.PGDir = t.TempDir()
	cfg.PGDataDir = filepath.Join(cfg.PGDir, "18", "docker")
}

func isEmpty(t *testing.T, dir string) bool {
	t.Helper()
	empty, err := fsx.IsEmptyDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return empty
}

func TestInitFreshMarksEveryVolume(t *testing.T) {
	cfg := testConfig(t)
	mustInit(t, cfg)
	id := instanceOf(t, cfg)
	for dir, vol := range map[string]string{cfg.BackupDir: "backups", cfg.PGDir: "db", cfg.AppDir: "app"} {
		m := marker(t, dir)
		if m.InstanceID != id || m.Volume != vol {
			t.Errorf("%s: marker %+v, want %s of %s", dir, m, vol, id)
		}
	}
	for _, d := range []string{"libraries", "_system", "_import"} {
		if st, err := os.Stat(filepath.Join(cfg.BackupDir, d)); err != nil || !st.IsDir() {
			t.Errorf("backups/%s missing: %v", d, err)
		}
	}
	// Idempotent.
	out := mustInit(t, cfg)
	if instanceOf(t, cfg) != id || strings.Contains(out, "SQS-") {
		t.Fatalf("second init changed the identity or warned:\n%s", out)
	}
}

// Audit H3 (repro): Docker creates a missing bind-mount path empty, so an
// unmounted backups disk is an empty folder. On an installation that already
// has an identity init must not mark it, nor fail: PostgreSQL waits for init
// and a backups problem must not take every library offline.
func TestInitLeavesUnmountedBackupsUnmarked(t *testing.T) {
	cfg := established(t)
	id := instanceOf(t, cfg)
	cfg.BackupDir = t.TempDir()
	out, err := initOut(t, cfg)
	if err != nil {
		t.Fatalf("a backups problem must not stop init: %v", err)
	}
	if !strings.Contains(out, "SQS-S014") || !strings.Contains(out, "SQSOLVE_BACKUPS_ADOPT") {
		t.Fatalf("init does not report the unmarked backups volume:\n%s", out)
	}
	if !isEmpty(t, cfg.BackupDir) {
		t.Fatal("init wrote into a backups folder it cannot identify")
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("init did not finish:\n%s", out)
	}
	// Explicit adoption of a new, empty disk.
	cfg.BackupsAdopt = true
	out = mustInit(t, cfg)
	if m := marker(t, cfg.BackupDir); m.InstanceID != id || m.Volume != "backups" {
		t.Fatalf("adopted marker %+v, want %s", m, id)
	}
	if !strings.Contains(out, "SQSOLVE_BACKUPS_ADOPT is set") {
		t.Fatalf("init does not remind to remove the adopt variable:\n%s", out)
	}
}

func TestInitBackupsProblemsAreReportedNotFatal(t *testing.T) {
	cases := []struct {
		name, code string
		prepare    func(dir string)
	}{
		{"another installation", "SQS-S003", func(dir string) {
			fsx.WriteMarker(dir, fsx.Marker{InstanceID: "OTHER", Volume: "backups", CreatedAt: time.Now()})
		}},
		{"foreign files", "SQS-S004", func(dir string) { os.WriteFile(filepath.Join(dir, "holiday.jpg"), []byte("x"), 0o600) }},
		{"unreadable marker", "SQS-S002", func(dir string) { os.WriteFile(filepath.Join(dir, fsx.MarkerName), []byte("{nope"), 0o600) }},
	}
	if os.Geteuid() != 0 {
		cases = append(cases, []struct {
			name, code string
			prepare    func(dir string)
		}{
			{"marker without permission", "SQS-S002 unreadable marker in", func(dir string) {
				fsx.WriteMarker(dir, fsx.Marker{InstanceID: "X", Volume: "backups", CreatedAt: time.Now()})
				os.Chmod(filepath.Join(dir, fsx.MarkerName), 0)
			}},
			{"folder without permission", "SQS-S018 cannot read the backups volume", func(dir string) {
				os.Chmod(dir, 0o100)
				t.Cleanup(func() { os.Chmod(dir, 0o700) })
			}},
		}...)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := established(t)
			cfg.BackupDir = t.TempDir()
			tc.prepare(cfg.BackupDir)
			before, _ := os.ReadFile(filepath.Join(cfg.BackupDir, fsx.MarkerName))
			out, err := initOut(t, cfg)
			if err != nil {
				t.Fatalf("must not be fatal: %v", err)
			}
			if !strings.Contains(out, tc.code) {
				t.Fatalf("want %s in the output:\n%s", tc.code, out)
			}
			after, _ := os.ReadFile(filepath.Join(cfg.BackupDir, fsx.MarkerName))
			if !bytes.Equal(before, after) {
				t.Fatal("init rewrote the marker of a volume it cannot identify")
			}
			if _, err := os.Stat(filepath.Join(cfg.BackupDir, "libraries")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("init created folders in a volume it cannot identify")
			}
		})
	}
}

func TestInitAdoptsForeignFilesOnlyWhenAsked(t *testing.T) {
	cfg := testConfig(t)
	os.WriteFile(filepath.Join(cfg.BackupDir, "old.txt"), []byte("x"), 0o600)
	out := mustInit(t, cfg)
	if !strings.Contains(out, "SQS-S004") {
		t.Fatalf("want SQS-S004:\n%s", out)
	}
	if _, err := fsx.ReadMarker(cfg.BackupDir); !errors.Is(err, fsx.ErrNoMarker) {
		t.Fatal("a folder with foreign files was marked without SQSOLVE_BACKUPS_ADOPT")
	}
	cfg.BackupsAdopt = true
	mustInit(t, cfg)
	if m := marker(t, cfg.BackupDir); m.InstanceID != instanceOf(t, cfg) {
		t.Fatalf("marker %+v", m)
	}
}

// docs/runbooks/recover-installation.md: new server, empty app and db, the
// existing backups volume. Init adopts its identity.
func TestInitRecoveryFlow(t *testing.T) {
	cfg := testConfig(t)
	fsx.WriteMarker(cfg.BackupDir, fsx.Marker{InstanceID: "01OLDINSTALLATION", Volume: "backups", CreatedAt: time.Now()})
	touch(t, filepath.Join(cfg.BackupDir, "_system", "sqsolve-state-20261001T041500Z.db"))
	out := mustInit(t, cfg)
	if !strings.Contains(out, "adopting identity 01OLDINSTALLATION") {
		t.Fatalf("no adoption message:\n%s", out)
	}
	if instanceOf(t, cfg) != "01OLDINSTALLATION" {
		t.Fatal("identity not adopted")
	}
	for _, dir := range []string{cfg.AppDir, cfg.PGDir} {
		if m := marker(t, dir); m.InstanceID != "01OLDINSTALLATION" {
			t.Fatalf("%s marked %+v", dir, m)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupDir, "_system", "sqsolve-state-20261001T041500Z.db")); err != nil {
		t.Fatal("the control plane copy is gone")
	}
	// PostgreSQL creates its new cluster, sqsolve starts and the recovery
	// restarts it: init runs again and again.
	touch(t, filepath.Join(cfg.PGDataDir, "PG_VERSION"))
	touch(t, cfg.DBPath())
	if out := mustInit(t, cfg); strings.Contains(out, "SQS-") {
		t.Fatalf("init after the recovery warns:\n%s", out)
	}
}

// Existing installations (mininas) have no db or app marker: init marks
// them silently when they hold the expected data.
func TestInitUpgradeMarksExistingVolumes(t *testing.T) {
	cfg := testConfig(t)
	fsx.WriteMarker(cfg.BackupDir, fsx.Marker{InstanceID: "01MININAS", Volume: "backups", CreatedAt: time.Now()})
	touch(t, instanceIDPath(cfg))
	os.WriteFile(instanceIDPath(cfg), []byte("01MININAS\n"), 0o600)
	touch(t, cfg.DBPath())
	touch(t, filepath.Join(cfg.PGDataDir, "PG_VERSION"))
	out := mustInit(t, cfg)
	if strings.Contains(out, "SQS-") || strings.Contains(out, "adopting") {
		t.Fatalf("the upgrade must be silent:\n%s", out)
	}
	if m := marker(t, cfg.PGDir); m.InstanceID != "01MININAS" || m.Volume != "db" {
		t.Fatalf("db marker %+v", m)
	}
	if m := marker(t, cfg.AppDir); m.InstanceID != "01MININAS" || m.Volume != "app" {
		t.Fatalf("app marker %+v", m)
	}
}

// Audit H4: an unmounted db volume is an empty folder and PostgreSQL would
// initialize a new, empty cluster under an installation with libraries.
func TestInitRefusesEmptyDBOnEstablishedInstall(t *testing.T) {
	cfg := established(t)
	newDB(t, cfg)
	_, err := initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-S012") || !strings.Contains(err.Error(), "SQSOLVE_DB_ADOPT") {
		t.Fatalf("want SQS-S012 naming SQSOLVE_DB_ADOPT, got %v", err)
	}
	if !isEmpty(t, cfg.PGDir) {
		t.Fatal("init wrote into the db volume it refused")
	}
	cfg.DBAdopt = true
	mustInit(t, cfg)
	if m := marker(t, cfg.PGDir); m.InstanceID != instanceOf(t, cfg) || m.Volume != "db" {
		t.Fatalf("db marker %+v", m)
	}
}

// A db volume whose cluster is gone but whose marker is ours is accepted:
// the system identifier guard of serve catches the new cluster.
func TestInitAcceptsOwnMarkedDBWithoutCluster(t *testing.T) {
	cfg := established(t)
	os.Remove(filepath.Join(cfg.PGDataDir, "PG_VERSION"))
	mustInit(t, cfg)
}

// listing is every path under dir with its mode, to prove init wrote nothing.
func listing(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			info, _ := d.Info()
			b.WriteString(p + " " + info.Mode().String() + "\n")
		}
		return nil
	})
	return b.String()
}

// Review of task 4: the volume field of the markers was not checked. With the
// db and backups host paths swapped (easy in TrueNAS's three path fields),
// init created PGDATA on the backups disk and backups folders on the db disk.
func TestInitRefusesSwappedDBAndBackups(t *testing.T) {
	cfg := established(t)
	cfg.PGDir, cfg.BackupDir = cfg.BackupDir, cfg.PGDir
	cfg.PGDataDir = filepath.Join(cfg.PGDir, "18", "docker")
	dbBefore, bkBefore := listing(t, cfg.PGDir), listing(t, cfg.BackupDir)
	// Adopt variables do not make a swap acceptable.
	cfg.DBAdopt, cfg.BackupsAdopt = true, true
	_, err := initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-S019") || !strings.Contains(err.Error(), "swapped") {
		t.Fatalf("want SQS-S019 for swapped mounts, got %v", err)
	}
	if listing(t, cfg.PGDir) != dbBefore || listing(t, cfg.BackupDir) != bkBefore {
		t.Fatal("init wrote into swapped volumes")
	}
}

func TestInitRefusesSwappedApp(t *testing.T) {
	cfg := established(t)
	cfg.AppDir, cfg.BackupDir = cfg.BackupDir, cfg.AppDir
	appBefore := listing(t, cfg.AppDir)
	cfg.AppAdopt = true
	_, err := initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-S019") {
		t.Fatalf("want SQS-S019 for the backups disk on the app path, got %v", err)
	}
	if listing(t, cfg.AppDir) != appBefore {
		t.Fatal("init wrote secrets into the backups disk")
	}
}

// A recovery whose backups path holds the db disk: init cannot adopt an
// identity from a marker of the wrong volume.
func TestInitDoesNotAdoptFromAnotherVolume(t *testing.T) {
	cfg := testConfig(t)
	fsx.WriteMarker(cfg.BackupDir, fsx.Marker{InstanceID: "01OLD", Volume: fsx.VolumeDB, CreatedAt: time.Now()})
	_, err := initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-S019") {
		t.Fatalf("want SQS-S019, got %v", err)
	}
	if _, err := os.Stat(instanceIDPath(cfg)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("init took an identity from the wrong volume")
	}
}

// The backups path alone holds another of this installation's volumes: a
// warning, as for every backups problem, and nothing is written there.
func TestInitWarnsAboutAnotherVolumeOnTheBackupsPath(t *testing.T) {
	cfg := established(t)
	cfg.BackupDir = t.TempDir()
	fsx.WriteMarker(cfg.BackupDir, fsx.Marker{InstanceID: instanceOf(t, cfg), Volume: fsx.VolumeApp, CreatedAt: time.Now()})
	before := listing(t, cfg.BackupDir)
	out := mustInit(t, cfg)
	if !strings.Contains(out, "SQS-S019") {
		t.Fatalf("want an SQS-S019 warning:\n%s", out)
	}
	if listing(t, cfg.BackupDir) != before {
		t.Fatal("init wrote into the wrong volume")
	}
}

// Another installation's marker of another volume is foreign, as before.
func TestInitForeignMarkerOfAnotherVolume(t *testing.T) {
	cfg := established(t)
	fsx.WriteMarker(cfg.PGDir, fsx.Marker{InstanceID: "OTHER", Volume: fsx.VolumeBackups, CreatedAt: time.Now()})
	_, err := initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-S011") || !strings.Contains(err.Error(), "backups volume of installation OTHER") {
		t.Fatalf("want SQS-S011 naming the foreign volume, got %v", err)
	}
}

// A cluster init cannot look into (another owner, mode 0700) is not taken
// for an empty volume: an existing installation must keep starting.
func TestInitUpgradeWithUnreadableDataDir(t *testing.T) {
	cfg := established(t)
	os.Remove(filepath.Join(cfg.PGDir, fsx.MarkerName))
	os.Chmod(cfg.PGDataDir, 0)
	defer os.Chmod(cfg.PGDataDir, 0o700)
	mustInit(t, cfg)
	if m := marker(t, cfg.PGDir); m.InstanceID != instanceOf(t, cfg) {
		t.Fatalf("db marker %+v", m)
	}
}

func TestInitRefusesDBOfAnotherInstallation(t *testing.T) {
	cfg := established(t)
	fsx.WriteMarker(cfg.PGDir, fsx.Marker{InstanceID: "OTHER", Volume: "db", CreatedAt: time.Now()})
	_, err := initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-S011") || !strings.Contains(err.Error(), "OTHER") {
		t.Fatalf("want SQS-S011 naming the other installation, got %v", err)
	}
	cfg.DBAdopt = true
	mustInit(t, cfg)
	if m := marker(t, cfg.PGDir); m.InstanceID != instanceOf(t, cfg) {
		t.Fatalf("db marker not adopted: %+v", m)
	}
}

func TestInitRefusesUnreadableDBMarker(t *testing.T) {
	cfg := established(t)
	os.WriteFile(filepath.Join(cfg.PGDir, fsx.MarkerName), []byte("garbage"), 0o600)
	_, err := initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-S015") {
		t.Fatalf("want SQS-S015, got %v", err)
	}
	cfg.DBAdopt = true
	mustInit(t, cfg)
	marker(t, cfg.PGDir)
}

// An unmounted app volume with the db and backups volumes in place: init
// would generate new secrets and a new control plane.
func TestInitRefusesEmptyAppWhenOthersAreEstablished(t *testing.T) {
	cfg := established(t)
	id := instanceOf(t, cfg)
	cfg.AppDir = t.TempDir()
	_, err := initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-S013") || !strings.Contains(err.Error(), "SQSOLVE_APP_ADOPT") {
		t.Fatalf("want SQS-S013 naming SQSOLVE_APP_ADOPT, got %v", err)
	}
	if !isEmpty(t, cfg.AppDir) {
		t.Fatal("init wrote into the app volume it refused")
	}
	cfg.AppAdopt = true
	mustInit(t, cfg)
	if instanceOf(t, cfg) != id {
		t.Fatal("the adopted app volume did not take the installation identity")
	}
}

func TestInitRefusesMismatchedAppMarker(t *testing.T) {
	cfg := established(t)
	fsx.WriteMarker(cfg.AppDir, fsx.Marker{InstanceID: "OTHER", Volume: "app", CreatedAt: time.Now()})
	_, err := initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-S010") {
		t.Fatalf("want SQS-S010, got %v", err)
	}
	os.WriteFile(filepath.Join(cfg.AppDir, fsx.MarkerName), []byte("garbage"), 0o600)
	_, err = initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-S017") {
		t.Fatalf("want SQS-S017, got %v", err)
	}
}

// Without any other identity an unreadable backups marker leaves init unable
// to tell a recovery from a new installation.
func TestInitRefusesToGuessIdentity(t *testing.T) {
	cfg := testConfig(t)
	os.WriteFile(filepath.Join(cfg.BackupDir, fsx.MarkerName), []byte("garbage"), 0o600)
	_, err := initOut(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "SQS-S016") {
		t.Fatalf("want SQS-S016, got %v", err)
	}
	if _, err := os.Stat(instanceIDPath(cfg)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("init invented an identity")
	}
}

// Audit Low (task 4 items 6 and 7): the PostgreSQL certificate and key are
// written as a pair, a missing half or a mismatch regenerates both, and the
// certificate is readable by sqsolve while the key stays private.
func TestInitPostgresCertificatePair(t *testing.T) {
	cfg := testConfig(t)
	mustInit(t, cfg)
	tlsDir := filepath.Join(cfg.PGConfDir(), "tls")
	crt, key := filepath.Join(tlsDir, "server.crt"), filepath.Join(tlsDir, "server.key")
	for path, want := range map[string]os.FileMode{tlsDir: 0o755, crt: 0o644, key: 0o600} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s: %04o, want %04o", filepath.Base(path), st.Mode().Perm(), want)
		}
	}
	pairOK := func() {
		t.Helper()
		if _, err := tls.LoadX509KeyPair(crt, key); err != nil {
			t.Fatalf("certificate and key do not match: %v", err)
		}
	}
	pairOK()
	first, _ := pgconf.ReadCert(crt)

	// Missing key: both are regenerated.
	os.Remove(key)
	mustInit(t, cfg)
	pairOK()
	second, _ := pgconf.ReadCert(crt)
	if second.Fingerprint == first.Fingerprint {
		t.Fatal("the certificate was not regenerated with its new key")
	}

	// A key that belongs to another certificate.
	_, otherKey, _ := pgconf.SelfSigned("x", []string{"localhost"}, time.Hour)
	os.WriteFile(key, otherKey, 0o600)
	mustInit(t, cfg)
	pairOK()

	// A crash between the two renames: the pair is completed, not mixed.
	cp, kp, _ := pgconf.SelfSigned("sqsolve-postgresql", []string{"localhost"}, time.Hour)
	os.WriteFile(key, kp, 0o600)
	os.WriteFile(crt+".partial", cp, 0o644)
	mustInit(t, cfg)
	pairOK()
	if got, _ := pgconf.ReadCert(crt); got.NotAfter.After(time.Now().Add(2 * time.Hour)) {
		t.Fatal("init replaced a pair it could have completed")
	}
	// Idempotent: a valid pair is kept.
	before, _ := pgconf.ReadCert(crt)
	out := mustInit(t, cfg)
	if after, _ := pgconf.ReadCert(crt); after.Fingerprint != before.Fingerprint || strings.Contains(out, "certificate generated") {
		t.Fatal("a valid pair was regenerated")
	}
}
