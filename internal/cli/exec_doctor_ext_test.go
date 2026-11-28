//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func TestExecScriptAndJSON(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "connections.toml")
	script := filepath.Join(dir, "job.sh")
	if err := os.WriteFile(script, []byte("echo from-script\n"), 0644); err != nil {
		t.Fatal(err)
	}
	emptyScript := filepath.Join(dir, "empty.sh")
	if err := os.WriteFile(emptyScript, nil, 0644); err != nil {
		t.Fatal(err)
	}

	s1 := testutil.StartCommandSSH(t, func(command string, ch ssh.Channel) uint32 {
		data, _ := io.ReadAll(ch)
		_, _ = io.WriteString(ch, "cmd="+command+" stdin="+strings.TrimSpace(string(data))+"\n")
		return 0
	})
	s2 := testutil.StartCommandSSH(t, func(command string, ch ssh.Channel) uint32 {
		_, _ = io.ReadAll(ch)
		_, _ = io.WriteString(ch.Stderr(), "boom\n")
		return 2
	})
	c1 := s1.Connection
	c1.Tags = []string{"app"}
	c2 := s2.Connection
	c2.Tags = []string{"app"}
	if err := (config.Store{Path: cfg}).Update(context.Background(), func(m map[string]config.Connection) error {
		m["app1"] = c1
		m["app2"] = c2
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Reject empty script or mixing --script with command.
	for _, args := range [][]string{
		{"exec", "--tag", "app", "--script", emptyScript},
		{"exec", "--tag", "app", "--script", script, "--", "uname"},
	} {
		if _, err := execute(t, append([]string{"--config", cfg}, args...)...); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}

	// --dry-run --json returns sorted connection names.
	dryOut, err := execute(t, "--config", cfg, "exec", "--tag", "app", "--dry-run", "--json", "--script", script)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal([]byte(dryOut), &names); err != nil || len(names) != 2 || names[0] != "app1" || names[1] != "app2" {
		t.Fatalf("dry-run json=%q err=%v", dryOut, err)
	}

	// Execute script with --json; app2 fails with exit 2 so command returns error while printing JSON.
	cmd := New()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--config", cfg, "--known-hosts", s1.KnownHosts, "--trust-new-host", "exec", "--tag", "app", "--json", "--script", script})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err == nil {
		t.Fatal("expected error when app2 returned exit 2")
	}
	var results []execJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("unmarshal exec json: %v\nstdout=%s", err, stdout.String())
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Name != "app1" || results[0].Status != "success" || results[0].ExitCode == nil || *results[0].ExitCode != 0 || !strings.Contains(results[0].Stdout, "stdin=echo from-script") {
		t.Fatalf("unexpected app1 result: %+v", results[0])
	}
	if results[1].Name != "app2" || results[1].Status != "failed" || results[1].ExitCode == nil || *results[1].ExitCode != 2 || !strings.Contains(results[1].Stderr, "boom\n") {
		t.Fatalf("unexpected app2 result: %+v", results[1])
	}
}

func TestDoctorTagAndJSON(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "connections.toml")
	s1 := testutil.StartSSH(t)
	s2 := testutil.StartSSH(t)
	c1 := s1.Connection
	c1.Tags = []string{"db"}
	c2 := s2.Connection
	c2.Tags = []string{"db"}
	if err := (config.Store{Path: cfg}).Update(context.Background(), func(m map[string]config.Connection) error {
		m["db1"] = c1
		m["db2"] = c2
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Reject combining NAME and --tag, or neither.
	for _, args := range [][]string{
		{"doctor"},
		{"doctor", "db1", "--tag", "db"},
	} {
		if _, err := execute(t, append([]string{"--config", cfg}, args...)...); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}

	// Single host JSON.
	out, err := execute(t, "--config", cfg, "--known-hosts", s1.KnownHosts, "--trust-new-host", "doctor", "--json", "db1")
	if err != nil {
		t.Fatalf("doctor --json db1: %v", err)
	}
	var single doctorReport
	if err := json.Unmarshal([]byte(out), &single); err != nil || !single.Ready || single.Name != "db1" {
		t.Fatalf("unexpected single doctor report: %+v (%v)", single, err)
	}

	// Batch --tag JSON.
	batchOut, err := execute(t, "--config", cfg, "--known-hosts", s1.KnownHosts, "--trust-new-host", "doctor", "--tag", "db", "--json")
	if err != nil {
		t.Fatalf("doctor --tag --json: %v", err)
	}
	var batch []doctorReport
	if err := json.Unmarshal([]byte(batchOut), &batch); err != nil || len(batch) != 2 || !batch[0].Ready || !batch[1].Ready {
		t.Fatalf("unexpected batch doctor report: %+v (%v)", batch, err)
	}
}
