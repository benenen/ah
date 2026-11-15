//go:build windows

package sshclient

import "os/exec"

func isolateProxyCommand(*exec.Cmd) {}

func killProxyCommand(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
