//go:build !windows

package sshclient

import (
	"os/exec"
	"syscall"
)

type proxyResources struct{}

func newProxyCommand(command string) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", command)
}

// isolateProxyCommand starts the command in its own process group so Close can
// stop every process the shell spawned, not only the shell itself.
func isolateProxyCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attachProxyProcess(*exec.Cmd, *proxyResources) error { return nil }

func releaseProxyResources(*proxyResources) {}

func killProxyCommand(cmd *exec.Cmd, _ *proxyResources) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
