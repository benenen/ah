//go:build darwin || linux

package sshclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestSudoSetupTimeout(t *testing.T) {
	for _, mode := range []string{"sftp-channel-open", "exec-channel-open", "shell-pty-request"} {
		t.Run(mode, func(t *testing.T) {
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
			stalled, serverDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(serverDone)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				server, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if mode != "shell-pty-request" {
						// Leave channel-open pending until the client tears down TCP.
						close(stalled)
						server.Wait()
						return
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					defer channel.Close()
					for request := range requests {
						if request.Type == "pty-req" {
							// Accept the channel, but never answer the PTY request.
							close(stalled)
							server.Wait()
							return
						}
						request.Reply(false, nil)
					}
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
			var terminal *os.File
			if mode == "shell-pty-request" {
				master, slave, err := pty.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer master.Close()
				defer slave.Close()
				terminal = slave
			}
			done := make(chan error, 1)
			go func() {
				switch mode {
				case "sftp-channel-open":
					files, err := client.SFTP()
					if files != nil {
						files.Close()
					}
					done <- err
				case "exec-channel-open":
					done <- client.Exec("true", nil, io.Discard, io.Discard)
				default:
					done <- client.Shell(terminal, io.Discard, io.Discard)
				}
			}()
			select {
			case <-stalled:
			case <-time.After(time.Second):
				cancel()
				<-done
				t.Fatal("server did not reach the pending setup request")
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("want sudo setup deadline, got %v", err)
				}
			case <-time.After(time.Second):
				cancel() // Rescue the call without a parent deadline hiding the bug.
				<-done
				t.Fatal("sudo setup timeout did not unblock the pending request")
			}
		})
	}
}
