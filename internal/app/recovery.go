// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"database/sql"
	"encoding/base64"
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
	return a.DB.SetSetting(ctx, keyCheckSetting, sealed, "sistema")
}

// RecoveryKit is the text of the recovery kit.
func (a *App) RecoveryKit(ctx context.Context) (string, error) {
	key, err := ReadMasterKey(a.Cfg)
	if err != nil {
		return "", err
	}
	fp := "Por confirmar"
	if info, err := a.PanelCertInfo(); err == nil {
		fp = info.Fingerprint
	}
	var b strings.Builder
	fmt.Fprintf(&b, "sqsolve: kit de recuperación\n\n")
	fmt.Fprintf(&b, "Instalación:   %s\n", a.InstanceID)
	fmt.Fprintf(&b, "Nombre:        %s\n", a.Svc.Settings(ctx).InstanceName)
	fmt.Fprintf(&b, "Generado:      %s\n", time.Now().In(a.Cfg.Location).Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(&b, "Versión:       %s\n\n", Version)
	fmt.Fprintf(&b, "CLAVE MAESTRA (guárdala fuera de este servidor; quien la tenga junto con los\n")
	fmt.Fprintf(&b, "volúmenes puede leer las contraseñas de las bibliotecas):\n\n    %s\n\n", base64.StdEncoding.EncodeToString(key))
	fmt.Fprintf(&b, "Huella SHA-256 del certificado del panel:\n    %s\n\n", fp)
	b.WriteString(`Cómo recuperar la instalación en otro servidor:

1. Instala sqsolve con el mismo compose.yaml y monta en /backups la copia
   del volumen de respaldos (debe incluir la carpeta _system).
2. Abre el panel. En la pantalla de instalación elige "Recuperar una
   instalación existente", escribe el código de instalación y pega la clave
   maestra de este kit.
3. sqsolve restaura usuarios, políticas y credenciales, y se reinicia.
4. En el panel, cada biblioteca aparece "Sin base en el servidor": usa
   "Recrear desde respaldo". Las contraseñas de las estaciones no cambian.
5. Si el servidor cambió de IP, actualiza la dirección anunciada en Ajustes
   y la ubicación en cada estación.

Sin este kit, las bibliotecas se pueden restaurar igual desde los respaldos
(Mantenimiento > Reconstruir catálogo), pero con contraseñas nuevas.
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
		return "", errors.New("SQS-C070 la clave maestra debe ser el texto base64 del kit (44 caracteres)")
	}
	copies := SystemCopies(c)
	if len(copies) == 0 {
		return "", errors.New("SQS-C071 no hay copias del plano de control en el volumen de respaldos (_system)")
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
		return "", fmt.Errorf("SQS-C072 la clave no corresponde a ninguna copia del plano de control: %v", lastErr)
	}
	in, err := os.Open(chosen)
	if err != nil {
		return "", err
	}
	defer in.Close()
	staged := c.DBPath() + ".recover"
	out, err := os.OpenFile(staged, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	out.Sync()
	out.Close()
	if err := fsx.WriteFileAtomic(masterKeyPath(c)+".recover", []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o400); err != nil {
		return "", err
	}
	return filepath.Base(chosen), nil
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

// ApplyPendingRecovery moves staged recovery files into place. It runs
// before Open, so the database is not in use yet.
func ApplyPendingRecovery(c *config.Config, out io.Writer) error {
	db := c.DBPath() + ".recover"
	key := masterKeyPath(c) + ".recover"
	if _, err := os.Stat(db); err != nil {
		return nil
	}
	if _, err := os.Stat(key); err != nil {
		return nil
	}
	now := time.Now().UTC().Format("20060102T150405Z")
	if _, err := os.Stat(c.DBPath()); err == nil {
		os.Rename(c.DBPath(), filepath.Join(stateBackupDir(c), "sqsolve-reemplazada-"+now+".db"))
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(c.DBPath() + suffix)
	}
	if err := os.Rename(db, c.DBPath()); err != nil {
		return err
	}
	if _, err := os.Stat(masterKeyPath(c)); err == nil {
		os.Rename(masterKeyPath(c), masterKeyPath(c)+".reemplazada-"+now)
	}
	if err := os.Rename(key, masterKeyPath(c)); err != nil {
		return err
	}
	fsx.SyncDir(c.SecretsDir())
	fsx.SyncDir(c.AppDir)
	fmt.Fprintln(out, "sqsolve: se aplicó la recuperación del plano de control")
	return nil
}
