package sshclient

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/testutil"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestSOCKSChainSSH(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for _, hops := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(hops), func(t *testing.T) {
			server := testutil.StartSSH(t)
			realTarget := net.JoinHostPort(server.Connection.Host, strconv.Itoa(server.Connection.Port))
			target := "ssh.internal:22"
			routes := map[string]string{target: realTarget}
			proxies := make([]string, hops)
			fixtures := make([]*testutil.SOCKSProxy, hops)
			wants := make([]string, hops)
			next := target
			for i := hops - 1; i >= 0; i-- {
				fixtures[i] = testutil.StartSOCKS5(t, routes)
				wants[i] = next
				name := net.JoinHostPort("hop"+strconv.Itoa(i)+".internal", "1080")
				proxies[i] = "socks5://" + name
				routes = map[string]string{name: fixtures[i].Address}
				next = name
			}
			proxies[0] = "socks5://" + fixtures[0].Address
			c := server.Connection
			c.Host = "ssh.internal"
			c.Port = 22
			c.Proxies = proxies
			known, err := os.ReadFile(server.KnownHosts)
			if err != nil {
				t.Fatal(err)
			}
			key := strings.SplitN(string(known), " ", 2)[1]
			p := filepath.Join(t.TempDir(), "known_hosts")
			if err := os.WriteFile(p, []byte("ssh.internal "+key), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, err := Dial(ctx, c, Options{KnownHosts: p})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			files, err := client.SFTP()
			if err != nil {
				t.Fatal(err)
			}
			defer files.Close()
			if _, err := files.ReadDir(server.Root); err != nil {
				t.Fatal(err)
			}
			for i, fixture := range fixtures {
				select {
				case got := <-fixture.Requests:
					if got != wants[i] {
						t.Fatalf("hop %d got %s want %s", i+1, got, wants[i])
					}
				default:
					t.Fatalf("hop %d bypassed", i+1)
				}
			}
			// A proxy route must not bypass host key verification.
			untrusted := filepath.Join(t.TempDir(), "absent")
			if bad, err := Dial(ctx, c, Options{KnownHosts: untrusted}); err == nil {
				bad.Close()
				t.Fatal("untrusted host accepted")
			}
			if err := os.WriteFile(untrusted, []byte(knownhosts.Normalize(fixtures[0].Address)+" "+key), 0600); err != nil {
				t.Fatal(err)
			}
			// A key trusted only for the TCP proxy is not trusted for the destination.
			if bad, err := Dial(ctx, c, Options{KnownHosts: untrusted}); err == nil {
				bad.Close()
				t.Fatal("proxy host identity accepted for SSH destination")
			}

		})
	}
}

func TestSOCKSHandshakeCancellation(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for _, explicitCancel := range []bool{false, true} {
		t.Run(strconv.FormatBool(explicitCancel), func(t *testing.T) {
			server := testutil.StartSSH(t)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			accepted := make(chan struct{})
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(3 * time.Second))
				close(accepted)
				io.Copy(io.Discard, c)
			}()
			defer func() { ln.Close(); <-finished }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := server.Connection
			c.Proxies = []string{ln.Addr().String()}
			if explicitCancel {
				go func() {
					select {
					case <-accepted:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			start := time.Now()
			client, err := Dial(ctx, c, Options{KnownHosts: server.KnownHosts, Timeout: 150 * time.Millisecond})
			if err == nil {
				client.Close()
				t.Fatal("stalled proxy accepted")
			}
			want := context.DeadlineExceeded
			if explicitCancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("want %v got %v", want, err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("proxy handshake did not stop promptly")
			}
		})
	}
}
