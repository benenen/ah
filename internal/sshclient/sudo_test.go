package sshclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/testutil"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestSudoSFTPAuthentication(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for _, mode := range []string{"password", "passwordless", "wrong", "missing"} {
		t.Run(mode, func(t *testing.T) {
			callbackCount := 0
			server := testutil.StartCommandSSH(t, func(command string, ch ssh.Channel) uint32 {
				if !strings.HasPrefix(command, "sudo ") {
					return 90
				}
				prompt := regexp.MustCompile(`AH_SUDO_PROMPT_[a-f0-9]+`).FindString(command)
				ready := regexp.MustCompile(`AH_SUDO_READY_[a-f0-9]+`).FindString(command)
				if prompt == "" || ready == "" || strings.Contains(command, "sudo-secret") {
					return 91
				}
				if mode != "passwordless" {
					// Split the marker to exercise streaming prompt recognition.
					io.WriteString(ch.Stderr(), prompt[:5])
					io.WriteString(ch.Stderr(), prompt[5:])
					line, err := bufio.NewReader(ch).ReadString('\n')
					if err != nil || line != "sudo-secret\n" {
						return 1
					}
				}
				io.WriteString(ch, ready)
				files, err := sftp.NewServer(ch)
				if err != nil {
					return 92
				}
				defer files.Close()
				files.Serve()
				return 0
			})
			c := server.Connection
			c.Sudo = true
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			opts := Options{KnownHosts: server.KnownHosts, Timeout: time.Second}
			if mode != "missing" {
				opts.SudoPassword = func(context.Context) ([]byte, error) {
					callbackCount++
					if mode == "wrong" {
						return []byte("wrong"), nil
					}
					return []byte("sudo-secret"), nil
				}
			}
			client, err := Dial(ctx, c, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			files, err := client.SFTP()
			if mode == "wrong" || mode == "missing" {
				if err == nil {
					files.Close()
					t.Fatal("sudo accepted invalid credentials")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer files.Close()
			if _, err := files.ReadDir(server.Root); err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "passwordless" {
				want = 0
			}
			if callbackCount != want {
				t.Fatalf("password callbacks=%d want %d", callbackCount, want)
			}
		})
	}
}

func TestSudoExecKeepsPasswordOutOfCommandInput(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartCommandSSH(t, func(command string, ch ssh.Channel) uint32 {
		if !strings.HasPrefix(command, "sudo ") || !strings.Contains(command, "cat") {
			return 90
		}
		prompt := regexp.MustCompile(`AH_SUDO_PROMPT_[a-f0-9]+`).FindString(command)
		ready := regexp.MustCompile(`AH_SUDO_READY_[a-f0-9]+`).FindString(command)
		io.WriteString(ch.Stderr(), prompt)
		line, err := bufio.NewReader(ch).ReadString('\n')
		if err != nil || line != "sudo-secret\n" {
			return 1
		}
		io.WriteString(ch, ready)
		io.Copy(ch, ch)
		return 0
	})
	c := server.Connection
	c.Sudo = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := Dial(ctx, c, Options{KnownHosts: server.KnownHosts, SudoPassword: func(context.Context) ([]byte, error) { return []byte("sudo-secret"), nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fmt.Fprint(w, "payload\n")
	w.Close()
	var out, stderr bytes.Buffer
	if err := client.Exec("cat", r, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if out.String() != "payload\n" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", out.String(), stderr.String())
	}
}

func TestSudoPromptCancellation(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartCommandSSH(t, func(command string, ch ssh.Channel) uint32 {
		prompt := regexp.MustCompile(`AH_SUDO_PROMPT_[a-f0-9]+`).FindString(command)
		io.WriteString(ch.Stderr(), prompt)
		io.Copy(io.Discard, ch)
		return 1
	})
	c := server.Connection
	c.Sudo = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := Dial(ctx, c, Options{KnownHosts: server.KnownHosts, Timeout: time.Second, SudoPassword: func(promptCtx context.Context) ([]byte, error) {
		cancel()
		<-promptCtx.Done()
		return nil, promptCtx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	started := time.Now()
	files, err := client.SFTP()
	if err == nil {
		files.Close()
		t.Fatal("canceled sudo accepted")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("sudo cancellation blocked")
	}
}
