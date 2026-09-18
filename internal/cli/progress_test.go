package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestProgressBarClearsShorterFrame(t *testing.T) {
	var output bytes.Buffer
	b := &progressBar{w: &output, active: true, width: 24}
	// A falling rate shortens the frame by one character while bytes increase.
	b.start = time.Now().Add(-20 * time.Second)
	b.update(16*1024*1024, 218*1024*1024)
	b.start = time.Now().Add(-200 * time.Second)
	b.last = time.Time{}
	b.update(17*1024*1024, 218*1024*1024)

	// Model carriage return and erase-to-end-of-line on a single terminal row.
	var row []byte
	cursor := 0
	stream := output.String()
	for i := 0; i < len(stream); {
		switch {
		case stream[i] == '\r':
			cursor = 0
			i++
		case strings.HasPrefix(stream[i:], "\033[K"):
			row = row[:cursor]
			i += len("\033[K")
		default:
			if cursor == len(row) {
				row = append(row, stream[i])
			} else {
				row[cursor] = stream[i]
			}
			cursor++
			i++
		}
	}
	frames := strings.Split(stream, "\r")
	latest := strings.ReplaceAll(frames[len(frames)-1], "\033[K", "")
	if string(row) != latest {
		t.Fatalf("terminal retained stale characters: got %q, want %q", row, latest)
	}
}
