//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
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

func TestUnknownHostKeyPrompt(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for _, tc := range []struct {
		name    string
		answer  string
		trusted bool
		prompt  bool
		flag    bool
	}{
		{"yes trusts the key", "yes\n", true, true, false},
		{"no refuses the key", "no\n", false, true, false},
		{"--trust-new-host skips the prompt", "", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := testutil.StartCommandSSH(t, func(string, ssh.Channel) uint32 { return 0 })
			p := filepath.Join(t.TempDir(), "connections.toml")
			if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error {
				m["nas"] = server.Connection
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			known := filepath.Join(t.TempDir(), "known_hosts")
			master, terminal, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = master.Close() }()
			defer func() { _ = terminal.Close() }()
			if _, err = master.Write([]byte(tc.answer)); err != nil {
				t.Fatal(err)
			}
			cmd := New()
			var out, stderr bytes.Buffer
			cmd.SetIn(terminal)
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			args := []string{"--config", p, "--known-hosts", known}
			if tc.flag {
				args = append(args, "--trust-new-host")
			}
			cmd.SetArgs(append(args, "c", "nas", "true"))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = cmd.ExecuteContext(ctx)
			prompted := strings.Contains(stderr.String(), "(yes/no)?")
			if prompted != tc.prompt {
				t.Fatalf("prompt=%v want %v: %q", prompted, tc.prompt, stderr.String())
			}
			saved, readErr := os.ReadFile(known)
			if !tc.trusted {
				if err == nil {
					t.Fatal("declined host key was accepted")
				}
				if !os.IsNotExist(readErr) {
					t.Fatalf("declined host key was saved: %q %v", saved, readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(server.KnownHosts)
			if err != nil {
				t.Fatal(err)
			}
			if string(saved) != string(want) {
				t.Fatalf("saved %q want %q", saved, want)
			}
			// The second connection reuses the saved trust and does not prompt again.
			stderr.Reset()
			cmd = New()
			cmd.SetIn(terminal)
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			cmd.SetArgs([]string{"--config", p, "--known-hosts", known, "c", "nas", "true"})
			if err = cmd.ExecuteContext(ctx); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stderr.String(), "can't be established") {
				t.Fatalf("prompted for a trusted host: %q", stderr.String())
			}
		})
	}
}

func TestUnknownHostKeyWithoutTerminalFailsClosed(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartSSH(t)
	p := filepath.Join(t.TempDir(), "connections.toml")
	if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error {
		m["nas"] = server.Connection
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(t.TempDir(), "known_hosts")
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	defer func() { _ = writer.Close() }()
	cmd := New()
	cmd.SetIn(input)
	cmd.SetOut(&bytes.Buffer{})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--config", p, "--known-hosts", known, "c", "nas", "true"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = cmd.ExecuteContext(ctx)
	if err == nil || !strings.Contains(err.Error(), "unknown host") {
		t.Fatalf("noninteractive connect: %q %v", stderr.String(), err)
	}
	if strings.Contains(stderr.String(), "can't be established") {
		t.Fatalf("noninteractive connect prompted: %q", stderr.String())
	}
	if _, statErr := os.Stat(known); !os.IsNotExist(statErr) {
		t.Fatalf("noninteractive connect saved trust: %v", statErr)
	}
}
