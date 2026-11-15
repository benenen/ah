//go:build !windows

package sshclient

import (
	"os/exec"
	"syscall"
)

// isolateProxyCommand starts the command in its own process group so Close can
// stop every process the shell spawned, not only the shell itself.
func isolateProxyCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProxyCommand(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
