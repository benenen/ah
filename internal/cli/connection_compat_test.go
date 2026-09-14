package cli

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/credentials"
)

func TestLegacyConnectionUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.toml")
	store := config.Store{Path: path}
	legacy := config.Connection{Host: "example.invalid", Port: 22, User: "alice", Proxy: "socks5h://127.0.0.1:1080", Sudo: true}
	if err := store.Update(context.Background(), func(m map[string]config.Connection) error { m["nas"] = legacy; return nil }); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) config.Connection {
		t.Helper()
		if _, err := execute(t, append([]string{"--config", path, "edit", "nas"}, args...)...); err != nil {
			t.Fatal(err)
		}
		m, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		return m["nas"]
	}
	c := run("--port", "2222")
	if !reflect.DeepEqual(c.ProxyChain(), []string{legacy.Proxy}) || !c.Sudo {
		t.Fatal("unrelated edit lost legacy settings")
	}
	c = run("--no-sudo")
	if c.Sudo {
		t.Fatal("legacy disable flag failed")
	}
	c = run("--sudo")
	if !c.Sudo {
		t.Fatal("enable failed")
	}
	c = run("--sudo=false")
	if c.Sudo {
		t.Fatal("explicit false failed")
	}
	c = run("--proxy", "localhost:1080", "--proxy", "second.invalid:1081")
	if c.Proxy != "" || len(c.Proxies) != 2 {
		t.Fatal("chain did not replace legacy proxy")
	}
	c = run("--clear-proxy")
	if len(c.ProxyChain()) != 0 {
		t.Fatal("clear failed")
	}
}

func TestSavedSudoPasswordCompatibility(t *testing.T) {
	for _, binding := range []string{"nas", "nas/sudo"} {
		t.Run(binding, func(t *testing.T) {
			dir := t.TempDir()
			a := &app{configPath: filepath.Join(dir, "connections.toml"), keyPath: filepath.Join(dir, "master.key")}
			cipher, err := (credentials.Store{Path: a.keyPath}).Encrypt(binding, []byte("sudo-secret"))
			if err != nil {
				t.Fatal(err)
			}
			options := a.connectionSSHOptions(New(), "nas", config.Connection{Sudo: true, SudoPassword: cipher}, false)
			secret, err := options.SudoPassword(context.Background())
			defer clear(secret)
			if err != nil || string(secret) != "sudo-secret" {
				t.Fatal("saved sudo authentication failed", err)
			}
		})
	}
	a := &app{}
	options := a.connectionSSHOptions(New(), "nas", config.Connection{Sudo: true, Password: "unused"}, false)
	if _, err := options.SudoPassword(context.Background()); err == nil {
		t.Fatal("missing sudo credential silently reused SSH password")
	}
}
