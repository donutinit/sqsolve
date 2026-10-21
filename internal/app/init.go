// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/pgconf"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// Paths inside the app volume.
func masterKeyPath(c *config.Config) string { return filepath.Join(c.SecretsDir(), "master.key") }
func adminPassPath(c *config.Config) string {
	return filepath.Join(c.SecretsDir(), "pg_admin_password")
}
func instanceIDPath(c *config.Config) string { return filepath.Join(c.AppDir, "instance_id") }
func panelCertPath(c *config.Config) string  { return filepath.Join(c.AppDir, "tls", "panel.crt") }
func panelKeyPath(c *config.Config) string   { return filepath.Join(c.AppDir, "tls", "panel.key") }
func stateBackupDir(c *config.Config) string { return filepath.Join(c.AppDir, "state-backups") }

// dataDirCheck checks PGDATA once init has prepared it. A variable so tests
// can simulate a volume that does not keep the mode.
var dataDirCheck = dataDirMode

// dataDirMode fails unless the PostgreSQL user can reach PGDATA from the
// volume root and PGDATA has a mode PostgreSQL accepts (0700 or 0750).
// Volumes whose permissions come from an NFSv4 ACL can refuse or rewrite a
// chmod, and then the server never starts.
func dataDirMode(root, pgdata string) error {
	for dir := filepath.Dir(pgdata); ; dir = filepath.Dir(dir) {
		st, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if st.Mode().Perm()&0o001 == 0 {
			return fmt.Errorf("%s was left at %04o and the PostgreSQL user cannot enter it", dir, st.Mode().Perm())
		}
		if dir == root || dir == filepath.Dir(dir) {
			break
		}
	}
	st, err := os.Stat(pgdata)
	if err != nil {
		return err
	}
	if p := st.Mode().Perm(); p != 0o700 && p != 0o750 {
		return fmt.Errorf("its folder was left at %04o and PostgreSQL requires 0700", p)
	}
	return nil
}

