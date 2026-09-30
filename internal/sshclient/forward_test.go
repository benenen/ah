package sshclient_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benenen/ah/internal/sshclient"
	"github.com/benenen/ah/internal/testutil"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

func TestForwardLocal(t *testing.T) {
	f := testutil.StartForwardSSH(t, func(ch ssh.NewChannel) {
		var address struct {
			Host       string
			Port       uint32
			Origin     string
			OriginPort uint32
		}
		if err := ssh.Unmarshal(ch.ExtraData(), &address); err != nil || address.Host != "target.internal" || address.Port != 80 {
			_ = ch.Reject(ssh.ConnectionFailed, "wrong target")
			return
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			return
		}
		defer func() { _ = channel.Close() }()
		go ssh.DiscardRequests(requests)
		// Send a response only after EOF to verify TCP/SSH half-close.
		data, err := io.ReadAll(channel)
		if err == nil {
			_, _ = channel.Write(append([]byte("reply:"), data...))
		}
		_ = channel.CloseWrite()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := sshclient.Dial(ctx, f.Connection, sshclient.Options{KnownHosts: f.KnownHosts})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan error, 1)
	go func() { done <- c.ForwardLocal(ctx, listener, "target.internal:80") }()
	var clients sync.WaitGroup
	for i := 0; i < 4; i++ {
		clients.Go(func() {
			conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := conn.Write([]byte("hello")); err != nil {
				t.Error(err)
				return
			}
			_ = conn.(*net.TCPConn).CloseWrite()
			data, err := io.ReadAll(conn)
			if err != nil || string(data) != "reply:hello" {
				t.Errorf("response %q: %v", data, err)
			}
		})
	}
	clients.Wait()
	// Leave an active, idle connection when canceling.
	idle, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idle.Close() }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forward did not stop")
	}
	if conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("listener remained open")
	}
}

func TestForwardSSHDisconnect(t *testing.T) {
	f := testutil.StartSSH(t)
	c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: f.KnownHosts})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan error, 1)
	go func() { done <- c.ForwardLocal(context.Background(), listener, "localhost:80") }()
	f.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("disconnect succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not stop listener")
	}
}

func TestForwardTargetRejected(t *testing.T) {
	f := testutil.StartSSH(t) // rejects direct-tcpip
	c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: f.KnownHosts})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan error, 1)
	go func() { done <- c.ForwardLocal(context.Background(), listener, "localhost:80") }()
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("rejection succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("rejection did not stop forwarding")
	}
}

func TestForwardTargetTimeout(t *testing.T) {
	release := make(chan struct{})
	f := testutil.StartForwardSSH(t, func(ch ssh.NewChannel) {
		// Withhold the channel-open response until the client times out.
		<-release
		_ = ch.Reject(ssh.ConnectionFailed, "test finished")
	})
	defer close(release)
	c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: f.KnownHosts, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan error, 1)
	go func() { done <- c.ForwardLocal(context.Background(), listener, "localhost:80") }()
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending SSH channel did not unblock")
	}
}

// echoTargets accepts direct-tcpip channels only for the listed HOST:PORT
// targets and answers each with "reply:" plus everything read before EOF.
func echoTargets(targets ...string) func(ssh.NewChannel) {
	return func(ch ssh.NewChannel) {
		var address struct {
			Host       string
			Port       uint32
			Origin     string
			OriginPort uint32
		}
		if err := ssh.Unmarshal(ch.ExtraData(), &address); err != nil {
			_ = ch.Reject(ssh.ConnectionFailed, "bad request")
			return
		}
		allowed := false
		for _, target := range targets {
			allowed = allowed || target == net.JoinHostPort(address.Host, strconv.Itoa(int(address.Port)))
		}
		if !allowed {
			_ = ch.Reject(ssh.ConnectionFailed, "unreachable")
			return
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			return
		}
		defer func() { _ = channel.Close() }()
		go ssh.DiscardRequests(requests)
		data, err := io.ReadAll(channel)
		if err == nil {
			_, _ = channel.Write(append([]byte("reply:"), data...))
		}
		_ = channel.CloseWrite()
	}
}

