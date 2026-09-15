package sshclient

import (
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

const (
	ioctlGetTermios     = unix.TCGETS
	defaultTermiosSpeed = 38400
)

// pty-req carries real baud rates, while Linux speed_t values are encodings, so
// they have to be translated or the server falls back to 9600.
var termiosBauds = map[uint32]uint32{
	unix.B0: 0, unix.B50: 50, unix.B75: 75, unix.B110: 110, unix.B134: 134,
	unix.B150: 150, unix.B200: 200, unix.B300: 300, unix.B600: 600,
	unix.B1200: 1200, unix.B1800: 1800, unix.B2400: 2400, unix.B4800: 4800,
	unix.B9600: 9600, unix.B19200: 19200, unix.B38400: 38400, unix.B57600: 57600,
	unix.B115200: 115200, unix.B230400: 230400,
}

func termiosFlags(state *unix.Termios) (input, output, control, local uint64) {
	return uint64(state.Iflag), uint64(state.Oflag), uint64(state.Cflag), uint64(state.Lflag)
}

func termiosSpeeds(state *unix.Termios) (uint32, uint32) {
	baud, ok := termiosBauds[state.Cflag&unix.CBAUD]
	if !ok {
		baud = defaultTermiosSpeed
	}
	return baud, baud
}

func setPlatformModes(modes ssh.TerminalModes, input, output, local uint64) {
	setFlagModes(modes, input, map[uint8]uint64{ssh.IUTF8: unix.IUTF8, ssh.IUCLC: unix.IUCLC})
	setFlagModes(modes, output, map[uint8]uint64{ssh.OLCUC: unix.OLCUC})
	setFlagModes(modes, local, map[uint8]uint64{ssh.XCASE: unix.XCASE})
}
