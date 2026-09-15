//go:build darwin || linux

package sshclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/benenen/ah/internal/config"
	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestShellInputCanStopWithoutClosingStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	stop := make(chan struct{})
	result := make(chan error, 1)
	go func() { _, err := (&shellInput{file: r, stop: stop}).Read(make([]byte, 1)); result <- err }()
	close(stop)
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("input did not stop")
	}
	if _, err = w.Write([]byte("x")); err != nil {
		t.Fatal("stdin was closed", err)
	}
}

func TestShellSetupFailureUnblocksInputWrite(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		server, chans, requests, err := ssh.NewServerConn(raw, cfg)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		ch, ok := <-chans
		if !ok {
			return
		}
		channel, reqs, err := ch.Accept()
		if err != nil {
			return
		}
		defer channel.Close()
		for req := range reqs {
			if req.Type == "shell" {
				time.Sleep(200 * time.Millisecond)
			}
			_ = req.Reply(false, nil)
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := Dial(ctx, config.Connection{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "test"}, Options{KnownHosts: filepath.Join(t.TempDir(), "hosts"), TrustHost: func(string, string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	input, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err = input.Truncate(8 << 20); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err = client.Shell(input, io.Discard, io.Discard); err == nil {
		t.Fatal("shell refusal was accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("shell setup failure left input write blocked")
	}
	client.Close()
	<-finished
}

func TestShellPTYRestoresTerminal(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	serverResult := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer raw.Close()
		server, chans, requests, err := ssh.NewServerConn(raw, cfg)
		if err != nil {
			serverResult <- err
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		ch, ok := <-chans
		if !ok {
			serverResult <- fmt.Errorf("no session channel")
			return
		}
		channel, reqs, err := ch.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer channel.Close()
		ptyRequested := false
		for req := range reqs {
			switch req.Type {
			case "pty-req":
				ptyRequested = true
				_ = req.Reply(true, nil)
			case "shell":
				if !ptyRequested {
					serverResult <- fmt.Errorf("shell started without PTY")
					return
				}
				_ = req.Reply(true, nil)
				_, err = channel.Write([]byte("hello from controlled shell\n"))
				if err != nil {
					serverResult <- err
					return
				}
				_, err = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				serverResult <- err
				return
			default:
				_ = req.Reply(false, nil)
			}
		}
		serverResult <- fmt.Errorf("shell was not requested")
	}()
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer terminal.Close()
	if err = pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := Dial(ctx, config.Connection{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "test"}, Options{KnownHosts: filepath.Join(t.TempDir(), "hosts"), TrustHost: func(string, string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var output bytes.Buffer
	if err = client.Shell(terminal, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if output.String() != "hello from controlled shell\n" {
		t.Fatalf("unexpected shell output %q", output.String())
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("terminal state was not restored")
	}
	if err = <-serverResult; err != nil {
		t.Fatal(err)
	}
}

// requestedTerm reports the TERM a shell sends with pty-req.
func requestedTerm(t *testing.T, connectionTerm, optionTerm string) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	terms := make(chan string, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		server, chans, requests, err := ssh.NewServerConn(raw, cfg)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		ch, ok := <-chans
		if !ok {
			return
		}
		channel, reqs, err := ch.Accept()
		if err != nil {
			return
		}
		defer channel.Close()
		for req := range reqs {
			switch req.Type {
			case "pty-req":
				// RFC 4254 pty-req starts with the TERM string.
				if len(req.Payload) >= 4 {
					size := binary.BigEndian.Uint32(req.Payload)
					if int(size) <= len(req.Payload)-4 {
						terms <- string(req.Payload[4 : 4+size])
					}
				}
				_ = req.Reply(true, nil)
			case "shell":
				_ = req.Reply(true, nil)
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			default:
				_ = req.Reply(false, nil)
			}
		}
	}()
	master, terminal, err := pty.Open()
	if err != nil {
		t.Skip("pty unavailable:", err)
	}
	defer master.Close()
	defer terminal.Close()
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := config.Connection{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "test", Term: connectionTerm}
	opts := Options{KnownHosts: filepath.Join(t.TempDir(), "hosts"), Term: optionTerm, TrustHost: func(string, string) (bool, error) { return true, nil }}
	client, err := Dial(ctx, c, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.Shell(terminal, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-terms:
		return value
	default:
		t.Fatal("no pty-req was sent")
		return ""
	}
}

// A server without the local terminfo entry degrades line editing, so the saved
// name must win over $TERM and an explicit override over both.
func TestShellSendsConfiguredTerm(t *testing.T) {
	t.Setenv("TERM", "xterm-ghostty")
	for _, tc := range []struct {
		name       string
		connection string
		option     string
		want       string
	}{
		{name: "environment", want: "xterm-ghostty"},
		{name: "connection", connection: "screen-256color", want: "screen-256color"},
		{name: "override", connection: "screen-256color", option: "xterm-256color", want: "xterm-256color"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestedTerm(t, tc.connection, tc.option); got != tc.want {
				t.Fatalf("term %q, want %q", got, tc.want)
			}
		})
	}
}
