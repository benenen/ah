//go:build linux || darwin

package cli

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func TestForwardDaemonLifecycle(t *testing.T) {
	cacheOutput, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	caches := strings.Split(strings.TrimSpace(string(cacheOutput)), "\n")
	if len(caches) != 2 {
		t.Fatalf("Go caches: %s", cacheOutput)
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// On macOS os.UserConfigDir uses HOME.
	t.Setenv("HOME", dir)
	t.Setenv("SSH_AUTH_SOCK", "")
	f := testutil.StartForwardSSH(t, func(ch ssh.NewChannel) {
		channel, requests, err := ch.Accept()
		if err != nil {
			return
		}
		defer func() { _ = channel.Close() }()
		go ssh.DiscardRequests(requests)
		data, err := io.ReadAll(channel)
		if err == nil {
			_, _ = channel.Write(data)
		}
		_ = channel.CloseWrite()
	})
	path := filepath.Join(dir, "connections.toml")
	if err := (config.Store{Path: path}).Update(context.Background(), func(m map[string]config.Connection) error {
		m["fixture"] = f.Connection
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "ah-bin")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), time.Minute)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "./cmd/ah")
	build.Dir = "../.."
	// Preserve the existing build cache/module cache despite our isolated HOME.
	build.Env = append(os.Environ(), "GOCACHE="+caches[0], "GOMODCACHE="+caches[1])
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", output, err)
	}
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		base := []string{"--config", path, "--known-hosts", f.KnownHosts, "--timeout", "2s", "forward"}
		c := exec.CommandContext(ctx, binary, append(base, args...)...)
		output, err := c.CombinedOutput()
		return strings.TrimSpace(string(output)), err
	}
	unusedPort := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		_ = ln.Close()
		return port
	}
	port1, port2 := unusedPort(), unusedPort()
	for port1 == port2 {
		port2 = unusedPort()
	}
	id1, err := run("fixture", port1, "80", "-d")
	if err != nil {
		t.Fatalf("start: %s: %v", id1, err)
	}
	t.Cleanup(func() { _, _ = run("kill", id1) })
	id2, err := run("fixture", port2, "80", "-d")
	if err != nil {
		t.Fatalf("start second: %s: %v", id2, err)
	}
	t.Cleanup(func() { _, _ = run("kill", id2) })
	if id1 == id2 || len(id1) != 16 || len(id2) != 16 {
		t.Fatalf("IDs: %q %q", id1, id2)
	}
	assertEcho := func(port string) {
		t.Helper()
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = c.Write([]byte("daemon survives its parent"))
		_ = c.(*net.TCPConn).CloseWrite()
		data, err := io.ReadAll(c)
		if err != nil || string(data) != "daemon survives its parent" {
			t.Fatalf("echo: %q %v", data, err)
		}
	}
	assertEcho(port1)
	assertEcho(port2)
	list, err := run("ls")
	if err != nil || !strings.Contains(list, id1) || !strings.Contains(list, id2) || strings.Count(list, "running") != 2 {
		t.Fatalf("list: %s %v", list, err)
	}
	// An occupied port must report startup failure, not an apparently valid ID.
	if output, err := run("fixture", port1, "80", "-d"); err == nil || !strings.Contains(output, "listen") {
		t.Fatalf("occupied port: %s %v", output, err)
	}
	if output, err := run("kill", id1); err != nil {
		t.Fatalf("stop: %s %v", output, err)
	}
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+port1, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("stopped listener still accepts connections")
	}
	assertEcho(port2)
	if output, err := run("kill", id2); err != nil {
		t.Fatalf("stop second: %s %v", output, err)
	}
	if _, err := run("kill", "../connections"); err == nil {
		t.Fatal("accepted invalid ID")
	}
	list, err = run("ls")
	if err != nil || strings.Contains(list, "running") || strings.Count(list, "stopped") != 2 {
		t.Fatalf("final list: %s %v", list, err)
	}
}
