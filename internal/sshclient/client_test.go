package sshclient_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/gofrs/flock"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/sshclient"
	"github.com/benenen/ah/internal/testutil"
)

func TestTrustedKeySFTP(t *testing.T) {
	f := testutil.StartSSH(t)
	c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: f.KnownHosts})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := c.SFTP()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wd, err := s.Getwd()
	if err != nil || wd != f.Root {
		t.Fatalf("Getwd=%q, %v", wd, err)
	}
}
func TestUnknownAndChangedKeys(t *testing.T) {
	f := testutil.StartSSH(t)
	other := testutil.StartSSH(t)
	missing := filepath.Join(t.TempDir(), "known_hosts")
	if c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: missing}); err == nil {
		c.Close()
		t.Fatal("unknown host accepted")
	}
	called := false
	c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: missing, TrustHost: func(host, fp string) (bool, error) {
		called = true
		if host == "" || fp == "" {
			t.Fatal("missing fingerprint")
		}
		return true, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if !called {
		t.Fatal("trust callback missing")
	}
	c, err = sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: missing})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	data, err := os.ReadFile(other.KnownHosts)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the first host address and replace only its key.
	original, _ := os.ReadFile(missing)
	a, b := 0, 0
	for original[a] != ' ' {
		a++
	}
	for data[b] != ' ' {
		b++
	}
	if err = os.WriteFile(missing, append(original[:a], data[b:]...), 0600); err != nil {
		t.Fatal(err)
	}
	called = false
	if c, err = sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: missing, TrustHost: func(string, string) (bool, error) { called = true; return true, nil }}); err == nil {
		c.Close()
		t.Fatal("changed key accepted")
	}
	if called {
		t.Fatal("changed key prompted for trust")
	}
}

// Answering an unknown host key prompt pauses the handshake clock, so a slow
// decision must not consume --timeout.
func TestTrustDecisionOutlivesHandshakeTimeout(t *testing.T) {
	f := testutil.StartSSH(t)
	missing := filepath.Join(t.TempDir(), "known_hosts")
	c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: missing, Timeout: 150 * time.Millisecond, TrustHost: func(string, string) (bool, error) {
		time.Sleep(500 * time.Millisecond)
		return true, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err = os.Stat(missing); err != nil {
		t.Fatal("trusted key was not saved", err)
	}
}

func TestAuthenticationFailure(t *testing.T) {
	f := testutil.StartSSH(t)
	other := testutil.StartSSH(t)
	f.Connection.IdentityFile = other.KeyPath
	if c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: f.KnownHosts}); err == nil {
		c.Close()
		t.Fatal("wrong key accepted")
	}
}
func TestHandshakeCancellation(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err == nil {
			defer conn.Close()
			buf := make([]byte, 128)
			for {
				if _, err = conn.Read(buf); err != nil {
					return
				}
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	addr := ln.Addr().(*net.TCPAddr)
	start := time.Now()
	if c, err := sshclient.Dial(ctx, config.Connection{Host: "127.0.0.1", Port: addr.Port, User: "test"}, sshclient.Options{KnownHosts: filepath.Join(t.TempDir(), "known_hosts"), Timeout: time.Second}); err == nil {
		c.Close()
		t.Fatal("handshake unexpectedly succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation did not interrupt handshake")
	}
	<-done
}

func TestCancellationClosesActiveSFTP(t *testing.T) {
	f := testutil.StartSSH(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := sshclient.Dial(ctx, f.Connection, sshclient.Options{KnownHosts: f.KnownHosts, Password: func() (string, error) {
		t.Error("password requested after successful key authentication")
		return "", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := c.SFTP()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		_, err = s.Getwd()
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SFTP remains open after cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	firstErr := c.Close()
	secondErr := c.Close()
	if firstErr != secondErr {
		t.Fatalf("Close is not idempotent: %v, %v", firstErr, secondErr)
	}
}

func TestTrustLockHonorsTimeout(t *testing.T) {
	f := testutil.StartSSH(t)
	known := filepath.Join(t.TempDir(), "known_hosts")
	lock := flock.New(known + ".lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	// Release eventually so a regression fails without wedging the test process.
	timer := time.AfterFunc(400*time.Millisecond, func() { _ = lock.Unlock() })
	defer timer.Stop()
	start := time.Now()
	c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: known, Timeout: 70 * time.Millisecond, TrustHost: func(string, string) (bool, error) { return true, nil }})
	if c != nil {
		c.Close()
	}
	if err == nil {
		t.Fatal("locked trust unexpectedly succeeded")
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatal("known_hosts lock exceeded connection timeout")
	}
}

func TestExplicitIdentityWithAgent(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprint("encrypted=", encrypted), func(t *testing.T) {
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
			for i := 0; i < 10; i++ {
				_, private, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				if err = ring.Add(agent.AddedKey{PrivateKey: private}); err != nil {
					t.Fatal(err)
				}
			}
			if err = ring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
				t.Fatal(err)
			}
			if encrypted {
				block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("test-only"))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(f.KeyPath, pem.EncodeToMemory(block), 0600); err != nil {
					t.Fatal(err)
				}
			}
			dir, err := os.MkdirTemp("/tmp", "ah-agent-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "a")
			ln, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := ln.Accept()
				if err == nil {
					defer conn.Close()
					_ = agent.ServeAgent(ring, conn)
				}
			}()
			t.Setenv("SSH_AUTH_SOCK", socket)
			c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: f.KnownHosts})
			if c != nil {
				c.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			<-done
		})
	}
}

