//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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
