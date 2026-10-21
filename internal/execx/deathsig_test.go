// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as a helper process: with SQSOLVE_EXECX_HELPER set, the
// test binary runs a fake pg_dump through Runner and blocks.
func TestMain(m *testing.M) {
	if dir := os.Getenv("SQSOLVE_EXECX_HELPER"); dir != "" {
		r := &Runner{paths: map[string]string{}, dirs: []string{dir}, GracePeriod: time.Second}
		r.Run(context.Background(), Cmd{Name: "pg_dump"})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// findByArg returns the pid whose command line contains arg.
func findByArg(arg string) int {
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err == nil && strings.Contains(string(b), arg) {
			return pid
		}
	}
	return 0
}

// alive reports whether pid runs; zombies count as dead.
func alive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(b), ')')
	return i < 0 || i+2 >= len(b) || b[i+2] != 'Z'
}

func TestChildDiesWithSqsolve(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Pdeathsig is Linux only")
	}
	marker := fmt.Sprintf("%d.%05d", 3000+os.Getpid()%1000, time.Now().UnixNano()%100000)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pg_dump"), []byte("#!/bin/sh\nexec sleep "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=^$")
	helper.Env = append(os.Environ(), "SQSOLVE_EXECX_HELPER="+dir)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	child := 0
	for i := 0; i < 100 && child == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		child = findByArg(marker)
	}
	if child == 0 {
		helper.Process.Kill()
		helper.Wait()
		t.Fatal("the fake pg_dump never started")
	}
	// Only processes this test started are signalled: the cleanup checks
	// the start time recorded now before it signals.
	start := procStart(child)
	t.Cleanup(func() { killOwn(child, start) })
	helper.Process.Kill()
	helper.Wait()
	for i := 0; i < 50; i++ {
		if !alive(child) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("pg_dump kept running after sqsolve died")
}

// A child of the program that ignores SIGTERM must not outlive Run: once
// the leader exits, or after the grace period if the leader ignores SIGTERM
// too, the whole group gets SIGKILL while the leader is still unreaped.
func TestCancelKillsTheWholeGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	member := `/bin/sh -c 'trap "" TERM; echo $$ > "$PIDFILE"; exec sleep 300' &
wait`
	for _, tc := range []struct {
		name, leader string
		min          time.Duration
	}{
		{"leader exits on TERM", `trap 'exit 3' TERM`, 0},
		{"leader ignores TERM", `trap '' TERM`, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fakeBin(t, "vacuumdb", tc.leader+"\n"+member)
			r.GracePeriod = time.Second
			pidfile := filepath.Join(t.TempDir(), "member.pid")
			ctx, cancel := context.WithCancel(context.Background())
			pid := 0
			start := ""
			var cancelled time.Time
			go func() {
				for i := 0; i < 100 && pid == 0; i++ {
					time.Sleep(20 * time.Millisecond)
					if b, err := os.ReadFile(pidfile); err == nil && strings.HasSuffix(string(b), "\n") {
						pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
						start = procStart(pid)
					}
				}
				cancelled = time.Now()
				cancel()
			}()
			_, err := r.Run(ctx, Cmd{Name: "vacuumdb", Env: []string{"PIDFILE=" + pidfile}})
			took := time.Since(cancelled)
			if pid == 0 {
				t.Fatal("the group member never started")
			}
			// Only processes this test started are signalled: the cleanup
			// checks the start time recorded when the member was found.
			if start == "" {
				t.Fatal("could not read the start time of the group member")
			}
			t.Cleanup(func() { killOwn(pid, start) })
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled, got %v", err)
			}
			if took < tc.min || took > 5*time.Second {
				t.Fatalf("Run returned %s after the cancel", took)
			}
			for i := 0; i < 20 && alive(pid); i++ {
				time.Sleep(50 * time.Millisecond)
			}
			if alive(pid) {
				t.Fatal("a member of the group that ignores SIGTERM survived Run")
			}
		})
	}
}
