//go:build darwin || linux

package sshclient

import (
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// Control characters and flags carried by pty-req, mirroring OpenSSH. Modes we
// omit fall back to the server default, which is why the local settings must be
// sent: sshd leaves IUTF8 off, so erase deletes one byte of a multi-byte
// character, and a locally rebound erase key would never reach the remote tty.
var (
	termiosChars = map[uint8]int{
		ssh.VINTR: unix.VINTR, ssh.VQUIT: unix.VQUIT, ssh.VERASE: unix.VERASE,
		ssh.VKILL: unix.VKILL, ssh.VEOF: unix.VEOF, ssh.VEOL: unix.VEOL,
		ssh.VEOL2: unix.VEOL2, ssh.VSTART: unix.VSTART, ssh.VSTOP: unix.VSTOP,
		ssh.VSUSP: unix.VSUSP, ssh.VREPRINT: unix.VREPRINT, ssh.VWERASE: unix.VWERASE,
		ssh.VLNEXT: unix.VLNEXT, ssh.VDISCARD: unix.VDISCARD,
	}
	termiosInput = map[uint8]uint64{
		ssh.IGNPAR: unix.IGNPAR, ssh.PARMRK: unix.PARMRK, ssh.INPCK: unix.INPCK,
		ssh.ISTRIP: unix.ISTRIP, ssh.INLCR: unix.INLCR, ssh.IGNCR: unix.IGNCR,
		ssh.ICRNL: unix.ICRNL, ssh.IXON: unix.IXON, ssh.IXANY: unix.IXANY,
		ssh.IXOFF: unix.IXOFF, ssh.IMAXBEL: unix.IMAXBEL,
	}
	termiosOutput = map[uint8]uint64{
		ssh.OPOST: unix.OPOST, ssh.ONLCR: unix.ONLCR, ssh.OCRNL: unix.OCRNL,
		ssh.ONOCR: unix.ONOCR, ssh.ONLRET: unix.ONLRET,
	}
	termiosLocal = map[uint8]uint64{
		ssh.ISIG: unix.ISIG, ssh.ICANON: unix.ICANON, ssh.ECHO: unix.ECHO,
		ssh.ECHOE: unix.ECHOE, ssh.ECHOK: unix.ECHOK, ssh.ECHONL: unix.ECHONL,
		ssh.NOFLSH: unix.NOFLSH, ssh.TOSTOP: unix.TOSTOP, ssh.IEXTEN: unix.IEXTEN,
		ssh.ECHOCTL: unix.ECHOCTL, ssh.ECHOKE: unix.ECHOKE, ssh.PENDIN: unix.PENDIN,
	}
	termiosControl = map[uint8]uint64{ssh.PARENB: unix.PARENB, ssh.PARODD: unix.PARODD}
)

// terminalModes describes the local terminal so the remote PTY behaves the same.
// Reading it must happen before the terminal is switched to raw mode.
func terminalModes(fd int) ssh.TerminalModes {
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: defaultTermiosSpeed, ssh.TTY_OP_OSPEED: defaultTermiosSpeed}
	state, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return modes
	}
	for op, index := range termiosChars {
		if index < len(state.Cc) {
			modes[op] = uint32(state.Cc[index])
		}
	}
	input, output, control, local := termiosFlags(state)
	setFlagModes(modes, input, termiosInput)
	setFlagModes(modes, output, termiosOutput)
	setFlagModes(modes, control, termiosControl)
	setFlagModes(modes, local, termiosLocal)
	setPlatformModes(modes, input, output, local)
	// CS7 and CS8 share the CSIZE bits and the server applies modes in arbitrary
	// order, so send only the width actually in use.
	if control&unix.CSIZE == unix.CS7 {
		modes[ssh.CS7] = 1
	} else {
		modes[ssh.CS8] = 1
	}
	ispeed, ospeed := termiosSpeeds(state)
	modes[ssh.TTY_OP_ISPEED], modes[ssh.TTY_OP_OSPEED] = ispeed, ospeed
	return modes
}

func setFlagModes(modes ssh.TerminalModes, flags uint64, table map[uint8]uint64) {
	for op, bit := range table {
		if flags&bit != 0 {
			modes[op] = 1
		} else {
			modes[op] = 0
		}
	}
}
