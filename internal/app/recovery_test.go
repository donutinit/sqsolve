// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
)

// controlPlane writes a control plane database at path, as the backups
// volume keeps it (VACUUM INTO), whose key check opens with key and whose
// "probe" setting tells which copy it is.
func controlPlane(t *testing.T, path string, key []byte, settings map[string]string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "cp.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	box, _ := secrets.NewBox(key)
	sealed, err := box.Seal(secrets.New("sqsolve-key-check"), "settings."+keyCheckSetting)
	if err != nil {
		t.Fatal(err)
	}
	db.SetSetting(ctx, keyCheckSetting, sealed, "system")
	for k, v := range settings {
		db.SetSetting(ctx, k, v, "system")
	}
	if err := db.BackupTo(ctx, path); err != nil {
		t.Fatal(err)
	}
}

// livePair runs init and gives the installation a control plane that opens
// with its master key. It returns that key.
func livePair(t *testing.T, cfg *config.Config) []byte {
	t.Helper()
	if err := Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	key, err := ReadMasterKey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	controlPlane(t, cfg.DBPath(), key, map[string]string{"probe": "old"})
	return key
}

// stagePair stages a recovered control plane with a new key, as
// StageRecovery leaves it, and returns the key.
func stagePair(t *testing.T, cfg *config.Config, settings map[string]string) []byte {
	t.Helper()
	key := secrets.RandomBytes(32)
	all := map[string]string{"probe": "new"}
	for k, v := range settings {
		all[k] = v
	}
	controlPlane(t, stagedDBPath(cfg), key, all)
	if err := os.WriteFile(stagedKeyPath(cfg), []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	return key
}

// probeOf reads the probe of the database file at path alone, from a copy, so
// no -wal or -shm file next to it is used.
func probeOf(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "copy.db")
	os.WriteFile(cp, b, 0o600)
	db, err := sql.Open("sqlite", "file:"+cp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'probe'`).Scan(&v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return strings.Trim(v, `"`)
}

// opensWith reports whether the database file at path passes the key check
// with key.
func opensWith(t *testing.T, path string, key []byte) bool {
	t.Helper()
	b, _ := os.ReadFile(path)
	cp := filepath.Join(t.TempDir(), "check.db")
	os.WriteFile(cp, b, 0o600)
	box, _ := secrets.NewBox(key)
	return checkCopy(cp, box) == nil
}

func keyFile(key []byte) string { return base64.StdEncoding.EncodeToString(key) + "\n" }

func onlyMatch(t *testing.T, pattern string) string {
	t.Helper()
	m, _ := filepath.Glob(pattern)
	if len(m) != 1 {
		t.Fatalf("%s: %v", pattern, m)
	}
	return m[0]
}

// assertRecovered checks that the recovered pair is in place and the old
// pair is kept aside, together.
func assertRecovered(t *testing.T, cfg *config.Config, oldKey, newKey []byte) {
	t.Helper()
	if b, _ := os.ReadFile(masterKeyPath(cfg)); string(b) != keyFile(newKey) {
		t.Fatal("the recovered master key is not in place")
	}
	if p := probeOf(t, cfg.DBPath()); p != "new" || !opensWith(t, cfg.DBPath(), newKey) {
		t.Fatalf("the live control plane is %q", p)
	}
	for _, p := range []string{stagedDBPath(cfg), stagedKeyPath(cfg), recoveryJournalPath(cfg), cfg.DBPath() + "-wal", cfg.DBPath() + "-shm"} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there", filepath.Base(p))
		}
	}
	oldDB := onlyMatch(t, filepath.Join(stateBackupDir(cfg), "sqsolve-replaced-*.db"))
	if p := probeOf(t, oldDB); p != "old" || !opensWith(t, oldDB, oldKey) {
		t.Fatalf("the old control plane set aside is %q", p)
	}
	if b, _ := os.ReadFile(onlyMatch(t, masterKeyPath(cfg)+".replaced-*")); string(b) != keyFile(oldKey) {
		t.Fatal("the old master key set aside is not the old one")
	}
}

// recoverySteps are the points where ApplyPendingRecovery can stop, in order.
var recoverySteps = []string{"prepared", "journal", "old database aside", "old database files aside", "database installed", "old key aside", "key installed", "done"}

