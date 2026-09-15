package sshclient

import (
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

const (
	ioctlGetTermios     = unix.TIOCGETA
	defaultTermiosSpeed = 38400
)

// Darwin speed_t values are the baud rates pty-req expects, so they pass through.

func termiosFlags(state *unix.Termios) (input, output, control, local uint64) {
	return state.Iflag, state.Oflag, state.Cflag, state.Lflag
}

func termiosSpeeds(state *unix.Termios) (uint32, uint32) {
	return uint32(state.Ispeed), uint32(state.Ospeed)
}

// Darwin has no IUTF8, IUCLC, OLCUC or XCASE; leaving them unset keeps the
// remote defaults, matching what OpenSSH sends from macOS.
func setPlatformModes(modes ssh.TerminalModes, input, output, local uint64) {}
