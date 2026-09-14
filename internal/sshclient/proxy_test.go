package sshclient

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// startSOCKS5 runs a minimal no-auth SOCKS5 CONNECT server that forwards to the
// requested target, letting the test confirm dialTCP traverses a real proxy.
func startSOCKS5(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSOCKS5(conn)
		}
	}()
	return ln.Addr().String()
}

func serveSOCKS5(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 512)
	// Greeting: VER, NMETHODS, METHODS...
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	n := int(buf[1])
	if _, err := io.ReadFull(conn, buf[:n]); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	// Request: VER, CMD, RSV, ATYP, ADDR, PORT.
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return
	}
	var host string
	switch buf[3] {
	case 0x01:
		if _, err := io.ReadFull(conn, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03:
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			return
		}
		l := int(buf[0])
		if _, err := io.ReadFull(conn, buf[:l]); err != nil {
			return
		}
		host = string(buf[:l])
	default:
		return
	}
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	port := int(buf[0])<<8 | int(buf[1])
	target, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(target, conn); done <- struct{}{} }()
	go func() { io.Copy(conn, target); done <- struct{}{} }()
	<-done
}

func TestDialTCPThroughSOCKS5(t *testing.T) {
	// Target service that greets each connection with a fixed banner.
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("HELLO"))
			c.Close()
		}
	}()
	socksAddr := startSOCKS5(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dialTCP(ctx, target.Addr().String(), Options{Proxy: "socks5://" + socksAddr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial through proxy: %v", err)
	}
	defer conn.Close()
	got := make([]byte, 5)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "HELLO" {
		t.Fatalf("banner = %q, want HELLO", got)
	}
}

func TestDialTCPDirectNoProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Write([]byte("OK"))
			c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialTCP(ctx, ln.Addr().String(), Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got := make([]byte, 2)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "OK" {
		t.Fatalf("got %q", got)
	}
}

func TestDialTCPInvalidProxy(t *testing.T) {
	ctx := context.Background()
	if _, err := dialTCP(ctx, "127.0.0.1:22", Options{Proxy: "://bad", Timeout: time.Second}); err == nil {
		t.Fatal("expected error for malformed proxy URL")
	}
}

func TestDialTCPProxyUnreachable(t *testing.T) {
	// Reserve a port then close it so the proxy connect is refused promptly.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := dialTCP(ctx, "10.0.0.1:22", Options{Proxy: "socks5://" + addr, Timeout: time.Second}); err == nil {
		t.Fatal("expected error when proxy is unreachable")
	} else if errors.Is(err, context.DeadlineExceeded) {
		t.Skip("environment delayed the refused connect past the deadline")
	}
}
