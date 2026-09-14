//go:build darwin || linux

package sshclient

import (
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Shell runs a remote shell, requesting a PTY only when stdin is a terminal.
// It consumes the connection and closes its transport when the shell ends.
func (c *Client) Shell(stdin *os.File, stdout, stderr io.Writer) error {
	return c.runSession("", true, nil, stdin, stdout, stderr)
}

// ShellCommand runs command under a PTY (like an interactive login) instead of
// the default login shell, injecting stdinPrefix ahead of the caller's input.
// It is used to launch sudo for an escalated interactive session.
func (c *Client) ShellCommand(command string, stdinPrefix []byte, stdin *os.File, stdout, stderr io.Writer) error {
	return c.runSession(command, true, stdinPrefix, stdin, stdout, stderr)
}

// Exec sends a command to the remote shell without requesting a PTY.
// Like Shell, it consumes and closes the connection and stops pending input reads.
func (c *Client) Exec(command string, stdin *os.File, stdout, stderr io.Writer) error {
	return c.runSession(command, false, nil, stdin, stdout, stderr)
}

// ExecPrefixed behaves like Exec but writes stdinPrefix to the remote stdin
// before forwarding the caller's input, feeding a sudo -S password line.
func (c *Client) ExecPrefixed(command string, stdinPrefix []byte, stdin *os.File, stdout, stderr io.Writer) error {
	return c.runSession(command, false, stdinPrefix, stdin, stdout, stderr)
}

func (c *Client) runSession(command string, interactive bool, stdinPrefix []byte, stdin *os.File, stdout, stderr io.Writer) (result error) {
	session, err := c.client.NewSession()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close(); _ = session.Close() }()
	if stdin != nil {
		pipe, err := session.StdinPipe()
		if err != nil {
			return err
		}
		stopInput := make(chan struct{})
		inputDone := make(chan struct{})
		defer func() { close(stopInput); _ = c.Close(); <-inputDone }()
		go func() {
			defer close(inputDone)
			// Feed sudo -S its password line before forwarding the caller's stdin,
			// so the escalated command inherits the remaining input unchanged.
			if len(stdinPrefix) > 0 {
				if _, err := pipe.Write(stdinPrefix); err != nil {
					_ = pipe.Close()
					return
				}
			}
			_, _ = io.Copy(pipe, &shellInput{file: stdin, stop: stopInput})
			_ = pipe.Close()
		}()
	}
	session.Stdout = stdout
	session.Stderr = stderr
	if interactive && stdin != nil && term.IsTerminal(int(stdin.Fd())) {
		fd := int(stdin.Fd())
		width, height, err := term.GetSize(fd)
		if err != nil {
			return err
		}
		terminal := os.Getenv("TERM")
		if terminal == "" {
			terminal = "xterm-256color"
		}
		if err = session.RequestPty(terminal, height, width, ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}); err != nil {
			return err
		}
		state, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, term.Restore(fd, state)) }()
		changes := make(chan os.Signal, 1)
		signal.Notify(changes, syscall.SIGWINCH)
		done := make(chan struct{})
		exited := make(chan struct{})
		defer func() { signal.Stop(changes); close(done); _ = c.Close(); <-exited }()
		go func() {
			defer close(exited)
			for {
				select {
				case <-done:
					return
				case <-changes:
					w, h, e := term.GetSize(fd)
					if e == nil {
						session.WindowChange(h, w)
					}
				}
			}
		}()
	}
	if command == "" {
		err = session.Shell()
	} else {
		err = session.Start(command)
	}
	if err != nil {
		return err
	}
	return session.Wait()
}

// Polling allows shell teardown to stop input without closing the caller's terminal.
type shellInput struct {
	file *os.File
	stop <-chan struct{}
}

func (r *shellInput) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		select {
		case <-r.stop:
			return 0, io.EOF
		default:
		}
		fds := []unix.PollFd{{Fd: int32(r.file.Fd()), Events: unix.POLLIN}}
		count, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if count == 0 {
			continue
		}
		n, err := unix.Read(int(r.file.Fd()), p)
		if err == unix.EINTR {
			continue
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		return n, err
	}
}
