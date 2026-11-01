package sshclient

import (
	"context"
	"net"
	"time"

	"github.com/benenen/ah/internal/config"
)

// DialVia takes ownership of parent on both success and failure. Each server
// is authenticated independently; the destination host key uses its own address.
func DialVia(ctx context.Context, c config.Connection, opts Options, parent *Client) (*Client, error) {
	opts.parent = parent
	opts.transport = func(dialCtx context.Context, address string) (net.Conn, error) {
		stop := context.AfterFunc(dialCtx, func() { _ = parent.Close() })
		defer stop()
		conn, err := parent.client.Dial("tcp", address)
		if err != nil {
			return nil, err
		}
		if dialCtx.Err() != nil {
			_ = conn.Close()
			return nil, dialCtx.Err()
		}
		return &jumpConn{Conn: conn, target: destinationAddr(address)}, nil
	}
	client, err := Dial(ctx, c, opts)
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	return client, nil
}

// SSH channels do not implement socket deadlines. Dial's handshake timer closes
// this channel on expiry, and cancellation closes the whole owned chain.
type jumpConn struct {
	net.Conn
	target net.Addr
}

func (c *jumpConn) RemoteAddr() net.Addr             { return c.target }
func (c *jumpConn) SetDeadline(time.Time) error      { return nil }
func (c *jumpConn) SetReadDeadline(time.Time) error  { return nil }
func (c *jumpConn) SetWriteDeadline(time.Time) error { return nil }
