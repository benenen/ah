package transfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pkg/sftp"
)

// SyncOptions synchronize directory contents into the exact target root.
type SyncOptions struct {
	TreeOptions
	Delete bool
}

type directoryEntry struct {
	info    os.FileInfo
	blocked bool
}

type directoryPlan struct {
	source, target               string
	sourceEntries, targetEntries map[string]directoryEntry
	changes                      []PlanEntry
	unchanged                    int
}

// DiffDirectories compares bytes and file permissions without changing either tree.
// Target-only entries are reported regardless of the deletion option.
func DiffDirectories(ctx context.Context, src, dst *sftp.Client, source, target string, opts SyncOptions, emit func(PlanEntry) error) (err error) {
	defer closeOnCancel(ctx, src, dst)(&err)
	p, err := planDirectories(ctx, filesystem{src}, filesystem{dst}, source, target, opts)
	if err != nil {
		return err
	}
	for _, e := range p.changes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

// SyncDirectories publishes changed files first, then removes target-only entries
// when requested. A failure leaves completed changes in place; the tree is not atomic.
func SyncDirectories(ctx context.Context, src, dst *sftp.Client, source, target string, opts SyncOptions) (result TreeResult, err error) {
	defer closeOnCancel(ctx, src, dst)(&err)
	sf, tf := filesystem{src}, filesystem{dst}
	p, err := planDirectories(ctx, sf, tf, source, target, opts)
	if err != nil {
		return result, err
	}
	for _, e := range p.changes {
		if e.Action == "conflict" {
			return result, fmt.Errorf("file type conflict: %s", e.Target)
		}
		if opts.CheckTarget != nil && (e.Action == "add" || e.Action == "modify" || e.Action == "mkdir" || (opts.Delete && e.Action == "target-only")) {
			if err := opts.CheckTarget(e.Target); err != nil {
				return result, err
			}
		}
	}
	options := opts.Options
	options.Force = true
	if err := tf.requirePublish(options); err != nil {
		return result, err
	}
	var created []string
	defer func() {
		for i := len(created) - 1; i >= 0; i-- {
			rel := created[i]
			err = errors.Join(err, tf.Chmod(tf.below(p.target, rel), p.sourceEntries[rel].info.Mode().Perm()))
		}
	}()
	for _, e := range p.changes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		rel, err := directoryRelative(tf, p.target, e.Target)
		if err != nil {
			return result, err
		}
		switch e.Action {
		case "mkdir", "add", "modify":
			if err := checkDirectoryParents(tf, p.target, e.Target); err != nil {
				return result, err
			}
		}
		switch e.Action {
		case "mkdir":
			if err := tf.Mkdir(e.Target); err != nil {
				return result, err
			}
			created = append(created, rel)
			result.Dirs++
		case "add", "modify":
			n, unchanged, copyErr := copyFile(ctx, sf, tf, e.Source, e.Target, options, opts.Progress)
			result.Bytes += n
			if copyErr != nil {
				return result, copyErr
			}
			if unchanged {
				result.Unchanged++
			} else {
				result.Files++
			}
		case "skip-special":
			if opts.Skipped != nil {
				entry := p.sourceEntries[rel]
				location := e.Source
				if !entry.blocked {
					entry = p.targetEntries[rel]
					location = e.Target
				}
				opts.Skipped(location, entry.info.Mode())
			}
		case "exclude":
			if opts.Excluded != nil {
				opts.Excluded(e.Target)
			}
		}
	}
	result.Unchanged += p.unchanged
	if !opts.Delete {
		return result, nil
	}
	// Refuse pruning if the source inventory changed after the comparison/copy.
	current, err := scanDirectory(ctx, sf, p.source, opts.Exclude, false)
	if err != nil {
		return result, err
	}
	if !sameInventory(p.sourceEntries, current) {
		return result, fmt.Errorf("source changed during sync; target-only entries were retained")
	}
	for i := len(p.changes) - 1; i >= 0; i-- {
		e := p.changes[i]
		if e.Action != "target-only" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		rel, err := directoryRelative(tf, p.target, e.Target)
		if err != nil {
			return result, err
		}
		old := p.targetEntries[rel].info
		if err := checkDirectoryParents(tf, p.target, e.Target); err != nil {
			return result, err
		}
		now, err := tf.Lstat(e.Target)
		if err != nil {
			return result, err
		}
		if now.Mode() != old.Mode() || (!old.IsDir() && (now.Size() != old.Size() || !now.ModTime().Equal(old.ModTime()))) {
			return result, fmt.Errorf("destination changed before deletion: %s", e.Target)
		}
		if old.IsDir() && tf.client != nil {
			err = tf.client.RemoveDirectory(e.Target)
		} else {
			err = tf.Remove(e.Target)
		}
		if err != nil {
			return result, fmt.Errorf("delete %s: %w", e.Target, err)
		}
		result.Deleted++
	}
	return result, nil
}