// Audit AUD-001: the recovery replaced the database and the master key in
// separate steps, ignored failed renames and syncs, and a start after a stop
// between the two skipped the rest. Stopped at any step, the next start
// (after sqsolve-init, which runs first) finishes it, and the old pair is
// kept aside together.
func TestRecoveryResumesAfterAStopAtEveryStep(t *testing.T) {
	for _, step := range recoverySteps {
		t.Run(step, func(t *testing.T) {
			cfg := testConfig(t)
			oldKey := livePair(t, cfg)
			newKey := stagePair(t, cfg, nil)
			// Files of the old database that a crash of the old server left.
			os.WriteFile(cfg.DBPath()+"-wal", []byte("wal"), 0o600)
			os.WriteFile(cfg.DBPath()+"-shm", []byte("shm"), 0o600)
			recoveryFail = func(s string) error {
				if s == step {
					return errors.New("power cut")
				}
				return nil
			}
			err := ApplyPendingRecovery(cfg, io.Discard)
			recoveryFail = nil
			if err == nil || !strings.Contains(err.Error(), "power cut") {
				t.Fatalf("want the simulated stop, got %v", err)
			}
			if err := Init(cfg, io.Discard); err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			if err := ApplyPendingRecovery(cfg, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "sqsolve: control plane recovery applied") {
				t.Fatalf("output: %q", out.String())
			}
			assertRecovered(t, cfg, oldKey, newKey)
			oldDB := onlyMatch(t, filepath.Join(stateBackupDir(cfg), "sqsolve-replaced-*.db"))
			for suffix, want := range map[string]string{"-wal": "wal", "-shm": "shm"} {
				if b, _ := os.ReadFile(oldDB + suffix); string(b) != want {
					t.Errorf("the old database's %s was not kept with it", suffix)
				}
			}
		})
	}
}

func TestRecoveryRefusesAKeyThatDoesNotOpenTheCopy(t *testing.T) {
	cfg := testConfig(t)
	oldKey := livePair(t, cfg)
	stagePair(t, cfg, nil)
	os.Chmod(stagedKeyPath(cfg), 0o600)
	os.WriteFile(stagedKeyPath(cfg), []byte(keyFile(secrets.RandomBytes(32))), 0o600)
	os.Chmod(stagedKeyPath(cfg), 0o400)
	// Review of the fixes: the refusal stopped every start, so the setup
	// page where the runbook says to recover again was never reachable.
	// The staged files are set aside and sqsolve starts as it was.
	var out strings.Builder
	if err := ApplyPendingRecovery(cfg, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SQS-C084 could not prepare the recovered control plane: the staged master key does not open the staged copy: ") ||
		!strings.Contains(out.String(), "recover again from the setup page") {
		t.Fatalf("output: %q", out.String())
	}
	assertRefused(t, cfg, oldKey)
}

// assertRefused checks that a refused staging left the installation as it
// was, with the staged files set aside, and that the next start is clean.
func assertRefused(t *testing.T, cfg *config.Config, oldKey []byte) {
	t.Helper()
	if b, _ := os.ReadFile(masterKeyPath(cfg)); string(b) != keyFile(oldKey) || probeOf(t, cfg.DBPath()) != "old" {
		t.Fatal("a refused recovery changed the installation")
	}
	for _, p := range []string{stagedDBPath(cfg), stagedKeyPath(cfg), recoveryJournalPath(cfg)} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there", filepath.Base(p))
		}
	}
	onlyMatch(t, stagedDBPath(cfg)+".refused-*")
	var out strings.Builder
	if err := ApplyPendingRecovery(cfg, &out); err != nil || out.Len() != 0 {
		t.Fatalf("the next start: %v %q", err, out.String())
	}
}

// A step that fails (here the old database cannot be set aside) stops the
// recovery before anything is replaced, and the next start continues.
func TestRecoveryStopsWhenTheOldDatabaseCannotBeSetAside(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the folder mode")
	}
	cfg := testConfig(t)
	oldKey := livePair(t, cfg)
	newKey := stagePair(t, cfg, nil)
	os.Chmod(stateBackupDir(cfg), 0o500)
	err := ApplyPendingRecovery(cfg, io.Discard)
	os.Chmod(stateBackupDir(cfg), 0o700)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-C086 the recovery stopped and continues at the next start: ") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("want SQS-C086, got %v", err)
	}
	if b, _ := os.ReadFile(masterKeyPath(cfg)); string(b) != keyFile(oldKey) || probeOf(t, cfg.DBPath()) != "old" {
		t.Fatal("the installation was changed although its database could not be set aside")
	}
	if err := ApplyPendingRecovery(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertRecovered(t, cfg, oldKey, newKey)
}

// A file that would be overwritten stops the recovery for the operator.
func TestRecoveryNeverOverwritesWhatItSetsAside(t *testing.T) {
	cfg := testConfig(t)
	livePair(t, cfg)
	stagePair(t, cfg, nil)
	recoveryFail = func(s string) error {
		if s == "journal" {
			return errors.New("power cut")
		}
		return nil
	}
	ApplyPendingRecovery(cfg, io.Discard)
	recoveryFail = nil
	j, err := readRecoveryJournal(cfg)
	if err != nil || j == nil {
		t.Fatalf("journal: %+v %v", j, err)
	}
	os.WriteFile(j.DBAside, []byte("something else"), 0o600)
	err = ApplyPendingRecovery(cfg, io.Discard)
	want := "SQS-C087 the recovery cannot continue: " + cfg.DBPath() + " and " + j.DBAside + " both exist; keep the one you need outside the app volume, remove the other and start again"
	if err == nil || err.Error() != want {
		t.Fatalf("want %q, got %v", want, err)
	}
	if b, _ := os.ReadFile(j.DBAside); string(b) != "something else" {
		t.Fatal("the file set aside was overwritten")
	}
}

