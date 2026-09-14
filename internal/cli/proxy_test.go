package cli

import (
	"github.com/benenen/ah/internal/testutil"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/benenen/ah/internal/config"
)

func TestProxyConfigCRUD(t *testing.T) {
	p := filepath.Join(t.TempDir(), "connections.toml")
	run := func(args ...string) error {
		_, err := execute(t, append([]string{"--config", p}, args...)...)
		return err
	}
	if err := run("new", "nas", "--host", "nas.internal", "--user", "alice", "--proxy", "socks5://127.0.0.1:1080", "--proxy", "proxy2.internal:1081"); err != nil {
		t.Fatal(err)
	}
	check := func(want []string) {
		t.Helper()
		m, err := (config.Store{Path: p}).Load()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m["nas"].Proxies, want) {
			t.Fatalf("proxies=%v want %v", m["nas"].Proxies, want)
		}
	}
	check([]string{"socks5://127.0.0.1:1080", "proxy2.internal:1081"})
	if err := run("edit", "nas", "--port", "2222"); err != nil {
		t.Fatal(err)
	}
	check([]string{"socks5://127.0.0.1:1080", "proxy2.internal:1081"})
	if err := run("edit", "nas", "--proxy", "[::1]:1080"); err != nil {
		t.Fatal(err)
	}
	check([]string{"[::1]:1080"})
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"http://proxy:80", "proxy", "proxy:0", "proxy:65536", "socks5://proxy:1080/path", ""} {
		if err := run("edit", "nas", "--proxy", bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
		after, err := os.ReadFile(p)
		if err != nil || string(after) != string(before) {
			t.Fatal("invalid edit modified config", err)
		}
	}
	if err := run("edit", "nas", "--proxy", "proxy:1080", "--clear-proxy"); err == nil {
		t.Fatal("conflicting flags accepted")
	}
	if err := run("edit", "nas", "--clear-proxy"); err != nil {
		t.Fatal(err)
	}
	check(nil)
}

func TestSavedProxyCopyAndCompletion(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartSSH(t)
	target := net.JoinHostPort(server.Connection.Host, strconv.Itoa(server.Connection.Port))
	second := testutil.StartSOCKS5(t, map[string]string{target: target})
	first := testutil.StartSOCKS5(t, map[string]string{"second.internal:1080": second.Address})
	dir := t.TempDir()
	p := filepath.Join(dir, "connections.toml")
	flags := []string{"--config", p, "--known-hosts", server.KnownHosts}
	run := func(args ...string) (string, error) {
		return execute(t, append(append([]string{}, flags...), args...)...)
	}
	if _, err := run("new", "nas", "--host", server.Connection.Host, "--port", strconv.Itoa(server.Connection.Port), "--user", server.Connection.User, "--identity-file", server.KeyPath, "--proxy", first.Address, "--proxy", "socks5://second.internal:1080"); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(src, []byte("through saved proxies"), 0600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(server.Root, "uploaded.txt")
	if _, err := run("cp", src, "nas:"+dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != "through saved proxies" {
		t.Fatal("copy", err)
	}
	if out, err := run("__complete", "cp", "nas:"+filepath.Join(server.Root, "upl")); err != nil || !strings.Contains(out, "uploaded.txt") {
		t.Fatalf("completion %q %v", out, err)
	}
	for _, proxy := range []*testutil.SOCKSProxy{first, second} {
		if len(proxy.Requests) != 2 {
			t.Fatalf("copy/completion did not both use proxy: %d", len(proxy.Requests))
		}
	}
}
