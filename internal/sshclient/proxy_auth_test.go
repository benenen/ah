package sshclient

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestProxyAuthentication(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		read := func(n int) ([]byte, error) { b := make([]byte, n); _, err := io.ReadFull(conn, b); return b, err }
		header, err := read(2)
		if err != nil {
			done <- err
			return
		}
		if _, err = read(int(header[1])); err != nil {
			done <- err
			return
		}
		conn.Write([]byte{5, 2})
		header, err = read(2)
		if err != nil {
			done <- err
			return
		}
		user, err := read(int(header[1]))
		if err != nil {
			done <- err
			return
		}
		size, err := read(1)
		if err != nil {
			done <- err
			return
		}
		password, err := read(int(size[0]))
		if err != nil {
			done <- err
			return
		}
		if string(user) != "test-user" || string(password) != "test-pass" {
			done <- io.ErrUnexpectedEOF
			return
		}
		conn.Write([]byte{1, 0})
		header, err = read(4)
		if err != nil {
			done <- err
			return
		}
		if header[3] != 3 {
			done <- io.ErrUnexpectedEOF
			return
		}
		size, err = read(1)
		if err != nil {
			done <- err
			return
		}
		if _, err = read(int(size[0]) + 2); err != nil {
			done <- err
			return
		}
		_, err = conn.Write(append([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 22}, []byte("HELLO")...))
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialTransport(ctx, "target.invalid:22", []string{"socks5h://test-user:test-pass@" + listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	banner := make([]byte, 5)
	if _, err := io.ReadFull(conn, banner); err != nil {
		t.Fatal(err)
	}
	if string(banner) != "HELLO" {
		t.Fatal("unexpected banner")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
