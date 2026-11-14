// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// TestSyncHBAFailuresAreReported: a pg_hba.conf that PostgreSQL rejects is
// replaced by the previous file and nothing is reloaded (SQS-P042); a reload
// that fails is reported (SQS-P043), the file stays written without being in
// force, and the next sync applies it (docs 08 §7).
func TestSyncHBAFailuresAreReported(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, j, err := e.svc.CreateLibrary(ctx, admin, core.CreateInput{DisplayName: "HBA", Slug: "hba_lib"})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	pass, err := e.svc.RevealPassword(ctx, admin, l.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	l = e.lib(l.ID)
	path := filepath.Join(e.c.ConfDir, "pg_hba.conf")
	su, err := e.c.Connect(ctx, "postgres", e.c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(context.WithoutCancel(ctx))
	exec := func(t *testing.T, q string) {
		t.Helper()
		if _, err := su.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// The library waits for Resolve: its role reaches the postgres database
	// from 127.0.0.1.
	if err := e.reach(l.OwnerRole, pass, "postgres"); err != nil {
		t.Fatal(err)
	}

	e.subtest(t, "reload", func(t *testing.T) {
		const grant = `GRANT EXECUTE ON FUNCTION pg_catalog.pg_reload_conf() TO sqsolve_admin`
		exec(t, `REVOKE EXECUTE ON FUNCTION pg_catalog.pg_reload_conf() FROM sqsolve_admin`)
		defer su.Exec(context.WithoutCancel(ctx), grant)
		err := e.svc.SetNetworkRules(ctx, admin, l.ID, []string{"10.9.9.0/24"})
		if err == nil || !strings.HasPrefix(err.Error(), "SQS-P043 could not reload PostgreSQL: ") ||
			!strings.Contains(err.Error(), "permission denied for function pg_reload_conf") {
			t.Fatalf("sync without the right to reload: %v", err)
		}
		// The file is written, but nothing loaded it.
		if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "10.9.9.0/24") {
			t.Fatalf("pg_hba.conf after the failed reload: %v\n%s", err, b)
		}
		if err := e.reach(l.OwnerRole, pass, "postgres"); err != nil {
			t.Fatalf("a rule that was not loaded is in force: %v", err)
		}
		// The next sync reloads although the file did not change.
		exec(t, grant)
		if err := e.svc.SyncHBA(ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.reach(l.OwnerRole, pass, "postgres"); err == nil {
			t.Fatal("the next sync did not apply the rule")
		}
		if err := e.svc.SetNetworkRules(ctx, admin, l.ID, nil); err != nil {
			t.Fatal(err)
		}
		if err := e.reach(l.OwnerRole, pass, "postgres"); err != nil {
			t.Fatalf("after the rule was removed: %v", err)
		}
	})

	// The test server runs without TLS, so "TLS only" writes lines that
	// PostgreSQL rejects.
	e.subtest(t, "validation", func(t *testing.T) {
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		st := e.svc.Settings(ctx)
		st.RequireTLS = true
		err = e.svc.SaveSettings(ctx, admin, st)
		if err == nil || !strings.HasPrefix(err.Error(), "SQS-P042 generated pg_hba.conf is not valid: line ") ||
			!strings.Contains(err.Error(), ": hostssl record cannot match because SSL is disabled") {
			t.Fatalf("a pg_hba.conf PostgreSQL rejects: %v", err)
		}
		if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, before) {
			t.Fatalf("the previous pg_hba.conf is not back: %v\n%s", err, after)
		}
		if err := e.reach(l.OwnerRole, pass, "postgres"); err != nil {
			t.Fatalf("after the rejected file: %v", err)
		}
		st.RequireTLS = false
		if err := e.svc.SaveSettings(ctx, admin, st); err != nil {
			t.Fatal(err)
		}
	})

	// Without pg_hba.conf.prev the rejected file cannot be replaced: the
	// error of the rewrite, its audit entry and HBAProblem say so after the
	// unchanged SQS-P042 message.
	e.subtest(t, "validation without the previous file", func(t *testing.T) {
		for _, p := range []string{path, path + ".prev"} {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}
		st := e.svc.Settings(ctx)
		st.RequireTLS = true
		err := e.svc.SaveSettings(ctx, admin, st)
		suffix := ". The previous pg_hba.conf could not be put back, so the rejected file stays on disk: open " + path + ".prev: no such file or directory"
		if err == nil || !strings.HasPrefix(err.Error(), "SQS-P042 generated pg_hba.conf is not valid: line ") ||
			!strings.Contains(err.Error(), ": hostssl record cannot match because SSL is disabled") || !strings.HasSuffix(err.Error(), suffix) {
			t.Fatalf("a rejected pg_hba.conf that cannot be replaced: %v", err)
		}
		if p := e.svc.HBAProblem(); p == nil || p.Error() != err.Error() {
			t.Fatalf("problem %v", p)
		}
		if es, _ := e.auditOf("runtime", "postgres.hba", nil); len(es) == 0 || es[len(es)-1].Outcome != "failed" || es[len(es)-1].Details["error"] != err.Error() {
			t.Fatalf("audit entries %+v", es)
		}
		if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "hostssl") {
			t.Fatalf("pg_hba.conf after the failed restore: %v\n%s", err, b)
		}
		st.RequireTLS = false
		if err := e.svc.SaveSettings(ctx, admin, st); err != nil {
			t.Fatal(err)
		}
		if err := e.reach(l.OwnerRole, pass, "postgres"); err != nil {
			t.Fatalf("after the rejected file was replaced: %v", err)
		}
	})
}
