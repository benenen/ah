//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/testutil"
	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

func saveBatchConnections(t *testing.T, entries map[string]config.Connection) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "connections.toml")
	if err := (config.Store{Path: filename}).Update(context.Background(), func(target map[string]config.Connection) error {
		for name, c := range entries {
			target[name] = c
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestBatchExecConcurrencyAndStreaming(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for _, jobs := range []int{1, 2} {
		t.Run(fmt.Sprint(jobs), func(t *testing.T) {
			var active, peak, calls atomic.Int32
			started := make(chan struct{}, 4)
			release := make(chan struct{})
			var once sync.Once
			commands := make(chan string, 8)
			server := testutil.StartCommandSSH(t, func(command string, ch ssh.Channel) uint32 {
				commands <- command
				// Each host must receive EOF rather than consume the caller's stdin.
				if _, err := io.Copy(io.Discard, ch); err != nil {
					return 99
				}
				n := active.Add(1)
				defer active.Add(-1)
				for previous := peak.Load(); n > previous; previous = peak.Load() {
					if peak.CompareAndSwap(previous, n) {
						break
					}
				}
				calls.Add(1)
				started <- struct{}{}
				<-release
				_, _ = io.WriteString(ch, "hel")
				_, _ = io.WriteString(ch, "lo\nlast")
				_, _ = io.WriteString(ch.Stderr(), "diagnostic\n")
				return 0
			})
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			entries := map[string]config.Connection{}
			for _, name := range []string{"a", "b", "c", "d", "ignored"} {
				c := server.Connection
				c.Tags = []string{"Prod", "web"}
				if name == "ignored" {
					c.Tags = []string{"prod"}
				}
				entries[name] = c
			}
			filename := saveBatchConnections(t, entries)
			cmd := New()
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetIn(strings.NewReader("must not be read"))
			cmd.SetArgs([]string{"--config", filename, "--known-hosts", server.KnownHosts, "exec", "--tag", "prod,WEB", "--jobs", fmt.Sprint(jobs), "--", "printf '%s' 'a b' | cat"})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- cmd.ExecuteContext(ctx) }()
			for range jobs {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("worker did not start")
				}
			}
			if peak.Load() > int32(jobs) {
				t.Fatal("concurrency limit exceeded")
			}
			once.Do(func() { close(release) })
			if err := <-done; err != nil {
				t.Fatal(err, output.String())
			}
			if peak.Load() != int32(jobs) || calls.Load() != 4 {
				t.Fatalf("peak %d calls %d", peak.Load(), calls.Load())
			}
			for range 4 {
				if command := <-commands; command != "printf '%s' 'a b' | cat" {
					t.Fatal(command)
				}
			}
			for _, name := range []string{"a", "b", "c", "d"} {
				if !strings.Contains(output.String(), "["+name+"] hello\n") || !strings.Contains(output.String(), "["+name+"] last\n") {
					t.Fatal(output.String())
				}
				if !strings.Contains(output.String(), "["+name+"] diagnostic\n") {
					t.Fatal("missing labeled stderr")
				}
			}
			if !strings.Contains(output.String(), "4 succeeded, 0 failed") || strings.Contains(output.String(), "[ignored]") {
				t.Fatal(output.String())
			}
		})
	}
}

func TestBatchExecContinuesAfterFailures(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	good := testutil.StartCommandSSH(t, func(_ string, ch ssh.Channel) uint32 { _, _ = io.WriteString(ch, "ok\n"); return 0 })
	bad := testutil.StartCommandSSH(t, func(_ string, ch ssh.Channel) uint32 { _, _ = io.WriteString(ch.Stderr(), "remote-error"); return 7 })
	known := filepath.Join(t.TempDir(), "known_hosts")
	a, _ := os.ReadFile(good.KnownHosts)
	b, _ := os.ReadFile(bad.KnownHosts)
	if err := os.WriteFile(known, append(a, b...), 0600); err != nil {
		t.Fatal(err)
	}
	denied := bad.Connection
	denied.IdentityFile = good.KeyPath
	entries := map[string]config.Connection{"a-bad": bad.Connection, "b-denied": denied, "c-good": good.Connection}
	for name, c := range entries {
		c.Tags = []string{"prod"}
		entries[name] = c
	}
	cfg := saveBatchConnections(t, entries)
	out, err := execute(t, "--config", cfg, "--known-hosts", known, "exec", "--tag", "prod", "--jobs", "1", "--", "true")
	if err == nil || !strings.Contains(out, "[c-good] ok\n") || !strings.Contains(out, "[a-bad] remote-error\n") || !strings.Contains(out, "1 succeeded, 2 failed") {
		t.Fatal(out, err)
	}
	if !strings.Contains(out, "7") || !strings.Contains(out, "b-denied") {
		t.Fatal("missing per-host exit status", out)
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		t.Fatal("aggregate error must not select one host's exit code")
	}
	out, err = execute(t, "--config", cfg, "--known-hosts", filepath.Join(t.TempDir(), "unknown"), "exec", "--tag", "PROD", "--dry-run", "--", "true")
	if err != nil || out != "a-bad\nb-denied\nc-good\n" {
		t.Fatal(out, err)
	}
}

func streamingBatchServer(t *testing.T, started, ended chan struct{}) *testutil.SSHServer {
	t.Helper()
	return testutil.StartCommandSSH(t, func(_ string, ch ssh.Channel) uint32 {
		close(started)
		defer close(ended)
		for {
			if _, err := io.WriteString(ch, "tick\n"); err != nil {
				return 0
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

func TestBatchExecCancellationStopsQueuedHosts(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	started, ended := make(chan struct{}), make(chan struct{})
	server := streamingBatchServer(t, started, ended)
	c := server.Connection
	c.Tags = []string{"prod"}
	cfg := saveBatchConnections(t, map[string]config.Connection{"a": c, "b": c, "c": c})
	cmd := New()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--config", cfg, "--known-hosts", server.KnownHosts, "exec", "--tag", "prod", "--jobs", "1", "--", "loop"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation blocked")
	}
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("SSH connection was not closed")
	}
	if !strings.Contains(output.String(), "1 canceled, 2 not-run") {
		t.Fatal(output.String())
	}
}

func TestBatchExecHostTimeoutContinues(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	started, ended := make(chan struct{}), make(chan struct{})
	slow := streamingBatchServer(t, started, ended)
	fast := testutil.StartCommandSSH(t, func(_ string, ch ssh.Channel) uint32 { _, _ = io.WriteString(ch, "done\n"); return 0 })
	known := filepath.Join(t.TempDir(), "known_hosts")
	a, _ := os.ReadFile(slow.KnownHosts)
	b, _ := os.ReadFile(fast.KnownHosts)
	if err := os.WriteFile(known, append(a, b...), 0600); err != nil {
		t.Fatal(err)
	}
	entries := map[string]config.Connection{"a-slow": slow.Connection, "b-fast": fast.Connection}
	for name, c := range entries {
		c.Tags = []string{"prod"}
		entries[name] = c
	}
	cfg := saveBatchConnections(t, entries)
	out, err := execute(t, "--config", cfg, "--known-hosts", known, "exec", "--tag", "prod", "--jobs", "1", "--exec-timeout", "1s", "--", "run")
	if err == nil || !strings.Contains(out, "[b-fast] done") || !strings.Contains(out, "1 succeeded, 0 failed, 1 canceled") {
		t.Fatal(out, err)
	}
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out connection stayed open")
	}
}

func TestBatchExecNeverPromptsWithTerminal(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartCommandSSH(t, func(_ string, ch ssh.Channel) uint32 { _, _ = io.WriteString(ch, "ok\n"); return 0 })
	c := server.Connection
	c.Tags = []string{"prod"}
	cfg := saveBatchConnections(t, map[string]config.Connection{"host": c})
	known := filepath.Join(t.TempDir(), "new_known_hosts")
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	cmd := New()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetIn(slave)
	args := []string{"--config", cfg, "--known-hosts", known, "exec", "--tag", "prod", "--", "true"}
	cmd.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err == nil || ctx.Err() != nil || strings.Contains(output.String(), "(yes/no)") {
		t.Fatal("batch attempted an interactive prompt", output.String(), err)
	}
	out, err := execute(t, append([]string{"--trust-new-host"}, args...)...)
	if err != nil || !strings.Contains(out, "[host] ok") {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(known); err != nil {
		t.Fatal("explicit host trust not saved", err)
	}
}

type failedBatchWriter struct{ err error }

func (w failedBatchWriter) Write([]byte) (int, error) { return 0, w.err }

func TestBatchExecOutputFailureClosesStreams(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	started, ended := make(chan struct{}), make(chan struct{})
	server := streamingBatchServer(t, started, ended)
	c := server.Connection
	c.Tags = []string{"prod"}
	filename := saveBatchConnections(t, map[string]config.Connection{"host": c})
	failure := errors.New("output sink failed")
	cmd := New()
	cmd.SetOut(failedBatchWriter{failure})
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", filename, "--known-hosts", server.KnownHosts, "exec", "--tag", "prod", "--", "loop"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, failure) || ctx.Err() != nil {
		t.Fatal("output failure did not stop batch promptly", err)
	}
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("output failure left SSH stream active")
	}
}
