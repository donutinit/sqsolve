// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package execx

import "syscall"

func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
