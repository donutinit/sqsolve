// SPDX-License-Identifier: AGPL-3.0-only

// Package execx runs the few external programs sqsolve needs (pg_dump,
// pg_restore, vacuumdb). No shell is ever involved, arguments are passed as
// an array, and cancelling only signals the process group sqsolve started.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/donutinit/sqsolve/internal/secrets"
)

// Allowed lists the programs that may be run.
var Allowed = []string{"pg_dump", "pg_restore", "vacuumdb", "restic"}

// Runner resolves and runs allowed binaries.
type Runner struct {
	mu    sync.Mutex
	paths map[string]string
	dirs  []string
	// GracePeriod between SIGTERM and SIGKILL when cancelled.
	GracePeriod time.Duration
}

// NewRunner looks binaries up in binDir first (if set), then in the
// well-known PostgreSQL locations and PATH.
func NewRunner(binDir string) *Runner {
	var dirs []string
	if binDir != "" {
		dirs = append(dirs, binDir)
	}
	matches, _ := filepath.Glob("/usr/lib/postgresql/*/bin")
	for i := len(matches) - 1; i >= 0; i-- {
		dirs = append(dirs, matches[i])
	}
	dirs = append(dirs, filepath.SplitList(os.Getenv("PATH"))...)
	return &Runner{paths: map[string]string{}, dirs: dirs, GracePeriod: 10 * time.Second}
}

// Path returns the absolute path of an allowed binary.
func (r *Runner) Path(name string) (string, error) {
	ok := false
	for _, a := range Allowed {
		if a == name {
			ok = true
		}
	}
	if !ok {
		return "", fmt.Errorf("SQS-X001 programa no permitido: %s", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.paths[name]; ok {
		return p, nil
	}
	for _, d := range r.dirs {
		if d == "" {
			continue
		}
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			abs, _ := filepath.Abs(p)
			r.paths[name] = abs
			return abs, nil
		}
	}
	return "", fmt.Errorf("SQS-X002 no se encontró %s", name)
}

// Credential is passed to libpq through a temporary PGPASSFILE.
type Credential struct {
	User     string
	Password secrets.String
}

// Cmd describes one execution.
type Cmd struct {
	Name       string
	Args       []string
	Env        []string // extra KEY=VALUE pairs
	Credential *Credential
	Stdout     *bytes.Buffer // optional; capped at MaxOutput
	MaxOutput  int
}

// Result of a finished command.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// Error wraps a failed execution with its stderr.
type Error struct {
	Name     string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 2000 {
		msg = msg[len(msg)-2000:]
	}
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("%s terminó con código %d: %s", e.Name, e.ExitCode, msg)
}

func (e *Error) Unwrap() error { return e.Err }

type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

// Run executes the command and waits. Cancelling ctx sends SIGTERM to the
// child's own process group and SIGKILL after the grace period.
func (r *Runner) Run(ctx context.Context, c Cmd) (Result, error) {
	path, err := r.Path(c.Name)
	if err != nil {
		return Result{}, err
	}
	max := c.MaxOutput
	if max == 0 {
		max = 64 << 10
	}
	cmd := exec.CommandContext(ctx, path, c.Args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid: the group created for this child only.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = r.GracePeriod
	env := []string{"PATH=" + filepath.Dir(path) + ":/usr/bin:/bin", "LC_ALL=C.UTF-8", "TZ=UTC"}
	env = append(env, c.Env...)
	if c.Credential != nil {
		pf, err := writePassfile(c.Credential)
		if err != nil {
			return Result{}, err
		}
		defer os.Remove(pf)
		env = append(env, "PGPASSFILE="+pf)
	}
	cmd.Env = env
	stderr := &capped{max: max}
	stdout := &capped{max: max}
	cmd.Stderr = stderr
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout
	} else {
		cmd.Stdout = stdout
	}
	start := time.Now()
	err = cmd.Run()
	res := Result{Stdout: stdout.buf.String(), Stderr: stderr.buf.String(), Duration: time.Since(start)}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		if ctx.Err() != nil {
			return res, &Error{Name: c.Name, ExitCode: res.ExitCode, Stderr: res.Stderr, Err: ctx.Err()}
		}
		return res, &Error{Name: c.Name, ExitCode: res.ExitCode, Stderr: res.Stderr, Err: err}
	}
	return res, nil
}

func escapePass(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, ":", `\:`)
}

func writePassfile(c *Credential) (string, error) {
	if strings.ContainsAny(c.Password.Reveal(), "\n\r") || strings.ContainsAny(c.User, "\n\r") {
		return "", errors.New("SQS-X003 credencial con saltos de línea")
	}
	f, err := os.CreateTemp("", "sqsolve-pgpass-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	line := "*:*:*:" + escapePass(c.User) + ":" + escapePass(c.Password.Reveal()) + "\n"
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}
