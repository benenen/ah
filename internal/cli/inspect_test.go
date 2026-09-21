package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/credentials"
)

func TestInspectConnectionHidesCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connections.toml")
	key := filepath.Join(dir, "master.key")
	password, err := (credentials.Store{Path: key}).Encrypt("fixture", []byte("ssh-secret"))
	if err != nil {
		t.Fatal(err)
	}
	sudoPassword, err := (credentials.Store{Path: key}).Encrypt("fixture/sudo", []byte("sudo-secret"))
	if err != nil {
		t.Fatal(err)
	}
	c := config.Connection{Host: "example.com", Port: 2222, User: "alice", IdentityFile: "/missing/private-key",
		Password: password, Sudo: true, SudoPassword: sudoPassword, SudoShell: "/bin/zsh", SFTPServer: "/usr/lib/sftp-server", Term: "screen",
		Proxies: []string{"socks5://proxy-user:proxy-secret@127.0.0.1:1080", "socks5h://[::1]:1081"}}
	if err := (config.Store{Path: path}).Update(context.Background(), func(m map[string]config.Connection) error { m["fixture"] = c; return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect must not need private keys, decryption, or a reachable SSH server.
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "--config", path, "--term", "vt100", "inspect", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{password, sudoPassword, "ssh-secret", "sudo-secret", "proxy-user", "proxy-secret"} {
		if strings.Contains(out, secret) {
			t.Fatal("inspect exposed credentials")
		}
	}
	var got inspectedConnection
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "fixture" || got.Host != c.Host || got.Port != 2222 || got.User != "alice" || got.ConfigFile != path || got.IdentityFile != c.IdentityFile || !got.PasswordConfigured || !got.Sudo || !got.SudoPasswordConfigured || got.SudoShell != c.SudoShell || got.SFTPServer != c.SFTPServer || got.Term != "screen" || got.EffectiveTerm != "vt100" {
		t.Fatalf("unexpected connection: %+v", got)
	}
	if len(got.Proxies) != 2 || got.Proxies[0].Address != "socks5://127.0.0.1:1080" || !got.Proxies[0].AuthenticationConfigured || got.Proxies[1].AuthenticationConfigured {
		t.Fatalf("unexpected proxies: %+v", got.Proxies)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("inspect changed config", err)
	}
}

func TestInspectDefaultsLegacyProxyAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.toml")
	t.Setenv("TERM", "local-terminal")
	for _, legacy := range []string{"", "socks5://alice:secret@localhost:1080"} {
		if err := (config.Store{Path: path}).Update(context.Background(), func(m map[string]config.Connection) error {
			m["fixture"] = config.Connection{Host: "example.com", User: "alice", Port: 22, Proxy: legacy}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		out, err := execute(t, "--config", path, "inspect", "fixture")
		if err != nil {
			t.Fatal(err)
		}
		var got inspectedConnection
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if got.Proxies == nil || got.PasswordConfigured || got.SudoPasswordConfigured || got.Term != "" || got.EffectiveTerm != "local-terminal" {
			t.Fatalf("defaults: %+v", got)
		}
		if legacy != "" && (len(got.Proxies) != 1 || got.Proxies[0].Address != "socks5://localhost:1080" || !got.Proxies[0].AuthenticationConfigured) {
			t.Fatalf("legacy proxy: %+v", got.Proxies)
		}
	}
	for _, args := range [][]string{{"inspect"}, {"inspect", "missing"}, {"inspect", "fixture", "extra"}} {
		out, err := execute(t, append([]string{"--config", path}, args...)...)
		if err == nil || out != "" {
			t.Fatalf("invalid request: %v, %q, %v", args, out, err)
		}
	}
}
