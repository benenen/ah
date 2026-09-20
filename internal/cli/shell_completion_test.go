//go:build darwin || linux

package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/testutil"
	"github.com/creack/pty"
)

// TestShellTabCompletion executes actual interactive Tab-completed commands, so
// quoting or colon handling bugs cannot hide behind correct __complete output.
func TestShellTabCompletion(t *testing.T) {
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), time.Minute)
	defer cancelBuild()
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "ah")
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "./cmd/ah")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	for _, shell := range []string{"bash", "zsh"} {
		for _, mode := range []string{"remote-remote", "local-local", "local-remote"} {
			for _, quoting := range []struct{ name, mark string }{{"unquoted", ""}, {"single", "'"}, {"double", "\""}} {
				t.Run(shell+"/"+mode+"/"+quoting.name, func(t *testing.T) {
					shellPath, err := exec.LookPath(shell)
					if err != nil {
						t.Skipf("%s unavailable: %v", shell, err)
					}
					a, b := testutil.StartSSH(t), testutil.StartSSH(t)
					home := t.TempDir()
					if err := os.Symlink(binDir, filepath.Join(home, "bin")); err != nil {
						t.Fatal(err)
					}
					configPath := filepath.Join(home, "connections.toml")
					if err := (config.Store{Path: configPath}).Update(context.Background(), func(m map[string]config.Connection) error { m["A"] = a.Connection; m["B"] = b.Connection; return nil }); err != nil {
						t.Fatal(err)
					}
					ka, err := os.ReadFile(a.KnownHosts)
					if err != nil {
						t.Fatal(err)
					}
					kb, err := os.ReadFile(b.KnownHosts)
					if err != nil {
						t.Fatal(err)
					}
					known := filepath.Join(home, "known_hosts")
					if err := os.WriteFile(known, append(ka, kb...), 0600); err != nil {
						t.Fatal(err)
					}
					sourceName := "source 中文$(touch INJECTED) 'quote' \\ [x];!.txt"
					destinationName := "destination 中文$(touch DEST_INJECTED) 'quote' [x];!"
					sourceRoot, targetRoot := a.Root, b.Root
					if mode == "local-local" {
						sourceRoot, targetRoot = home, home
					} else if mode == "local-remote" {
						sourceRoot = home
					}
					payload := []byte("Tab completed both independent paths\n")
					if err := os.WriteFile(filepath.Join(sourceRoot, sourceName), payload, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(filepath.Join(targetRoot, destinationName), 0700); err != nil {
						t.Fatal(err)
					}
					script := exec.CommandContext(buildCtx, binary, "completion", shell)
					scriptBytes, err := script.Output()
					if err != nil {
						t.Fatal(err)
					}
					scriptPath := filepath.Join(home, "completion."+shell)
					if err := os.WriteFile(scriptPath, scriptBytes, 0600); err != nil {
						t.Fatal(err)
					}
					debugPath := filepath.Join(home, "completion-debug.log")
					t.Cleanup(func() {
						if t.Failed() {
							if data, err := os.ReadFile(debugPath); err == nil {
								t.Logf("completion debug:\n%s", data)
							}
						}
					})
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					args := []string{"--noprofile", "--norc", "-i"}
					if shell == "zsh" {
						args = []string{"-f", "-i"}
					}
					cmd := exec.CommandContext(ctx, shellPath, args...)
					cmd.Dir = home
					cmd.Env = []string{"PATH=" + binDir + ":/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + home, "ZDOTDIR=" + home, "TERM=xterm", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8", "PS1=ah-test> ", "PS2=continuation> ", "SSH_AUTH_SOCK=", "BASH_SILENCE_DEPRECATION_WARNING=1", "BASH_COMP_DEBUG_FILE=" + debugPath}
					terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 240})
					if err != nil {
						t.Fatal(err)
					}
					output := make(chan string, 32)
					readDone := make(chan struct{})
					go func() {
						defer close(output)
						defer close(readDone)
						buf := make([]byte, 4096)
						for {
							n, err := terminal.Read(buf)
							if n > 0 {
								select {
								case output <- string(buf[:n]):
								case <-ctx.Done():
									return
								}
							}
							if err != nil {
								return
							}
						}
					}()
					t.Cleanup(func() {
						cancel()
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
						_ = terminal.Close()
						_ = cmd.Wait()
						<-readDone
					})
					var transcript strings.Builder
					write := func(s string) {
						t.Helper()
						if _, err := terminal.Write([]byte(s)); err != nil {
							t.Fatal(err)
						}
					}
					await := func(marker string) {
						t.Helper()
						for !strings.Contains(transcript.String(), marker) {
							select {
							case chunk, ok := <-output:
								if !ok {
									t.Fatalf("shell closed before %q:\n%s", marker, transcript.String())
								}
								transcript.WriteString(chunk)
							case <-ctx.Done():
								t.Fatalf("shell timeout waiting for %q:\n%s", marker, transcript.String())
							}
						}
					}
					quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
					init := ""
					if shell == "zsh" {
						// -u uses zsh's own function directories without the compaudit
						// security check: an insecure one makes compinit ask whether to
						// continue, and a prompted shell never prints AH_READY.
						init = "autoload -Uz compinit; compinit -D -u; "
					}
					write(init + "source " + quote(scriptPath) + "; printf '\\nAH_READY\\n'\n")
					await("\r\nAH_READY\r\n")
					sourceInput := "A\t~/sou\t"
					if quoting.mark != "" {
						sourceInput = quoting.mark + "A:~/sou\t"
					}
					targetInput := "B\t~/des\t"
					if mode == "local-local" {
						sourceInput = quoting.mark + "./sou\t"
						targetInput = quoting.mark + "./des\t"
						// Zsh keeps a directory completion open for further path input.
						if shell == "zsh" {
							targetInput += quoting.mark
						}
					}
					if mode == "local-remote" {
						sourceInput = quoting.mark + "./sou\t"
					}
					write(fmt.Sprintf("./bin/ah --config %s --known-hosts %s cp %s %s\n", quote(configPath), quote(known), sourceInput, targetInput))
					write("printf '\\nAH_RESULT:%s\\n' \"$?\"\n")
					await("\r\nAH_RESULT:")
					// Wait through the status line instead of mistaking an echoed command for output.
					for {
						status := strings.SplitN(transcript.String(), "\r\nAH_RESULT:", 2)[1]
						if end := strings.Index(status, "\r\n"); end >= 0 {
							if status[:end] != "0" {
								t.Fatalf("completed command exited %s:\n%s", status[:end], transcript.String())
							}
							break
						}
						select {
						case chunk, ok := <-output:
							if !ok {
								t.Fatalf("shell closed before status line:\n%s", transcript.String())
							}
							transcript.WriteString(chunk)
						case <-ctx.Done():
							t.Fatalf("shell timed out before status line:\n%s", transcript.String())
						}
					}
					got, err := os.ReadFile(filepath.Join(targetRoot, destinationName, sourceName))
					if err != nil || string(got) != string(payload) {
						t.Fatalf("completed copy: %v data=%q\n%s", err, got, transcript.String())
					}
					for _, marker := range []string{"INJECTED", "DEST_INJECTED"} {
						if _, err := os.Stat(filepath.Join(home, marker)); !os.IsNotExist(err) {
							t.Fatalf("completion executed filename as shell code: %v\n%s", err, transcript.String())
						}
					}
				})
			}
		}
	}

}
