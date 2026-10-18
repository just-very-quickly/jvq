// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package runner

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
)

// shell runs command with cmd.exe in a new process group. A timeout or an
// interrupt stops the whole process tree with taskkill, so a test binary
// started by go test doesn't outlive the run.
func shell(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd.exe")
	// cmd.exe parses its own command line; hand it over as written.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `cmd.exe /S /C "` + command + `"`,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	return cmd
}
