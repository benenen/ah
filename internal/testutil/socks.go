package testutil

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// SOCKSProxy forwards only explicitly mapped destinations, including names that
// cannot be resolved by the client. Requests exposes the actual hop targets.
type SOCKSProxy struct {
	Address  string
	Requests chan string
}

func StartSOCKS5(t *testing.T, routes map[string]string) *SOCKSProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &SOCKSProxy{Address: ln.Addr().String(), Requests: make(chan string, 64)}
	var mu sync.Mutex
	conns := make(map[net.Conn]bool)
	var wg sync.WaitGroup
	closed := false
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				c.Close()
				return
			}
			conns[c] = true
			wg.Add(1)
			mu.Unlock()
			go func() {
				defer wg.Done()
				defer c.Close()
				defer func() { mu.Lock(); delete(conns, c); mu.Unlock() }()
				c.SetDeadline(time.Now().Add(10 * time.Second))
				h := make([]byte, 2)
				if _, err := io.ReadFull(c, h); err != nil || h[0] != 5 {
					return
				}
				methods := make([]byte, int(h[1]))
				if _, err := io.ReadFull(c, methods); err != nil {
					return
				}
				if _, err := c.Write([]byte{5, 0}); err != nil {
					return
				}
				request := make([]byte, 4)
				if _, err := io.ReadFull(c, request); err != nil || request[0] != 5 || request[1] != 1 {
					return
				}
				var host string
				switch request[3] {
				case 1:
					b := make([]byte, 4)
					if _, err := io.ReadFull(c, b); err != nil {
						return
					}
					host = net.IP(b).String()
				case 4:
					b := make([]byte, 16)
					if _, err := io.ReadFull(c, b); err != nil {
						return
					}
					host = net.IP(b).String()
				case 3:
					b := make([]byte, 1)
					if _, err := io.ReadFull(c, b); err != nil {
						return
					}
					name := make([]byte, int(b[0]))
					if _, err := io.ReadFull(c, name); err != nil {
						return
					}
					host = string(name)
				default:
					return
				}
				port := make([]byte, 2)
				if _, err := io.ReadFull(c, port); err != nil {
					return
				}
				target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port))))
				select {
				case p.Requests <- target:
				default:
				}
				mapped, ok := routes[target]
				if !ok {
					c.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				upstream, err := net.DialTimeout("tcp", mapped, time.Second)
				if err != nil {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer upstream.Close()
				if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
					return
				}
				done := make(chan struct{})
				go func() { defer close(done); io.Copy(upstream, c); upstream.Close() }()
				io.Copy(c, upstream)
				c.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() {
		mu.Lock()
		closed = true
		ln.Close()
		for c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return p
}
