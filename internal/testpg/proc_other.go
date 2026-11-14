// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package testpg

import "syscall"

// procAttr cannot ask for a signal at the death of the test elsewhere.
func procAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// alive cannot tell a dead process here, so no folder is taken for stale.
func alive(int) bool { return true }
