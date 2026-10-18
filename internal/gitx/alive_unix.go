// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package gitx

import (
	"errors"
	"syscall"
)

// alive reports whether a process with this id exists.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
