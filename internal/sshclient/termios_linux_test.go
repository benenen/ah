package sshclient

import (
	"fmt"
	"testing"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// Without IUTF8 the remote tty erases one byte at a time, which breaks backspace
// on multi-byte input, so the local value has to reach the server.
func TestTerminalModesForwardUTF8AndRemappedErase(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skip("pty unavailable:", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	fd := int(tty.Fd())
	state, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		t.Fatal(err)
	}
	state.Iflag |= unix.IUTF8
	state.Cc[unix.VERASE] = 0x08
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, state); err != nil {
		t.Fatal(err)
	}
	modes := terminalModes(fd)
	if modes[ssh.IUTF8] != 1 {
		t.Fatal("IUTF8 not forwarded")
	}
	if modes[ssh.VERASE] != 0x08 {
		t.Fatalf("erase %d, want 8", modes[ssh.VERASE])
	}
	state.Iflag &^= unix.IUTF8
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, state); err != nil {
		t.Fatal(err)
	}
	if terminalModes(fd)[ssh.IUTF8] != 0 {
		t.Fatal("IUTF8 reported while disabled")
	}
}

// setTermiosSpeed requests a baud rate on an open pty. Linux keeps speed_t
// encodings in the CBAUD bits rather than real rates.
func setTermiosSpeed(fd int, state *unix.Termios, baud uint32) error {
	flag, ok := linuxBaudFlags[baud]
	if !ok {
		return fmt.Errorf("unsupported test baud rate %d", baud)
	}
	state.Cflag = state.Cflag&^unix.CBAUD | flag
	return unix.IoctlSetTermios(fd, unix.TCSETS, state)
}

var linuxBaudFlags = map[uint32]uint32{9600: unix.B9600, 38400: unix.B38400, 115200: unix.B115200}

// Mocking the termios flags keeps this off the kernel: setting an unlisted speed
// on a real pty is not portable, but the fallback still has to be covered.
func TestTermiosSpeedsTranslateEncodingAndFallBack(t *testing.T) {
	const unknownEncoding = 0x1004 // B460800, absent from termiosBauds
	for _, tc := range []struct {
		name        string
		cflag, want uint32
	}{
		{"known", unix.B115200, 115200},
		{"unlisted", unknownEncoding, defaultTermiosSpeed},
		{"unset", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ispeed, ospeed := termiosSpeeds(&unix.Termios{Cflag: tc.cflag})
			if ispeed != tc.want || ospeed != tc.want {
				t.Fatalf("speeds %d/%d, want %d", ispeed, ospeed, tc.want)
			}
		})
	}
}
