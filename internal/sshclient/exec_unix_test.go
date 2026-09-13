//go:build darwin || linux

package sshclient

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/benenen/ah/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func TestExecStopsPendingInput(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for _, cancelDuringCommand := range []bool{false, true} {
		name := "completion"
		if cancelDuringCommand {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			server := testutil.StartCommandSSH(t, func(_ string, ch ssh.Channel) uint32 {
				close(started)
				if cancelDuringCommand {
					_, _ = io.Copy(io.Discard, ch)
				}
				return 0
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client, err := Dial(ctx, server.Connection, Options{KnownHosts: server.KnownHosts})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer writer.Close()
			done := make(chan error, 1)
			go func() { done <- client.Exec("controlled command", input, io.Discard, io.Discard) }()
			defer func() { cancel(); client.Close() }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("exec not started")
			}
			if cancelDuringCommand {
				cancel()
			}
			select {
			case err := <-done:
				if cancelDuringCommand && err == nil {
					t.Fatal("cancel accepted as success")
				}
				if !cancelDuringCommand && err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("exec left input blocked")
			}
			if _, err := writer.Write([]byte("stdin remains open")); err != nil {
				t.Fatalf("caller stdin closed: %v", err)
			}
		})
	}
}