func planDirectories(ctx context.Context, sf, tf filesystem, source, target string, opts SyncOptions) (p directoryPlan, err error) {
	if err := ValidateExcludes(opts.Exclude); err != nil {
		return p, err
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if source, err = canonicalDirectoryRoot(sf, source); err != nil {
		return p, fmt.Errorf("source: %w", err)
	}
	if target, err = canonicalDirectoryRoot(tf, target); err != nil {
		return p, fmt.Errorf("destination: %w", err)
	}
	if opts.SameFilesystem && (withinDirectory(sf, source, target) || withinDirectory(tf, target, source)) {
		return p, fmt.Errorf("source and destination directories must not overlap")
	}
	p.source, p.target = source, target
	p.sourceEntries, err = scanDirectory(ctx, sf, source, opts.Exclude, false)
	if err != nil {
		return p, fmt.Errorf("source: %w", err)
	}
	p.targetEntries, err = scanDirectory(ctx, tf, target, opts.Exclude, true)
	if err != nil {
		return p, fmt.Errorf("destination: %w", err)
	}
	keys := map[string]bool{}
	for rel := range p.sourceEntries {
		keys[rel] = true
	}
	for rel := range p.targetEntries {
		keys[rel] = true
	}
	ordered := make([]string, 0, len(keys))
	for rel := range keys {
		ordered = append(ordered, rel)
	}
	sort.Strings(ordered)
	protected := protectedDirectories(p.sourceEntries, p.targetEntries)
	for _, rel := range ordered {
		if blockedParent(rel, p.sourceEntries) || blockedParent(rel, p.targetEntries) {
			continue
		}
		from, sourceExists := p.sourceEntries[rel]
		to, targetExists := p.targetEntries[rel]
		action := ""
		switch {
		case from.blocked || to.blocked:
			action = "skip-special"
			if (sourceExists && excluded(rel, from.info.IsDir(), opts.Exclude)) || (targetExists && excluded(rel, to.info.IsDir(), opts.Exclude)) {
				action = "exclude"
			}
		case !sourceExists:
			action = "target-only"
			if to.info.IsDir() && protected[rel] {
				action = "retain"
			}
		case !targetExists:
			action = "add"
			if from.info.IsDir() {
				action = "mkdir"
			}
		case from.info.IsDir() != to.info.IsDir():
			action = "conflict"
		case from.info.IsDir():
			continue
		default:
			same := false
			if from.info.Size() == to.info.Size() && from.info.Mode().Perm() == to.info.Mode().Perm() {
				in, err := sf.Open(sf.below(source, rel))
				if err != nil {
					return p, err
				}
				var compareErr error
				same, compareErr = sameContent(ctx, in, tf, tf.below(target, rel))
				if err := errors.Join(compareErr, in.Close()); err != nil {
					return p, err
				}
			}
			if same {
				p.unchanged++
				continue
			}
			action = "modify"
		}
		e := PlanEntry{Action: action, Target: tf.below(target, rel)}
		if sourceExists {
			e.Source = sf.below(source, rel)
		}
		p.changes = append(p.changes, e)
	}
	return p, nil
}

func scanDirectory(ctx context.Context, f filesystem, root string, patterns []string, allowMissing bool) (map[string]directoryEntry, error) {
	entries := map[string]directoryEntry{}
	var walk func(string) error
	walk = func(rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := f.Lstat(f.below(root, rel))
		if err != nil {
			if rel == "" && allowMissing && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if rel == "" && !info.IsDir() {
			return fmt.Errorf("expected directory: %s", root)
		}
		blocked := (rel != "" && excluded(rel, info.IsDir(), patterns)) || (!info.IsDir() && !info.Mode().IsRegular())
		entries[rel] = directoryEntry{info: info, blocked: blocked}
		if !info.IsDir() || blocked {
			return nil
		}
		children, err := f.ReadDir(f.below(root, rel))
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.Name() == "." || child.Name() == ".." || (strings.Contains(child.Name(), "/") || (f.client == nil && strings.ContainsRune(child.Name(), filepath.Separator))) {
				return fmt.Errorf("invalid directory entry %q", child.Name())
			}
			if err := walk(path.Join(rel, child.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	return entries, walk("")
}

func canonicalDirectoryRoot(f filesystem, value string) (string, error) {
	root, err := f.resolve(value)
	if err != nil {
		return "", err
	}
	root = f.clean(root)
	info, err := f.Lstat(root)
	if err == nil && !info.IsDir() {
		return "", fmt.Errorf("expected directory (root symlinks are not followed): %s", root)
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	existing := root
	suffix := []string{}
	for os.IsNotExist(err) {
		parent := f.dir(existing)
		if parent == existing {
			return "", err
		}
		suffix = append(suffix, f.base(existing))
		existing = parent
		info, err = f.Stat(existing)
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("parent is not a directory: %s", existing)
	}
	if f.client == nil {
		existing, err = filepath.EvalSymlinks(existing)
	} else {
		existing, err = f.client.RealPath(existing)
	}
	if err != nil {
		return "", err
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		existing = f.join(existing, suffix[i])
	}
	// Only the target root may be missing; its parent must already exist.
	if len(suffix) > 1 {
		return "", fmt.Errorf("directory parent does not exist: %s", root)
	}
	return existing, nil
}

func withinDirectory(f filesystem, root, name string) bool {
	return root == name || strings.HasPrefix(name, strings.TrimSuffix(root, f.separator())+f.separator())
}

func blockedParent(rel string, entries map[string]directoryEntry) bool {
	for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
		if entries[parent].blocked {
			return true
		}
	}
	return false
}

func protectedDirectories(maps ...map[string]directoryEntry) map[string]bool {
	protected := map[string]bool{}
	for _, entries := range maps {
		for name, e := range entries {
			if !e.blocked {
				continue
			}
			for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
				protected[parent] = true
			}
		}
	}
	return protected
}

func sameInventory(a, b map[string]directoryEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for name, old := range a {
		now, ok := b[name]
		if !ok || old.info.Mode() != now.info.Mode() || old.info.Size() != now.info.Size() || !old.info.ModTime().Equal(now.info.ModTime()) {
			return false
		}
	}
	return true
}

func directoryRelative(f filesystem, root, name string) (string, error) {
	if !withinDirectory(f, root, name) {
		return "", fmt.Errorf("path outside destination: %s", name)
	}
	if name == root {
		return "", nil
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(name, root), f.separator())
	if f.client == nil {
		rel = filepath.ToSlash(rel)
	}
	return rel, nil
}

func checkDirectoryParents(f filesystem, root, name string) error {
	if name == root {
		return nil
	}
	for parent := f.dir(name); ; parent = f.dir(parent) {
		info, err := f.Lstat(parent)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("destination parent is not a directory: %s", parent)
		}
		if parent == root {
			return nil
		}
		if !withinDirectory(f, root, parent) {
			return fmt.Errorf("path outside destination: %s", name)
		}
	}
}
