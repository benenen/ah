//go:build darwin || linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

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
	var b [1]byte
	for {
		if err = ctx.Err(); err != nil {
			return secret, err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err = unix.Poll(fds, 50)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return secret, err
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return secret, io.EOF
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		var n int
		n, err = unix.Read(fd, b[:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return secret, err
		}
		if n == 0 {
			return secret, io.EOF
		}
		switch b[0] {
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
			if b[0] >= 32 {
				if len(secret) >= 4096 {
					return secret, fmt.Errorf("secret exceeds 4096 bytes")
				}
				secret = append(secret, b[0])
			}
		}
	}
}
