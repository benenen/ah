package transfer

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// The benchmarks run against an in-process SFTP server over net.Pipe, so the
// remote modes measure protocol and CPU overhead, not a real network. The
// latency variants delay every server response to show how round trips bound
// throughput. Run them with:
//
//	go test ./internal/transfer -run '^$' -bench . -benchmem

var benchModes = []string{"local-local", "local-remote", "remote-local"}

// benchClients returns the clients Copy and CopyTree take for mode; nil means local.
func benchClients(b *testing.B, mode string, rtt time.Duration) (src, dst *sftp.Client) {
	open := func() *sftp.Client {
		if rtt > 0 {
			return delayedSFTP(b, rtt)
		}
		return localSFTP(b)
	}
	switch mode {
	case "local-remote":
		dst = open()
	case "remote-local":
		src = open()
	}
	return src, dst
}

func writeRandomFile(b *testing.B, p string, size int) {
	b.Helper()
	data := make([]byte, size)
	_, _ = rand.Read(data)
	if err := os.WriteFile(p, data, 0644); err != nil {
		b.Fatal(err)
	}
}

func writeBenchTree(b *testing.B, root string, files, size int) {
	b.Helper()
	for i := range files {
		dir := filepath.Join(root, fmt.Sprintf("d%02d", i%20))
		if err := os.MkdirAll(dir, 0755); err != nil {
			b.Fatal(err)
		}
		writeRandomFile(b, filepath.Join(dir, fmt.Sprintf("f%04d", i)), size)
	}
}

func BenchmarkCopy(b *testing.B) {
	for _, size := range []int{64 << 10, 8 << 20} {
		for _, mode := range benchModes {
			b.Run(fmt.Sprintf("%s/%dKiB", mode, size>>10), func(b *testing.B) {
				benchmarkCopy(b, mode, size, 0)
			})
		}
	}
}

func BenchmarkCopyLatency(b *testing.B) {
	for _, rtt := range []time.Duration{time.Millisecond, 10 * time.Millisecond} {
		for _, mode := range []string{"local-remote", "remote-local"} {
			b.Run(fmt.Sprintf("%s/rtt=%s/8MiB", mode, rtt), func(b *testing.B) {
				benchmarkCopy(b, mode, 8<<20, rtt)
			})
		}
	}
}

func benchmarkCopy(b *testing.B, mode string, size int, rtt time.Duration) {
	src, dst := benchClients(b, mode, rtt)
	dir := b.TempDir()
	source := filepath.Join(dir, "source")
	writeRandomFile(b, source, size)
	b.SetBytes(int64(size))
	i := 0
	for b.Loop() {
		target := filepath.Join(dir, fmt.Sprintf("target-%d", i))
		i++
		if n, err := Copy(context.Background(), src, dst, source, target, Options{}); err != nil || n != int64(size) {
			b.Fatalf("copy %d %v", n, err)
		}
		b.StopTimer()
		if err := os.Remove(target); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

const benchTreeFiles, benchTreeFileSize = 500, 4 << 10

func BenchmarkCopyTree(b *testing.B) {
	for _, mode := range benchModes {
		b.Run(mode, func(b *testing.B) { benchmarkCopyTree(b, mode, 0, false) })
	}
	b.Run("local-remote/rtt=1ms", func(b *testing.B) { benchmarkCopyTree(b, "local-remote", time.Millisecond, false) })
}

// BenchmarkCopyTreeUnchanged re-copies with Force onto an identical tree, so
// every file takes the compare-and-skip path.
func BenchmarkCopyTreeUnchanged(b *testing.B) {
	for _, mode := range benchModes {
		b.Run(mode, func(b *testing.B) { benchmarkCopyTree(b, mode, 0, true) })
	}
}

func benchmarkCopyTree(b *testing.B, mode string, rtt time.Duration, unchanged bool) {
	src, dst := benchClients(b, mode, rtt)
	dir := b.TempDir()
	source := filepath.Join(dir, "src")
	writeBenchTree(b, source, benchTreeFiles, benchTreeFileSize)
	opts := TreeOptions{SameFilesystem: mode == "local-local"}
	ctx := context.Background()
	if unchanged {
		opts.Force = true
		if _, err := CopyTree(ctx, src, dst, source, filepath.Join(dir, "copy"), opts); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(benchTreeFiles * benchTreeFileSize)
	i := 0
	for b.Loop() {
		target := filepath.Join(dir, "copy")
		if !unchanged {
			target = filepath.Join(dir, fmt.Sprintf("copy-%d", i))
		}
		i++
		result, err := CopyTree(ctx, src, dst, source, target, opts)
		if err != nil {
			b.Fatal(err)
		}
		if got := result.Files + result.Unchanged; got != benchTreeFiles {
			b.Fatalf("result %+v", result)
		}
		if !unchanged {
			b.StopTimer()
			if err := os.RemoveAll(target); err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
		}
	}
	b.ReportMetric(float64(benchTreeFiles*b.N)/b.Elapsed().Seconds(), "files/s")
}

// delayedSFTP is localSFTP with every server response held back by rtt.
// Responses stay pipelined, so concurrent requests overlap their waits.
func delayedSFTP(tb testing.TB, rtt time.Duration) *sftp.Client {
	tb.Helper()
	a, b := net.Pipe()
	delayed := newDelayedConn(a, rtt)
	server, err := sftp.NewServer(delayed)
	if err != nil {
		tb.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(); _ = server.Close() }()
	client, err := sftp.NewClientPipe(b, b)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = client.Close(); _ = b.Close(); _ = delayed.Close(); <-done })
	return client
}

type delayedWrite struct {
	data []byte
	at   time.Time
}

type delayedConn struct {
	net.Conn
	delay   time.Duration
	mu      sync.Mutex
	closed  bool
	queue   chan delayedWrite
	drained chan struct{}
}

func newDelayedConn(c net.Conn, delay time.Duration) *delayedConn {
	d := &delayedConn{Conn: c, delay: delay, queue: make(chan delayedWrite, 4096), drained: make(chan struct{})}
	go func() {
		defer close(d.drained)
		var failed bool
		for w := range d.queue {
			if failed {
				continue
			}
			time.Sleep(time.Until(w.at))
			if _, err := d.Conn.Write(w.data); err != nil {
				failed = true
			}
		}
	}()
	return d
}

func (d *delayedConn) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return 0, net.ErrClosed
	}
	d.queue <- delayedWrite{data: append([]byte(nil), p...), at: time.Now().Add(d.delay)}
	return len(p), nil
}

func (d *delayedConn) Close() error {
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.queue)
	}
	d.mu.Unlock()
	err := d.Conn.Close()
	<-d.drained
	return err
}
