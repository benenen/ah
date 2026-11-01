package transfer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

// resumeFile never modifies a previous partial. Each invocation stages its own
// file, so concurrent retries cannot write into one another's published inode.
func resumeFile(ctx context.Context, tf filesystem, source, target string, in transferFile, info os.FileInfo, opts Options, progress func(int64, int64), inputClosed *bool) (n int64, err error) {
	digest, prefix, candidate, offset, err := findResume(ctx, tf, source, target, in, info)
	if err != nil {
		return 0, err
	}
	hash := sha256.New()
	tmp := tf.join(tf.dir(target), prefix+rand.Text())
	out, err := tf.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return 0, err
	}
	closed, published := false, false
	defer func() {
		if !closed {
			err = errors.Join(err, out.Close())
		}
		if !published && err != nil {
			err = fmt.Errorf("resume partial retained at %s: %w", tmp, err)
		}
	}()
	if err = out.Chmod(0600); err != nil {
		return 0, err
	}
	hash.Reset()
	// Validate reused bytes against the current source while cloning them into
	// a private staging file. A forged or changing partial cannot be published.
	if candidate != "" {
		partial, e := tf.Open(candidate)
		if e != nil {
			return 0, e
		}
		buf, want := make([]byte, 128*1024), make([]byte, 128*1024)
		for n < offset {
			size := int64(len(buf))
			if offset-n < size {
				size = offset - n
			}
			_, e = io.ReadFull(contextReader{ctx, partial}, buf[:size])
			if e != nil {
				break
			}
			_, e = io.ReadFull(contextReader{ctx, in}, want[:size])
			if e != nil {
				break
			}
			if !bytes.Equal(buf[:size], want[:size]) {
				e = fmt.Errorf("resume partial content does not match source: %s", candidate)
				break
			}
			_, _ = hash.Write(want[:size])
			var written int
			written, e = contextWriter{ctx, out}.Write(want[:size])
			n += int64(written)
			if e != nil {
				break
			}
			if written != int(size) {
				e = io.ErrShortWrite
				break
			}
			if progress != nil {
				progress(n, info.Size())
			}
		}
		e = errors.Join(e, partial.Close())
		if e != nil {
			return n, e
		}
	}
	reader := io.TeeReader(contextReader{ctx, in}, hash)
	var remaining int64
	remaining, err = io.CopyBuffer(contextWriter{ctx, out}, &resumeReader{reader: reader, base: n, total: info.Size(), progress: progress}, make([]byte, 128*1024))
	n += remaining
	if err != nil {
		return n, err
	}
	after, err := in.Stat()
	if err != nil {
		return n, err
	}
	if n != info.Size() || !bytes.Equal(hash.Sum(nil), digest) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return n, fmt.Errorf("source changed during resumable copy")
	}
	err = in.Close()
	*inputClosed = true
	if err != nil {
		return n, err
	}
	if err = out.Chmod(info.Mode().Perm()); err != nil {
		return n, err
	}
	err = out.Close()
	closed = true
	if err != nil {
		return n, err
	}
	if err = ctx.Err(); err != nil {
		return n, err
	}
	if opts.BackupKeep > 0 {
		saved, e := tf.backup(target, opts.BackupKeep, opts.protect)
		if e != nil {
			return n, e
		}
		if opts.savedAs != nil {
			*opts.savedAs = saved
		}
	}
	if opts.replace() {
		err = tf.PosixRename(tmp, target)
	} else {
		err = tf.Link(tmp, target)
	}
	if err != nil {
		return n, fmt.Errorf("publish destination: %w", err)
	}
	published = true
	if !opts.replace() {
		if err = tf.Remove(tmp); err != nil {
			return n, fmt.Errorf("published destination; remove resume partial %s: %w", tmp, err)
		}
	}
	return n, nil
}

type resumeReader struct {
	reader      io.Reader
	base, total int64
	progress    func(int64, int64)
}

func (r *resumeReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.base += int64(n)
	if r.progress != nil {
		r.progress(r.base, r.total)
	}
	return n, err
}

// findResume fingerprints the current source and only selects regular partials
// for that exact content and destination. Reading the partial remains mandatory.
func findResume(ctx context.Context, tf filesystem, source, target string, in transferFile, info os.FileInfo) (digest []byte, prefix, candidate string, offset int64, err error) {
	if _, err = in.Seek(0, io.SeekStart); err != nil {
		return
	}
	hash := sha256.New()
	if _, err = io.CopyBuffer(hash, contextReader{ctx, in}, make([]byte, 128*1024)); err != nil {
		return
	}
	digest = hash.Sum(nil)
	if _, err = in.Seek(0, io.SeekStart); err != nil {
		return
	}
	identity := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%x\x00%d", source, target, digest, info.Mode().Perm())))
	prefix = fmt.Sprintf(".ah-resume-%x-", identity[:16])
	entries, e := tf.ReadDir(tf.dir(target))
	if os.IsNotExist(e) {
		return
	}
	if e != nil {
		err = e
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Size() > entries[j].Size() })
	for _, e := range entries {
		if len(e.Name()) > len(prefix) && e.Name()[:len(prefix)] == prefix && e.Mode().IsRegular() && e.Size() > 0 && e.Size() <= info.Size() {
			candidate = tf.join(tf.dir(target), e.Name())
			offset = e.Size()
			break
		}
	}
	return
}
