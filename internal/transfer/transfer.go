// Package transfer copies single regular files between local and SFTP endpoints.
package transfer

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/pkg/sftp"
)

type Endpoint struct{ Name, Path string }

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func ParseEndpoint(value string) (Endpoint, error) {
	if value == "" || strings.ContainsFunc(value, unicode.IsControl) {
		return Endpoint{}, fmt.Errorf("invalid endpoint %q: path is empty or contains control characters", value)
	}
	if strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~/") || !strings.Contains(value, ":") {
		return Endpoint{Path: value}, nil
	}
	name, p, _ := strings.Cut(value, ":")
	if !aliasPattern.MatchString(name) || p == "" {
		return Endpoint{}, fmt.Errorf("invalid endpoint %q: expected NAME:PATH or a local path", value)
	}
	return Endpoint{Name: name, Path: p}, nil
}

// ResolvePath treats ~ as the SFTP login directory, never as a local home path.
func ResolvePath(client *sftp.Client, p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := client.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve remote home: %w", err)
		}
		return path.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/")), nil
	}
	if strings.HasPrefix(p, "~") {
		return "", fmt.Errorf("~user paths are not supported; use ~/ or an absolute path")
	}
	return p, nil
}

// Copy accepts a nil src or dst for the local filesystem. Non-nil clients use
// SFTP; Copy owns their I/O while running and closes them on cancellation.
// The destination is staged in the same directory; a hard link atomically publishes
// without clobbering, while --force atomically renames. SFTP destinations require
// the corresponding OpenSSH extension.
func Copy(ctx context.Context, src, dst *sftp.Client, source, target string, force bool) (n int64, err error) {
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			if src != nil {
				_ = src.Close()
			}
			if dst != nil {
				_ = dst.Close()
			}
		case <-done:
		}
	}()
	defer func() {
		close(done)
		<-stopped
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
	}()
	sourceFS, targetFS := filesystem{src}, filesystem{dst}
	source, err = sourceFS.resolve(source)
	if err != nil {
		return 0, err
	}
	target, err = targetFS.resolve(target)
	if err != nil {
		return 0, err
	}
	beforeOpen, statErr := sourceFS.Stat(source)
	if statErr != nil {
		return 0, fmt.Errorf("stat source: %w", statErr)
	}
	if !beforeOpen.Mode().IsRegular() {
		return 0, fmt.Errorf("source must be a regular file: %s", source)
	}
	in, err := sourceFS.Open(source)
	if err != nil {
		return 0, fmt.Errorf("open source: %w", err)
	}
	inputClosed := false
	defer func() {
		if !inputClosed {
			err = errors.Join(err, in.Close())
		}
	}()
	info, err := in.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("source must be a regular file: %s", source)
	}
	if targetInfo, statErr := targetFS.Stat(target); statErr == nil && targetInfo.IsDir() {
		target = targetFS.join(target, sourceFS.base(source))
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return 0, fmt.Errorf("stat destination: %w", statErr)
	}
	if !force {
		if _, statErr := targetFS.Lstat(target); statErr == nil {
			return 0, fmt.Errorf("destination exists (use --force to replace): %s", target)
		} else if !os.IsNotExist(statErr) {
			return 0, fmt.Errorf("stat destination: %w", statErr)
		}
		if dst != nil {
			if _, ok := dst.HasExtension("hardlink@openssh.com"); !ok {
				return 0, fmt.Errorf("destination server must support hardlink@openssh.com for atomic no-clobber copy")
			}
		}
	} else if dst != nil {
		if _, ok := dst.HasExtension("posix-rename@openssh.com"); !ok {
			return 0, fmt.Errorf("destination server must support posix-rename@openssh.com for --force")
		}
	}
	tmp := targetFS.join(targetFS.dir(target), ".ah-copy-"+rand.Text())
	out, err := targetFS.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return 0, fmt.Errorf("create destination temporary file: %w", err)
	}
	outputClosed := false
	published := false
	defer func() {
		if !outputClosed {
			err = errors.Join(err, out.Close())
		}
		if !published {
			if cleanupErr := targetFS.Remove(tmp); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				err = errors.Join(err, fmt.Errorf("remove temporary file %s (may need manual cleanup): %w", tmp, cleanupErr))
			}
		}
	}()
	if err = out.Chmod(0600); err != nil {
		return 0, fmt.Errorf("restrict temporary file permissions: %w", err)
	}
	// Wrappers prevent io.Copy from invoking optimized methods that bypass the bounded buffer.
	n, err = io.CopyBuffer(contextWriter{ctx, out}, contextReader{ctx, in}, make([]byte, 128*1024))
	if err != nil {
		return n, fmt.Errorf("transfer data: %w", err)
	}
	if err = in.Close(); err != nil {
		inputClosed = true
		return n, fmt.Errorf("close source: %w", err)
	}
	inputClosed = true
	if err = out.Chmod(info.Mode().Perm()); err != nil {
		return n, fmt.Errorf("set destination permissions: %w", err)
	}
	if err = out.Close(); err != nil {
		outputClosed = true
		return n, fmt.Errorf("close destination: %w", err)
	}
	outputClosed = true
	if err = ctx.Err(); err != nil {
		return n, err
	}
	if force {
		err = targetFS.PosixRename(tmp, target)
	} else {
		err = targetFS.Link(tmp, target)
	}
	if err != nil {
		return n, fmt.Errorf("publish destination: %w", err)
	}
	if !force {
		if err = targetFS.Remove(tmp); err != nil {
			return n, fmt.Errorf("destination published but failed to remove temporary file %s: %w", tmp, err)
		}
	}
	published = true
	return n, nil
}

