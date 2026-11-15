//go:build !windows

package cli

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/testutil"
)

// TestCLIProxyCommandHelper is not a test: proxy commands re-execute the test
// binary with this name to relay stdin/stdout to the host and port arguments.
func TestCLIProxyCommandHelper(t *testing.T) {
	if os.Getenv("AH_PROXY_COMMAND_HELPER") != "1" {
		t.Skip("helper process")
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) != 2 {
		os.Exit(2)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort(args[0], args[1]))
	if err != nil {
		os.Exit(3)
	}
	go func() { _, _ = io.Copy(conn, os.Stdin); conn.Close() }()
	_, _ = io.Copy(os.Stdout, conn)
	os.Exit(0)
}

func TestCopyThroughProxyCommandUsesConnectionName(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("AH_PROXY_COMMAND_HELPER", "1")
	server := testutil.StartSSH(t)
	dir := t.TempDir()
	key, err := os.ReadFile(server.KnownHosts)
	if err != nil {
		t.Fatal(err)
	}
	// Only the connection name is trusted, not the transport address.
	known := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(known, []byte("shiben "+strings.SplitN(string(key), " ", 2)[1]), 0600); err != nil {
		t.Fatal(err)
	}
	c := server.Connection
	c.ProxyCommand = "exec '" + os.Args[0] + "' -test.run=^TestCLIProxyCommandHelper$ -- %h %p"
	p := filepath.Join(dir, "config")
	if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error {
		m["shiben"] = c
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("via proxy command"), 0600); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--config", p, "--known-hosts", known, "--key-file", filepath.Join(dir, "key")}
	if out, err := execute(t, append(flags, "cp", source, "shiben:~/result")...); err != nil {
		t.Fatal(out, err)
	}
	b, err := os.ReadFile(filepath.Join(server.Root, "result"))
	if err != nil || string(b) != "via proxy command" {
		t.Fatal(string(b), err)
	}
	if out, err := execute(t, append(flags, "doctor", "shiben")...); err != nil || !strings.Contains(out, "proxy-command\tOK") {
		t.Fatal(out, err)
	}
}
