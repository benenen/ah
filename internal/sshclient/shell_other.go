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
