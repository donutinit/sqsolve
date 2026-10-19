// SPDX-License-Identifier: AGPL-3.0-only

package execx

import "syscall"

// sysProcAttr puts the child in its own process group and asks the kernel to
// kill it if sqsolve dies first, so no pg_dump keeps writing to a file that
// reconciliation already removed.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
