// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// sysProcAttr puts the child in its own process group and asks the kernel to
// kill it if sqsolve dies first, so no pg_dump keeps writing to a file that
// reconciliation already removed.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// waitExit blocks until the process pid exits without reaping it: until
// cmd.Wait reaps it, its pid (and so the id of its process group) cannot be
// given to any other process.
func waitExit(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err
		}
	}
}
