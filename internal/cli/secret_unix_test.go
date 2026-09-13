//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestSecretInputCancellationRestoresTerminal(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := readSecret(ctx, slave, &bytes.Buffer{}, "Password: "); done <- err }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled password read blocked")
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if *before != *after {
		t.Fatal("terminal state was not restored")
	}
}

func TestSecretInputEditingAndInterrupt(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		interrupted bool
	}{{"ab\x7fc\r", "ac", false}, {"秘密\x7f码\r", "秘码", false}, {"\x03", "", true}} {
		t.Run(tc.want+tc.input, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			// Prompt notification avoids typing while the terminal is still in canonical mode.
			ready := make(chan struct{})
			out := &notifyWriter{ready: ready}
			go func() { <-ready; time.Sleep(20 * time.Millisecond); _, _ = master.Write([]byte(tc.input)) }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			secret, err := readSecret(ctx, slave, out, "Password: ")
			if tc.interrupted {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || string(secret) != tc.want {
				t.Fatalf("secret=%q err=%v", secret, err)
			}
		})
	}
}

type notifyWriter struct {
	ready    chan struct{}
	notified bool
}

func (w *notifyWriter) Write(p []byte) (int, error) {
	if !w.notified {
		close(w.ready)
		w.notified = true
	}
	return len(p), nil
}
