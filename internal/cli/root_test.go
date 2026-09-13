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

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := New()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}
func TestConnectionCRUD(t *testing.T) {
	p := filepath.Join(t.TempDir(), "connections.toml")
	run := func(args ...string) (string, error) { return execute(t, append([]string{"--config", p}, args...)...) }
	if _, err := run("list"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("new", "dev", "--host", "example.com", "--user", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("new", "dev", "--host", "other.example.com", "--user", "bob"); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := run("edit", "dev", "--port", "2222", "--identity-file", "/tmp/key"); err != nil {
		t.Fatal(err)
	}
	got, err := config.Store{Path: p}.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got["dev"].Host != "example.com" || got["dev"].User != "alice" || got["dev"].Port != 2222 || got["dev"].IdentityFile != "/tmp/key" {
		t.Fatalf("edit lost fields: %+v", got)
	}
	if _, err := run("edit", "dev", "--identity-file", ""); err != nil {
		t.Fatal(err)
	}
	out, err := run("list")
	if err != nil || !strings.Contains(out, "dev") || !strings.Contains(out, "2222") {
		t.Fatal("missing list entry", out, err)
	}
	if _, err := run("edit", "dev", "--port", "0"); err == nil {
		t.Fatal("invalid explicit port accepted")
	}
	if _, err := run("rm", "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("rm", "dev"); err == nil {
		t.Fatal("missing deletion accepted")
	}
	if _, err := run("edit", "missing", "--host", "example.com"); err == nil {
		t.Fatal("missing edit accepted")
	}
	if _, err := run("new", "bad", "--host", "example.com"); err == nil {
		t.Fatal("missing user accepted")
	}
}

func TestCPAndBothRemoteCompletionPositions(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	a, b := testutil.StartSSH(t), testutil.StartSSH(t)
	p := filepath.Join(t.TempDir(), "connections.toml")
	if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error { m["A"] = a.Connection; m["B"] = b.Connection; return nil }); err != nil {
		t.Fatal(err)
	}
	ka, _ := os.ReadFile(a.KnownHosts)
	kb, _ := os.ReadFile(b.KnownHosts)
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(known, append(ka, kb...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Root, "a file中文.txt"), []byte("from A to B"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(b.Root, "destination"), 0700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		return execute(t, append([]string{"--config", p, "--known-hosts", known}, args...)...)
	}
	if _, err := run("cp", "A:~/a file中文.txt", "B:~/destination/"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(b.Root, "destination", "a file中文.txt")); err != nil || string(data) != "from A to B" {
		t.Fatal("remote copy failed", err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"__complete", "cp", "A:~/a"}, "A:~/a file中文.txt"},
		{[]string{"__complete", "cp", "A:~/a file中文.txt", "B:~/d"}, "B:~/destination/"},
		{[]string{"__complete", "cp", ""}, "A:"},
	} {
		out, err := run(tc.args...)
		if err != nil || !strings.Contains(out, tc.want) {
			t.Fatalf("completion %v: %q err=%v", tc.args, out, err)
		}
	}
	// Completion must not trust/write unknown hosts even with the interactive flag enabled.
	untrusted := filepath.Join(t.TempDir(), "absent_known_hosts")
	out, err := execute(t, "--config", p, "--known-hosts", untrusted, "--trust-new-host", "__complete", "cp", "A:~/a")
	if err != nil || strings.Contains(out, "a file中文.txt") {
		t.Fatalf("untrusted completion: %q %v", out, err)
	}
	if _, err := os.Stat(untrusted); !os.IsNotExist(err) {
		t.Fatal("completion persisted trust")
	}
}

func TestCompletionScriptsAndInvalidArguments(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		out, err := execute(t, "completion", shell)
		if err != nil || len(out) < 100 {
			t.Fatalf("missing %s completion", shell)
		}
	}
	for _, args := range [][]string{{"cp", "A:a"}, {"new"}, {"completion", "invalid"}, {"--timeout", "0s", "list"}} {
		if _, err := execute(t, args...); err == nil {
			t.Fatalf("accepted invalid args %v", args)
		}
	}
}
