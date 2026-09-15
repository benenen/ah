package cli

import (
	"path/filepath"
	"testing"

	"github.com/benenen/ah/internal/config"
)

// The saved TERM lets a connection pin a name the remote terminfo actually has.
func TestTermIsSavedClearedAndValidated(t *testing.T) {
	p := filepath.Join(t.TempDir(), "connections.toml")
	run := func(args ...string) (string, error) { return execute(t, append([]string{"--config", p}, args...)...) }
	if _, err := run("new", "dev", "--host", "example.com", "--user", "alice", "--term", "xterm-256color"); err != nil {
		t.Fatal(err)
	}
	load := func() config.Connection {
		t.Helper()
		got, err := config.Store{Path: p}.Load()
		if err != nil {
			t.Fatal(err)
		}
		return got["dev"]
	}
	if got := load().Term; got != "xterm-256color" {
		t.Fatalf("term %q after new", got)
	}
	if _, err := run("edit", "dev", "--port", "2222"); err != nil {
		t.Fatal(err)
	}
	if got := load().Term; got != "xterm-256color" {
		t.Fatalf("unrelated edit changed term to %q", got)
	}
	if _, err := run("edit", "dev", "--term", "screen-256color"); err != nil {
		t.Fatal(err)
	}
	if got := load().Term; got != "screen-256color" {
		t.Fatalf("term %q after edit", got)
	}
	if _, err := run("edit", "dev", "--clear-term"); err != nil {
		t.Fatal(err)
	}
	if got := load().Term; got != "" {
		t.Fatalf("term %q after clear", got)
	}
	if _, err := run("edit", "dev", "--term", "bad name"); err == nil {
		t.Fatal("term with whitespace accepted")
	}
	if _, err := run("edit", "dev", "--term", "x", "--clear-term"); err == nil {
		t.Fatal("conflicting term flags accepted")
	}
}
