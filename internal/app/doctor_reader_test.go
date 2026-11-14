// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
)

// What a reader without the diagnostics permission gets of a failed
// connection to PostgreSQL (pg.Public, docs 08 §5).
const hiddenText = "the connection to PostgreSQL failed (the cause is in the sqsolve log)"

// TestChecksForHidesTheDriverText: on the Health page the doctor checks keep
// the driver's text of a failed connection, and the path of the superuser
// password file, only for a reader with the diagnostics permission; the
// whole text goes to the process log for every reader (docs 08 §5).
func TestChecksForHidesTheDriverText(t *testing.T) {
	ctx := context.Background()
	cl := pg.New(t.TempDir(), 5432, secrets.New("admin password"), secrets.New("superuser password"))
	pingErr := cl.Ping(ctx) // no pool: SQS-P001 without the driver's text
	_, connectErr := cl.ConnectDB(ctx, "postgres")
	_, superErr := cl.Superuser(ctx, "postgres")
	var buf bytes.Buffer
	a := &App{Svc: &core.Service{Log: slog.New(slog.NewJSONHandler(&buf, nil))}}
	checks := []Check{
		errCheck("PostgreSQL", "fail", "", superErr, "check the postgres container and the shared socket volume"),
		superuserCheck(superErr, "/var/lib/postgresql/sqsolve/superuser_password"),
		errCheck("pg_hba.conf", "fail", "valid, but the last rewrite did not take effect: ", connectErr, "save Settings, General again or use Maintenance > Regenerate pg_hba.conf"),
		errCheck("Audit log", "warn", "", errors.New("disk I/O error"), ""),
		errCheck("PostgreSQL cluster", "warn", "could not read the system identifier: ", pingErr, ""),
		{Name: "SQLite", Status: "ok", Detail: "integrity OK"},
	}
	whole := append([]Check(nil), checks...)

	const p001 = "SQS-P001 PostgreSQL is not available: "
	got := a.ChecksFor(checks, "/health", false)
	want := []Check{
		{Name: "PostgreSQL", Status: "fail", Detail: p001 + hiddenText, Hint: "check the postgres container and the shared socket volume"},
		{Name: "Superuser over the socket", Status: "fail", Detail: p001 + hiddenText, Hint: "the superuser password file does not match the cluster"},
		{Name: "pg_hba.conf", Status: "fail", Detail: "valid, but the last rewrite did not take effect: " + hiddenText, Hint: "save Settings, General again or use Maintenance > Regenerate pg_hba.conf"},
		{Name: "Audit log", Status: "warn", Detail: "disk I/O error"},
		{Name: "PostgreSQL cluster", Status: "warn", Detail: "could not read the system identifier: SQS-P001 PostgreSQL is not available"},
		{Name: "SQLite", Status: "ok", Detail: "integrity OK"},
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Status != want[i].Status || got[i].Detail != want[i].Detail || got[i].Hint != want[i].Hint {
			t.Errorf("for a reader without the permission:\n got %+v\nwant %+v", got[i], want[i])
		}
		for _, internal := range []string{cl.SocketDir, ".s.PGSQL", "sqsolve_admin", "user=postgres", "superuser_password"} {
			if strings.Contains(got[i].Detail+got[i].Hint, internal) {
				t.Errorf("%s names %q: %+v", got[i].Name, internal, got[i])
			}
		}
	}
	if !reflect.DeepEqual(checks, whole) {
		t.Error("ChecksFor changed the checks it was given")
	}
	if full := a.ChecksFor(checks, "/health", true); !reflect.DeepEqual(full, whole) {
		t.Errorf("for a reader with the permission:\n got %+v\nwant %+v", full, whole)
	}
	if whole[1].Hint != "the password in /var/lib/postgresql/sqsolve/superuser_password does not match the cluster" ||
		!strings.HasPrefix(whole[1].Detail, p001+"failed to connect to `user=postgres database=postgres`: ") {
		t.Errorf("the whole superuser check: %+v", whole[1])
	}

	// One line for each hidden error and each reader, with the whole error.
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ln map[string]any
		if err := json.Unmarshal([]byte(raw), &ln); err != nil {
			t.Fatalf("log line %q: %v", raw, err)
		}
		lines = append(lines, ln)
	}
	wantLines := []struct{ check, err string }{
		{"PostgreSQL", superErr.Error()}, {"Superuser over the socket", superErr.Error()}, {"pg_hba.conf", connectErr.Error()},
	}
	if len(lines) != 2*len(wantLines) {
		t.Fatalf("log: %+v", lines)
	}
	for i, ln := range lines {
		w := wantLines[i%len(wantLines)]
		if ln["level"] != "WARN" || ln["msg"] != "doctor: the connection to PostgreSQL failed" || ln["path"] != "/health" ||
			ln["check"] != w.check || ln["err"] != w.err {
			t.Errorf("log line %d: %+v", i, ln)
		}
	}
}
