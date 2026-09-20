//go:build darwin

package sshclient

import "golang.org/x/sys/unix"

// setTermiosSpeed requests a baud rate on an open pty. Darwin stores real rates
// in the termios speed fields.
func setTermiosSpeed(fd int, state *unix.Termios, baud uint32) error {
	state.Ispeed, state.Ospeed = uint64(baud), uint64(baud)
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, state)
}
