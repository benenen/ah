//go:build darwin || linux

package sshclient

import (
	"context"
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
	return c.runSession("", true, stdin, stdout, stderr)
}

// Exec sends a command to the remote shell without requesting a PTY.
// Like Shell, it consumes and closes the connection and stops pending input reads.
func (c *Client) Exec(command string, stdin *os.File, stdout, stderr io.Writer) error {
	return c.runSession(command, false, stdin, stdout, stderr)
}

func (c *Client) runSession(command string, interactive bool, stdin *os.File, stdout, stderr io.Writer) (result error) {
	setupCtx := c.ctx
	finishSetup := func() {}
	if c.sudo {
		var cancel context.CancelFunc
		setupCtx, cancel = context.WithTimeout(c.ctx, c.timeout)
		stop := context.AfterFunc(setupCtx, func() { c.Close() })
		finishSetup = func() { stop(); cancel() }
		defer finishSetup()
	}
	session, err := c.client.NewSession()
	if err != nil {
		if c.sudo && setupCtx.Err() != nil {
			return setupCtx.Err()
		}
		return err
	}
	defer func() { _ = c.Close(); _ = session.Close() }()
	if stdin != nil && !c.sudo {
		pipe, err := session.StdinPipe()
		if err != nil {
			return err
		}
		stopInput := make(chan struct{})
		inputDone := make(chan struct{})
		defer func() { close(stopInput); _ = c.Close(); <-inputDone }()
		go func() {
			defer close(inputDone)
			_, _ = io.Copy(pipe, &shellInput{file: stdin, stop: stopInput})
			_ = pipe.Close()
		}()
	}
	if !c.sudo {
		session.Stdout = stdout
		session.Stderr = stderr
	}
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
		echo := uint32(1)
		if c.sudo {
			echo = 0
		}
		if err = session.RequestPty(terminal, height, width, ssh.TerminalModes{ssh.ECHO: echo, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}); err != nil {
			if c.sudo && setupCtx.Err() != nil {
				return setupCtx.Err()
			}
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
	if c.sudo {
		return c.runSudoSession(setupCtx, finishSetup, session, command, interactive, stdin, stdout, stderr)
	}
	if interactive {
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

func (c *Client) runSudoSession(ctx context.Context, finishSetup func(), session *ssh.Session, command string, interactive bool, stdin *os.File, stdout, stderr io.Writer) error {
	action := "exec /bin/sh -c " + shellQuote(command)
	if interactive {
		action = `cd "$HOME" && exec /bin/sh -l`
	}
	stream, err := c.startSudo(ctx, session, action, stderr, interactive && stdin != nil && term.IsTerminal(int(stdin.Fd())))
	finishSetup()
	if err != nil {
		return err
	}
	outputDone := make(chan error, 1)
	go func() { _, err := io.Copy(stdout, stream.output); outputDone <- err }()
	stopInput, inputDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(inputDone)
		if stdin != nil {
			io.Copy(stream.input, &shellInput{file: stdin, stop: stopInput})
		}
		stream.input.Close()
	}()
	err = session.Wait()
	close(stopInput)
	c.Close()
	<-inputDone
	outputErr := <-outputDone
	stream.prompts.flush()
	return errors.Join(err, outputErr, stream.prompts.failure())
}
