package sshclient

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// proxyStderrLimit bounds the retained proxy command diagnostics.
const proxyStderrLimit = 4096

// expandProxyCommand substitutes OpenSSH-style tokens. Values are quoted for
// the shell so a host or user can never inject additional commands.
func expandProxyCommand(command, host string, port int, user string) string {
	var b strings.Builder
	for i := 0; i < len(command); i++ {
		if command[i] != '%' || i+1 == len(command) {
			b.WriteByte(command[i])
			continue
		}
		i++
		switch command[i] {
		case 'h':
			b.WriteString(localShellQuote(host))
		case 'p':
			b.WriteString(strconv.Itoa(port))
		case 'r':
			b.WriteString(localShellQuote(user))
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(command[i])
		}
	}
	return b.String()
}

// localShellQuote quotes for the shell that runs the proxy command. On
// Windows host and user are validated to contain no whitespace.
func localShellQuote(s string) string {
	if runtime.GOOS == "windows" {
		return `"` + s + `"`
	}
	return shellQuote(s)
}

// startProxyCommand runs command through the platform shell and returns its
// stdin/stdout as the SSH transport. The process is owned by the returned
// conn: Close kills and reaps it.
func startProxyCommand(command string, target net.Addr) (*commandConn, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/C", command)
	} else {
		cmd = exec.Command("/bin/sh", "-c", command)
	}
	isolateProxyCommand(cmd)
	// A descendant that escaped the kill may still hold stderr; do not let it
	// block Close.
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	c := &commandConn{cmd: cmd, stdin: stdin, stdout: stdout, target: target}
	cmd.Stderr = &c.stderr
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("start proxy command: %w", err)
	}
	return c, nil
}

// commandConn adapts a proxy command to net.Conn. Pipes do not support
// deadlines; Dial's handshake timer closes the conn on expiry instead.
type commandConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	target net.Addr
	stderr tailBuffer
	once   sync.Once
	err    error
}

func (c *commandConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *commandConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }
func (c *commandConn) LocalAddr() net.Addr         { return destinationAddr("proxy-command") }
func (c *commandConn) RemoteAddr() net.Addr        { return c.target }
func (c *commandConn) SetDeadline(time.Time) error { return nil }

func (c *commandConn) SetReadDeadline(time.Time) error  { return nil }
func (c *commandConn) SetWriteDeadline(time.Time) error { return nil }

func (c *commandConn) Close() error {
	c.once.Do(func() {
		_ = c.stdin.Close()
		// The SSH session is over; the proxy has nothing left to deliver.
		killProxyCommand(c.cmd)
		err := c.cmd.Wait()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) && !errors.Is(err, exec.ErrWaitDelay) {
			c.err = err
		}
	})
	return c.err
}

// diagnostics returns the command's recent stderr for error messages.
func (c *commandConn) diagnostics() string {
	return strings.TrimSpace(c.stderr.String())
}

// tailBuffer keeps the last proxyStderrLimit bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(p) > proxyStderrLimit {
		p = p[len(p)-proxyStderrLimit:]
	}
	if over := t.buf.Len() + len(p) - proxyStderrLimit; over > 0 {
		t.buf.Next(over)
	}
	t.buf.Write(p)
	return n, nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}
