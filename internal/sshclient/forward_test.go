package sshclient_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/benenen/ah/internal/sshclient"
	"github.com/benenen/ah/internal/testutil"
	"golang.org/x/crypto/ssh"
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
