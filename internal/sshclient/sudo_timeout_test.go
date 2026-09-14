package sshclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Silently drop replies after exec succeeds, including channel-close replies.
// This models a connection that remains locally open after the peer disappears.
type sudoBlackholeConn struct {
	net.Conn
	drop atomic.Bool
}

func (c *sudoBlackholeConn) Write(p []byte) (int, error) {
	if c.drop.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func TestSudoInitializationTimeoutClosesUnresponsiveTransport(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	acceptedExec := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		transport := &sudoBlackholeConn{Conn: conn}
		server, channels, requests, err := ssh.NewServerConn(transport, serverConfig)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			channel, requests, err := incoming.Accept()
			if err != nil {
				return
			}
			for request := range requests {
				if request.Type == "exec" {
					if err := request.Reply(true, nil); err != nil {
						return
					}
					transport.drop.Store(true)
					close(acceptedExec)
				} else {
					request.Reply(false, nil)
				}
			}
			channel.Close()
		}
	}()
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(known, []byte(knownhosts.Line([]string{listener.Addr().String()}, signer.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := Dial(ctx, config.Connection{Host: host, Port: port, User: "test", Sudo: true}, Options{
		KnownHosts: known, Timeout: 150 * time.Millisecond, PasswordOnly: true,
		Password: func() (string, error) { return "unused", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { client.Close(); <-serverDone }()
	done := make(chan error, 1)
	go func() {
		files, err := client.SFTP()
		if files != nil {
			files.Close()
		}
		done <- err
	}()
	select {
	case <-acceptedExec:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("server never accepted exec")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want sudo initialization deadline, got %v", err)
		}
	case <-time.After(time.Second):
		cancel() // Rescue the operation without letting its parent deadline hide the bug.
		<-done
		t.Fatal("sudo initialization timeout did not unblock an unresponsive transport")
	}
}