func roundTrip(t *testing.T, conn net.Conn) string {
	t.Helper()
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestForwardDynamic(t *testing.T) {
	f := testutil.StartForwardSSH(t, echoTargets("app.internal:80", "10.0.0.7:8080", "[fd00::7]:443"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := sshclient.Dial(ctx, f.Connection, sshclient.Options{KnownHosts: f.KnownHosts})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var reported []error
	done := make(chan error, 1)
	go func() {
		done <- c.ForwardDynamic(ctx, listener, func(err error) { mu.Lock(); reported = append(reported, err); mu.Unlock() })
	}()
	socks, err := proxy.SOCKS5("tcp", listener.Addr().String(), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	// Domain, IPv4 and IPv6 targets reach the server unresolved.
	for _, target := range []string{"app.internal:80", "10.0.0.7:8080", "[fd00::7]:443"} {
		conn, err := socks.Dial("tcp", target)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		if got := roundTrip(t, conn); got != "reply:hello" {
			t.Fatalf("%s: %q", target, got)
		}
	}
	// A rejected target fails that request only; the proxy keeps serving.
	if conn, err := socks.Dial("tcp", "down.internal:80"); err == nil {
		_ = conn.Close()
		t.Fatal("rejected target connected")
	}
	conn, err := socks.Dial("tcp", "app.internal:80")
	if err != nil {
		t.Fatalf("proxy stopped after a failed request: %v", err)
	}
	if got := roundTrip(t, conn); got != "reply:hello" {
		t.Fatalf("after failure: %q", got)
	}
	// A non-SOCKS client is dropped without ending the proxy.
	raw, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = raw.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	_ = raw.SetReadDeadline(time.Now().Add(3 * time.Second))
	// Closing with unread input resets instead of sending EOF; only a timeout means it stayed open.
	var timeout net.Error
	if _, err := io.ReadAll(raw); errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("non-SOCKS client was not closed: %v", err)
	}
	_ = raw.Close()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("proxy did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 2 || !strings.Contains(reported[0].Error(), "down.internal:80") || !strings.Contains(reported[1].Error(), "unsupported SOCKS version") {
		t.Fatalf("reported: %v", reported)
	}
}

func TestForwardDynamicTargetTimeout(t *testing.T) {
	release := make(chan struct{})
	f := testutil.StartForwardSSH(t, func(ch ssh.NewChannel) {
		<-release
		_ = ch.Reject(ssh.ConnectionFailed, "test finished")
	})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := sshclient.Dial(ctx, f.Connection, sshclient.Options{KnownHosts: f.KnownHosts, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.ForwardDynamic(ctx, listener, nil) }()
	socks, err := proxy.SOCKS5("tcp", listener.Addr().String(), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if conn, err := socks.Dial("tcp", "slow.internal:80"); err == nil {
		_ = conn.Close()
		t.Fatal("stalled target connected")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("stalled channel open was not answered")
	}
	select {
	case err := <-done:
		t.Fatalf("a stalled request ended the proxy: %v", err)
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy did not stop with a pending channel open")
	}
}

func TestForwardRemote(t *testing.T) {
	f := testutil.StartRemoteForwardSSH(t, nil)
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				data, err := io.ReadAll(conn)
				if err == nil {
					_, _ = conn.Write(append([]byte("reply:"), data...))
				}
			}()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := sshclient.Dial(ctx, f.Connection, sshclient.Options{KnownHosts: f.KnownHosts})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	listener, err := c.ListenRemote(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	remoteAddress := listener.Addr().String()
	if strings.HasSuffix(remoteAddress, ":0") {
		t.Fatalf("remote port not assigned: %s", remoteAddress)
	}
	done := make(chan error, 1)
	go func() { done <- c.ForwardRemote(ctx, listener, target.Addr().String()) }()
	// The fixture server listens on this machine, so its address is reachable.
	for i := 0; i < 3; i++ {
		conn, err := net.DialTimeout("tcp", remoteAddress, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if got := roundTrip(t, conn); got != "reply:hello" {
			t.Fatalf("response %q", got)
		}
	}
	// Like a local forward, an unreachable target ends the forward.
	_ = target.Close()
	conn, err := net.DialTimeout("tcp", remoteAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "connect forwarding target") {
			t.Fatalf("target failure: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("target failure did not stop forwarding")
	}
}

func TestListenRemoteRejected(t *testing.T) {
	f := testutil.StartSSH(t) // discards global requests
	c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: f.KnownHosts})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if listener, err := c.ListenRemote(context.Background(), "127.0.0.1:9000"); err == nil {
		_ = listener.Close()
		t.Fatal("rejected tcpip-forward succeeded")
	}
}
