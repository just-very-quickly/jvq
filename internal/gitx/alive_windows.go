// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package gitx

import "syscall"

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

// alive reports whether a process with this id is still running.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
