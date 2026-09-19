package sshclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// ForwardLocal serves a TCP listener through SSH until canceled or an error
// occurs. It owns and closes both the listener and this SSH client.
func (c *Client) ForwardLocal(ctx context.Context, listener net.Listener, target string) error {
	ctx, cancel := context.WithCancelCause(ctx)
	var workers sync.WaitGroup
	stop := context.AfterFunc(ctx, func() {
		_ = listener.Close()
		_ = c.Close()
	})
	defer func() {
		cancel(context.Canceled)
		stop()
		_ = listener.Close()
		_ = c.Close()
		workers.Wait()
	}()
	workers.Go(func() {
		err := c.client.Wait()
		if err == nil {
			err = io.EOF
		}
		cancel(fmt.Errorf("SSH connection closed: %w", err))
	})
	for {
		local, err := listener.Accept()
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			return fmt.Errorf("accept forward connection: %w", err)
		}
		workers.Go(func() {
			defer func() { _ = local.Close() }()
			// Closing the transport on timeout also releases a pending SSH
			// channel-open request; DialContext alone cannot interrupt it.
			dialCtx, dialCancel := context.WithTimeout(ctx, c.timeout)
			stopDial := context.AfterFunc(dialCtx, func() {
				cancel(fmt.Errorf("connect forwarding target: %w", dialCtx.Err()))
			})
			remote, err := c.client.Dial("tcp", target)
			stopped := stopDial()
			dialCancel()
			if err != nil {
				cancel(fmt.Errorf("connect forwarding target %s: %w", target, err))
				return
			}
			defer func() { _ = remote.Close() }()
			if !stopped || ctx.Err() != nil {
				return
			}
			if err := relayForward(ctx, local, remote); err != nil {
				cancel(fmt.Errorf("forward data: %w", err))
			}
		})
	}
}

func relayForward(ctx context.Context, a, b net.Conn) error {
	stop := context.AfterFunc(ctx, func() { _ = a.Close(); _ = b.Close() })
	defer stop()
	results := make(chan error, 2)
	copyHalf := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if err == nil {
			if half, ok := dst.(interface{ CloseWrite() error }); ok {
				err = half.CloseWrite()
			}
		}
		if err != nil {
			_ = a.Close()
			_ = b.Close()
		}
		results <- err
	}
	go copyHalf(a, b)
	go copyHalf(b, a)
	return errors.Join(<-results, <-results)
}
