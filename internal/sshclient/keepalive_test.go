package sshclient

import (
	"context"
	"github.com/benenen/ah/internal/testutil"
	"golang.org/x/crypto/ssh"
	"testing"
	"time"
)

func TestKeepAlive(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for _, respond := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "negative-reply"}[respond], func(t *testing.T) {
			requests := make(chan struct{}, 10)
			server := testutil.StartRequestSSH(t, func(_ *ssh.ServerConn, rs <-chan *ssh.Request) {
				for r := range rs {
					if r.Type == "keepalive@openssh.com" {
						requests <- struct{}{}
						if respond {
							_ = r.Reply(false, nil)
						}
					}
				}
			})
			c, err := Dial(context.Background(), server.Connection, Options{KnownHosts: server.KnownHosts, Timeout: 200 * time.Millisecond, KeepAlive: 10 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			select {
			case <-requests:
			case <-time.After(time.Second):
				t.Fatal("no keepalive request")
			}
			if respond {
				select {
				case <-requests:
				case <-time.After(time.Second):
					t.Fatal("negative reply ended keepalive")
				}
				s, err := c.client.NewSession()
				if err != nil {
					t.Fatal("connection lost", err)
				}
				_ = s.Close()
			} else {
				select {
				case <-c.done:
				case <-time.After(time.Second):
					t.Fatal("unresponsive connection retained")
				}
			}
		})
	}
}
