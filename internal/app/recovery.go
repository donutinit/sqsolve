// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/secrets"
)

const keyCheckSetting = "key_check"

// EnsureKeyCheck stores a value sealed with the master key so a recovery can
// tell whether a pasted key is the right one.
func (a *App) EnsureKeyCheck(ctx context.Context) error {
	var v string
	if ok, err := a.DB.GetSetting(ctx, keyCheckSetting, &v); err != nil || ok {
		return err
	}
	sealed, err := a.Box.Seal(secrets.New("sqsolve-key-check"), "settings."+keyCheckSetting)
	if err != nil {
		return err
	}
	return a.DB.SetSetting(ctx, keyCheckSetting, sealed, "system")
}

// RecoveryKit is the text of the recovery kit.
func (a *App) RecoveryKit(ctx context.Context) (string, error) {
	key, err := ReadMasterKey(a.Cfg)
	if err != nil {
		return "", err
	}
	fp := "To be confirmed"
	if info, err := a.PanelCertInfo(); err == nil {
		fp = info.Fingerprint
	}
	var b strings.Builder
	fmt.Fprintf(&b, "sqsolve: recovery kit\n\n")
	fmt.Fprintf(&b, "Installation:  %s\n", a.InstanceID)
	fmt.Fprintf(&b, "Name:          %s\n", a.Svc.Settings(ctx).InstanceName)
	fmt.Fprintf(&b, "Generated:     %s\n", time.Now().In(a.Cfg.Location).Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(&b, "Version:       %s\n\n", Version)
	fmt.Fprintf(&b, "MASTER KEY (keep it away from this server; whoever has it together with the\n")
	fmt.Fprintf(&b, "volumes can read the library passwords):\n\n    %s\n\n", base64.StdEncoding.EncodeToString(key))
	fmt.Fprintf(&b, "SHA-256 fingerprint of the panel certificate:\n    %s\n\n", fp)
	b.WriteString(`How to recover the installation on another server:

1. Install sqsolve with the same compose.yaml and mount the copy of the
   backups volume at /backups (it must include the _system folder).
2. Open the panel. On the setup screen choose "Recover an existing
   installation", type the installation code and paste the master key from
   this kit.
3. sqsolve restores users, policies and credentials, then restarts.
4. In the panel, every library shows "No database on the server": use
   "Recreate from backup". Workstation passwords do not change.
5. If the server changed its IP, update the advertised address in Settings
   and the location on every workstation.

Without this kit, the libraries can still be restored from the backups
(Maintenance > Rebuild catalog), but with new passwords.
`)
	return b.String(), nil
}

// SystemCopies lists the control plane copies in the backups volume, newest
// first.
func SystemCopies(c *config.Config) []string {
	matches, _ := filepath.Glob(filepath.Join(c.BackupDir, "_system", "sqsolve-state-*.db"))
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	return matches
}

// StageRecovery checks the pasted key against the newest control plane copy
// and stages both so the next start uses them.
func StageRecovery(c *config.Config, keyB64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
	if err != nil || len(key) != 32 {
		return "", errors.New("SQS-C070 the master key must be the base64 text from the kit (44 characters)")
	}
	copies := SystemCopies(c)
	if len(copies) == 0 {
		return "", errors.New("SQS-C071 there are no control plane copies in the backups volume (_system)")
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		return "", err
	}
	var chosen string
	var lastErr error
	for _, cp := range copies {
		if err := checkCopy(cp, box); err != nil {
			lastErr = err
			continue
		}
		chosen = cp
		break
	}
	if chosen == "" {
		return "", fmt.Errorf("SQS-C072 the key does not match any control plane copy: %v", lastErr)
	}
	// The staged key makes a staging complete, so it is written last, and a
	// key left by an earlier attempt goes first: it must never pair with
	// this copy.
	if err := os.Remove(stagedKeyPath(c)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := fsx.SyncDir(c.SecretsDir()); err != nil {
		return "", err
	}
	if err := copyFile(chosen, stagedDBPath(c)); err != nil {
		return "", err
	}
	if err := recoveryStep("staged database"); err != nil {
		return "", err
	}
	if err := fsx.WriteFileAtomic(stagedKeyPath(c), []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o400); err != nil {
		return "", err
	}
	return filepath.Base(chosen), nil
}

// copyFile copies src to dst through a .partial file, fsync, rename and a
// sync of the folder.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".partial"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := fsx.SealFile(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// forgetCluster removes the stored PostgreSQL system identifier from the
// database at path. The copy is a single file (VACUUM INTO): the change is
// written into it, without -wal or -shm files left next to it.
func forgetCluster(path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(DELETE)")
	if err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM settings WHERE key = ?`, pgSystemIDSetting)
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	return err
}

func checkCopy(path string, box *secrets.Box) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, keyCheckSetting).Scan(&raw); err != nil {
		return err
	}
	sealed := strings.Trim(raw, `"`)
	_, err = box.Open(sealed, "settings."+keyCheckSetting)
	return err
}

// The files of a recovery. StageRecovery writes the staged database, then
// the staged key; ApplyPendingRecovery moves both into place at the next
// start, recording what it does in the journal.
func stagedDBPath(c *config.Config) string  { return c.DBPath() + ".recover" }
func stagedKeyPath(c *config.Config) string { return masterKeyPath(c) + ".recover" }
func recoveryJournalPath(c *config.Config) string {
	return filepath.Join(c.AppDir, "recovery-journal.json")
}

// recoveryFail, when set, stops a recovery at a named step (tests).
var recoveryFail func(step string) error

func recoveryStep(step string) error {
	if recoveryFail != nil {
		return recoveryFail(step)
	}
	return nil
}

// recoveryJournal is written before the first file of the installation is
// moved and removed after the last one. Where the old pair goes is decided
// once, so a start that finds the journal finishes the same recovery.
type recoveryJournal struct {
	Started  time.Time `json:"started"`
	DBAside  string    `json:"db_aside"`
	KeyAside string    `json:"key_aside"`
}

func readRecoveryJournal(c *config.Config) (*recoveryJournal, error) {
	b, err := os.ReadFile(recoveryJournalPath(c))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var j recoveryJournal
	if err == nil {
		err = json.Unmarshal(b, &j)
	}
	if err == nil && (j.DBAside == "" || j.KeyAside == "") {
		err = errors.New("it does not say where the old files go")
	}
	if err != nil {
		return nil, fmt.Errorf("SQS-C088 the recovery journal %s cannot be read: %w. "+
			"Check the files named in docs/runbooks/recover-installation.md before removing it", recoveryJournalPath(c), err)
	}
	return &j, nil
}

// lstatExists reports whether path exists; an error other than "does not
// exist" is returned.
func lstatExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ApplyPendingRecovery moves staged recovery files into place. It runs
// before Open, so the database is not in use yet.
//
// The staged key must open the staged copy before anything is touched.
// Then a journal records where the old database (with its -wal and -shm
// files) and the old master key go, and each move is synced before the next:
// a recovery stopped at any point, by an error or a power cut, is finished by
// the next start, and the old pair is kept aside together (audit AUD-001).
func ApplyPendingRecovery(c *config.Config, out io.Writer) error {
	j, err := readRecoveryJournal(c)
	if err != nil {
		return err
	}
	if j == nil {
		db, err := lstatExists(stagedDBPath(c))
		if err != nil {
			return err
		}
		key, err := lstatExists(stagedKeyPath(c))
		if err != nil {
			return err
		}
		if !db || !key {
			if db || key {
				fmt.Fprintln(out, "sqsolve: an incomplete recovery was ignored; recover again from the setup page")
			}
			return nil
		}
		now := time.Now().UTC()
		stamp := now.Format("20060102T150405Z")
		j = &recoveryJournal{Started: now, DBAside: filepath.Join(stateBackupDir(c), "sqsolve-replaced-"+stamp+".db"),
			KeyAside: masterKeyPath(c) + ".replaced-" + stamp}
		err = prepareRecovery(c)
		if err == nil {
			if ferr := recoveryStep("prepared"); ferr != nil {
				return ferr
			}
			err = os.MkdirAll(stateBackupDir(c), 0o700)
		}
		if err == nil {
			b, _ := json.Marshal(j)
			err = fsx.WriteFileAtomic(recoveryJournalPath(c), b, 0o600)
		}
		if err != nil {
			// Nothing of the installation was touched. Refusing every start
			// would also close the setup page where the recovery is staged
			// again, so the staged files go aside and sqsolve starts as it was.
			return refuseRecovery(c, fmt.Errorf("SQS-C084 could not prepare the recovered control plane: %w", err), out)
		}
	}
	err = resumeRecovery(c, j)
	var gone errStagedKeyGone
	if errors.As(err, &gone) {
		return abandonRecovery(c, j, err, out)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "sqsolve: control plane recovery applied")
	return nil
}

// refuseRecovery sets a staging that cannot be applied aside, the key first
// so that what is left is never a complete staging, and reports why.
func refuseRecovery(c *config.Config, cause error, out io.Writer) error {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	var kept []string
	for _, f := range []struct{ path, dir string }{{stagedKeyPath(c), c.SecretsDir()}, {stagedDBPath(c), c.AppDir}} {
		dest := f.path + ".refused-" + stamp
		if err := os.Rename(f.path, dest); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w (and the staged files could not be set aside: %v)", cause, err)
		}
		if err := fsx.SyncDir(f.dir); err != nil {
			return fmt.Errorf("%w (and the staged files could not be set aside: %v)", cause, err)
		}
		kept = append(kept, dest)
	}
	fmt.Fprintf(out, "sqsolve: %v. Nothing was changed; the staged files were kept as %s and %s. "+
		"sqsolve starts as it was: recover again from the setup page\n", cause, kept[1], kept[0])
	return nil
}

// errStagedKeyGone is returned by resumeRecovery when the staged key is
// missing while its database still waits: the key was never touched.
type errStagedKeyGone struct{ path string }

func (e errStagedKeyGone) Error() string {
	return fmt.Sprintf("SQS-C089 the staged master key %s is missing while its database waits", e.path)
}

// abandonRecovery puts back the old database files the journal set aside
// (the key is set aside only after the new database is in place, so it was
// not), sets the staged database aside and removes the journal.
func abandonRecovery(c *config.Config, j *recoveryJournal, cause error, out io.Writer) error {
	stop := func(err error) error {
		return fmt.Errorf("SQS-C086 the recovery stopped and continues at the next start: %w", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := aside(j.DBAside+suffix, c.DBPath()+suffix, stop); err != nil {
			return err
		}
	}
	for _, dir := range []string{stateBackupDir(c), c.AppDir} {
		if err := fsx.SyncDir(dir); err != nil {
			return stop(err)
		}
	}
	kept := stagedDBPath(c) + ".refused-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(stagedDBPath(c), kept); err != nil {
		return stop(err)
	}
	if err := os.Remove(recoveryJournalPath(c)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return stop(err)
	}
	if err := fsx.SyncDir(c.AppDir); err != nil {
		return stop(err)
	}
	fmt.Fprintf(out, "sqsolve: %v. The previous database was put back and the staged one kept as %s. "+
		"sqsolve starts as it was: recover again from the setup page\n", cause, kept)
	return nil
}

// prepareRecovery checks that the staged key opens the staged copy and makes
// the copy forget the old cluster. Nothing of the installation is touched.
func prepareRecovery(c *config.Config) error {
	s, err := secrets.ReadFile(stagedKeyPath(c))
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s.Reveal()))
	if err != nil || len(key) != 32 {
		return errors.New("the staged master key is not the key of a recovery kit")
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		return err
	}
	if err := checkCopy(stagedDBPath(c), box); err != nil {
		return fmt.Errorf("the staged master key does not open the staged copy: %w", err)
	}
	// The copy remembers the cluster of the old server; this one has a new
	// cluster by design, so its identifier is learnt again (ADR 0023). Done
	// on the staged file, before anything is moved.
	return forgetCluster(stagedDBPath(c))
}

