package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// progressBar renders an in-place copy progress bar to a terminal. It is a no-op
// when the writer is not a TTY, so pipes, scripts and history stay clean. update
// runs on the single copy goroutine and finish after that goroutine completes, so
// no locking is needed.
type progressBar struct {
	w        io.Writer
	active   bool
	width    int
	start    time.Time
	last     time.Time
	rendered bool
}

func newProgressBar(w io.Writer) *progressBar {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return &progressBar{w: w}
	}
	now := time.Now()
	return &progressBar{w: w, active: true, width: 24, start: now, last: now}
}

func (b *progressBar) update(copied, total int64) {
	if b == nil || !b.active {
		return
	}
	now := time.Now()
	// Throttle redraws, but always paint the final complete frame.
	if copied < total && now.Sub(b.last) < 80*time.Millisecond {
		return
	}
	b.last = now
	b.rendered = true

	var pct float64 = 1
	if total > 0 {
		pct = float64(copied) / float64(total)
	}
	filled := int(pct * float64(b.width))
	if filled > b.width {
		filled = b.width
	}
	bar := strings.Repeat("#", filled) + strings.Repeat("-", b.width-filled)
	rate := ""
	if elapsed := now.Sub(b.start).Seconds(); elapsed > 0 {
		rate = "  " + humanBytes(int64(float64(copied)/elapsed)) + "/s"
	}
	// Clear the old suffix when a shorter rate makes this frame narrower.
	fmt.Fprintf(b.w, "\r[%s] %3.0f%%  %s/%s%s\033[K", bar, pct*100, humanBytes(copied), humanBytes(total), rate)
}

// finish clears the progress line so a following stdout summary starts clean.
func (b *progressBar) finish() {
	if b == nil || !b.active || !b.rendered {
		return
	}
	fmt.Fprint(b.w, "\r\033[K")
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
