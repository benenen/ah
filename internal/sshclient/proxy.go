package sshclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/benenen/ah/internal/config"
	"golang.org/x/net/proxy"
)

// dialTransport nests context-aware dialers: each proxy connects through all
// earlier hops, so only the first proxy is resolved and dialed locally.
func dialTransport(ctx context.Context, address string, chain []string) (net.Conn, error) {
	var forward proxy.Dialer = &net.Dialer{}
	for i, value := range chain {
		endpoint, err := config.ProxyAddress(value)
		if err != nil {
			return nil, fmt.Errorf("proxy hop %d: %w", i+1, err)
		}
		var auth *proxy.Auth
		if strings.Contains(value, "://") {
			u, _ := url.Parse(value) // ProxyAddress already validated the URL.
			if u.User != nil {
				password, _ := u.User.Password()
				auth = &proxy.Auth{User: u.User.Username(), Password: password}
			}
		}
		forward, err = proxy.SOCKS5("tcp", endpoint, auth, forward)
		if err != nil {
			return nil, fmt.Errorf("proxy hop %d: %w", i+1, err)
		}
	}
	dialer, ok := forward.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("proxy dialer does not support cancellation")
	}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// x/net applies the context deadline to the socket as well. Its I/O
		// timer can fire before the context timer goroutine updates ctx.Err().
		var timeout net.Error
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) && errors.As(err, &timeout) && timeout.Timeout() {
			return nil, errors.Join(context.DeadlineExceeded, err)
		}
		return nil, err
	}
	if len(chain) > 0 {
		// The underlying TCP peer is the first proxy, not the SSH host. Never use
		// that proxy's address as an alternate identity during known_hosts checks.
		conn = &destinationConn{Conn: conn, target: destinationAddr(address)}
	}
	return conn, nil
}

type destinationConn struct {
	net.Conn
	target net.Addr
}

func (c *destinationConn) RemoteAddr() net.Addr { return c.target }

type destinationAddr string

func (a destinationAddr) Network() string { return "tcp" }
func (a destinationAddr) String() string  { return string(a) }
