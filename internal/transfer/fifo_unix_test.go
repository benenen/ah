//go:build darwin || linux

package transfer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCopyRejectsFIFOWithoutOpeningIt(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	src, dst := localSFTP(t), localSFTP(t)
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(250 * time.Millisecond)
		f, err := os.OpenFile(fifo, os.O_RDWR|unix.O_NONBLOCK, 0600)
		if err == nil {
			time.Sleep(20 * time.Millisecond)
			_ = f.Close()
		}
	}()
	start := time.Now()
	_, err := Copy(context.Background(), src, dst, fifo, filepath.Join(dir, "out"), false, nil)
	elapsed := time.Since(start)
	<-released
	if err == nil {
		t.Fatal("accepted FIFO")
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("opened FIFO and waited for writer: %v", elapsed)
	}
}

func TestCopyRejectsLocalFIFOWithoutOpeningIt(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	// A nonblocking writer prevents a regressed implementation from hanging forever.
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(250 * time.Millisecond)
		f, err := os.OpenFile(fifo, os.O_RDWR|unix.O_NONBLOCK, 0600)
		if err == nil {
			time.Sleep(20 * time.Millisecond)
			_ = f.Close()
		}
	}()
	start := time.Now()
	_, err := Copy(context.Background(), nil, nil, fifo, filepath.Join(dir, "out"), false, nil)
	elapsed := time.Since(start)
	<-released
	if err == nil {
		t.Fatal("accepted local FIFO")
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("opened FIFO: %v", elapsed)
	}
}
