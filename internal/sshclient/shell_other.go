//go:build !darwin && !linux

package sshclient

import (
	"fmt"
	"io"
	"os"
)

func (c *Client) Shell(_ *os.File, _, _ io.Writer) error {
	return fmt.Errorf("interactive SSH currently supports macOS and Linux")
}

func (c *Client) ShellCommand(_ string, _ []byte, _ *os.File, _, _ io.Writer) error {
	return fmt.Errorf("interactive SSH currently supports macOS and Linux")
}

func (c *Client) Exec(_ string, _ *os.File, _, _ io.Writer) error {
	return fmt.Errorf("SSH command execution currently supports macOS and Linux")
}

func (c *Client) ExecPrefixed(_ string, _ []byte, _ *os.File, _, _ io.Writer) error {
	return fmt.Errorf("SSH command execution currently supports macOS and Linux")
}
