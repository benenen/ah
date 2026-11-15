//go:build darwin || linux

package sshclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

func TestInteractiveCommand(t *testing.T) {
	if got := interactiveCommand(config.DefaultShell, false); got != "" {
		t.Fatalf("default on POSIX: %q", got)
	}
	if got := interactiveCommand(config.DefaultShell, true); got != "" {
		t.Fatalf("default on Windows: %q", got)
	}
	if got := interactiveCommand("", true); got != "cmd.exe" {
		t.Fatalf("Windows platform default: %q", got)
	}
	if got := interactiveCommand("pwsh", true); got != "pwsh" {
		t.Fatalf("Windows explicit: %q", got)
	}
	if !isWindowsServer([]byte("SSH-2.0-OpenSSH_for_Windows_9.5")) || isWindowsServer([]byte("SSH-2.0-OpenSSH_9.8p1 Ubuntu-3")) {
		t.Fatal("server platform misdetected")
	}
}

// runSelection executes the POSIX selection command locally with stub
// commands, so the remote script is tested by a real /bin/sh.
func runSelection(t *testing.T, shell, platform string, available ...string) string {
	t.Helper()
	bin := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("uname", "echo "+platform)
	for _, name := range append(available, "login-shell") {
		write(name, `echo "`+name+` $*"`)
	}
	cmd := exec.Command("/bin/sh", "-c", interactiveCommand(shell, false))
	// Only the stubs are on PATH; the script needs nothing but sh builtins.
	cmd.Env = []string{"PATH=" + bin, "SHELL=" + filepath.Join(bin, "login-shell")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPOSIXShellSelection(t *testing.T) {
	for _, tc := range []struct {
		name, shell, platform string
		available             []string
		want                  string
	}{
		{"linux default", "", "Linux", []string{"bash", "zsh"}, "bash -l"},
		{"macOS default", "", "Darwin", []string{"bash", "zsh"}, "zsh -l"},
		{"missing default falls back quietly", "", "Linux", nil, "login-shell -l"},
		{"explicit", "fish", "Linux", []string{"fish", "bash"}, "fish -l"},
		{"missing explicit warns", "fish", "Linux", []string{"bash"}, "ah: shell fish not found, starting the login shell\nlogin-shell -l"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runSelection(t, tc.shell, tc.platform, tc.available...); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// startedShell reports how an interactive session asked the server to start.
func startedShell(t *testing.T, version string, c config.Connection, opts Options) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true, ServerVersion: version}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	started := make(chan string, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		server, chans, requests, err := ssh.NewServerConn(raw, cfg)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		ch, ok := <-chans
		if !ok {
			return
		}
		channel, reqs, err := ch.Accept()
		if err != nil {
			return
		}
		defer channel.Close()
		for req := range reqs {
			switch req.Type {
			case "pty-req":
				_ = req.Reply(true, nil)
			case "shell", "exec":
				var command struct{ Command string }
				_ = ssh.Unmarshal(req.Payload, &command)
				started <- strings.TrimSpace(req.Type + " " + command.Command)
				_ = req.Reply(true, nil)
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			default:
				_ = req.Reply(false, nil)
			}
		}
	}()
	master, terminal, err := pty.Open()
	if err != nil {
		t.Skip("pty unavailable:", err)
	}
	defer master.Close()
	defer terminal.Close()
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Host, c.Port, c.User = "127.0.0.1", ln.Addr().(*net.TCPAddr).Port, "test"
	opts.KnownHosts = filepath.Join(t.TempDir(), "hosts")
	opts.TrustHost = func(string, string) (bool, error) { return true, nil }
	client, err := Dial(ctx, c, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Shell(terminal, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-started:
		return got
	default:
		t.Fatal("no shell was started")
		return ""
	}
}

func TestShellChoiceReachesServer(t *testing.T) {
	const windows = "SSH-2.0-OpenSSH_for_Windows_9.5"
	const linux = "SSH-2.0-OpenSSH_9.8p1"
	for _, tc := range []struct {
		name, version, connection, option, want string
	}{
		{"windows platform default", windows, "", "", "exec cmd.exe"},
		{"windows connection", windows, "pwsh", "", "exec pwsh"},
		{"override wins", windows, "pwsh", "powershell", "exec powershell"},
		{"server default", windows, config.DefaultShell, "", "shell"},
		{"override to server default", linux, "zsh", config.DefaultShell, "shell"},
		{"posix connection", linux, "zsh", "", "exec " + interactiveCommand("zsh", false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := startedShell(t, tc.version, config.Connection{Shell: tc.connection}, Options{Shell: tc.option})
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
