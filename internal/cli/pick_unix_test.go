//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

type pickerScreen struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (s *pickerScreen) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Write(data)
}
func (s *pickerScreen) text() string { s.mu.Lock(); defer s.mu.Unlock(); return s.data.String() }

func runPicker(t *testing.T, state *connectionPicker, keys string, cancelSelection bool) (string, error) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(slave, &pty.Winsize{Rows: 12, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	var screen pickerScreen
	readDone := make(chan struct{})
	go func() { defer close(readDone); _, _ = io.Copy(&screen, master) }()
	defer func() { master.Close(); slave.Close(); <-readDone }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type choice struct {
		name string
		err  error
	}
	done := make(chan choice, 1)
	go func() { name, err := selectConnection(ctx, slave, slave, state); done <- choice{name, err} }()
	for !strings.Contains(screen.text(), "Search:") {
		select {
		case <-ctx.Done():
			t.Fatal("selector did not render")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if cancelSelection {
		cancel()
	} else {
		if _, err := master.Write([]byte(keys)); err != nil {
			t.Fatal(err)
		}
	}
	result := <-done
	after, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("selector left terminal settings changed")
	}
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(screen.text(), "\x1b[?1049l") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(screen.text(), "\x1b[?1049l") {
		t.Fatal("alternate screen not restored")
	}
	if strings.Contains(screen.text(), "must-never-display") {
		t.Fatal("credential in selector output")
	}
	return result.name, result.err
}

func TestPickerTerminalKeysAndUTF8(t *testing.T) {
	for _, tc := range []struct{ name, keys, want string }{
		{"down", "\x1b[B\r", "beta"},
		{"up wrap", "\x1b[A\r", "gamma"},
		{"tab", "\t\r", "beta"},
		{"UTF8 and backspace", "数据庘\x7f库\r", "beta"},
		{"clear search", "missing\x15alpha\r", "alpha"},
		{"no match enter ignored", "missing\r\x15beta\r", "beta"},
		{"escape", "\x1b", ""},
		{"control d", "\x04", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, err := runPicker(t, pickerFixture(), tc.keys, false)
			if err != nil || name != tc.want {
				t.Fatal(name, err)
			}
		})
	}
}

func TestPickerCancellationRestoresTerminal(t *testing.T) {
	for _, external := range []bool{false, true} {
		_, err := runPicker(t, pickerFixture(), "\x03", external)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}
