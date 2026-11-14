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

// An allowed program that is absent, not executable or a folder is not
// found, by Path and by Run, which then starts nothing.
func TestMissingProgramIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pg_dump"), []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "pg_restore"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &Runner{paths: map[string]string{}, dirs: []string{dir, ""}, GracePeriod: time.Second}
	for _, name := range []string{"pg_dump", "pg_restore", "vacuumdb"} {
		want := "SQS-X002 " + name + " not found"
		if _, err := r.Path(name); err == nil || err.Error() != want {
			t.Errorf("Path(%s): %v", name, err)
		}
		if _, err := r.Run(context.Background(), Cmd{Name: name}); err == nil || err.Error() != want {
			t.Errorf("Run(%s): %v", name, err)
		}
	}
}

func TestNotAllowed(t *testing.T) {
	r := NewRunner("")
	if _, err := r.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "true"}}); err == nil || err.Error() != "SQS-X001 program not allowed: sh" {
		t.Fatalf("sh must not be allowed: %v", err)
	}
}

// A password or user with a line break would write a second line into the
// password file: refused before anything is written or run.
func TestCredentialWithLineBreaksIsRefused(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	r := fakeBin(t, "pg_dump", `touch "`+marker+`"`)
	for _, c := range []*Credential{
		{User: "u", Password: secrets.New("a\nb")},
		{User: "u", Password: secrets.New("a\rb")},
		{User: "u\nx", Password: secrets.New("ab")},
	} {
		_, err := r.Run(context.Background(), Cmd{Name: "pg_dump", Credential: c})
		if err == nil || err.Error() != "SQS-X003 credential with line breaks" {
			t.Fatalf("credential %q: %v", c.User, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the program ran with a refused credential")
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