// ResolveLocalPath expands the current user's home and returns an absolute path.
func ResolveLocalPath(p string) (string, error) {
	if p == "" || strings.ContainsFunc(p, unicode.IsControl) {
		return "", fmt.Errorf("invalid local path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve local home: %w", err)
		}
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	} else if strings.HasPrefix(p, "~") {
		return "", fmt.Errorf("~user paths are not supported; use ~/ or an absolute path")
	}
	return filepath.Abs(p)
}

type transferFile interface {
	io.Reader
	io.Writer
	io.Closer
	Stat() (os.FileInfo, error)
	Chmod(os.FileMode) error
}

// filesystem adapts only the operations needed by the shared copy algorithm.
type filesystem struct{ client *sftp.Client }

func (f filesystem) resolve(p string) (string, error) {
	if f.client == nil {
		return ResolveLocalPath(p)
	}
	return ResolvePath(f.client, p)
}
func (f filesystem) Stat(p string) (os.FileInfo, error) {
	if f.client == nil {
		return os.Stat(p)
	}
	return f.client.Stat(p)
}
func (f filesystem) Lstat(p string) (os.FileInfo, error) {
	if f.client == nil {
		return os.Lstat(p)
	}
	return f.client.Lstat(p)
}
func (f filesystem) Open(p string) (transferFile, error) {
	if f.client == nil {
		return os.Open(p)
	}
	return f.client.Open(p)
}
func (f filesystem) OpenFile(p string, flags int) (transferFile, error) {
	if f.client == nil {
		return os.OpenFile(p, flags, 0600)
	}
	return f.client.OpenFile(p, flags)
}
func (f filesystem) Remove(p string) error {
	if f.client == nil {
		return os.Remove(p)
	}
	return f.client.Remove(p)
}
func (f filesystem) PosixRename(a, b string) error {
	if f.client == nil {
		return os.Rename(a, b)
	}
	return f.client.PosixRename(a, b)
}
func (f filesystem) Link(a, b string) error {
	if f.client == nil {
		return os.Link(a, b)
	}
	return f.client.Link(a, b)
}
func (f filesystem) join(a, b string) string {
	if f.client == nil {
		return filepath.Join(a, b)
	}
	return path.Join(a, b)
}
func (f filesystem) base(p string) string {
	if f.client == nil {
		return filepath.Base(p)
	}
	return path.Base(p)
}
func (f filesystem) dir(p string) string {
	if f.client == nil {
		return filepath.Dir(p)
	}
	return path.Dir(p)
}

type contextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}

type contextWriter struct {
	ctx context.Context
	out io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.out.Write(p)
}