func TestRecoveryJournalThatCannotBeRead(t *testing.T) {
	cfg := testConfig(t)
	livePair(t, cfg)
	os.WriteFile(recoveryJournalPath(cfg), []byte("{"), 0o600)
	err := ApplyPendingRecovery(cfg, io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-C088 the recovery journal "+recoveryJournalPath(cfg)+" cannot be read: ") {
		t.Fatalf("want SQS-C088, got %v", err)
	}
}

// Only a complete staging is applied: the key is staged last.
func TestRecoveryIgnoresAnIncompleteStaging(t *testing.T) {
	cfg := testConfig(t)
	oldKey := livePair(t, cfg)
	stagePair(t, cfg, nil)
	os.Remove(stagedKeyPath(cfg))
	var out strings.Builder
	if err := ApplyPendingRecovery(cfg, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "an incomplete recovery was ignored") {
		t.Fatalf("output: %q", out.String())
	}
	if b, _ := os.ReadFile(masterKeyPath(cfg)); string(b) != keyFile(oldKey) || probeOf(t, cfg.DBPath()) != "old" {
		t.Fatal("an incomplete staging changed the installation")
	}
}

// A staged key left by an earlier attempt never pairs with a copy staged
// later: staging removes it first and writes the new key last.
func TestStageRecoveryReplacesAStaleStagedKey(t *testing.T) {
	cfg := testConfig(t)
	oldKey := livePair(t, cfg)
	newKey := secrets.RandomBytes(32)
	os.MkdirAll(filepath.Join(cfg.BackupDir, "_system"), 0o700)
	controlPlane(t, filepath.Join(cfg.BackupDir, "_system", "sqsolve-state-20261020T041500Z.db"), newKey, map[string]string{"probe": "new"})
	os.WriteFile(stagedKeyPath(cfg), []byte(keyFile(secrets.RandomBytes(32))), 0o400)
	recoveryFail = func(s string) error {
		if s == "staged database" {
			return errors.New("power cut")
		}
		return nil
	}
	_, err := StageRecovery(cfg, base64.StdEncoding.EncodeToString(newKey))
	recoveryFail = nil
	if err == nil {
		t.Fatal("want the simulated stop")
	}
	if err := ApplyPendingRecovery(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(masterKeyPath(cfg)); string(b) != keyFile(oldKey) {
		t.Fatal("the stale staged key was applied")
	}
	name, err := StageRecovery(cfg, base64.StdEncoding.EncodeToString(newKey))
	if err != nil || name != "sqsolve-state-20261020T041500Z.db" {
		t.Fatalf("%q %v", name, err)
	}
	if err := ApplyPendingRecovery(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertRecovered(t, cfg, oldKey, newKey)
}

// A staged database whose key went missing after the old database was set
// aside is not applied with the old key: the old database goes back and
// sqsolve starts as it was.
func TestRecoveryPutsTheDatabaseBackWhenTheStagedKeyGoesMissing(t *testing.T) {
	cfg := testConfig(t)
	oldKey := livePair(t, cfg)
	stagePair(t, cfg, nil)
	os.WriteFile(cfg.DBPath()+"-wal", []byte("wal"), 0o600)
	recoveryFail = func(s string) error {
		if s == "old database files aside" {
			return errors.New("power cut")
		}
		return nil
	}
	ApplyPendingRecovery(cfg, io.Discard)
	recoveryFail = nil
	if _, err := os.Stat(cfg.DBPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the stop did not happen with the old database aside")
	}
	os.Remove(stagedKeyPath(cfg))
	var out strings.Builder
	if err := ApplyPendingRecovery(cfg, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SQS-C089 the staged master key "+stagedKeyPath(cfg)+" is missing while its database waits") ||
		!strings.Contains(out.String(), "recover again from the setup page") {
		t.Fatalf("output: %q", out.String())
	}
	if b, _ := os.ReadFile(cfg.DBPath() + "-wal"); string(b) != "wal" {
		t.Fatal("the old database's -wal did not come back with it")
	}
	os.Remove(cfg.DBPath() + "-wal")
	assertRefused(t, cfg, oldKey)
}

// Review of the fixes: a console command opened during an unfinished
// recovery and created an empty database where the recovery puts its own.
func TestConsoleRefusesToOpenDuringARecovery(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	oldKey := livePair(t, cfg)
	newKey := stagePair(t, cfg, nil)
	recoveryFail = func(s string) error {
		if s == "old database files aside" {
			return errors.New("power cut")
		}
		return nil
	}
	ApplyPendingRecovery(cfg, io.Discard)
	recoveryFail = nil
	a, err := Open(ctx, cfg, quietLog())
	if err == nil {
		a.Close()
	}
	want := "SQS-C090 a recovery of the installation is being applied (" + recoveryJournalPath(cfg) + "): start the sqsolve service so it finishes, then run this again"
	if err == nil || err.Error() != want {
		t.Fatalf("want %q, got %v", want, err)
	}
	if _, err := os.Stat(cfg.DBPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the console created a database during the recovery")
	}
	if err := ApplyPendingRecovery(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertRecovered(t, cfg, oldKey, newKey)
}