// Init prepares the volumes. It runs as root in the sqsolve-init container,
// without network, before PostgreSQL starts. It is idempotent.
func Init(c *config.Config, out io.Writer) error {
	root := os.Geteuid() == 0
	say := func(format string, args ...any) { fmt.Fprintf(out, "sqsolve init: "+format+"\n", args...) }
	chown := func(path string, uid, gid int) {
		if root {
			if err := os.Lchown(path, uid, gid); err != nil {
				say("warning: could not change the owner of %s: %v", path, err)
			}
		}
	}
	mkdir := func(path string, mode os.FileMode, uid, gid int) error {
		if err := os.MkdirAll(path, mode); err != nil {
			return fmt.Errorf("SQS-C050 could not create %s: %w", path, err)
		}
		os.Chmod(path, mode)
		chown(path, uid, gid)
		return nil
	}
	writeIfMissing := func(path string, data []byte, mode os.FileMode, uid, gid int) (bool, error) {
		if _, err := os.Stat(path); err == nil {
			os.Chmod(path, mode)
			chown(path, uid, gid)
			return false, nil
		}
		if err := fsx.WriteFileAtomic(path, data, mode); err != nil {
			return false, err
		}
		chown(path, uid, gid)
		return true, nil
	}

	// Identity of the three volumes, decided before writing anything
	// (ADR 0023).
	for _, v := range []struct {
		on   bool
		name string
	}{{c.BackupsAdopt, "SQSOLVE_BACKUPS_ADOPT"}, {c.DBAdopt, "SQSOLVE_DB_ADOPT"}, {c.AppAdopt, "SQSOLVE_APP_ADOPT"}} {
		if v.on {
			say("warning: %s is set: remove it from the compose file once the volume is marked, so an unmounted disk is never adopted", v.name)
		}
	}
	plan, err := planVolumes(c)
	if err != nil {
		return err
	}
	for _, w := range plan.Warnings {
		say("warning: %s", w)
	}
	instanceID := plan.ID
	mark := func(dir, volume string) error {
		if err := fsx.WriteMarker(dir, fsx.Marker{InstanceID: instanceID, Volume: volume, CreatedAt: time.Now().UTC()}); err != nil {
			return fmt.Errorf("SQS-C081 could not mark the %s volume: %w", volume, err)
		}
		chown(filepath.Join(dir, fsx.MarkerName), c.UID, c.GID)
		return nil
	}

	// App volume.
	for _, d := range []string{c.AppDir, c.SecretsDir(), filepath.Join(c.AppDir, "tls"), stateBackupDir(c)} {
		if err := mkdir(d, 0o700, c.UID, c.GID); err != nil {
			return err
		}
	}
	if plan.AdoptedFrom != "" {
		say("adopting identity %s from the %s volume (recovery of a previous installation)", instanceID, plan.AdoptedFrom)
	}

	// Backups volume: written only when it belongs to this installation.
	if plan.MarkBackups {
		if err := os.MkdirAll(c.BackupDir, 0o700); err != nil {
			return fmt.Errorf("SQS-C051 backups volume: %w", err)
		}
		if err := mark(c.BackupDir, "backups"); err != nil {
			return err
		}
		say("backups volume marked for installation %s", instanceID)
	}
	if _, err := writeIfMissing(instanceIDPath(c), []byte(instanceID+"\n"), 0o600, c.UID, c.GID); err != nil {
		return err
	}
	if plan.MarkApp {
		if err := mark(c.AppDir, "app"); err != nil {
			return err
		}
	} else {
		chown(filepath.Join(c.AppDir, fsx.MarkerName), c.UID, c.GID)
	}
	if plan.BackupsOK {
		for _, d := range []string{"libraries", "_system", "_import"} {
			if err := mkdir(filepath.Join(c.BackupDir, d), 0o700, c.UID, c.GID); err != nil {
				return err
			}
		}
		os.Chmod(c.BackupDir, 0o700)
		chown(c.BackupDir, c.UID, c.GID)
		chown(filepath.Join(c.BackupDir, fsx.MarkerName), c.UID, c.GID)
	}

	// Secrets of sqsolve.
	if created, err := writeIfMissing(masterKeyPath(c), []byte(base64.StdEncoding.EncodeToString(secrets.RandomBytes(32))+"\n"), 0o400, c.UID, c.GID); err != nil {
		return err
	} else if created {
		say("master key generated")
	}
	if _, err := writeIfMissing(adminPassPath(c), []byte(secrets.GeneratePassword(32).Reveal()+"\n"), 0o400, c.UID, c.GID); err != nil {
		return err
	}

	// PostgreSQL configuration inside the db volume.
	conf := c.PGConfDir()
	if err := mkdir(c.PGDir, 0o755, -1, -1); err != nil {
		return err
	}
	if plan.MarkDB {
		if err := mark(c.PGDir, "db"); err != nil {
			return err
		}
	} else {
		// sqsolve reads it (doctor); a new server can use another UID.
		chown(filepath.Join(c.PGDir, fsx.MarkerName), c.UID, c.GID)
	}
	// PGDATA is prepared here, like the permissions container of TrueNAS
	// apps, so the postgres container runs as its own user and never needs
	// root on the volume.
	pgdata := func() error {
		if err := os.MkdirAll(c.PGDataDir, 0o700); err != nil {
			return err
		}
		// On an NFSv4 ACL volume a new folder inherits the ACL and ignores
		// the mode it is created with (TrueNAS gives 0770), so each folder
		// between the volume root and PGDATA gets an explicit chmod.
		for dir := filepath.Dir(c.PGDataDir); strings.HasPrefix(dir, c.PGDir+string(filepath.Separator)); dir = filepath.Dir(dir) {
			if err := os.Chmod(dir, 0o755); err != nil {
				return err
			}
		}
		if err := os.Chmod(c.PGDataDir, 0o700); err != nil {
			return err
		}
		chown(c.PGDataDir, c.PGUID, c.PGGID)
		return dataDirCheck(c.PGDir, c.PGDataDir)
	}
	if err := pgdata(); err != nil {
		return fmt.Errorf("SQS-C054 PostgreSQL will not be able to use the database volume (%v). "+
			"This happens with folders that use NFSv4 ACLs: on TrueNAS, create the dataset with the Generic preset", err)
	}
	if err := mkdir(conf, 0o750, c.UID, c.PGGID); err != nil {
		return err
	}
	// setgid: files that sqsolve rewrites later (pg_hba.conf) keep the
	// postgres group, so the server can read them.
	os.Chmod(conf, 0o750|os.ModeSetgid)
	if created, err := writeIfMissing(c.SuperuserPasswordFile(), []byte(secrets.GeneratePassword(32).Reveal()+"\n"), 0o440, c.UID, c.PGGID); err != nil {
		return err
	} else if created {
		say("PostgreSQL superuser password generated")
	}
	pgc := pgconf.PostgresConf(pgconf.Params{DataDir: c.PGDataDir, ConfDir: conf, SocketDir: c.PGSocket, Port: c.PGPort,
		Listen: c.PGListen, MaxConnections: c.PGMaxConnections, MemoryMB: c.PGMemoryMB, SSL: true})
	if err := fsx.WriteFileAtomic(filepath.Join(conf, "postgresql.conf"), []byte(pgc), 0o640); err != nil {
		return err
	}
	chown(filepath.Join(conf, "postgresql.conf"), c.UID, c.PGGID)
	hba, _ := pgconf.HBA(pgconf.HBAOptions{})
	if _, err := writeIfMissing(filepath.Join(conf, "pg_hba.conf"), []byte(hba), 0o640, c.UID, c.PGGID); err != nil {
		return err
	}
	if _, err := writeIfMissing(filepath.Join(conf, "pg_ident.conf"), []byte("# Empty: sqsolve does not use user name maps.\n"), 0o640, c.UID, c.PGGID); err != nil {
		return err
	}
	// The folder and the certificate are readable so sqsolve can show its
	// expiry; the key belongs to the PostgreSQL user with mode 0600, which
	// PostgreSQL accepts. Owned by root, sqsolve cannot replace them.
	tlsDir := filepath.Join(conf, "tls")
	tlsOwner := c.UID
	if root {
		tlsOwner = 0
	}
	if err := mkdir(tlsDir, 0o755, tlsOwner, c.PGGID); err != nil {
		return err
	}
	crt, key := filepath.Join(tlsDir, "server.crt"), filepath.Join(tlsDir, "server.key")
	if _, err := fsx.RecoverPair(key, crt); err != nil {
		return fmt.Errorf("SQS-C080 could not write the PostgreSQL certificate: %w", err)
	}
	if _, err := tls.LoadX509KeyPair(crt, key); err != nil {
		hosts := []string{"localhost"}
		if c.AdvertisedHost != "" {
			hosts = append(hosts, c.AdvertisedHost)
		}
		cp, kp, err := pgconf.SelfSigned("sqsolve-postgresql", hosts, 10*365*24*time.Hour)
		if err != nil {
			return err
		}
		// Key first, then the certificate: a pair that never mixes halves.
		if err := fsx.WritePair(key, kp, 0o600, crt, cp, 0o644); err != nil {
			return fmt.Errorf("SQS-C080 could not write the PostgreSQL certificate: %w", err)
		}
		say("PostgreSQL TLS certificate generated")
	}
	chown(crt, tlsOwner, c.PGGID)
	os.Chmod(crt, 0o644)
	chown(key, c.PGUID, c.PGGID)
	os.Chmod(key, 0o600)

	if c.FixPerms && root {
		dirs := []string{c.AppDir}
		if plan.BackupsOK {
			dirs = append(dirs, c.BackupDir)
		}
		for _, dir := range dirs {
			filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err == nil {
					os.Lchown(p, c.UID, c.GID)
				}
				return nil
			})
		}
		say("permissions fixed recursively")
	}
	if !root {
		say("warning: not run as root; file owners were not changed")
	}
	say("done")
	return nil
}
