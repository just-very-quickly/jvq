// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package runner

import (
	"context"
	"os/exec"
	"syscall"
)

// shell runs command with sh -c in its own process group, so a timeout or
// an interrupt stops everything it started (go test starts the test binary
// as a child), not just the shell.
func shell(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative pid signals the whole group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return cmd
}
