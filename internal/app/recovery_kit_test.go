// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

// kitKey reads the master key printed in a recovery kit.
func kitKey(t *testing.T, kit string) string {
	t.Helper()
	m := regexp.MustCompile(`library passwords\):\n\n    ([A-Za-z0-9+/]{43}=)\n`).FindStringSubmatch(kit)
	if m == nil {
		t.Fatalf("no master key in the kit:\n%s", kit)
	}
	return m[1]
}

// docs/runbooks/recover-installation.md: the key printed in the recovery kit
// is the one that stages the control plane copy of the backups volume. The
// kit names the installation and the panel certificate; the key check is
// written once.
func TestRecoveryKitKeyStagesTheCopy(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()
	if err := a.EnsureKeyCheck(ctx); err != nil {
		t.Fatal(err)
	}
	var sealed, again string
	a.DB.GetSetting(ctx, keyCheckSetting, &sealed)
	if err := a.EnsureKeyCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if a.DB.GetSetting(ctx, keyCheckSetting, &again); sealed == "" || again != sealed {
		t.Fatal("the key check was written again")
	}
	if err := a.DB.BackupTo(ctx, filepath.Join(cfg.BackupDir, "_system", "sqsolve-state-20261020T041500Z.db")); err != nil {
		t.Fatal(err)
	}
	kit, err := a.RecoveryKit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kit, "Installation:  "+a.InstanceID+"\n") || !strings.Contains(kit, "SHA-256 fingerprint of the panel certificate:\n    To be confirmed\n") {
		t.Fatalf("kit:\n%s", kit)
	}
	key, err := ReadMasterKey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := kitKey(t, kit); got != base64.StdEncoding.EncodeToString(key) {
		t.Fatal("the kit does not print the master key")
	}
	if err := a.LoadPanelCert(); err != nil {
		t.Fatal(err)
	}
	info, _ := a.PanelCertInfo()
	if kit, _ = a.RecoveryKit(ctx); !strings.Contains(kit, "    "+info.Fingerprint+"\n") {
		t.Fatalf("the kit does not name the panel certificate %s:\n%s", info.Fingerprint, kit)
	}
	name, err := StageRecovery(cfg, " "+kitKey(t, kit)+"\n")
	if err != nil || name != "sqsolve-state-20261020T041500Z.db" {
		t.Fatalf("staging with the key of the kit: %q %v", name, err)
	}
	if b, _ := os.ReadFile(stagedKeyPath(cfg)); string(b) != base64.StdEncoding.EncodeToString(key)+"\n" {
		t.Fatal("the staged key is not the key of the kit")
	}
	if !opensWith(t, stagedDBPath(cfg), key) {
		t.Fatal("the staged copy does not open with the key")
	}
}

// StageRecovery refuses a key that is not one, a backups volume without
// copies and a key that opens none of them, and stages nothing then; with
// several copies it takes the newest one the key opens.
func TestStageRecoveryRefusals(t *testing.T) {
	cfg := testConfig(t)
	livePair(t, cfg)
	key := secrets.RandomBytes(32)
	for _, bad := range []string{"", "not base64 at all", base64.StdEncoding.EncodeToString(secrets.RandomBytes(16))} {
		if _, err := StageRecovery(cfg, bad); err == nil || err.Error() != "SQS-C070 the master key must be the base64 text from the kit (44 characters)" {
			t.Errorf("key %q: %v", bad, err)
		}
	}
	if _, err := StageRecovery(cfg, base64.StdEncoding.EncodeToString(key)); err == nil || err.Error() != "SQS-C071 there are no control plane copies in the backups volume (_system)" {
		t.Fatalf("no copies: %v", err)
	}
	sys := filepath.Join(cfg.BackupDir, "_system")
	os.MkdirAll(sys, 0o700)
	controlPlane(t, filepath.Join(sys, "sqsolve-state-20261019T041500Z.db"), key, map[string]string{"probe": "older"})
	controlPlane(t, filepath.Join(sys, "sqsolve-state-20261020T041500Z.db"), secrets.RandomBytes(32), map[string]string{"probe": "newer"})
	_, err := StageRecovery(cfg, base64.StdEncoding.EncodeToString(secrets.RandomBytes(32)))
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-C072 the key does not match any control plane copy: ") {
		t.Fatalf("a key of no copy: %v", err)
	}
	for _, p := range []string{stagedDBPath(cfg), stagedKeyPath(cfg)} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a refused key staged %s", filepath.Base(p))
		}
	}
	name, err := StageRecovery(cfg, base64.StdEncoding.EncodeToString(key))
	if err != nil || name != "sqsolve-state-20261019T041500Z.db" || probeOf(t, stagedDBPath(cfg)) != "older" {
		t.Fatalf("with a newer copy of another key: %q %v", name, err)
	}
}

// A staging that cannot be written leaves no staged key, so the next start
// applies nothing.
func TestStageRecoveryWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the folder mode")
	}
	cfg := testConfig(t)
	livePair(t, cfg)
	key := secrets.RandomBytes(32)
	sys := filepath.Join(cfg.BackupDir, "_system")
	os.MkdirAll(sys, 0o700)
	controlPlane(t, filepath.Join(sys, "sqsolve-state-20261020T041500Z.db"), key, nil)
	os.Chmod(cfg.AppDir, 0o500)
	_, err := StageRecovery(cfg, base64.StdEncoding.EncodeToString(key))
	os.Chmod(cfg.AppDir, 0o700)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("staging into a read-only app volume: %v", err)
	}
	if _, err := os.Lstat(stagedKeyPath(cfg)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a key was staged without its database")
	}
}

// The kit needs the master key; a key check that cannot be read is not
// replaced by a new one.
func TestRecoveryKitAndKeyCheckFailures(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	a := openApp(t, ctx, cfg)
	defer a.Close()
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES (?, '{broken', ?)`, keyCheckSetting, store.NowT()); err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureKeyCheck(ctx); err == nil {
		t.Fatal("an unreadable key check was accepted")
	}
	var raw string
	a.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, keyCheckSetting).Scan(&raw)
	if raw != "{broken" {
		t.Fatalf("the key check was overwritten: %q", raw)
	}
	os.Chmod(masterKeyPath(cfg), 0o600)
	os.Remove(masterKeyPath(cfg))
	if _, err := a.RecoveryKit(ctx); err == nil || !strings.HasPrefix(err.Error(), "SQS-C100 could not read the master key (did you run sqsolve init?): ") {
		t.Fatalf("kit without the master key: %v", err)
	}
}
