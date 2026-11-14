// SPDX-License-Identifier: AGPL-3.0-only

package testpg

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// procAttr puts postgres in its own process group and asks the kernel to
// send it SIGQUIT (immediate shutdown) when the test process dies first, so
// a killed test leaves no cluster running. The kernel sends it when the
// thread that started postgres exits, which Go does only for a goroutine
// that locks its thread and ends; the tests do not.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGQUIT}
}

// alive reports whether pid may still run: anything but a pid missing from
// /proc counts, a zombie and a pid taken by another process too.
func alive(pid int) bool {
	_, err := os.Stat("/proc/" + strconv.Itoa(pid))
	return !errors.Is(err, fs.ErrNotExist)
}
