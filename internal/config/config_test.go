// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9432" || c.PGPort != 5432 || c.PGDataDir != "/var/lib/postgresql/18/docker" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.PGConfDir() != "/var/lib/postgresql/sqsolve" {
		t.Fatal(c.PGConfDir())
	}
}

func TestInvalid(t *testing.T) {
	_, err := load(env(map[string]string{
		"SQSOLVE_WORKERS":         "0",
		"SQSOLVE_LOG_LEVEL":       "loud",
		"SQSOLVE_APP_DIR":         "relative",
		"SQSOLVE_TRUSTED_PROXIES": "10.0.0.0/8, nope",
		"TZ":                      "Mars/Olympus",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"SQSOLVE_WORKERS", "SQSOLVE_LOG_LEVEL", "SQSOLVE_APP_DIR", "nope", "Mars"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestProxies(t *testing.T) {
	c, err := load(env(map[string]string{"SQSOLVE_TRUSTED_PROXIES": "10.1.2.3, 192.168.0.0/16"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 2 || c.TrustedProxies[0].Bits() != 32 {
		t.Fatalf("%v", c.TrustedProxies)
	}
}

// SQSOLVE_BACKUPS_ADOPT used to accept only "1"; every adopt variable is a
// boolean like the others (audit finding, task 4 item 15).
func TestAdoptVariablesAreBooleans(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		c, err := load(env(map[string]string{"SQSOLVE_BACKUPS_ADOPT": v, "SQSOLVE_DB_ADOPT": v, "SQSOLVE_APP_ADOPT": v}))
		if err != nil {
			t.Fatal(err)
		}
		if !c.BackupsAdopt || !c.DBAdopt || !c.AppAdopt {
			t.Errorf("%q not read as true: %+v", v, c)
		}
	}
	c, err := load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.BackupsAdopt || c.DBAdopt || c.AppAdopt || c.PGAcceptSystemID != "" {
		t.Fatalf("adopt variables must default to off: %+v", c)
	}
}

func TestAcceptSystemIDMustBeANumber(t *testing.T) {
	c, err := load(env(map[string]string{"SQSOLVE_PG_ACCEPT_SYSTEM_ID": " 7429863312345678901 "}))
	if err != nil || c.PGAcceptSystemID != "7429863312345678901" {
		t.Fatalf("%v %q", err, c.PGAcceptSystemID)
	}
	_, err = load(env(map[string]string{"SQSOLVE_PG_ACCEPT_SYSTEM_ID": "yes"}))
	if err == nil || !strings.Contains(err.Error(), "SQS-C001 SQSOLVE_PG_ACCEPT_SYSTEM_ID") {
		t.Fatalf("want SQS-C001 for a non numeric identifier, got %v", err)
	}
}
