package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/testutil"
)

func TestCopyMultipleLocal(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "connections.toml")
	hist := filepath.Join(dir, "history.db")
	f1 := filepath.Join(dir, "a.txt")
	f2 := filepath.Join(dir, "b.txt")
	dstDir := filepath.Join(dir, "out")
	notDir := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f1, []byte("alpha"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("beta"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notDir, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Destination must be an existing directory.
	if _, err := execute(t, "--config", cfg, "--history-file", hist, "cp", f1, f2, notDir); err == nil {
		t.Fatal("expected error when multi-source destination is not a directory")
	}

	// Dry run previews both sources without writing.
	out, err := execute(t, "--config", cfg, "--history-file", hist, "cp", "--dry-run", f1, f2, dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "add") || !strings.Contains(out, "a.txt") || !strings.Contains(out, "b.txt") {
		t.Fatalf("unexpected dry-run output: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote file")
	}

	// Actual copy writes both and records two history entries.
	if _, err := execute(t, "--config", cfg, "--history-file", hist, "cp", f1, f2, dstDir); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		name, body string
	}{{"a.txt", "alpha"}, {"b.txt", "beta"}} {
		got, err := os.ReadFile(filepath.Join(dstDir, want.name))
		if err != nil || string(got) != want.body {
			t.Fatalf("%s: got=%q err=%v", want.name, got, err)
		}
	}
}

func TestCopyByTagValidationAndDryRun(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "connections.toml")
	hist := filepath.Join(dir, "history.db")
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"cp", "--tag", "prod", src, "/tmp/out"},
		{"cp", "--tag", "prod", "web:/a.txt", ":/tmp/"},
		{"cp", "--tag", "prod", "--jobs", "0", src, ":/tmp/"},
		{"cp", "--tag", "prod", src, ":/tmp/"},
	} {
		if _, err := execute(t, append([]string{"--config", cfg, "--history-file", hist}, args...)...); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}

	s1 := testutil.StartSSH(t)
	s2 := testutil.StartSSH(t)
	c1 := s1.Connection
	c1.Tags = []string{"web"}
	c2 := s2.Connection
	c2.Tags = []string{"web"}
	if err := (config.Store{Path: cfg}).Update(context.Background(), func(m map[string]config.Connection) error {
		m["web1"] = c1
		m["web2"] = c2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	remoteDir := filepath.Join(dir, "remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmd := New()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--config", cfg, "--history-file", hist, "--known-hosts", s1.KnownHosts, "--trust-new-host", "cp", "--tag", "web", "--dry-run", src, ":" + remoteDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dry-run failed: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "[web1] add") || !strings.Contains(stdout.String(), "[web2] add") {
		t.Fatalf("unexpected dry-run stdout: %q", stdout.String())
	}
}
