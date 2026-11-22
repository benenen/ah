//go:build !windows

package sshclient

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/testutil"
)

// TestProxyCommandHelper is not a test: the proxy command re-executes the test
// binary with this name to relay stdin/stdout to the host and port arguments.
func TestProxyCommandHelper(t *testing.T) {
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

func helperProxyCommand(t *testing.T) string {
	t.Helper()
	t.Setenv("AH_PROXY_COMMAND_HELPER", "1")
	return "exec " + shellQuote(os.Args[0]) + " -test.run=^TestProxyCommandHelper$ -- %h %p"
}

func TestProxyCommandSSH(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartSSH(t)
	c := server.Connection
	c.ProxyCommand = helperProxyCommand(t)
	c.HostKeyAlias = "devbox"
	known, err := os.ReadFile(server.KnownHosts)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.SplitN(string(known), " ", 2)[1]
	aliased := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(aliased, []byte("devbox "+key), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stages []string
	client, err := Dial(ctx, c, Options{KnownHosts: aliased, Observe: func(stage string, _ error) { stages = append(stages, stage) }})
	if err != nil {
		t.Fatal(err)
	}
	files, err := client.SFTP()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.ReadDir(server.Root); err != nil {
		t.Fatal(err)
	}
	files.Close()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(stages, ","), "proxy-command") {
		t.Fatalf("stages %v lack proxy-command", stages)
	}

	// The alias is the identity: a key recorded only for the real address,
	// which a proxy command usually shares across servers, must not match.
	if bad, err := Dial(ctx, c, Options{KnownHosts: server.KnownHosts}); err == nil {
		bad.Close()
		t.Fatal("proxy command connection verified against the transport address")
	} else if !strings.Contains(err.Error(), "devbox") {
		t.Fatalf("error does not name the alias: %v", err)
	}
}

func TestProxyCommandFailureReportsStderr(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartSSH(t)
	c := server.Connection
	c.ProxyCommand = "echo 'device offline' >&2; exit 3"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, c, Options{KnownHosts: server.KnownHosts})
	if err == nil || !strings.Contains(err.Error(), "device offline") {
		t.Fatalf("got %v, want proxy command stderr", err)
	}
}

func TestProxyCommandHandshakeTimeoutKillsProcess(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartSSH(t)
	c := server.Connection
	// The command never speaks SSH; only the handshake timer can end it. The
	// second form keeps a grandchild holding the pipes after sh is killed.
	for _, command := range []string{"exec sleep 30", "sleep 30; true"} {
		c.ProxyCommand = command
		start := time.Now()
		_, err := Dial(context.Background(), c, Options{KnownHosts: server.KnownHosts, Timeout: 500 * time.Millisecond})
		if err == nil {
			t.Fatal("silent proxy command connected")
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("%q: timeout took %v; proxy command was not killed", command, elapsed)
		}
	}
}

func TestExpandProxyCommand(t *testing.T) {
	got, err := expandProxyCommandFor("linux", "uucli proxy dev %h %p %r 100%%", "10.0.0.1", 2222, "o'neil")
	if err != nil {
		t.Fatal(err)
	}
	want := `uucli proxy dev '10.0.0.1' 2222 'o'\''neil' 100%`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	win, err := expandProxyCommandFor("windows", "proxy.exe %h %p %r", "host.internal", 22, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if win != `proxy.exe "host.internal" 22 "alice"` {
		t.Fatalf("windows expand = %q", win)
	}
	for _, bad := range []string{`a"b`, `%PATH%`, `a&calc`, `a|calc`, `a^b`, "a\nb"} {
		if _, err := expandProxyCommandFor("windows", "proxy.exe %h %p", bad, 22, "alice"); err == nil {
			t.Fatalf("windows accepted unsafe host %q", bad)
		}
		if _, err := expandProxyCommandFor("windows", "proxy.exe %h %r", "host.internal", 22, bad); err == nil {
			t.Fatalf("windows accepted unsafe user %q", bad)
		}
	}
}

func TestTailBufferKeepsRecentOutput(t *testing.T) {
	var b tailBuffer
	_, _ = b.Write([]byte(strings.Repeat("a", proxyStderrLimit)))
	_, _ = b.Write([]byte("end"))
	got := b.String()
	if len(got) != proxyStderrLimit || !strings.HasSuffix(got, "end") {
		t.Fatalf("tail length %d suffix %q", len(got), got[len(got)-3:])
	}
}

func TestExpandHomeTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := expandHome("~")
	if err != nil || got != home {
		t.Fatalf("expandHome(~) = %q, %v; want %q", got, err, home)
	}
	sub, err := expandHome("~/.ssh/known_hosts")
	if err != nil || sub != filepath.Join(home, ".ssh", "known_hosts") {
		t.Fatalf("expandHome(~/...) = %q, %v", sub, err)
	}
}