func TestConnectionOutlivesHandshakeTimeout(t *testing.T) {
	f := testutil.StartSSH(t)
	c, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: f.KnownHosts, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(150 * time.Millisecond)
	s, err := c.SFTP()
	if err != nil {
		t.Fatal("handshake timeout closed established connection", err)
	}
	defer s.Close()
	if _, err = s.Getwd(); err != nil {
		t.Fatal(err)
	}
}

func TestImplicitPassphraseCancellationPropagates(t *testing.T) {
	for _, cancelErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cancelErr.Error(), func(t *testing.T) {
			f := testutil.StartSSH(t)
			data, err := os.ReadFile(f.KeyPath)
			if err != nil {
				t.Fatal(err)
			}
			key, err := ssh.ParseRawPrivateKey(data)
			if err != nil {
				t.Fatal(err)
			}
			block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("test-only"))
			if err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			if err = os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), pem.EncodeToMemory(block), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("SSH_AUTH_SOCK", "")
			f.Connection.IdentityFile = ""
			client, err := sshclient.Dial(context.Background(), f.Connection, sshclient.Options{KnownHosts: f.KnownHosts, Passphrase: func(string) ([]byte, error) { return nil, cancelErr }})
			if client != nil {
				client.Close()
			}
			if !errors.Is(err, cancelErr) {
				t.Fatalf("expected cancellation %v, got %v", cancelErr, err)
			}
		})
	}
}

func TestPasswordOnlySkipsLocalIdentities(t *testing.T) {
	f := testutil.StartPasswordSSH(t, "fixture-password")
	connection := f.Connection
	connection.IdentityFile = filepath.Join(t.TempDir(), "missing-key")
	for _, password := range []string{"wrong-password", "fixture-password"} {
		t.Run(password, func(t *testing.T) {
			client, err := sshclient.Dial(context.Background(), connection, sshclient.Options{
				KnownHosts: f.KnownHosts, PasswordOnly: true, Password: func() (string, error) { return password, nil },
			})
			if password == "wrong-password" {
				if err == nil {
					client.Close()
					t.Fatal("wrong password accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			s, err := client.SFTP()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if wd, err := s.Getwd(); err != nil || wd != f.Root {
				t.Fatalf("password connection unusable: %q %v", wd, err)
			}
		})
	}
}
