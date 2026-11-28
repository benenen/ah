//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/testutil"
	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

func TestConnectRemoteCommand(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for _, tc := range []struct {
		name     string
		args     []string
		command  string
		code     uint32
		terminal bool
	}{
		{"flags", []string{"c", "nas", "ls", "-lah", "/home", "--help"}, "ls -lah /home --help", 0, false},
		{"shell expression", []string{"connect", "nas", "printf '%s\\n' 'hello world' | cat"}, "printf '%s\\n' 'hello world' | cat", 0, false},
		{"exit status", []string{"c", "nas", "exit", "7"}, "exit 7", 7, false},
		{"terminal without pty", []string{"c", "nas", "pwd"}, "pwd", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan string, 1)
			server := testutil.StartCommandSSH(t, func(command string, channel ssh.Channel) uint32 {
				received <- command
				if !tc.terminal {
					_, _ = io.Copy(channel, channel)
				}
				_, _ = io.WriteString(channel, "stdout\n")
				_, _ = io.WriteString(channel.Stderr(), "stderr\n")
				return tc.code
			})
			p := filepath.Join(t.TempDir(), "connections.toml")
			if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error { m["nas"] = server.Connection; return nil }); err != nil {
				t.Fatal(err)
			}
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer writer.Close()
			if tc.terminal {
				master, terminal, err := pty.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer master.Close()
				defer terminal.Close()
				input = terminal
			} else {
				if _, err := writer.Write([]byte("piped input\n")); err != nil {
					t.Fatal(err)
				}
				writer.Close()
			}
			cmd := New()
			var out, stderr bytes.Buffer
			cmd.SetIn(input)
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			cmd.SetArgs(append([]string{"--config", p, "--known-hosts", server.KnownHosts}, tc.args...))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err = cmd.ExecuteContext(ctx)
			if tc.code == 0 && err != nil {
				t.Fatal(err)
			}
			if tc.code != 0 {
				var exit *ssh.ExitError
				if !errors.As(err, &exit) || exit.ExitStatus() != int(tc.code) {
					t.Fatalf("exit: %v", err)
				}
			}
			select {
			case got := <-received:
				if got != tc.command {
					t.Fatalf("command=%q want %q", got, tc.command)
				}
			default:
				t.Fatal("no exec request")
			}
			want := "stdout\n"
			if !tc.terminal {
				want = "piped input\n" + want
			}
			if out.String() != want || stderr.String() != "stderr\n" {
				t.Fatalf("stdout=%q stderr=%q", out.String(), stderr.String())
			}
		})
	}
}

func TestConnectRemoteCommandPTYFlags(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		args     []string
		terminal bool
		wantPTY  bool
		wantCmd  string
		wantWarn bool
	}{
		{"-t with tty", []string{"c", "-t", "nas", "htop"}, true, true, "htop", false},
		{"-t without tty", []string{"c", "-t", "nas", "htop"}, false, false, "htop", true},
		{"-tt without tty", []string{"c", "-tt", "nas", "htop"}, false, true, "htop", false},
		{"-t after name goes to remote", []string{"c", "nas", "cmd", "-t"}, false, false, "cmd -t", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &ssh.ServerConfig{NoClientAuth: true}
			cfg.AddHostKey(signer)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			gotPTY := make(chan bool, 1)
			gotCmd := make(chan string, 1)
			go func() {
				raw, err := ln.Accept()
				if err != nil {
					return
				}
				defer raw.Close()
				server, chans, reqs, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(reqs)
				ch := <-chans
				channel, requests, err := ch.Accept()
				if err != nil {
					return
				}
				defer channel.Close()
				ptySeen := false
				for req := range requests {
					switch req.Type {
					case "pty-req":
						ptySeen = true
						_ = req.Reply(true, nil)
					case "exec":
						gotPTY <- ptySeen
						if len(req.Payload) >= 4 {
							n := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
							if 4+n <= len(req.Payload) {
								gotCmd <- string(req.Payload[4 : 4+n])
							}
						}
						_ = req.Reply(true, nil)
						_, _ = io.WriteString(channel, "ok\n")
						_, _ = channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
						_ = channel.Close()
						_ = server.Wait()
						return
					default:
						_ = req.Reply(false, nil)
					}
				}
			}()
			p := filepath.Join(t.TempDir(), "connections.toml")
			addr := ln.Addr().(*net.TCPAddr)
			if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error {
				m["nas"] = config.Connection{Host: "127.0.0.1", Port: addr.Port, User: "alice"}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var input *os.File
			if tc.terminal {
				master, terminal, err := pty.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer master.Close()
				defer terminal.Close()
				input = terminal
			} else {
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				_ = w.Close()
				input = r
			}
			cmd := New()
			var out, stderr bytes.Buffer
			cmd.SetIn(input)
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			cmd.SetArgs(append([]string{"--config", p, "--known-hosts", filepath.Join(t.TempDir(), "hosts"), "--trust-new-host"}, tc.args...))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := cmd.ExecuteContext(ctx); err != nil {
				t.Fatal(err)
			}
			if pty := <-gotPTY; pty != tc.wantPTY {
				t.Fatalf("pty=%v want %v", pty, tc.wantPTY)
			}
			if c := <-gotCmd; c != tc.wantCmd {
				t.Fatalf("cmd=%q want %q", c, tc.wantCmd)
			}
			hasWarn := strings.Contains(stderr.String(), "Pseudo-terminal will not be allocated")
			if hasWarn != tc.wantWarn {
				t.Fatalf("warn=%v want %v (stderr=%q)", hasWarn, tc.wantWarn, stderr.String())
			}
		})
	}
}
