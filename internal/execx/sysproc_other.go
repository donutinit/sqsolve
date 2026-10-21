// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package execx

import (
	"errors"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// waitExit is Linux only; elsewhere the group escalation is skipped and
// WaitDelay kills the leader.
func waitExit(int) error { return errors.New("waiting without reaping is not supported here") }