// resumeRecovery runs the moves the journal j describes from wherever the
// files are. While the staged database exists, the live database files are
// the old ones; once it is in place, the same holds for the key.
func resumeRecovery(c *config.Config, j *recoveryJournal) error {
	stop := func(err error) error {
		return fmt.Errorf("SQS-C086 the recovery stopped and continues at the next start: %w", err)
	}
	step := func(name string, sync ...string) error {
		for _, dir := range sync {
			if err := fsx.SyncDir(dir); err != nil {
				return stop(err)
			}
		}
		if err := recoveryStep(name); err != nil {
			return stop(err)
		}
		return nil
	}
	if err := step("journal"); err != nil {
		return err
	}
	db, key := c.DBPath(), masterKeyPath(c)
	if ok, err := lstatExists(stagedDBPath(c)); err != nil {
		return stop(err)
	} else if ok {
		if ok, err := lstatExists(stagedKeyPath(c)); err != nil {
			return stop(err)
		} else if !ok {
			return errStagedKeyGone{stagedKeyPath(c)}
		}
		if err := aside(db, j.DBAside, stop); err != nil {
			return err
		}
		if err := step("old database aside"); err != nil {
			return err
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if err := aside(db+suffix, j.DBAside+suffix, stop); err != nil {
				return err
			}
		}
		if err := step("old database files aside", stateBackupDir(c), c.AppDir); err != nil {
			return err
		}
		if err := os.Rename(stagedDBPath(c), db); err != nil {
			return stop(err)
		}
		if err := step("database installed", c.AppDir); err != nil {
			return err
		}
	}
	if ok, err := lstatExists(stagedKeyPath(c)); err != nil {
		return stop(err)
	} else if ok {
		if err := aside(key, j.KeyAside, stop); err != nil {
			return err
		}
		if err := step("old key aside", c.SecretsDir()); err != nil {
			return err
		}
		if err := os.Rename(stagedKeyPath(c), key); err != nil {
			return stop(err)
		}
		if err := step("key installed", c.SecretsDir()); err != nil {
			return err
		}
	}
	if err := step("done"); err != nil {
		return err
	}
	if err := os.Remove(recoveryJournalPath(c)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return stop(err)
	}
	if err := fsx.SyncDir(c.AppDir); err != nil {
		return stop(err)
	}
	return nil
}

// aside moves live to dest, unless an earlier attempt already did. It never
// replaces dest.
func aside(live, dest string, stop func(error) error) error {
	if ok, err := lstatExists(live); err != nil || !ok {
		if err != nil {
			return stop(err)
		}
		return nil
	}
	if ok, err := lstatExists(dest); err != nil {
		return stop(err)
	} else if ok {
		return fmt.Errorf("SQS-C087 the recovery cannot continue: %s and %s both exist; keep the one you need outside the app volume, remove the other and start again", live, dest)
	}
	if err := os.Rename(live, dest); err != nil {
		return stop(err)
	}
	return nil
}
