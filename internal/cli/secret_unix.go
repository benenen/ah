//go:build darwin || linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// maxHostAnswer bounds a yes/no host key confirmation.
const maxHostAnswer = 64

// pollByte reads one byte from a polled terminal, honoring ctx cancellation so
// a reader never outlives the caller and competes with the next session.
func pollByte(ctx context.Context, fd int) (byte, error) {
	var b [1]byte
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(fds, 50)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return 0, io.EOF
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		n, err := unix.Read(fd, b[:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, io.EOF
		}
		return b[0], nil
	}
}

// readSecret polls the caller's terminal so cancellation never leaves a reader
// behind competing with the next shell session. Raw mode is always restored.
func readSecret(ctx context.Context, input *os.File, out io.Writer, prompt string) (secret []byte, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	fd := int(input.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, term.Restore(fd, state))
		_, writeErr := fmt.Fprintln(out)
		err = errors.Join(err, writeErr)
		if err != nil {
			clear(secret)
			secret = nil
		}
	}()
	if _, err = fmt.Fprint(out, prompt); err != nil {
		return nil, err
	}
	for {
		b, err := pollByte(ctx, fd)
		if err != nil {
			return secret, err
		}
		switch b {
		case '\r', '\n':
			return secret, nil
		case 3:
			return secret, context.Canceled
		case 4:
			return secret, io.EOF
		case 8, 127:
			if len(secret) > 0 {
				_, size := utf8.DecodeLastRune(secret)
				clear(secret[len(secret)-size:])
				secret = secret[:len(secret)-size]
			}
		case 21:
			clear(secret)
			secret = secret[:0]
		default:
			if b >= 32 {
				if len(secret) >= 4096 {
					return secret, fmt.Errorf("secret exceeds 4096 bytes")
				}
				secret = append(secret, b)
			}
		}
	}
}

// confirmHost asks before saving a previously unknown host key. It reads in
// canonical mode, so the answer stays visible and any input typed ahead remains
// available to the session that follows.
func confirmHost(ctx context.Context, input *os.File, out io.Writer, host, fingerprint string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(out, "The authenticity of host %s can't be established.\nKey fingerprint is %s.\nAre you sure you want to continue connecting (yes/no)? ", host, fingerprint); err != nil {
		return false, err
	}
	for {
		answer, err := readLine(ctx, input)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(string(answer))) {
		case "yes", "y":
			return true, nil
		case "no", "n":
			return false, nil
		}
		if _, err := fmt.Fprint(out, "Please type 'yes' or 'no': "); err != nil {
			return false, err
		}
	}
}

// readLine reads one terminal line in canonical mode, honoring cancellation.
func readLine(ctx context.Context, input *os.File) ([]byte, error) {
	fd := int(input.Fd())
	var line []byte
	for {
		b, err := pollByte(ctx, fd)
		if err != nil {
			return nil, err
		}
		if b == '\r' || b == '\n' {
			return line, nil
		}
		if b == 3 { // Ctrl-C, in case the terminal is not in canonical mode.
			return nil, context.Canceled
		}
		if len(line) < maxHostAnswer {
			line = append(line, b)
		}
	}
}
