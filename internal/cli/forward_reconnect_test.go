package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/forward"
	"github.com/benenen/ah/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func TestRetryForwardCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := retryForwardWithDelay(ctx, time.Millisecond, time.Millisecond, func() error {
		attempts++
		if attempts == 2 {
			cancel()
		}
		return errors.New("offline")
	})
	if !errors.Is(err, context.Canceled) || attempts != 2 {
		t.Fatal(attempts, err)
	}
}

func TestForwardReconnectRetainsIDAndStop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartForwardSSH(t, func(ch ssh.NewChannel) {
		channel, requests, err := ch.Accept()
		if err != nil {
			return
		}
		defer channel.Close()
		go ssh.DiscardRequests(requests)
		_, _ = io.Copy(channel, channel)
	})
	p := filepath.Join(dir, "config")
	if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error { m["fixture"] = server.Connection; return nil }); err != nil {
		t.Fatal(err)
	}
	record, err := forward.Allocate(forward.Local, "fixture", "127.0.0.1:0", "target:80")
	if err != nil {
		t.Fatal(err)
	}
	record.Reconnect = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := New()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetContext(ctx)
	a := &app{configPath: p, knownHosts: server.KnownHosts, timeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- a.runForward(cmd, record, nil, nil) }()
	waitEcho := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			r, err := forward.Read(record.ID)
			if err == nil && r.Status == "running" {
				c, err := net.DialTimeout("tcp", r.Listen, 50*time.Millisecond)
				if err == nil {
					_ = c.SetDeadline(time.Now().Add(100 * time.Millisecond))
					_, werr := c.Write([]byte("ok"))
					b := make([]byte, 2)
					_, rerr := io.ReadFull(c, b)
					_ = c.Close()
					if werr == nil && rerr == nil && string(b) == "ok" {
						return
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("forward never became usable")
	}
	waitEcho()
	server.DisconnectClients()
	waitEcho()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := forward.Stop(stopCtx, record.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop failed during reconnect")
	}
	records, err := forward.List(context.Background())
	if err != nil || len(records) != 1 || records[0].ID != record.ID || records[0].Status != "stopped" {
		t.Fatal(records, err)
	}
}

func TestRestartValidatesJumpRouteBeforeStopping(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	p := filepath.Join(dir, "config")
	if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error {
		m["cycle"] = config.Connection{Host: "example.com", Port: 22, User: "alice", Jump: "cycle"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r, err := forward.Allocate(forward.Local, "cycle", "127.0.0.1:12345", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{}, 1)
	reg, err := forward.RegisterStarting(r, func() { stopped <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close(nil)
	if err := reg.Running(); err != nil {
		t.Fatal(err)
	}
	_, err = execute(t, "--config", p, "forward", "restart", r.ID)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatal("invalid jump route accepted", err)
	}
	select {
	case <-stopped:
		t.Fatal("invalid replacement stopped the original forward")
	default:
	}
}
