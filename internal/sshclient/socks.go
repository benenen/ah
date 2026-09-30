package sshclient

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// SOCKS5 reply codes from RFC 1928.
const (
	socksSucceeded          = 0x00
	socksGeneralFailure     = 0x01
	socksConnectionRefused  = 0x05
	socksTTLExpired         = 0x06
	socksCommandUnsupported = 0x07
	socksAddressUnsupported = 0x08
)

// serveSOCKS answers one SOCKS5 CONNECT request without authentication and
// relays it through a direct-tcpip channel. It always consumes local.
func (c *Client) serveSOCKS(ctx context.Context, local net.Conn) error {
	if err := local.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return err
	}
	target, err := readSOCKSRequest(local)
	if err != nil {
		return fmt.Errorf("SOCKS request from %s: %w", local.RemoteAddr(), err)
	}
	// Answer a stalled channel open and release the client; the pending request
	// itself ends when the server answers or the SSH connection closes.
	dialCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	stop := context.AfterFunc(dialCtx, func() {
		_ = writeSOCKSReply(local, socksTTLExpired)
		_ = local.Close()
	})
	remote, err := c.client.Dial("tcp", target)
	if !stop() {
		if remote != nil {
			_ = remote.Close()
		}
		return fmt.Errorf("SOCKS connect %s: %w", target, dialCtx.Err())
	}
	if err != nil {
		code := byte(socksGeneralFailure)
		var rejected *ssh.OpenChannelError
		if errors.As(err, &rejected) && rejected.Reason == ssh.ConnectionFailed {
			code = socksConnectionRefused
		}
		return errors.Join(fmt.Errorf("SOCKS connect %s: %w", target, err), writeSOCKSReply(local, code))
	}
	defer func() { _ = remote.Close() }()
	if err := writeSOCKSReply(local, socksSucceeded); err != nil {
		return err
	}
	if err := local.SetDeadline(time.Time{}); err != nil {
		return err
	}
	if err := relayForward(ctx, local, remote); err != nil {
		return fmt.Errorf("SOCKS relay %s: %w", target, err)
	}
	return nil
}

// readSOCKSRequest negotiates "no authentication" and returns the CONNECT
// target as HOST:PORT. Unsupported requests are answered before returning.
func readSOCKSRequest(conn net.Conn) (string, error) {
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return "", err
	}
	if header[0] != 5 {
		return "", fmt.Errorf("unsupported SOCKS version %d", header[0])
	}
	methods := make([]byte, header[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", err
	}
	noAuth := false
	for _, m := range methods {
		noAuth = noAuth || m == 0
	}
	if !noAuth {
		_, err := conn.Write([]byte{5, 0xff})
		return "", errors.Join(errors.New("client does not offer no-authentication"), err)
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	var request [4]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil {
		return "", err
	}
	if request[0] != 5 {
		return "", fmt.Errorf("unsupported SOCKS version %d", request[0])
	}
	var host string
	switch request[3] {
	case 1, 4:
		ip := make(net.IP, 4)
		if request[3] == 4 {
			ip = make(net.IP, 16)
		}
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", err
		}
		host = ip.String()
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return "", err
		}
		name := make([]byte, length[0])
		if _, err := io.ReadFull(conn, name); err != nil {
			return "", err
		}
		host = string(name)
	default:
		return "", errors.Join(fmt.Errorf("unsupported SOCKS address type %d", request[3]), writeSOCKSReply(conn, socksAddressUnsupported))
	}
	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return "", err
	}
	if request[1] != 1 {
		return "", errors.Join(fmt.Errorf("unsupported SOCKS command %d", request[1]), writeSOCKSReply(conn, socksCommandUnsupported))
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), nil
}

// writeSOCKSReply sends a reply with an unspecified bound address.
func writeSOCKSReply(conn net.Conn, code byte) error {
	_, err := conn.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}
