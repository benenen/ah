package testutil

import (
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// StartRemoteForwardSSH serves tcpip-forward requests like OpenSSH: it listens
// on the requested address and opens a forwarded-tcpip channel per connection.
// A non-nil direct handler also accepts direct-tcpip channels.
func StartRemoteForwardSSH(t *testing.T, direct func(ssh.NewChannel)) *SSHServer {
	t.Helper()
	return startSSH(t, nil, nil, false, direct, serveRemoteForwards)
}

func serveRemoteForwards(sc *ssh.ServerConn, requests <-chan *ssh.Request) {
	var mu sync.Mutex
	listeners := map[string]net.Listener{}
	var relays sync.WaitGroup
	defer func() {
		mu.Lock()
		for _, l := range listeners {
			_ = l.Close()
		}
		mu.Unlock()
		relays.Wait()
	}()
	for req := range requests {
		var bind struct {
			Addr string
			Port uint32
		}
		if (req.Type != "tcpip-forward" && req.Type != "cancel-tcpip-forward") || ssh.Unmarshal(req.Payload, &bind) != nil {
			_ = req.Reply(false, nil)
			continue
		}
		key := net.JoinHostPort(bind.Addr, strconv.Itoa(int(bind.Port)))
		if req.Type == "cancel-tcpip-forward" {
			mu.Lock()
			l, ok := listeners[key]
			delete(listeners, key)
			mu.Unlock()
			if ok {
				_ = l.Close()
			}
			_ = req.Reply(ok, nil)
			continue
		}
		l, err := net.Listen("tcp", key)
		if err != nil {
			_ = req.Reply(false, nil)
			continue
		}
		port := uint32(l.Addr().(*net.TCPAddr).Port)
		mu.Lock()
		// Cancellation names the port the client received.
		listeners[net.JoinHostPort(bind.Addr, strconv.Itoa(int(port)))] = l
		mu.Unlock()
		_ = req.Reply(true, ssh.Marshal(struct{ Port uint32 }{port}))
		relays.Go(func() {
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				relays.Go(func() {
					defer func() { _ = conn.Close() }()
					origin := conn.RemoteAddr().(*net.TCPAddr)
					channel, requests, err := sc.OpenChannel("forwarded-tcpip", ssh.Marshal(struct {
						Addr       string
						Port       uint32
						OriginAddr string
						OriginPort uint32
					}{bind.Addr, port, origin.IP.String(), uint32(origin.Port)}))
					if err != nil {
						return
					}
					defer func() { _ = channel.Close() }()
					go ssh.DiscardRequests(requests)
					done := make(chan struct{})
					go func() {
						_, _ = io.Copy(channel, conn)
						_ = channel.CloseWrite()
						close(done)
					}()
					_, _ = io.Copy(conn, channel)
					_ = conn.(*net.TCPConn).CloseWrite()
					<-done
				})
			}
		})
	}
}
