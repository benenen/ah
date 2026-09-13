package cli

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
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/history"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestCopyCanceledDuringSFTPInitializationRecordsCanceled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SSH_AUTH_SOCK", "")
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	// Authentication is deliberately disabled only on this isolated loopback fixture.
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, stopServer := context.WithCancel(context.Background())
	serverDone := make(chan struct{})
	subsystemStarted := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		stop := context.AfterFunc(serverCtx, func() { conn.Close() })
		defer stop()
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return
		}
		server, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			return
		}
		requestsDone := make(chan struct{})
		go func() { defer close(requestsDone); ssh.DiscardRequests(requests) }()
		defer func() { server.Close(); <-requestsDone }()
		for next := range channels {
			if next.ChannelType() != "session" {
				next.Reject(ssh.UnknownChannelType, "session required")
				continue
			}
			channel, requests, err := next.Accept()
			if err != nil {
				return
			}
			defer channel.Close()
			for request := range requests {
				var subsystem struct{ Name string }
				if request.Type != "subsystem" || ssh.Unmarshal(request.Payload, &subsystem) != nil || subsystem.Name != "sftp" {
					request.Reply(false, nil)
					continue
				}
				if err := request.Reply(true, nil); err != nil {
					return
				}
				close(subsystemStarted)
				// Consume SSH channel traffic but never send the SFTP version response.
				_, _ = io.Copy(io.Discard, channel)
				return
			}
		}
	}()
	t.Cleanup(func() {
		stopServer()
		listener.Close()
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second):
			t.Error("SSH fixture did not stop")
		}
	})
	knownPath := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(knownPath, []byte(knownhosts.Line([]string{listener.Addr().String()}, signer.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	host, portString, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "connections.toml")
	if err := (config.Store{Path: configPath}).Update(context.Background(), func(connections map[string]config.Connection) error {
		connections["stalled"] = config.Connection{Host: host, Port: port, User: "test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(source, []byte("copy me"), 0600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "history.db")
	cmd := New()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--config", configPath, "--known-hosts", knownPath, "--history-file", dbPath, "cp", source, "stalled:/destination.txt"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commandDone := make(chan error, 1)
	commandFinished := make(chan struct{})
	go func() {
		defer close(commandFinished)
		commandDone <- cmd.ExecuteContext(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		stopServer()
		select {
		case <-commandFinished:
		case <-time.After(2 * time.Second):
			t.Error("copy command goroutine did not stop")
		}
	})
	select {
	case <-subsystemStarted:
	case err := <-commandDone:
		t.Fatalf("copy ended before SFTP initialization: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("copy never requested SFTP subsystem")
	}
	cancel()
	select {
	case err := <-commandDone:
		if err == nil {
			t.Error("canceled copy returned success")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("copy error does not preserve cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled copy did not return promptly")
	}
	store, err := history.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, err := store.Search(context.Background(), "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("history rows=%d, want one canceled attempt", len(rows))
	}
	if rows[0].Status != "canceled" || rows[0].FinishedAt.IsZero() {
		t.Fatalf("cancellation recorded incorrectly: %+v", rows[0])
	}
}
