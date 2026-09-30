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
// occurs. It owns and closes both the listener and this SSH client. A failed
// target connection or relay ends the forward.
func (c *Client) ForwardLocal(ctx context.Context, listener net.Listener, target string) error {
	return c.serveForward(ctx, listener, func(ctx context.Context, fail context.CancelCauseFunc, local net.Conn) {
		remote, err := c.dialRemote(ctx, fail, target)
		if err != nil {
			fail(err)
			return
		}
		defer func() { _ = remote.Close() }()
		if err := relayForward(ctx, local, remote); err != nil {
			fail(fmt.Errorf("forward data: %w", err))
		}
	})
}

// ListenRemote asks the SSH server to listen on address (tcpip-forward). The
// request cannot be interrupted on its own, so a timeout or cancellation closes
// the SSH client.
func (c *Client) ListenRemote(ctx context.Context, address string) (net.Listener, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	listener, err := c.client.Listen("tcp", address)
	if !stop() {
		if listener != nil {
			_ = listener.Close()
		}
		return nil, fmt.Errorf("remote listen %s: %w", address, ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("remote listen %s: %w", address, err)
	}
	return listener, nil
}

// ForwardRemote relays connections accepted by a ListenRemote listener to a
// local target until canceled or an error occurs. It owns and closes both the
// listener and this SSH client. A failed target connection or relay ends the
// forward, matching ForwardLocal.
func (c *Client) ForwardRemote(ctx context.Context, listener net.Listener, target string) error {
	return c.serveForward(ctx, listener, func(ctx context.Context, fail context.CancelCauseFunc, remote net.Conn) {
		local, err := (&net.Dialer{Timeout: c.timeout}).DialContext(ctx, "tcp", target)
		if err != nil {
			fail(fmt.Errorf("connect forwarding target %s: %w", target, err))
			return
		}
		defer func() { _ = local.Close() }()
		if err := relayForward(ctx, remote, local); err != nil {
			fail(fmt.Errorf("forward data: %w", err))
		}
	})
}

// ForwardDynamic serves a SOCKS5 proxy on listener whose CONNECT requests are
// dialed by the SSH server, so names resolve on the server side. Unlike the
// fixed-target forwards, a failed request or relay only closes that client
// connection and is passed to report; only cancellation or losing SSH ends it.
// It owns and closes both the listener and this SSH client.
func (c *Client) ForwardDynamic(ctx context.Context, listener net.Listener, report func(error)) error {
	return c.serveForward(ctx, listener, func(ctx context.Context, _ context.CancelCauseFunc, local net.Conn) {
		if err := c.serveSOCKS(ctx, local); err != nil && ctx.Err() == nil && report != nil {
			report(err)
		}
	})
}

// serveForward accepts connections until canceled, the SSH connection closes
// or handle fails the forward, then waits for every handler to return.
func (c *Client) serveForward(ctx context.Context, listener net.Listener, handle func(context.Context, context.CancelCauseFunc, net.Conn)) error {
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
		conn, err := listener.Accept()
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			return fmt.Errorf("accept forward connection: %w", err)
		}
		workers.Go(func() {
			defer func() { _ = conn.Close() }()
			handle(ctx, cancel, conn)
		})
	}
}

// dialRemote opens a direct-tcpip channel. Only closing the transport releases
// a pending channel-open request, so a timeout fails the whole forward.
func (c *Client) dialRemote(ctx context.Context, fail context.CancelCauseFunc, target string) (net.Conn, error) {
	dialCtx, dialCancel := context.WithTimeout(ctx, c.timeout)
	defer dialCancel()
	stopDial := context.AfterFunc(dialCtx, func() {
		fail(fmt.Errorf("connect forwarding target: %w", dialCtx.Err()))
	})
	remote, err := c.client.Dial("tcp", target)
	stopped := stopDial()
	if err != nil {
		return nil, fmt.Errorf("connect forwarding target %s: %w", target, err)
	}
	if !stopped || ctx.Err() != nil {
		_ = remote.Close()
		return nil, context.Cause(ctx)
	}
	return remote, nil
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
