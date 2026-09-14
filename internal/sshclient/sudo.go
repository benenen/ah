package sshclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

type sudoIO struct {
	input   io.WriteCloser
	output  io.Reader
	prompts *sudoPromptWriter
}

// startSudo consumes authentication before callers send shell input or SFTP
// packets. Passwords are sent only in response to our unpredictable prompt.
func (c *Client) startSudo(ctx context.Context, session *ssh.Session, action string, stderr io.Writer, terminal bool) (*sudoIO, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(nonce)
	prompt, ready := "AH_SUDO_PROMPT_"+token, "AH_SUDO_READY_"+token
	input, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	pw := &sudoPromptWriter{ctx: ctx, prompt: []byte(prompt), input: input, output: stderr, password: c.sudoPassword, session: session, abort: c.Close}
	session.Stderr = pw
	stop := context.AfterFunc(ctx, func() { c.Close(); session.Close() })
	defer stop()
	prefix := ""
	if terminal {
		prefix = "stty echo; "
	}
	command := "sudo -S -p " + shellQuote(prompt) + " -H -- /bin/sh -c " + shellQuote(prefix+"printf '%s' "+shellQuote(ready)+"; "+action)
	if err := session.Start(command); err != nil {
		c.Close()
		return nil, err
	}
	if terminal {
		reader := bufio.NewReader(output)
		err = readSudoPTYReady(reader, []byte(prompt), []byte(ready), pw, stderr)
		output = reader
	} else {
		received := make([]byte, len(ready))
		_, err = io.ReadFull(output, received)
		if err == nil && string(received) != ready {
			err = fmt.Errorf("unexpected sudo startup output")
		}
	}
	if err != nil {
		c.Close()
		session.Close()
		session.Wait()
		pw.flush()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if authErr := pw.failure(); authErr != nil {
			return nil, authErr
		}
		return nil, fmt.Errorf("sudo initialization failed: check sudo rights, password and remote shell availability")
	}
	if err := pw.failure(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := pw.finishAuth(); err != nil {
		return nil, err
	}
	return &sudoIO{input: input, output: output, prompts: pw}, nil
}

type sudoPromptWriter struct {
	abort           func() error
	authenticated   bool
	mu              sync.Mutex
	ctx             context.Context
	prompt, pending []byte
	input           io.Writer
	output          io.Writer
	password        func(context.Context) ([]byte, error)
	session         *ssh.Session
	sent            bool
	err             error
}

func (w *sudoPromptWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.authenticated {
		return w.output.Write(p)
	}
	w.pending = append(w.pending, p...)
	for {
		i := bytes.Index(w.pending, w.prompt)
		if i < 0 {
			n := len(w.pending) - len(w.prompt) + 1
			if n > 0 {
				if _, err := w.output.Write(w.pending[:n]); err != nil {
					return 0, err
				}
				w.pending = append(w.pending[:0], w.pending[n:]...)
			}
			return len(p), nil
		}
		if _, err := w.output.Write(w.pending[:i]); err != nil {
			return 0, err
		}
		w.pending = append(w.pending[:0], w.pending[i+len(w.prompt):]...)
		var secret []byte
		if w.sent {
			w.err = fmt.Errorf("sudo rejected the supplied password")
		} else if w.password == nil {
			w.err = fmt.Errorf("sudo requires a password")
		} else {
			secret, w.err = w.password(w.ctx)
		}
		w.sent = true
		if w.err == nil && (len(secret) == 0 || bytes.ContainsAny(secret, "\r\n\x00")) {
			w.err = fmt.Errorf("sudo password must be nonempty and contain no newline or NUL")
		}
		if w.err == nil {
			line := append(secret, '\n')
			_, w.err = w.input.Write(line)
			clear(line)
		}
		clear(secret)
		if w.err != nil {
			if w.abort != nil {
				w.abort()
			}
			w.session.Close()
			return 0, w.err
		}
	}
}
func (w *sudoPromptWriter) failure() error { w.mu.Lock(); defer w.mu.Unlock(); return w.err }
func (w *sudoPromptWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 {
		w.output.Write(w.pending)
		w.pending = nil
	}
}

func (c *Client) sudoSFTP() (*sftp.Client, error) {
	ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	session, err := c.client.NewSession()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	action := `for p in /usr/lib/openssh/sftp-server /usr/lib/ssh/sftp-server /usr/libexec/openssh/sftp-server /usr/libexec/sftp-server; do if [ -x "$p" ]; then exec "$p"; fi; done; printf 'SFTP server not found\n' >&2; exit 127`
	if c.sftpServer != "" {
		action = "exec " + shellQuote(c.sftpServer)
	}
	stream, err := c.startSudo(ctx, session, action, io.Discard, false)
	if err != nil {
		session.Close()
		return nil, err
	}
	closer := &sudoSessionWriter{WriteCloser: stream.input, session: session, prompts: stream.prompts, client: c}
	files, err := sftp.NewClientPipe(stream.output, closer)
	if err != nil {
		closer.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("start sudo SFTP: verify the sftp-server path and sudo permissions: %w", err)
	}
	return files, nil
}

type sudoSessionWriter struct {
	client *Client
	io.WriteCloser
	session *ssh.Session
	prompts *sudoPromptWriter
	once    sync.Once
	err     error
}

func (w *sudoSessionWriter) Close() error {
	w.once.Do(func() {
		w.err = w.client.Close()
		w.WriteCloser.Close()
		w.session.Close()
		w.session.Wait()
		w.prompts.flush()
	})
	return w.err
}

func (w *sudoPromptWriter) finishAuth() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.authenticated = true
	_, err := w.output.Write(w.pending)
	w.pending = nil
	return err
}

// A remote PTY merges stderr into stdout. Keep only possible marker prefixes
// while forwarding startup text, and never expose authentication prompts.
func readSudoPTYReady(reader *bufio.Reader, prompt, ready []byte, pw *sudoPromptWriter, output io.Writer) error {
	var pending []byte
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return err
		}
		pending = append(pending, b)
		if bytes.Equal(pending, ready) {
			return nil
		}
		if bytes.Equal(pending, prompt) {
			if _, err := pw.Write(prompt); err != nil {
				return err
			}
			pending = nil
			continue
		}
		for len(pending) > 0 && !bytes.HasPrefix(prompt, pending) && !bytes.HasPrefix(ready, pending) {
			if _, err := output.Write(pending[:1]); err != nil {
				return err
			}
			pending = pending[1:]
		}
	}
}
