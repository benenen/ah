package sshclient

import (
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
