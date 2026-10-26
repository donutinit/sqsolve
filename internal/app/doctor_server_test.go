// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/testpg"
)

// The readiness probe answers ready only after reconciliation and while
// PostgreSQL answers; doctor turns what it finds on the server into checks:
// databases outside the catalog, a pg_hba.conf PostgreSQL would refuse, a
// modified audit log, a superuser password that does not match, the cluster
// identity and a server that stopped (docs 13 §7).
func TestDoctorReportsServerProblems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := testpg.Start(t)
	cfg := clusterConfig(t, c, t.TempDir())
	a := openApp(t, ctx, cfg)
	defer a.Close()
	if a.PGAlive(ctx) {
		t.Fatal("alive before reconciliation")
	}
	a.Background(ctx)
	defer a.Shutdown(5 * time.Second)
	waitFor(t, "PostgreSQL", 20*time.Second, a.PGReady)
	if !a.PGAlive(ctx) {
		t.Fatal("not alive with PostgreSQL up")
	}
	a.Audit.Record(ctx, audit.System, "test.entry", "system", "", audit.OK, nil)
	checks := checkMap(Doctor(ctx, a))
	for _, n := range []string{"PostgreSQL", "PostgreSQL cluster", "Superuser over the socket", "pg_hba.conf", "Databases outside the catalog", "Audit log", "SQLite"} {
		if checks[n].Status != "ok" {
			t.Errorf("%s on a healthy installation: %+v", n, checks[n])
		}
	}
	if d := checks["Databases outside the catalog"].Detail; d != "none" {
		t.Errorf("databases outside the catalog: %q", d)
	}

	// A database made by hand is reported; sqsolve's auxiliary ones are not.
	su, err := c.Connect(ctx, "postgres", c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range []string{"handmade", pg.VerifyDB("abcd1234"), "wedding__prev_202610250000"} {
		if _, err := su.Exec(ctx, "CREATE DATABASE "+db); err != nil {
			t.Fatal(err)
		}
	}
	su.Close(ctx)
	if ch := checkMap(Doctor(ctx, a))["Databases outside the catalog"]; ch.Status != "warn" || ch.Detail != "handmade" {
		t.Errorf("a database made by hand: %+v", ch)
	}

	// A pg_hba.conf on disk that PostgreSQL would refuse.
	hba := filepath.Join(cfg.PGConfDir(), "pg_hba.conf")
	good, _ := os.ReadFile(hba)
	os.WriteFile(hba, append(append([]byte(nil), good...), "host all all 10.0.0.0/99 scram-sha-256\n"...), 0o640)
	ch := checkMap(Doctor(ctx, a))["pg_hba.conf"]
	os.WriteFile(hba, good, 0o640)
	if ch.Status != "fail" || !strings.Contains(ch.Detail, "line ") || ch.Hint != "use Maintenance > Regenerate pg_hba.conf" {
		t.Errorf("an invalid pg_hba.conf: %+v", ch)
	}

	// Someone edits the audit log.
	if _, err := a.DB.ExecContext(ctx, `UPDATE audit_log SET actor_name = 'mallory' WHERE seq = (SELECT max(seq) FROM audit_log)`); err != nil {
		t.Fatal(err)
	}
	var last int64
	a.DB.QueryRowContext(ctx, `SELECT max(seq) FROM audit_log`).Scan(&last)
	if ch := checkMap(Doctor(ctx, a))["Audit log"]; ch.Status != "fail" || ch.Detail != "chain broken at entry "+strconv.FormatInt(last, 10) {
		t.Errorf("an edited audit log: %+v (last entry %d)", ch, last)
	}

	// The cluster identity: accepted at the next start, then not stored yet.
	var current string
	a.DB.GetSetting(ctx, pgSystemIDSetting, &current)
	a.DB.SetSetting(ctx, pgSystemIDSetting, "1234", "test")
	cfg.PGAcceptSystemID = current
	if ch := checkMap(Doctor(ctx, a))["PostgreSQL cluster"]; ch.Status != "warn" ||
		ch.Detail != "system identifier "+current+" differs from 1234 and SQSOLVE_PG_ACCEPT_SYSTEM_ID accepts it at the next start" {
		t.Errorf("an accepted change: %+v", ch)
	}
	cfg.PGAcceptSystemID = ""
	a.DB.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, pgSystemIDSetting)
	if ch := checkMap(Doctor(ctx, a))["PostgreSQL cluster"]; ch.Status != "ok" || ch.Detail != "system identifier "+current+" (stored at the next start)" {
		t.Errorf("no identifier stored: %+v", ch)
	}
	a.DB.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES (?, '{broken', ?)`, pgSystemIDSetting, store.NowT())
	if ch := checkMap(Doctor(ctx, a))["PostgreSQL cluster"]; ch.Status != "warn" || !strings.HasPrefix(ch.Detail, "system identifier "+current+"; the stored one cannot be read: ") {
		t.Errorf("an unreadable stored identifier: %+v", ch)
	}

	// The console with a superuser password that is not the cluster's.
	os.Chmod(cfg.SuperuserPasswordFile(), 0o640)
	os.WriteFile(cfg.SuperuserPasswordFile(), []byte("not the password\n"), 0o440)
	os.Chmod(cfg.SuperuserPasswordFile(), 0o440)
	cli, err := Open(ctx, cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.PG.Open(ctx); err != nil {
		t.Fatal(err)
	}
	ch = checkMap(Doctor(ctx, cli))["Superuser over the socket"]
	cli.Close()
	if ch.Status != "fail" || !strings.HasPrefix(ch.Detail, "SQS-P001 PostgreSQL is not available: ") ||
		ch.Hint != "the password in "+cfg.SuperuserPasswordFile()+" does not match the cluster" {
		t.Errorf("a wrong superuser password: %+v", ch)
	}

	// PostgreSQL stops.
	c.Stop()
	if a.PGAlive(ctx) {
		t.Fatal("alive with PostgreSQL stopped")
	}
	if ch := checkMap(Doctor(ctx, a))["PostgreSQL"]; ch.Status != "fail" || !strings.HasPrefix(ch.Detail, "SQS-P001 PostgreSQL is not available") ||
		ch.Hint != "check the postgres container and the shared socket volume" {
		t.Errorf("PostgreSQL stopped: %+v", ch)
	}
}
