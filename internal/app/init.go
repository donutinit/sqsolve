// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"encoding/base64"
	"errors"
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
	"github.com/donutinit/sqsolve/internal/ulid"
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

// dataDirMode fails unless PGDATA has a mode PostgreSQL accepts (0700 or
// 0750). Volumes whose permissions come from an NFSv4 ACL can refuse or
// rewrite the chmod, and then the server never starts.
func dataDirMode(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if p := st.Mode().Perm(); p != 0o700 && p != 0o750 {
		return fmt.Errorf("su carpeta quedó en %04o y PostgreSQL exige 0700", p)
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
				say("aviso: no se pudo cambiar el dueño de %s: %v", path, err)
			}
		}
	}
	mkdir := func(path string, mode os.FileMode, uid, gid int) error {
		if err := os.MkdirAll(path, mode); err != nil {
			return fmt.Errorf("SQS-C050 no se pudo crear %s: %w", path, err)
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

	// App volume.
	for _, d := range []string{c.AppDir, c.SecretsDir(), filepath.Join(c.AppDir, "tls"), stateBackupDir(c)} {
		if err := mkdir(d, 0o700, c.UID, c.GID); err != nil {
			return err
		}
	}

	// Backups volume and instance identity.
	if err := os.MkdirAll(c.BackupDir, 0o700); err != nil {
		return fmt.Errorf("SQS-C051 volumen de respaldos: %w", err)
	}
	instanceID := ""
	if b, err := os.ReadFile(instanceIDPath(c)); err == nil {
		instanceID = strings.TrimSpace(string(b))
	}
	marker, merr := fsx.ReadMarker(c.BackupDir)
	switch {
	case merr == nil && instanceID == "":
		instanceID = marker.InstanceID
		say("se adopta la identidad %s del volumen de respaldos (recuperación de una instalación anterior)", instanceID)
	case merr == nil && marker.InstanceID != instanceID:
		return fmt.Errorf("SQS-S003 el volumen de respaldos pertenece a otra instalación (%s); esta es %s", marker.InstanceID, instanceID)
	case errors.Is(merr, fsx.ErrNoMarker):
		empty, err := fsx.IsEmptyDir(c.BackupDir)
		if err != nil {
			return err
		}
		if !empty && os.Getenv("SQSOLVE_BACKUPS_ADOPT") != "1" {
			return errors.New("SQS-S004 el volumen de respaldos tiene archivos pero no es de sqsolve. Monta una carpeta vacía o define SQSOLVE_BACKUPS_ADOPT=1 si sabes que es la correcta")
		}
		if instanceID == "" {
			instanceID = ulid.New()
		}
		if err := fsx.WriteMarker(c.BackupDir, fsx.Marker{InstanceID: instanceID, Volume: "backups", CreatedAt: time.Now().UTC()}); err != nil {
			return err
		}
		say("volumen de respaldos marcado para la instalación %s", instanceID)
	case merr != nil:
		return merr
	}
	if _, err := writeIfMissing(instanceIDPath(c), []byte(instanceID+"\n"), 0o600, c.UID, c.GID); err != nil {
		return err
	}
	for _, d := range []string{"libraries", "_system", "_import"} {
		if err := mkdir(filepath.Join(c.BackupDir, d), 0o700, c.UID, c.GID); err != nil {
			return err
		}
	}
	os.Chmod(c.BackupDir, 0o700)
	chown(c.BackupDir, c.UID, c.GID)
	chown(filepath.Join(c.BackupDir, fsx.MarkerName), c.UID, c.GID)

	// Secrets of sqsolve.
	if created, err := writeIfMissing(masterKeyPath(c), []byte(base64.StdEncoding.EncodeToString(secrets.RandomBytes(32))+"\n"), 0o400, c.UID, c.GID); err != nil {
		return err
	} else if created {
		say("clave maestra generada")
	}
	if _, err := writeIfMissing(adminPassPath(c), []byte(secrets.GeneratePassword(32).Reveal()+"\n"), 0o400, c.UID, c.GID); err != nil {
		return err
	}

	// PostgreSQL configuration inside the db volume.
	conf := c.PGConfDir()
	if err := mkdir(c.PGDir, 0o755, -1, -1); err != nil {
		return err
	}
	// PGDATA is prepared here, like the permissions container of TrueNAS
	// apps, so the postgres container runs as its own user and never needs
	// root on the volume.
	if err := os.MkdirAll(filepath.Dir(c.PGDataDir), 0o755); err != nil {
		return fmt.Errorf("SQS-C050 no se pudo crear %s: %w", filepath.Dir(c.PGDataDir), err)
	}
	if err := mkdir(c.PGDataDir, 0o700, c.PGUID, c.PGGID); err != nil {
		return err
	}
	if err := dataDirCheck(c.PGDataDir); err != nil {
		return fmt.Errorf("SQS-C054 el volumen de la base de datos no conserva los permisos que pide PostgreSQL (%v). "+
			"Pasa con carpetas que usan ACL de tipo NFSv4: en TrueNAS, crea el dataset con el preset Generic", err)
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
		say("contraseña del superusuario de PostgreSQL generada")
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
	if _, err := writeIfMissing(filepath.Join(conf, "pg_ident.conf"), []byte("# Vacío: sqsolve no usa mapas de usuarios.\n"), 0o640, c.UID, c.PGGID); err != nil {
		return err
	}
	tlsDir := filepath.Join(conf, "tls")
	tlsOwner := c.UID
	if root {
		tlsOwner = 0
	}
	if err := mkdir(tlsDir, 0o750, tlsOwner, c.PGGID); err != nil {
		return err
	}
	crt, key := filepath.Join(tlsDir, "server.crt"), filepath.Join(tlsDir, "server.key")
	if _, err := os.Stat(crt); err != nil {
		hosts := []string{"localhost"}
		if c.AdvertisedHost != "" {
			hosts = append(hosts, c.AdvertisedHost)
		}
		cp, kp, err := pgconf.SelfSigned("sqsolve-postgresql", hosts, 10*365*24*time.Hour)
		if err != nil {
			return err
		}
		if err := fsx.WriteFileAtomic(crt, cp, 0o644); err != nil {
			return err
		}
		keyMode := os.FileMode(0o640)
		if !root {
			keyMode = 0o600
		}
		if err := fsx.WriteFileAtomic(key, kp, keyMode); err != nil {
			return err
		}
		say("certificado TLS de PostgreSQL generado")
	}
	chown(crt, tlsOwner, c.PGGID)
	chown(key, tlsOwner, c.PGGID)
	if root {
		os.Chmod(key, 0o640)
	}

	if c.FixPerms && root {
		for _, dir := range []string{c.AppDir, c.BackupDir} {
			filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err == nil {
					os.Lchown(p, c.UID, c.GID)
				}
				return nil
			})
		}
		say("permisos corregidos de forma recursiva")
	}
	if !root {
		say("aviso: no se ejecutó como root; no se cambiaron dueños de archivos")
	}
	say("listo")
	return nil
}
