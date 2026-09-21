package sshclient_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/ah/internal/sshclient"
	"github.com/benenen/ah/internal/testutil"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestAgentAuthenticationAfterSlowTrustDecision(t *testing.T) {
	f := testutil.StartSSH(t)
	data, err := os.ReadFile(f.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "ah-agent-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		stop := context.AfterFunc(ctx, func() { _ = c.Close() })
		defer stop()
		_ = agent.ServeAgent(ring, c)
	}()
	defer func() { cancel(); _ = listener.Close(); <-done }()
	t.Setenv("SSH_AUTH_SOCK", socket)
	t.Setenv("HOME", t.TempDir())
	// No identity file or password fallback: authentication must sign via agent.
	f.Connection.IdentityFile = ""
	c, err := sshclient.Dial(ctx, f.Connection, sshclient.Options{
		KnownHosts: filepath.Join(t.TempDir(), "known_hosts"), Timeout: 200 * time.Millisecond,
		TrustHost: func(string, string) (bool, error) {
			time.Sleep(500 * time.Millisecond)
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
}
