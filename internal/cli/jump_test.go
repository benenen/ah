package cli

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func jumpRelay(ch ssh.NewChannel) {
	var address struct {
		Host       string
		Port       uint32
		Origin     string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(ch.ExtraData(), &address); err != nil {
		_ = ch.Reject(ssh.ConnectionFailed, "bad address")
		return
	}
	conn, err := net.Dial("tcp", net.JoinHostPort(address.Host, strconv.Itoa(int(address.Port))))
	if err != nil {
		_ = ch.Reject(ssh.ConnectionFailed, "unreachable")
		return
	}
	defer conn.Close()
	channel, rs, err := ch.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	go ssh.DiscardRequests(rs)
	done := make(chan struct{})
	go func() { _, _ = io.Copy(conn, channel); _ = conn.Close(); close(done) }()
	_, _ = io.Copy(channel, conn)
	_ = channel.Close()
	<-done
}

func TestCopyThroughNamedJumpChain(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	first := testutil.StartForwardSSH(t, jumpRelay)
	second := testutil.StartForwardSSH(t, jumpRelay)
	target := testutil.StartSSH(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	known := filepath.Join(dir, "known_hosts")
	var keys []byte
	for _, s := range []*testutil.SSHServer{first, second, target} {
		b, err := os.ReadFile(s.KnownHosts)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, b...)
	}
	if err := os.WriteFile(known, keys, 0600); err != nil {
		t.Fatal(err)
	}
	c2 := second.Connection
	c2.Jump = "first"
	ct := target.Connection
	ct.Jump = "second"
	store := config.Store{Path: p}
	if err := store.Update(context.Background(), func(m map[string]config.Connection) error {
		m["first"] = first.Connection
		m["second"] = c2
		m["target"] = ct
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("through two SSH hops"), 0600); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--config", p, "--known-hosts", known, "--key-file", filepath.Join(dir, "key")}
	run := func(args ...string) (string, error) {
		return execute(t, append(append([]string{}, flags...), args...)...)
	}
	if out, err := run("cp", source, "target:~/result"); err != nil {
		t.Fatal(out, err)
	}
	b, err := os.ReadFile(filepath.Join(target.Root, "result"))
	if err != nil || string(b) != "through two SSH hops" {
		t.Fatal(string(b), err)
	}
	if out, err := run("doctor", "target"); err != nil || !strings.Contains(out, "jump-first/host-key\tOK") || !strings.Contains(out, "ssh-jump\tOK") {
		t.Fatal(out, err)
	}
	// A renamed hop remains connected to its existing dependents.
	if out, err := run("rename", "first", "bastion"); err != nil {
		t.Fatal(out, err)
	}
	entries, err := store.Load()
	if err != nil || entries["second"].Jump != "bastion" {
		t.Fatal("rename broke route", err)
	}
	if out, err := run("cp", "--dry-run", source, "target:~/new-result"); err != nil {
		t.Fatal(out, err)
	}
	// A cycle is rejected before dialing, rather than silently using direct TCP.
	if err := store.Update(context.Background(), func(m map[string]config.Connection) error {
		c := m["bastion"]
		c.Jump = "target"
		m["bastion"] = c
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := run("doctor", "target"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatal("cycle accepted", err)
	}
}

func TestJumpFailureNeverFallsBack(t *testing.T) {
	target := testutil.StartSSH(t)
	denied := testutil.StartForwardSSH(t, func(ch ssh.NewChannel) { _ = ch.Reject(ssh.Prohibited, "forwarding denied") })
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	known := filepath.Join(dir, "known_hosts")
	var keys []byte
	for _, s := range []*testutil.SSHServer{target, denied} {
		b, err := os.ReadFile(s.KnownHosts)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, b...)
	}
	if err := os.WriteFile(known, keys, 0600); err != nil {
		t.Fatal(err)
	}
	ct := target.Connection
	ct.Jump = "denied"
	if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error {
		m["target"] = ct
		m["denied"] = denied.Connection
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "--config", p, "--known-hosts", known, "doctor", "target")
	if err == nil || !strings.Contains(out, "ssh-jump\tFAIL") {
		t.Fatal("jump failure ignored", out, err)
	}
}

func TestJumpHandshakeTimeoutReleasesChain(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	closed := make(chan struct{}, 1)
	hop := testutil.StartForwardSSH(t, func(ch ssh.NewChannel) {
		channel, requests, err := ch.Accept()
		if err != nil {
			return
		}
		defer channel.Close()
		go ssh.DiscardRequests(requests)
		_, _ = io.Copy(io.Discard, channel)
		closed <- struct{}{}
	})
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	target := hop.Connection
	target.Host = "silent.internal"
	target.Jump = "hop"
	if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error {
		m["target"] = target
		m["hop"] = hop.Connection
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := execute(t, "--timeout", "200ms", "--config", p, "--known-hosts", hop.KnownHosts, "doctor", "target")
	if err == nil {
		t.Fatal("silent SSH server did not time out")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("jump channel leaked after handshake timeout")
	}
}
