//go:build darwin || linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
)

func selectConnection(ctx context.Context, input, output *os.File, state *connectionPicker) (name string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	terminal, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return "", err
	}
	defer func() {
		_, writeErr := fmt.Fprint(output, "\x1b[0m\x1b[?25h\x1b[?1049l")
		err = errors.Join(err, writeErr, term.Restore(int(input.Fd()), terminal))
	}()
	if _, err := fmt.Fprint(output, "\x1b[?1049h\x1b[?25l"); err != nil {
		return "", err
	}
	columns, rows := 0, 0
	dirty := true
	for {
		width, height, sizeErr := term.GetSize(int(input.Fd()))
		if sizeErr != nil || width <= 0 || height <= 0 {
			width, height = 80, 24
		}
		if width != columns || height != rows {
			columns, rows = width, height
			dirty = true
		}
		if dirty {
			if _, err := io.WriteString(output, state.render(columns, rows)); err != nil {
				return "", err
			}
			dirty = false
		}
		key, r, err := readPickKey(ctx, input)
		if err != nil {
			return "", err
		}
		switch key {
		case "tick":
			continue
		case "cancel":
			return "", nil
		case "interrupt":
			return "", context.Canceled
		case "enter":
			if len(state.matches) > 0 && columns >= 20 && rows >= 5 {
				return state.matches[state.selected], nil
			}
		case "next":
			state.move(1)
			dirty = true
		case "previous":
			state.move(-1)
			dirty = true
		case "backspace":
			if len(state.query) > 0 {
				state.query = state.query[:len(state.query)-1]
				state.filter()
				dirty = true
			}
		case "clear":
			state.query = nil
			state.filter()
			dirty = true
		case "text":
			if len(string(state.query))+utf8.RuneLen(r) <= 4096 {
				state.query = append(state.query, r)
				state.filter()
				dirty = true
			}
		}
	}
}

func pickByte(ctx context.Context, input *os.File, timeout time.Duration) (byte, error) {
	timed, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return pollByte(timed, int(input.Fd()))
}

func readPickKey(ctx context.Context, input *os.File) (string, rune, error) {
	b, err := pickByte(ctx, input, 150*time.Millisecond)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "tick", 0, nil
		}
		return "", 0, err
	}
	switch b {
	case 3:
		return "interrupt", 0, nil
	case 4:
		return "cancel", 0, nil
	case '\r', '\n':
		return "enter", 0, nil
	case '\t', 14:
		return "next", 0, nil
	case 16:
		return "previous", 0, nil
	case 8, 127:
		return "backspace", 0, nil
	case 21:
		return "clear", 0, nil
	case 27:
		second, err := pickByte(ctx, input, 50*time.Millisecond)
		if err != nil {
			if ctx.Err() != nil {
				return "", 0, ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return "cancel", 0, nil
			}
			return "", 0, err
		}
		if second != '[' && second != 'O' {
			return "tick", 0, nil
		}
		sequence := ""
		for range 16 {
			next, err := pickByte(ctx, input, 50*time.Millisecond)
			if err != nil {
				if ctx.Err() != nil {
					return "", 0, ctx.Err()
				}
				return "tick", 0, nil
			}
			sequence += string(next)
			if (next >= 'A' && next <= 'Z') || (next >= 'a' && next <= 'z') || next == '~' {
				break
			}
		}
		switch sequence {
		case "A", "Z":
			return "previous", 0, nil
		case "B":
			return "next", 0, nil
		}
		return "tick", 0, nil
	}
	data := []byte{b}
	for !utf8.FullRune(data) && len(data) < utf8.UTFMax {
		next, err := pickByte(ctx, input, 150*time.Millisecond)
		if err != nil {
			if ctx.Err() != nil {
				return "", 0, ctx.Err()
			}
			return "tick", 0, nil
		}
		data = append(data, next)
	}
	r, _ := utf8.DecodeRune(data)
	if r != utf8.RuneError && unicode.IsPrint(r) {
		return "text", r, nil
	}
	return "tick", 0, nil
}
