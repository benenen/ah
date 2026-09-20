//go:build darwin || linux

package sshclient

import (
	"testing"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func TestTerminalModesReportLocalTerminal(t *testing.T) {
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
	modes := terminalModes(fd)
	if modes[ssh.VERASE] != uint32(state.Cc[unix.VERASE]) {
		t.Fatalf("erase %d, want %d", modes[ssh.VERASE], state.Cc[unix.VERASE])
	}
	if modes[ssh.VINTR] != uint32(state.Cc[unix.VINTR]) {
		t.Fatalf("intr %d, want %d", modes[ssh.VINTR], state.Cc[unix.VINTR])
	}
	if modes[ssh.ICANON] != 1 || modes[ssh.ECHO] != 1 || modes[ssh.ECHOE] != 1 {
		t.Fatal("cooked terminal reported as raw", modes)
	}
	// CS7 and CS8 share the CSIZE bits, so only the active width may be sent.
	if _, ok := modes[ssh.CS7]; ok || modes[ssh.CS8] != 1 {
		t.Fatal("unexpected character size modes", modes[ssh.CS7], modes[ssh.CS8])
	}
	// pty-req carries baud rates, not speed_t encodings, and the kernel default
	// differs per platform (9600 on macOS, 38400 on Linux), so request a rate that
	// is neither and check it is forwarded as-is.
	const baud = 115200
	if err := setTermiosSpeed(fd, state, baud); err != nil {
		t.Fatal(err)
	}
	modes = terminalModes(fd)
	if modes[ssh.TTY_OP_ISPEED] != baud || modes[ssh.TTY_OP_OSPEED] != baud {
		t.Fatalf("terminal speed %d/%d, want %d", modes[ssh.TTY_OP_ISPEED], modes[ssh.TTY_OP_OSPEED], baud)
	}
	raw, err := term.MakeRaw(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Restore(fd, raw)
	modes = terminalModes(fd)
	if modes[ssh.ICANON] != 0 || modes[ssh.ECHO] != 0 || modes[ssh.ISIG] != 0 || modes[ssh.OPOST] != 0 {
		t.Fatal("raw terminal reported as cooked", modes)
	}
}

func TestTerminalModesFallBackWithoutTerminal(t *testing.T) {
	modes := terminalModes(-1)
	if modes[ssh.ECHO] != 1 {
		t.Fatal("echo disabled without a terminal")
	}
	if modes[ssh.TTY_OP_ISPEED] != defaultTermiosSpeed || modes[ssh.TTY_OP_OSPEED] != defaultTermiosSpeed {
		t.Fatal("unexpected fallback speed", modes[ssh.TTY_OP_ISPEED], modes[ssh.TTY_OP_OSPEED])
	}
}
