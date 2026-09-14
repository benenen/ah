//go:build darwin || linux

package sshclient

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/testutil"
	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

func TestSudoShellMergedPTYPrompt(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartPTYCommandSSH(t, func(command string, ch ssh.Channel) uint32 {
		prompt := regexp.MustCompile(`AH_SUDO_PROMPT_[a-f0-9]+`).FindString(command)
		ready := regexp.MustCompile(`AH_SUDO_READY_[a-f0-9]+`).FindString(command)
		if prompt == "" || ready == "" || !strings.Contains(command, "/bin/sh -l") {
			return 90
		}
		io.WriteString(ch, prompt)
		line, err := bufio.NewReader(ch).ReadString('\n')
		if err != nil || line != "sudo-secret\n" {
			return 1
		}
		io.WriteString(ch, ready+"root shell\n")
		return 0
	})
	c := server.Connection
	c.Sudo = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := Dial(ctx, c, Options{KnownHosts: server.KnownHosts, SudoPassword: func(context.Context) ([]byte, error) { return []byte("sudo-secret"), nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer terminal.Close()
	var out bytes.Buffer
	if err := client.Shell(terminal, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if out.String() != "root shell\n" {
		t.Fatalf("output %q", out.String())
	}
}

func TestSudoStderrStreamsAfterAuthentication(t *testing.T) {
	var out bytes.Buffer
	writer := &sudoPromptWriter{output: &out, prompt: []byte("long authentication prompt")}
	writer.Write([]byte("welcome"))
	writer.finishAuth()
	writer.Write([]byte("Enter value: "))
	if out.String() != "welcomeEnter value: " {
		t.Fatalf("stderr delayed: %q", out.String())
	}
}
