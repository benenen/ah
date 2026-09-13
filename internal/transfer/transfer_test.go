package transfer

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func localSFTP(t *testing.T) *sftp.Client {
	t.Helper()
	a, b := net.Pipe()
	server, err := sftp.NewServer(a)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(); _ = server.Close() }()
	client, err := sftp.NewClientPipe(b, b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(); _ = b.Close(); _ = a.Close(); <-done })
	return client
}

func TestParseEndpoint(t *testing.T) {
	for _, s := range []string{"", "A", "A:", ":/tmp", "bad name:/tmp", "A:/a\nfile"} {
		if _, err := ParseEndpoint(s); err == nil {
			t.Errorf("accepted invalid endpoint %q", s)
		}
	}
	for _, s := range []string{"A:/a b/中文.txt", "B:~/file", "A:relative:name"} {
		e, err := ParseEndpoint(s)
		if err != nil {
			t.Fatal(err)
		}
		if e.Name+":"+e.Path != s {
			t.Fatalf("lost path: %+v", e)
		}
	}
}

func TestCopyPublishesAndProtectsDestination(t *testing.T) {
	src, dst := localSFTP(t), localSFTP(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	data := strings.Repeat("传输\x00data", 50000)
	if err := os.WriteFile(source, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	n, err := Copy(context.Background(), src, dst, source, target, false)
	if err != nil || n != int64(len(data)) {
		t.Fatalf("copy n=%d err=%v", n, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != data {
		t.Fatal("copy corrupted", err)
	}
	if err := os.WriteFile(source, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Copy(context.Background(), src, dst, source, target, false); err == nil {
		t.Fatal("overwrote existing file")
	}
	got, _ = os.ReadFile(target)
	if string(got) != data {
		t.Fatal("original changed on refusal")
	}
	if _, err := Copy(context.Background(), src, dst, source, target, true); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(target)
	if string(got) != "replacement" {
		t.Fatal("force did not replace")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("temporary files leaked: %v", entries)
	}
}

func TestCopyDirectoryAndFailures(t *testing.T) {
	src, dst := localSFTP(t), localSFTP(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "file")
	targetDir := filepath.Join(dir, "dest")
	if err := os.WriteFile(source, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(targetDir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Copy(context.Background(), src, dst, source, targetDir, false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(targetDir, "file")); err != nil || string(b) != "hello" {
		t.Fatal("directory copy failed", err)
	}
	for _, sourcePath := range []string{dir, filepath.Join(dir, "missing")} {
		if _, err := Copy(context.Background(), src, dst, sourcePath, filepath.Join(dir, "never"), false); err == nil {
			t.Fatal("accepted invalid source")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Copy(ctx, src, dst, source, filepath.Join(dir, "canceled"), false); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := os.Stat(filepath.Join(dir, "canceled")); !os.IsNotExist(err) {
		t.Fatal("published canceled copy")
	}
}

// Two concurrent publishers must not overwrite each other's completed output.
func TestConcurrentCopyDoesNotClobber(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	one, two := filepath.Join(dir, "one"), filepath.Join(dir, "two")
	if err := os.WriteFile(one, []byte(strings.Repeat("one", 100000)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(two, []byte(strings.Repeat("two", 100000)), 0600); err != nil {
		t.Fatal(err)
	}
	s1, d1, s2, d2 := localSFTP(t), localSFTP(t), localSFTP(t), localSFTP(t)
	results := make(chan error, 2)
	start := make(chan struct{})
	go func() { <-start; _, err := Copy(context.Background(), s1, d1, one, target, false); results <- err }()
	go func() { <-start; _, err := Copy(context.Background(), s2, d2, two, target, false); results <- err }()
	close(start)
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("want exactly one publisher, got %v / %v", first, second)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != strings.Repeat("one", 100000) && string(data) != strings.Repeat("two", 100000) {
		t.Fatal("partial or mixed file published")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatalf("staging files leaked: %v", entries)
	}
}

// A server that stops responding must not pin the copy after cancellation.
func TestCopyCancellationUnblocksRemoteIO(t *testing.T) {
	a, b := net.Pipe()
	gate := &gatedConnection{Conn: a, started: make(chan struct{}), release: make(chan struct{})}
	server, err := sftp.NewServer(gate)
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); _ = server.Serve(); _ = server.Close() }()
	src, err := sftp.NewClientPipe(b, b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(gate.release); _ = src.Close(); _ = a.Close(); _ = b.Close(); <-serverDone })
	dst := localSFTP(t)
	source := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(source, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	gate.block.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := Copy(ctx, src, dst, source, source+"-target", false); result <- err }()
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("copy did not reach blocked remote I/O")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("copy ignored cancellation during remote I/O")
	}
}

type gatedConnection struct {
	net.Conn
	block            atomic.Bool
	once             sync.Once
	started, release chan struct{}
}

func (c *gatedConnection) Write(p []byte) (int, error) {
	if c.block.Load() {
		c.once.Do(func() { close(c.started) })
		<-c.release
	}
	return c.Conn.Write(p)
}
