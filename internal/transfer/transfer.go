// Package transfer copies single regular files between SFTP endpoints.
package transfer

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/pkg/sftp"
)

type Endpoint struct{ Name, Path string }

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func ParseEndpoint(value string) (Endpoint, error) {
	name, p, ok := strings.Cut(value, ":")
	if !ok || !aliasPattern.MatchString(name) || p == "" || strings.ContainsFunc(p, unicode.IsControl) {
		return Endpoint{}, fmt.Errorf("invalid endpoint %q: expected NAME:PATH", value)
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

// Copy owns I/O for both SFTP clients while running and closes them on cancellation.
// The destination is staged in the same directory; a hard link atomically publishes
// without clobbering, while --force requires the atomic POSIX rename extension.
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
			_ = src.Close()
			_ = dst.Close()
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
	source, err = ResolvePath(src, source)
	if err != nil {
		return 0, err
	}
	target, err = ResolvePath(dst, target)
	if err != nil {
		return 0, err
	}
	beforeOpen, statErr := src.Stat(source)
	if statErr != nil {
		return 0, fmt.Errorf("stat source: %w", statErr)
	}
	if !beforeOpen.Mode().IsRegular() {
		return 0, fmt.Errorf("source must be a regular file: %s", source)
	}
	in, err := src.Open(source)
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
	if targetInfo, statErr := dst.Stat(target); statErr == nil && targetInfo.IsDir() {
		target = path.Join(target, path.Base(source))
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return 0, fmt.Errorf("stat destination: %w", statErr)
	}
	if !force {
		if _, statErr := dst.Lstat(target); statErr == nil {
			return 0, fmt.Errorf("destination exists (use --force to replace): %s", target)
		} else if !os.IsNotExist(statErr) {
			return 0, fmt.Errorf("stat destination: %w", statErr)
		}
		if _, ok := dst.HasExtension("hardlink@openssh.com"); !ok {
			return 0, fmt.Errorf("destination server must support hardlink@openssh.com for atomic no-clobber copy")
		}
	} else if _, ok := dst.HasExtension("posix-rename@openssh.com"); !ok {
		return 0, fmt.Errorf("destination server must support posix-rename@openssh.com for --force")
	}
	tmp := path.Join(path.Dir(target), ".ah-copy-"+rand.Text())
	out, err := dst.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
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
			if cleanupErr := dst.Remove(tmp); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				err = errors.Join(err, fmt.Errorf("remove temporary file %s (may need manual cleanup): %w", tmp, cleanupErr))
			}
		}
	}()
	if err = out.Chmod(0600); err != nil {
		return 0, fmt.Errorf("restrict temporary file permissions: %w", err)
	}
	// Wrappers prevent io.Copy from invoking optimized methods that bypass the bounded buffer.
	n, err = io.CopyBuffer(struct{ io.Writer }{out}, struct{ io.Reader }{in}, make([]byte, 128*1024))
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
		err = dst.PosixRename(tmp, target)
	} else {
		err = dst.Link(tmp, target)
	}
	if err != nil {
		return n, fmt.Errorf("publish destination: %w", err)
	}
	if !force {
		if err = dst.Remove(tmp); err != nil {
			return n, fmt.Errorf("destination published but failed to remove temporary file %s: %w", tmp, err)
		}
	}
	published = true
	return n, nil
}
