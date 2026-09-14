//go:build darwin || linux

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/testutil"
)

func TestFishRemotePathCompletion(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	home := t.TempDir()
	binary := filepath.Join(home, "bin", "ah")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/ah")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	server := testutil.StartSSH(t)
	configPath := filepath.Join(home, "connections.toml")
	if err := (config.Store{Path: configPath}).Update(ctx, func(m map[string]config.Connection) error { m["nas"] = server.Connection; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"report.csv", "季度 报告.csv"} {
		if err := os.WriteFile(filepath.Join(server.Root, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(server.Root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	generator := exec.CommandContext(ctx, binary, "completion", "fish")
	script, err := generator.CombinedOutput()
	if err != nil {
		t.Fatalf("generate fish: %v %s", err, script)
	}
	scriptPath := filepath.Join(home, "ah.fish")
	if err := os.WriteFile(scriptPath, script, 0600); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "'", "\\'") + "'"
	}
	for _, program := range []string{"ah", "./bin/ah"} {
		for _, source := range []string{"", "./README.md "} {
			line := program + " --config " + quote(configPath) + " --known-hosts " + quote(server.KnownHosts) + " cp " + source + "nas:" + server.Root + "/"
			cmd := exec.CommandContext(ctx, fish, "--no-config", "-c", "source "+quote(scriptPath)+"; complete -C "+quote(line))
			cmd.Dir = home
			cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+filepath.Dir(binary)+":"+os.Getenv("PATH"), "SSH_AUTH_SOCK=")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("fish: %v %s", err, out)
			}
			for _, want := range []string{"report.csv", "季度", "folder/"} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("%s missing %q: %s", line, want, out)
				}
			}
		}
	}
}
