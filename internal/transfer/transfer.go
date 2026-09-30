// Package transfer copies regular files and directory trees between local and SFTP endpoints.
package transfer

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
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
// Copy streams one regular file between the given filesystems.
func Copy(ctx context.Context, src, dst *sftp.Client, source, target string, opts Options) (n int64, err error) {
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	defer closeOnCancel(ctx, src, dst)(&err)
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
	if targetInfo, statErr := targetFS.Stat(target); statErr == nil && targetInfo.IsDir() {
		target = targetFS.join(target, sourceFS.base(source))
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return 0, fmt.Errorf("stat destination: %w", statErr)
	}
	if err = targetFS.requirePublish(opts); err != nil {
		return 0, err
	}
	n, unchanged, err := copyFile(ctx, sourceFS, targetFS, source, target, opts, opts.Progress)
	if unchanged && opts.Unchanged != nil {
		opts.Unchanged(target)
	}
	return n, err
}

// Options control how Copy and CopyTree publish destination files.
type Options struct {
	// Force replaces existing destination files; for CopyTree it also merges
	// into an existing destination directory.
	Force bool
	// BackupKeep > 0 hard-links an existing destination file to
	// bak.NAME.bak-TIMESTAMP before replacing it, first deleting the oldest
	// backups of that file so at most BackupKeep remain. It implies Force.
	BackupKeep int
	// Progress, when non-nil, receives running and total byte counts. It must
	// be cheap and non-blocking.
	Progress func(copied, total int64)
	// Unchanged, when non-nil, receives each destination that was left alone
	// because replacing it would not change it: same content and permissions.
	Unchanged func(target string)
}

func (o Options) replace() bool { return o.Force || o.BackupKeep > 0 }

// closeOnCancel closes the SFTP clients when ctx ends so blocked I/O returns.
// The returned function stops watching and adds ctx's error to *err.
func closeOnCancel(ctx context.Context, src, dst *sftp.Client) func(*error) {
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
	return func(err *error) {
		close(done)
		<-stopped
		if ctx.Err() != nil {
			*err = errors.Join(*err, ctx.Err())
		}
	}
}

// requirePublish checks for the SFTP extensions that publish a staged file.
func (f filesystem) requirePublish(opts Options) error {
	if f.client == nil {
		return nil
	}
	if !opts.replace() || opts.BackupKeep > 0 {
		if _, ok := f.client.HasExtension("hardlink@openssh.com"); !ok {
			return fmt.Errorf("destination server must support hardlink@openssh.com for atomic no-clobber copy and backups")
		}
	}
	if opts.replace() {
		if _, ok := f.client.HasExtension("posix-rename@openssh.com"); !ok {
			return fmt.Errorf("destination server must support posix-rename@openssh.com to replace files")
		}
	}
	return nil
}

// copyFile stages source next to the exact target path and publishes it. It
// refuses an existing target unless force, and never replaces a directory. A
// target that already matches source in content and permissions is left
// untouched (no replacement, no backup) and reported as unchanged.
func copyFile(ctx context.Context, sourceFS, targetFS filesystem, source, target string, opts Options, progress func(copied, total int64)) (n int64, unchanged bool, err error) {
	n, err = copyOrSkip(ctx, sourceFS, targetFS, source, target, opts, progress, &unchanged)
	return n, unchanged, err
}

