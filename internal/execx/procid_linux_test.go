// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
