// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/secrets"
)

// fakeBin installs a script under an allowed name in a temp dir.
func fakeBin(t *testing.T, name, body string) *Runner {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Runner{paths: map[string]string{}, dirs: []string{dir}, GracePeriod: 2 * time.Second}
	return r
}

func TestNotAllowed(t *testing.T) {
	r := NewRunner("")
	if _, err := r.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "true"}}); err == nil {
		t.Fatal("sh must not be allowed")
	}
}

func TestRunAndPassfile(t *testing.T) {
	r := fakeBin(t, "pg_dump", `cat "$PGPASSFILE"; echo "args:$*"; echo warn >&2`)
	res, err := r.Run(context.Background(), Cmd{
		Name:       "pg_dump",
		Args:       []string{"--dbname", "a b;rm -rf /"},
		Credential: &Credential{User: "u", Password: secrets.New(`p:w\d`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, `*:*:*:u:p\:w\\d`) {
		t.Fatalf("passfile content: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "args:--dbname a b;rm -rf /") {
		t.Fatalf("args not passed verbatim: %q", res.Stdout)
	}
	if res.Stderr != "warn\n" {
		t.Fatalf("stderr %q", res.Stderr)
	}
}

func TestFailure(t *testing.T) {
	r := fakeBin(t, "pg_restore", `echo "boom" >&2; exit 4`)
	_, err := r.Run(context.Background(), Cmd{Name: "pg_restore"})
	var e *Error
	if !errors.As(err, &e) || e.ExitCode != 4 || !strings.Contains(e.Error(), "boom") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestCancelSendsTermToOwnGroup(t *testing.T) {
	r := fakeBin(t, "vacuumdb", `trap 'echo got-term >&2; exit 3' TERM
sleep 30 &
wait`)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := r.Run(ctx, Cmd{Name: "vacuumdb"})
	if err == nil {
		t.Fatal("expected error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancel took too long")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
