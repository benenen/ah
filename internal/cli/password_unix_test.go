//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/testutil"
	"github.com/creack/pty"
)

func passwordCommand(t *testing.T, password string, args ...string) (string, error) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := New()
	cmd.SetArgs(args)
	cmd.SetIn(slave)
	var out bytes.Buffer
	ready := make(chan struct{})
	cmd.SetOut(&out)
	cmd.SetErr(&passwordPromptWriter{out: &out, ready: ready})
	typed := make(chan struct{})
	go func() {
		defer close(typed)
		select {
		case <-ready:
			_, _ = master.Write([]byte(password + "\r"))
		case <-ctx.Done():
		}
	}()
	err = cmd.ExecuteContext(ctx)
	cancel()
	<-typed
	return out.String(), err
}

type passwordPromptWriter struct {
	out   *bytes.Buffer
	ready chan struct{}
	sent  bool
}

func (w *passwordPromptWriter) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	if !w.sent && strings.Contains(string(p), "password:") {
		close(w.ready)
		w.sent = true
	}
	return n, err
}

func TestEncryptedPasswordCreateCopyCompleteAndEdit(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	a, b := testutil.StartPasswordSSH(t, "a-secret"), testutil.StartPasswordSSH(t, "b-secret")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "connections.toml")
	keyPath := filepath.Join(dir, "master.key")
	known := filepath.Join(dir, "known_hosts")
	ka, _ := os.ReadFile(a.KnownHosts)
	kb, _ := os.ReadFile(b.KnownHosts)
	if err := os.WriteFile(known, append(ka, kb...), 0600); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--config", configPath, "--key-file", keyPath, "--known-hosts", known}
	for _, tc := range []struct {
		name, password string
		server         *testutil.SSHServer
	}{{"A", "a-secret", a}, {"B", "b-secret", b}} {
		args := append(append([]string{}, flags...), "new", tc.name, "--host", tc.server.Connection.Host, "--port", strconv.Itoa(tc.server.Connection.Port), "--user", "test", "--password")
		out, err := passwordCommand(t, tc.password, args...)
		if err != nil {
			t.Fatalf("new: %v %s", err, out)
		}
		if strings.Contains(out, tc.password) {
			t.Fatal("password printed")
		}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "a-secret") || strings.Contains(string(data), "b-secret") || !strings.Contains(string(data), "enc:v1:") {
		t.Fatal("TOML must contain encrypted passwords only")
	}
	if err := os.WriteFile(filepath.Join(a.Root, "report.txt"), []byte("password copy"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		return execute(t, append(append([]string{}, flags...), args...)...)
	}
	if out, err := run("cp", "A:~/report.txt", "B:~/copied.txt"); err != nil {
		t.Fatalf("copy: %s %v", out, err)
	}
	if got, err := os.ReadFile(filepath.Join(b.Root, "copied.txt")); err != nil || string(got) != "password copy" {
		t.Fatal("copy bytes", err)
	}
	for _, args := range [][]string{{"__complete", "cp", "A:~/rep"}, {"__complete", "cp", "A:~/report.txt", "B:~/cop"}} {
		out, err := run(args...)
		if err != nil || (!strings.Contains(out, "report.txt") && !strings.Contains(out, "copied.txt")) {
			t.Fatalf("password completion: %s %v", out, err)
		}
	}
	if _, err := passwordCommand(t, "wrong-password", append(append([]string{}, flags...), "edit", "B", "--password")...); err != nil {
		t.Fatal(err)
	}
	if out, err := run("cp", "A:~/report.txt", "B:~/wrong.txt"); err == nil || strings.Contains(out, "wrong-password") {
		t.Fatal("wrong password accepted or exposed")
	}
	if _, err := run("edit", "B", "--clear-password"); err != nil {
		t.Fatal(err)
	}
	entries, err := (config.Store{Path: configPath}).Load()
	if err != nil {
		t.Fatal(err)
	}
	// Read persisted data as TOML text to keep the assertion about the public format.
	_ = entries
	data, _ = os.ReadFile(configPath)
	section := strings.Split(string(data), "[connections.B]")[1]
	if strings.Contains(section, "password") {
		t.Fatal("clear-password did not remove ciphertext")
	}
	before := append([]byte(nil), data...)
	if _, err := run("new", "noninteractive", "--host", "example.com", "--user", "test", "--password"); err == nil {
		t.Fatal("nonterminal password prompt accepted")
	}
	data, _ = os.ReadFile(configPath)
	if !bytes.Equal(before, data) {
		t.Fatal("failed prompt changed config")
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatal(fmt.Errorf("master key missing: %w", err))
	}
}
