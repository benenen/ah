package transfer

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pkg/sftp"
)

// TreeOptions extend Options for recursive copies.
type TreeOptions struct {
	Options
	// SameFilesystem reports that source and target are on one filesystem, so a
	// target inside the source tree would copy the tree into itself.
	SameFilesystem bool
	// Skipped receives each symbolic link or special file left out of the copy.
	Skipped func(path string, mode os.FileMode)
	// CheckTarget, when non-nil, vets every destination file path before it is
	// written; an error aborts the copy.
	CheckTarget func(path string) error
}

// TreeResult summarizes a recursive copy.
type TreeResult struct {
	Files, Dirs int
	// Unchanged counts existing destination files left alone because they
	// already matched; they are not in Files or Bytes.
	Unchanged int
	Bytes     int64
}

type treeEntry struct {
	rel  string // slash-separated path below the source root; "" is the root
	mode os.FileMode
	size int64
}

// CopyTree copies a directory like cp -r: when target is an existing directory
// the tree lands at target/BASE, otherwise target becomes the copy and its
// parent must exist. Without Force or BackupKeep an existing copy root is
// refused before anything is written; with them the tree is merged and files
// are replaced (or backed up) one by one. Symbolic links and special files are
// skipped and reported. A regular-file source is copied as by Copy.
//
// Each file is published atomically, but the tree is not: after a failure the
// files already copied remain, and a rerun needs Force to merge over them.
func CopyTree(ctx context.Context, src, dst *sftp.Client, source, target string, opts TreeOptions) (result TreeResult, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	defer closeOnCancel(ctx, src, dst)(&err)
	sourceFS, targetFS := filesystem{src}, filesystem{dst}
	if source, err = sourceFS.resolve(source); err != nil {
		return result, err
	}
	if target, err = targetFS.resolve(target); err != nil {
		return result, err
	}
	source, target = sourceFS.clean(source), targetFS.clean(target)
	info, err := sourceFS.Stat(source)
	if err != nil {
		return result, fmt.Errorf("stat source: %w", err)
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return result, fmt.Errorf("source must be a regular file or directory: %s", source)
		}
		if targetInfo, statErr := targetFS.Stat(target); statErr == nil && targetInfo.IsDir() {
			target = targetFS.join(target, sourceFS.base(source))
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return result, fmt.Errorf("stat destination: %w", statErr)
		}
		if err = targetFS.requirePublish(opts.Options); err != nil {
			return result, err
		}
		if opts.CheckTarget != nil {
			if err = opts.CheckTarget(target); err != nil {
				return result, err
			}
		}
		var unchanged bool
		result.Bytes, unchanged, err = copyFile(ctx, sourceFS, targetFS, source, target, opts.Options, opts.Progress)
		switch {
		case err != nil:
		case unchanged:
			result.Unchanged = 1
			if opts.Unchanged != nil {
				opts.Unchanged(target)
			}
		default:
			result.Files = 1
		}
		return result, err
	}

	if targetInfo, statErr := targetFS.Stat(target); statErr == nil && targetInfo.IsDir() {
		target = targetFS.join(target, sourceFS.base(source))
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return result, fmt.Errorf("stat destination: %w", statErr)
	}
	if opts.SameFilesystem && (target == source || strings.HasPrefix(target, strings.TrimSuffix(source, targetFS.separator())+targetFS.separator())) {
		return result, fmt.Errorf("cannot copy directory %s into itself (%s)", source, target)
	}
	if existing, statErr := targetFS.Lstat(target); statErr == nil {
		if !opts.replace() {
			return result, fmt.Errorf("destination exists (use --force to merge): %s", target)
		}
		if !existing.IsDir() {
			return result, fmt.Errorf("destination exists and is not a directory: %s", target)
		}
	} else if !os.IsNotExist(statErr) {
		return result, fmt.Errorf("stat destination: %w", statErr)
	}
	if err = targetFS.requirePublish(opts.Options); err != nil {
		return result, err
	}

	// Walk first so progress has a total and skipped entries are known up front.
	entries := []treeEntry{{mode: info.Mode()}}
	var total int64
	var walk func(rel string) error
	walk = func(rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := sourceFS.below(source, rel)
		children, err := sourceFS.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("read source directory %s: %w", dir, err)
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			childRel := path.Join(rel, child.Name())
			switch mode := child.Mode(); {
			case mode.IsDir():
				entries = append(entries, treeEntry{rel: childRel, mode: mode})
				if err := walk(childRel); err != nil {
					return err
				}
			case mode.IsRegular():
				entries = append(entries, treeEntry{rel: childRel, mode: mode, size: child.Size()})
				total += child.Size()
			default:
				if opts.Skipped != nil {
					opts.Skipped(sourceFS.below(source, childRel), mode)
				}
			}
		}
		return nil
	}
	if err = walk(""); err != nil {
		return result, err
	}

	// Directories stay owner-writable until every file is in place, then take
	// the source permissions deepest first so read-only ones apply last.
	var created []treeEntry
	defer func() {
		for i := len(created) - 1; i >= 0; i-- {
			d := created[i]
			if chmodErr := targetFS.Chmod(targetFS.below(target, d.rel), d.mode.Perm()); chmodErr != nil && err == nil {
				err = fmt.Errorf("set directory permissions: %w", chmodErr)
			}
		}
	}()
	var copied int64
	for _, e := range entries {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		to := targetFS.below(target, e.rel)
		if e.mode.IsDir() {
			existing, statErr := targetFS.Lstat(to)
			switch {
			case statErr == nil && existing.IsDir():
				// Merging: keep the existing directory and its permissions.
			case statErr == nil:
				return result, fmt.Errorf("destination exists and is not a directory: %s", to)
			case os.IsNotExist(statErr):
				if err = targetFS.Mkdir(to); err != nil {
					return result, fmt.Errorf("create directory %s: %w", to, err)
				}
				created = append(created, e)
			default:
				return result, fmt.Errorf("stat destination: %w", statErr)
			}
			result.Dirs++
			continue
		}
		if opts.CheckTarget != nil {
			if err = opts.CheckTarget(to); err != nil {
				return result, err
			}
		}
		var progress func(int64, int64)
		if opts.Progress != nil {
			base := copied
			progress = func(n, _ int64) { opts.Progress(base+n, total) }
		}
		n, unchanged, copyErr := copyFile(ctx, sourceFS, targetFS, sourceFS.below(source, e.rel), to, opts.Options, progress)
		result.Bytes += n
		if unchanged {
			n = e.size // progress counts skipped bytes as done
		}
		copied += n
		if copyErr != nil {
			return result, fmt.Errorf("%s: %w", sourceFS.below(source, e.rel), copyErr)
		}
		if unchanged {
			result.Unchanged++
			if opts.Unchanged != nil {
				opts.Unchanged(to)
			}
			continue
		}
		result.Files++
	}
	if opts.Progress != nil && total == 0 {
		opts.Progress(0, 0)
	}
	return result, nil
}

func (f filesystem) clean(p string) string {
	if f.client == nil {
		return filepath.Clean(p)
	}
	return path.Clean(p)
}

func (f filesystem) separator() string {
	if f.client == nil {
		return string(filepath.Separator)
	}
	return "/"
}

// below joins a slash-separated relative path onto root in f's path syntax.
func (f filesystem) below(root, rel string) string {
	if rel == "" {
		return root
	}
	if f.client == nil {
		return filepath.Join(root, filepath.FromSlash(rel))
	}
	return path.Join(root, rel)
}
