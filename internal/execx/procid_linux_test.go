// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// procStart returns the start time of pid (field 22 of /proc/<pid>/stat,
// in clock ticks since boot), or "" when it is gone. A later process that
// reuses the pid has another one.
func procStart(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ""
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3 (state)
	if len(f) < 20 {
		return ""
	}
	return f[19]
}

// sigkillMask is SIGKILL in the pending masks of /proc/<pid>/status.
const sigkillMask = 1 << (unix.SIGKILL - 1)

// killed reports whether the process pid, found with the start time start,
// can no longer run, and the status lines it judged by. It is killed when
// it is gone, when it has exited and waits to be reaped (Z) or is being
// reaped (X), or when a SIGKILL is pending for it: nothing can block, catch
// or ignore SIGKILL, and the kernel keeps one sent to the process or its
// group in the pending mask of the process (ShdPnd) until the process is
// reaped, so one that is still finishing its exit (R or D) counts as killed
// too.
func killed(pid int, start string) (bool, string) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH) {
		return true, "gone"
	}
	if err != nil {
		return false, err.Error()
	}
	// Read after the status: a process reaped while its status was read
	// can show empty masks, but then its pid no longer has this start time.
	if procStart(pid) != start {
		return true, "gone"
	}
	return statusKilled(string(b))
}

// statusKilled judges the text of /proc/<pid>/status as killed does.
func statusKilled(status string) (bool, string) {
	dead := false
	var seen []string
	for _, line := range strings.Split(status, "\n") {
		key, val, _ := strings.Cut(line, ":")
		val = strings.TrimSpace(val)
		switch key {
		case "State":
			dead = dead || strings.HasPrefix(val, "Z") || strings.HasPrefix(val, "X")
		case "SigPnd", "ShdPnd":
			m, err := strconv.ParseUint(val, 16, 64)
			dead = dead || (err == nil && m&sigkillMask != 0)
		default:
			continue
		}
		seen = append(seen, key+" "+val)
	}
	return dead, strings.Join(seen, ", ")
}

// killOwn sends SIGKILL to pid only while it is still the process the test
// started, identified by the start time recorded when the test found it.
// The pidfd is opened before the identity is checked, so the signal cannot
// reach a process that took the pid in between; on any mismatch or error
// nothing is signalled.
func killOwn(pid int, start string) {
	if pid <= 0 || start == "" {
		return
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	if procStart(pid) != start {
		return
	}
	unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
}

// killOwn leaves alone a pid whose start time does not match, and kills
// the process it recorded.
func TestKillOwnChecksIdentity(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	start := procStart(pid)
	if start == "" {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("no start time")
	}
	killOwn(pid, start+"1") // another process with the same pid
	killOwn(pid, "")
	time.Sleep(100 * time.Millisecond)
	if !alive(pid) {
		cmd.Wait()
		t.Fatal("signalled a process whose identity did not match")
	}
	killOwn(pid, start)
	err := cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("the own process was not killed: %v", err)
	}
}

// killed counts a sleeping process as running, and as killed one with a
// SIGKILL pending, one that waits to be reaped and one that is gone.
func TestKilledReadsTheProcessState(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	start := procStart(pid)
	if start == "" {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("no start time")
	}
	if ok, state := killed(pid, start); ok {
		killOwn(pid, start)
		cmd.Wait()
		t.Fatalf("a sleeping process counted as killed (%s)", state)
	}
	if ok, _ := killed(pid, start+"1"); !ok {
		t.Error("a pid that names another process did not count as gone")
	}
	killOwn(pid, start)
	// Unreaped, so the pid is still there: SIGKILL pending or a zombie.
	if ok, state := killed(pid, start); !ok {
		t.Errorf("a process sent SIGKILL did not count as killed (%s)", state)
	}
	cmd.Wait()
	if ok, state := killed(pid, start); !ok {
		t.Errorf("a reaped process did not count as killed (%s)", state)
	}
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{"State:\tR (running)\nSigPnd:\t0000000000000000\nShdPnd:\t0000000000000100\n", true},
		{"State:\tD (disk sleep)\nSigPnd:\t0000000000000100\nShdPnd:\t0000000000000000\n", true},
		{"State:\tX (dead)\nSigPnd:\t0000000000000000\nShdPnd:\t0000000000000000\n", true},
		{"State:\tS (sleeping)\nSigPnd:\t0000000000000000\nShdPnd:\t0000000000004000\n", false}, // SIGTERM only
	} {
		if got, state := statusKilled(tc.status); got != tc.want {
			t.Errorf("statusKilled(%s) = %v, want %v", state, got, tc.want)
		}
	}
}
