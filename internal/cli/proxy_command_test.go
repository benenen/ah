package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benenen/ah/internal/config"
)

func TestProxyCommandConnection(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "connections.toml")
	run := func(args ...string) (string, error) { return execute(t, append([]string{"--config", p}, args...)...) }
	command := "uucli proxy dev %h %p"
	if _, err := run("new", "shiben", "--host", "127.0.0.1", "--user", "shiben", "--proxy-command", command); err != nil {
		t.Fatal(err)
	}
	if _, err := run("edit", "shiben", "--jump", "other"); err == nil {
		t.Fatal("accepted jump together with proxy_command")
	}
	out, err := run("inspect", "shiben")
	if err != nil {
		t.Fatal(err)
	}
	var info inspectedConnection
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatal(err)
	}
	if info.ProxyCommand != command || info.HostKeyAlias != "" || info.HostKeyIdentity != "shiben" {
		t.Fatalf("inspect %+v", info)
	}

	// Cloning keeps the original known_hosts identity for the same server.
	if _, err := run("clone", "shiben", "copy"); err != nil {
		t.Fatal(err)
	}
	entries, err := config.Store{Path: p}.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := entries["copy"].HostKeyIdentity("copy"); got != "shiben" {
		t.Fatalf("clone identity %q", got)
	}
	if entries["shiben"].HostKeyAlias != "" {
		t.Fatal("clone changed the source alias")
	}

	if _, err := run("edit", "shiben", "--proxy-command", "", "--host-key-alias", "box"); err != nil {
		t.Fatal(err)
	}
	entries, err = config.Store{Path: p}.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c := entries["shiben"]; c.ProxyCommand != "" || c.HostKeyIdentity("shiben") != "box" {
		t.Fatalf("edit result %+v", c)
	}
}

func TestImportProxyCommandRequiresConsent(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "export.toml")
	data := "[connections.dev]\nhost = \"127.0.0.1\"\nport = 22\nuser = \"alice\"\nproxy_command = \"uucli proxy dev %h %p\"\n"
	if err := os.WriteFile(archive, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "connections.toml")
	run := func(args ...string) (string, error) {
		return execute(t, append([]string{"--config", target, "config", "import", archive}, args...)...)
	}
	out, err := run("--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "proxy-command\tdev\tuucli proxy dev %h %p") {
		t.Fatalf("dry run does not show the command:\n%s", out)
	}
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "--allow-proxy-command") {
		t.Fatalf("import without consent: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("rejected import wrote config: %v", err)
	}
	if _, err := run("--allow-proxy-command"); err != nil {
		t.Fatal(err)
	}
	entries, err := config.Store{Path: target}.Load()
	if err != nil {
		t.Fatal(err)
	}
	if entries["dev"].ProxyCommand == "" {
		t.Fatal("proxy_command not imported")
	}
}
