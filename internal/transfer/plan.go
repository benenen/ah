package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/pkg/sftp"
)

// PlanEntry describes a read-only observation, not a guarantee that a later
// copy will see the same files. Backup names are generated only during copying.
type PlanEntry struct {
	Action string `json:"action"`
	Source string `json:"source,omitempty"`
	Target string `json:"target"`
}

// Plan resolves cp destinations and inspects contents without creating target
// files, directories, backups or history. emit may return an output error.
func Plan(ctx context.Context, src, dst *sftp.Client, source, target string, recursive bool, opts TreeOptions, emit func(PlanEntry) error) (err error) {
	if err = ValidateExcludes(opts.Exclude); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	defer closeOnCancel(ctx, src, dst)(&err)
	sf, tf := filesystem{src}, filesystem{dst}
	source, err = sf.resolve(source)
	if err != nil {
		return err
	}
	target, err = tf.resolve(target)
	if err != nil {
		return err
	}
	source, target = sf.clean(source), tf.clean(target)
	info, err := sf.Stat(source)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if info.IsDir() && !recursive {
		return fmt.Errorf("source is a directory (use --recursive)")
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("source must be a regular file or directory")
	}
	if ti, e := tf.Stat(target); e == nil && ti.IsDir() {
		target = tf.join(target, sf.base(source))
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	if info.IsDir() && opts.SameFilesystem && (target == source || strings.HasPrefix(target, strings.TrimSuffix(source, tf.separator())+tf.separator())) {
		return fmt.Errorf("cannot copy directory into itself")
	}
	if err = tf.requirePublish(opts.Options); err != nil {
		return err
	}
	parent, err := tf.Stat(tf.dir(target))
	if err != nil {
		return fmt.Errorf("destination parent: %w", err)
	}
	if !parent.IsDir() {
		return fmt.Errorf("destination parent is not a directory")
	}
	var walk func(string, string, string, os.FileInfo) error
	walk = func(from, to, rel string, info os.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if rel != "" && excluded(rel, info.IsDir(), opts.Exclude) {
			return emit(PlanEntry{Action: "exclude", Source: from, Target: to})
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return emit(PlanEntry{Action: "skip-special", Source: from, Target: to})
		}
		existing, e := tf.Lstat(to)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		exists := e == nil
		if exists && !opts.replace() {
			return fmt.Errorf("destination exists (use --force): %s", to)
		}
		if info.IsDir() {
			if exists && !existing.IsDir() {
				return fmt.Errorf("destination is not a directory: %s", to)
			}
			action := "mkdir"
			if exists {
				action = "merge"
			}
			if err := emit(PlanEntry{Action: action, Source: from, Target: to}); err != nil {
				return err
			}
			children, err := sf.ReadDir(from)
			if err != nil {
				return err
			}
			sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
			for _, child := range children {
				childRel := child.Name()
				if rel != "" {
					childRel = rel + "/" + childRel
				}
				if err := walk(sf.join(from, child.Name()), tf.join(to, child.Name()), childRel, child); err != nil {
					return err
				}
			}
			return nil
		}
		if opts.CheckTarget != nil {
			if err := opts.CheckTarget(to); err != nil {
				return err
			}
		}
		action := "add"
		if exists {
			if existing.IsDir() {
				return fmt.Errorf("destination is a directory: %s", to)
			}
			action = "replace"
			if existing.Mode().IsRegular() && existing.Size() == info.Size() && existing.Mode().Perm() == info.Mode().Perm() {
				in, err := sf.Open(from)
				if err != nil {
					return err
				}
				same, compareErr := sameContent(ctx, in, tf, to)
				if err := errors.Join(compareErr, in.Close()); err != nil {
					return err
				}
				if same {
					return emit(PlanEntry{Action: "unchanged", Source: from, Target: to})
				}
			}
			if opts.BackupKeep > 0 {
				if !existing.Mode().IsRegular() {
					return fmt.Errorf("cannot back up non-regular destination: %s", to)
				}
				backups, err := tf.Backups(to)
				if err != nil {
					return err
				}
				count := len(backups) - opts.BackupKeep + 1
				for i := 0; i < count; i++ {
					if err := emit(PlanEntry{Action: "prune-backup", Target: backups[i]}); err != nil {
						return err
					}
				}
				if err := emit(PlanEntry{Action: "backup", Target: to}); err != nil {
					return err
				}
			}
		}
		if opts.Resume {
			in, err := sf.Open(from)
			if err != nil {
				return err
			}
			_, _, candidate, offset, scanErr := findResume(ctx, tf, from, to, in, info)
			if scanErr == nil && candidate != "" {
				same, e := sameContent(ctx, io.LimitReader(in, offset), tf, candidate)
				scanErr = e
				if e == nil && !same {
					scanErr = fmt.Errorf("resume partial content does not match source: %s", candidate)
				}
			}
			if err := errors.Join(scanErr, in.Close()); err != nil {
				return err
			}
			if candidate != "" {
				if err := emit(PlanEntry{Action: "resume-prefix", Source: candidate, Target: to}); err != nil {
					return err
				}
			}
		}
		return emit(PlanEntry{Action: action, Source: from, Target: to})
	}
	return walk(source, target, "", info)
}
