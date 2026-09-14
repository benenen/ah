//go:build darwin || linux

package cli

import (
	"bufio"
	"context"
	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/credentials"
	"github.com/benenen/ah/internal/testutil"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSudoPasswordConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "connections.toml")
	key := filepath.Join(dir, "master.key")
	flags := []string{"--config", p, "--key-file", key}
	args := append(append([]string{}, flags...), "new", "nas", "--host", "example.com", "--user", "alice", "--sudo", "--sudo-password")
	if out, err := passwordCommand(t, "sudo-secret", args...); err != nil || strings.Contains(out, "sudo-secret") {
		t.Fatalf("new sudo: %v", err)
	}
	m, err := (config.Store{Path: p}).Load()
	if err != nil {
		t.Fatal(err)
	}
	c := m["nas"]
	if !c.Sudo || c.SudoPassword == "" {
		t.Fatal("sudo settings missing")
	}
	secret, err := (credentials.Store{Path: key}).Decrypt("nas/sudo", c.SudoPassword)
	if err != nil || string(secret) != "sudo-secret" {
		t.Fatal("sudo decrypt", err)
	}
	clear(secret)
	if _, err := (credentials.Store{Path: key}).Decrypt("nas", c.SudoPassword); err == nil {
		t.Fatal("sudo password interchangeable with SSH password")
	}
	raw, err := os.ReadFile(p)
	if err != nil || strings.Contains(string(raw), "sudo-secret") {
		t.Fatal("plaintext saved", err)
	}
	if _, err := execute(t, append(append([]string{}, flags...), "edit", "nas", "--clear-sudo-password", "--sudo=false")...); err != nil {
		t.Fatal(err)
	}
	m, err = (config.Store{Path: p}).Load()
	if err != nil || m["nas"].Sudo || m["nas"].SudoPassword != "" {
		t.Fatal("sudo clear failed", err)
	}
}

func TestSudoManualCopyAndSavedCompletion(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartPTYCommandSSH(t, func(command string, ch ssh.Channel) uint32 {
		if !strings.HasPrefix(command, "sudo ") {
			return 90
		}
		prompt := regexp.MustCompile(`AH_SUDO_PROMPT_[a-f0-9]+`).FindString(command)
		ready := regexp.MustCompile(`AH_SUDO_READY_[a-f0-9]+`).FindString(command)
		if strings.Contains(command, "/bin/sh -l") {
			io.WriteString(ch, prompt)
		} else {
			io.WriteString(ch.Stderr(), prompt)
		}
		line, err := bufio.NewReader(ch).ReadString('\n')
		if err != nil || line != "sudo-secret\n" {
			return 1
		}
		io.WriteString(ch, ready)
		if !strings.Contains(command, "sftp-server") {
			io.WriteString(ch, "root\n")
			return 0
		}
		files, err := sftp.NewServer(ch)
		if err != nil {
			return 92
		}
		defer files.Close()
		files.Serve()
		return 0
	})
	dir := t.TempDir()
	p := filepath.Join(dir, "connections.toml")
	key := filepath.Join(dir, "master.key")
	c := server.Connection
	c.Sudo = true
	if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error { m["nas"] = c; return nil }); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--config", p, "--key-file", key, "--known-hosts", server.KnownHosts, "--history-file", filepath.Join(dir, "history.db")}
	args := func(values ...string) []string { return append(append([]string{}, flags...), values...) }
	src := filepath.Join(dir, "payload.txt")
	dst := filepath.Join(server.Root, "sudo-copy.txt")
	if err := os.WriteFile(src, []byte("sudo payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := passwordCommand(t, "sudo-secret", args("cp", src, "nas:"+dst)...); err != nil || strings.Contains(out, "sudo-secret") {
		t.Fatalf("manual sudo copy: %s %v", out, err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "sudo payload" {
		t.Fatal("sudo copy data", err)
	}
	if out, err := passwordCommand(t, "sudo-secret", args("c", "nas", "whoami")...); err != nil || !strings.Contains(out, "root") {
		t.Fatalf("manual sudo command: %s %v", out, err)
	}
	if out, err := passwordCommand(t, "sudo-secret", args("c", "nas")...); err != nil || !strings.Contains(out, "root") {
		t.Fatalf("manual sudo shell: %s %v", out, err)
	}
	completionArgs := args("__complete", "cp", "nas:"+filepath.Join(server.Root, "sudo-c"))
	if out, err := execute(t, completionArgs...); err != nil || strings.Contains(out, "sudo-copy.txt") {
		t.Fatalf("completion prompted or bypassed sudo: %s %v", out, err)
	}
	if _, err := passwordCommand(t, "sudo-secret", args("edit", "nas", "--sudo-password")...); err != nil {
		t.Fatal(err)
	}
	if out, err := execute(t, completionArgs...); err != nil || !strings.Contains(out, "sudo-copy.txt") {
		t.Fatalf("saved sudo completion: %s %v", out, err)
	}
}
