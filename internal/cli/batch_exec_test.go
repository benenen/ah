package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchExecRejectsInvalidSelection(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "missing.toml")
	for _, args := range [][]string{
		{"exec", "--", "true"},
		{"exec", "--tag", "prod", "true"},
		{"exec", "--tag", "prod", "--jobs", "0", "--", "true"},
		{"exec", "--tag", "prod", "--jobs", "65", "--", "true"},
		{"exec", "--tag", "bad tag", "--", "true"},
		{"exec", "--tag", "prod", "--exec-timeout=-1s", "--", "true"},
		{"exec", "--tag", "prod", "--", " "},
		{"exec", "--tag", "prod", "--", "true"},
	} {
		if _, err := execute(t, append([]string{"--config", cfg}, args...)...); err == nil {
			t.Fatal("accepted invalid selection", args)
		}
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatal("invalid command wrote configuration")
	}
}

func TestBatchOutputBoundsAndPartialLines(t *testing.T) {
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &execOutput{cancel: cancel}
	writer := &execPrefixWriter{output: output, writer: &out, prefix: "[host] "}
	for _, part := range []string{"hel", "lo\n\n", strings.Repeat("x", maxExecLine+7)} {
		if _, err := io.WriteString(writer, part); err != nil {
			t.Fatal(err)
		}
	}
	if len(writer.pending) > maxExecLine {
		t.Fatal("unbounded output buffer")
	}
	if err := writer.flush(); err != nil {
		t.Fatal(err)
	}
	want := "[host] hello\n[host] \n[host] " + strings.Repeat("x", maxExecLine) + "\n[host] " + strings.Repeat("x", 7) + "\n"
	if out.String() != want {
		t.Fatal("prefixes or partial line flushing incorrect")
	}
	if ctx.Err() != nil {
		t.Fatal("output unexpectedly canceled")
	}
}

type shortExecWriter struct{}

func (shortExecWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestBatchOutputShortWriteCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &execOutput{cancel: cancel}
	writer := &execPrefixWriter{output: output, writer: shortExecWriter{}, prefix: "[host] "}
	if _, err := io.WriteString(writer, "line\n"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("write failure did not cancel sessions")
	}
}

func TestBatchOutputNewlineAtFragmentBoundary(t *testing.T) {
	var out bytes.Buffer
	output := &execOutput{cancel: func() {}}
	writer := &execPrefixWriter{output: output, writer: &out, prefix: "[host] "}
	full := strings.Repeat("x", maxExecLine)
	if _, err := io.WriteString(writer, full); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "\nnext\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.flush(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "[host] "+full+"\n[host] next\n" {
		t.Fatal("extra empty line at fragment boundary")
	}
}