func copyOrSkip(ctx context.Context, sourceFS, targetFS filesystem, source, target string, opts Options, progress func(copied, total int64), unchanged *bool) (n int64, err error) {
	force := opts.replace()
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
	if existing, statErr := targetFS.Lstat(target); statErr == nil {
		if !force {
			return 0, fmt.Errorf("destination exists (use --force to replace): %s", target)
		}
		if existing.IsDir() {
			return 0, fmt.Errorf("destination is a directory: %s", target)
		}
		if existing.Mode().IsRegular() && existing.Size() == info.Size() && existing.Mode().Perm() == info.Mode().Perm() {
			same, err := sameContent(ctx, in, targetFS, target)
			if err != nil {
				return 0, err
			}
			if same {
				*unchanged = true
				if progress != nil {
					progress(info.Size(), info.Size())
				}
				return 0, nil
			}
			if _, err := in.Seek(0, io.SeekStart); err != nil {
				return 0, fmt.Errorf("rewind source: %w", err)
			}
		}
	} else if !os.IsNotExist(statErr) {
		return 0, fmt.Errorf("stat destination: %w", statErr)
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
	var reader io.Reader = contextReader{ctx, in}
	if progress != nil {
		reader = &progressReader{in: reader, total: info.Size(), cb: progress}
	}
	n, err = io.CopyBuffer(contextWriter{ctx, out}, reader, make([]byte, 128*1024))
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
	if opts.BackupKeep > 0 {
		if err = targetFS.backup(target, opts.BackupKeep); err != nil {
			return n, err
		}
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

// sameContent reports whether in (read from its current offset) and the file at
// target hold identical bytes. It stops at the first difference.
func sameContent(ctx context.Context, in io.Reader, targetFS filesystem, target string) (same bool, err error) {
	existing, err := targetFS.Open(target)
	if err != nil {
		return false, fmt.Errorf("open destination for comparison: %w", err)
	}
	defer func() { err = errors.Join(err, existing.Close()) }()
	a, b := make([]byte, 128*1024), make([]byte, 128*1024)
	src, dst := contextReader{ctx, in}, contextReader{ctx, existing}
	for {
		na, errA := io.ReadFull(src, a)
		nb, errB := io.ReadFull(dst, b)
		if errA != nil && errA != io.EOF && errA != io.ErrUnexpectedEOF {
			return false, fmt.Errorf("read source: %w", errA)
		}
		if errB != nil && errB != io.EOF && errB != io.ErrUnexpectedEOF {
			return false, fmt.Errorf("read destination: %w", errB)
		}
		if na != nb || !bytes.Equal(a[:na], b[:nb]) {
			return false, nil
		}
		if errA != nil || errB != nil {
			return errA != nil && errB != nil, nil
		}
	}
}

// Backups of NAME are named bak.NAME.bak-TIMESTAMP in NAME's directory.
const backupPrefix, backupMarker = "bak.", ".bak-"

// backupLayout is a fixed-width UTC timestamp, so names sort chronologically.
const backupLayout = "20060102T150405.000000000Z"

// backup hard-links an existing regular target to a new timestamped name after
// pruning that file's oldest backups to keep-1. A missing target needs none.
func (f filesystem) backup(target string, keep int) error {
	info, err := f.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat destination for backup: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cannot back up non-regular destination: %s", target)
	}
	backups, err := f.Backups(target)
	if err != nil {
		return err
	}
	for len(backups) >= keep {
		if err := f.Remove(backups[0]); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove old backup: %w", err)
		}
		backups = backups[1:]
	}
	name := f.join(f.dir(target), backupPrefix+f.base(target)+backupMarker+time.Now().UTC().Format(backupLayout))
	if err := f.Link(target, name); err != nil {
		return fmt.Errorf("back up destination: %w", err)
	}
	return nil
}

// Backups lists target's backups, oldest first.
func (f filesystem) Backups(target string) ([]string, error) {
	entries, err := f.ReadDir(f.dir(target))
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	prefix := backupPrefix + f.base(target) + backupMarker
	var names []string
	for _, e := range entries {
		stamp, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || !e.Mode().IsRegular() {
			continue
		}
		if _, err := time.Parse(backupLayout, stamp); err == nil {
			names = append(names, f.join(f.dir(target), e.Name()))
		}
	}
	sort.Strings(names)
	return names, nil
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
	io.Seeker
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
func (f filesystem) ReadDir(p string) ([]os.FileInfo, error) {
	if f.client != nil {
		return f.client.ReadDir(p)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	infos := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if os.IsNotExist(err) {
			continue // removed while listing
		}
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}
func (f filesystem) Mkdir(p string) error {
	if f.client == nil {
		return os.Mkdir(p, 0700)
	}
	if err := f.client.Mkdir(p); err != nil {
		return err
	}
	return f.client.Chmod(p, 0700)
}
func (f filesystem) Chmod(p string, mode os.FileMode) error {
	if f.client == nil {
		return os.Chmod(p, mode)
	}
	return f.client.Chmod(p, mode)
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

// progressReader reports cumulative bytes read to a callback for progress display.
// It deliberately implements only Read so io.CopyBuffer keeps using the bounded buffer.
type progressReader struct {
	in     io.Reader
	total  int64
	copied int64
	cb     func(copied, total int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.in.Read(p)
	if n > 0 {
		r.copied += int64(n)
		r.cb(r.copied, r.total)
	}
	return n, err
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
