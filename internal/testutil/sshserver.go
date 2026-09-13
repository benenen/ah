// Package testutil provides isolated SSH/SFTP fixtures for integration tests.
package testutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/benenen/ah/internal/config"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type SSHServer struct {
	Connection config.Connection
	KnownHosts string
	Root       string
	KeyPath    string
	listener   net.Listener
	mu         sync.Mutex
	conns      map[net.Conn]bool
	closed     bool
	wg         sync.WaitGroup
}

func StartSSH(t *testing.T) *SSHServer {
	t.Helper()
	root := t.TempDir()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	userPub, userKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	userSSH, err := ssh.NewPublicKey(userPub)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(userKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "identity")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) != string(userSSH.Marshal()) {
			return nil, os.ErrPermission
		}
		return nil, nil
	}}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portText)
	known := filepath.Join(root, "known_hosts")
	if err = os.WriteFile(known, []byte(knownhosts.Line([]string{ln.Addr().String()}, hostSigner.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &SSHServer{Connection: config.Connection{Host: host, Port: port, User: "test", IdentityFile: keyPath}, KnownHosts: known, Root: root, KeyPath: keyPath, listener: ln, conns: map[net.Conn]bool{}}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			if f.closed {
				f.mu.Unlock()
				conn.Close()
				return
			}
			f.conns[conn] = true
			f.wg.Add(1)
			f.mu.Unlock()
			go f.serve(conn, cfg)
		}
	}()
	t.Cleanup(f.Close)
	return f
}

func (f *SSHServer) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	defer f.wg.Done()
	defer conn.Close()
	defer func() { f.mu.Lock(); delete(f.conns, conn); f.mu.Unlock() }()
	sc, channels, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	var sessions sync.WaitGroup
	defer sessions.Wait()
	for ch := range channels {
		if ch.ChannelType() != "session" {
			ch.Reject(ssh.UnknownChannelType, "session required")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			defer channel.Close()
			for req := range requests {
				var subsystem struct{ Name string }
				if req.Type == "subsystem" && ssh.Unmarshal(req.Payload, &subsystem) == nil && subsystem.Name == "sftp" {
					req.Reply(true, nil)
					server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(f.Root))
					if err != nil {
						return
					}
					_ = server.Serve()
					_ = server.Close()
					return
				}
				req.Reply(false, nil)
			}
		}()
	}
}

func (f *SSHServer) Close() {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		f.listener.Close()
		for c := range f.conns {
			c.Close()
		}
	}
	f.mu.Unlock()
	f.wg.Wait()
}
