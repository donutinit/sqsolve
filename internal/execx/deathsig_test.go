// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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
	// Only processes this test started are signalled.
	t.Cleanup(func() {
		if alive(child) {
			syscall.Kill(child, syscall.SIGKILL)
		}
	})
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
