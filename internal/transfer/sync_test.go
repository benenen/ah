package transfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func putSyncFile(t *testing.T, root, name, data string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(data), 0640); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryDiffAndSync(t *testing.T) {
	for _, direction := range []string{"local-local", "local-remote", "remote-local", "remote-remote"} {
		t.Run(direction, func(t *testing.T) {
			var src, dst *sftp.Client
			if strings.HasPrefix(direction, "remote-") {
				src = localSFTP(t)
			}
			if strings.HasSuffix(direction, "-remote") {
				dst = localSFTP(t)
			}
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source, target := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
			putSyncFile(t, source, "same", "same")
			putSyncFile(t, target, "same", "same")
			putSyncFile(t, source, "changed", "new!")
			putSyncFile(t, target, "changed", "old!")
			putSyncFile(t, source, "空 格/new", "added")
			putSyncFile(t, target, "old/sub/file", "extra")
			original, err := os.Stat(filepath.Join(target, "same"))
			if err != nil {
				t.Fatal(err)
			}
			opts := SyncOptions{TreeOptions: TreeOptions{SameFilesystem: true}}
			var changes []string
			err = DiffDirectories(context.Background(), src, dst, source, target, opts, func(e PlanEntry) error {
				rel, err := filepath.Rel(target, e.Target)
				if err != nil {
					return err
				}
				changes = append(changes, e.Action+":"+filepath.ToSlash(rel))
				return nil
			})
			want := []string{"modify:changed", "target-only:old", "target-only:old/sub", "target-only:old/sub/file", "mkdir:空 格", "add:空 格/new"}
			if err != nil || !reflect.DeepEqual(changes, want) {
				t.Fatalf("changes %v, error %v", changes, err)
			}
			if readFile(t, filepath.Join(target, "changed")) != "old!" {
				t.Fatal("diff wrote destination")
			}
			result, err := SyncDirectories(context.Background(), src, dst, source, target, opts)
			if err != nil || result.Files != 2 || result.Unchanged != 1 || result.Deleted != 0 {
				t.Fatalf("result %+v, %v", result, err)
			}
			if readFile(t, filepath.Join(target, "changed")) != "new!" || readFile(t, filepath.Join(target, "空 格/new")) != "added" {
				t.Fatal("wrong contents")
			}
			if _, err := os.Stat(filepath.Join(target, "src")); !os.IsNotExist(err) {
				t.Fatal("unexpected source basename")
			}
			now, err := os.Stat(filepath.Join(target, "same"))
			if err != nil || !os.SameFile(original, now) {
				t.Fatal("unchanged file replaced", err)
			}
			if readFile(t, filepath.Join(target, "old/sub/file")) != "extra" {
				t.Fatal("default removed extra")
			}
			opts.Delete = true
			result, err = SyncDirectories(context.Background(), src, dst, source, target, opts)
			if err != nil || result.Deleted != 3 || result.Unchanged != 3 {
				t.Fatalf("delete result %+v, %v", result, err)
			}
			if _, err := os.Stat(filepath.Join(target, "old")); !os.IsNotExist(err) {
				t.Fatal("extra directory retained", err)
			}
		})
	}
}

func TestDirectorySyncProtectsExcludedAndSpecialEntries(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	putSyncFile(t, source, "keep", "source")
	putSyncFile(t, target, "keep", "old")
	putSyncFile(t, target, "extra/cache/keep.log", "protected")
	putSyncFile(t, target, "extra/remove", "remove")
	putSyncFile(t, target, "ignored/local", "protected")
	putSyncFile(t, source, "ignored/remote", "ignored")
	outside := filepath.Join(dir, "outside")
	putSyncFile(t, outside, "secret", "untouched")
	if err := os.Symlink(outside, filepath.Join(target, "link")); err != nil {
		t.Skip(err)
	}
	putSyncFile(t, source, "link/secret", "do not follow")
	opts := SyncOptions{TreeOptions: TreeOptions{SameFilesystem: true, Exclude: []string{"*.log", "ignored/"}}, Delete: true}
	result, err := SyncDirectories(context.Background(), nil, nil, source, target, opts)
	if err != nil || result.Deleted != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	if readFile(t, filepath.Join(target, "extra/cache/keep.log")) != "protected" || readFile(t, filepath.Join(target, "ignored/local")) != "protected" || readFile(t, filepath.Join(outside, "secret")) != "untouched" {
		t.Fatal("protected file modified")
	}
}

func TestDirectorySyncRejectsConflictAndOverlapBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	putSyncFile(t, source, "a", "new")
	putSyncFile(t, source, "z/child", "new")
	putSyncFile(t, target, "z", "conflict")
	putSyncFile(t, target, "extra", "retain")
	opts := SyncOptions{TreeOptions: TreeOptions{SameFilesystem: true}, Delete: true}
	if _, err := SyncDirectories(context.Background(), nil, nil, source, target, opts); err == nil {
		t.Fatal("accepted conflict")
	}
	if _, err := os.Stat(filepath.Join(target, "a")); !os.IsNotExist(err) {
		t.Fatal("wrote before detecting conflict")
	}
	if readFile(t, filepath.Join(target, "extra")) != "retain" {
		t.Fatal("deleted on failure")
	}
	for _, pair := range [][2]string{{source, source}, {source, filepath.Join(source, "nested")}, {source, dir}} {
		if _, err := SyncDirectories(context.Background(), nil, nil, pair[0], pair[1], opts); err == nil {
			t.Fatal("accepted overlap", pair)
		}
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(dir, alias); err == nil {
		if _, err := SyncDirectories(context.Background(), nil, nil, source, filepath.Join(alias, "src", "nested"), opts); err == nil {
			t.Fatal("accepted aliased overlap")
		}
	}
}

func TestDirectorySyncFailureRetainsExtras(t *testing.T) {
	for _, failure := range []string{"guard", "cancel", "source-change"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
			putSyncFile(t, source, "new", "new data")
			putSyncFile(t, target, "extra", "retain")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts := SyncOptions{TreeOptions: TreeOptions{SameFilesystem: true}, Delete: true}
			switch failure {
			case "guard":
				opts.CheckTarget = func(p string) error {
					if filepath.Base(p) == "extra" {
						return fmt.Errorf("protected")
					}
					return nil
				}
			case "cancel":
				opts.Progress = func(int64, int64) { cancel() }
			case "source-change":
				opts.Progress = func(int64, int64) { putSyncFile(t, source, "later", "added during sync") }
			}
			_, err := SyncDirectories(ctx, nil, nil, source, target, opts)
			if err == nil {
				t.Fatal("expected failure")
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if readFile(t, filepath.Join(target, "extra")) != "retain" {
				t.Fatal("pruned after failure")
			}
			if failure == "guard" {
				if _, err := os.Stat(filepath.Join(target, "new")); !os.IsNotExist(err) {
					t.Fatal("guard did not run before writes")
				}
			}
		})
	}
}

func TestDirectorySyncMissingRootAndPermissionDifference(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	putSyncFile(t, source, "empty/file", "data")
	if err := os.Mkdir(filepath.Join(source, "emptydir"), 0750); err != nil {
		t.Fatal(err)
	}
	opts := SyncOptions{TreeOptions: TreeOptions{SameFilesystem: true}}
	if _, err := SyncDirectories(context.Background(), nil, nil, source, target, opts); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(target, "empty/file"), 0600); err != nil {
		t.Fatal(err)
	}
	// Equal size and time must not hide content or permission changes.
	when := time.Unix(1700000000, 0)
	for _, root := range []string{source, target} {
		if err := os.Chtimes(filepath.Join(root, "empty/file"), when, when); err != nil {
			t.Fatal(err)
		}
	}
	var actions []string
	if err := DiffDirectories(context.Background(), nil, nil, source, target, opts, func(e PlanEntry) error { actions = append(actions, e.Action); return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actions, []string{"modify"}) {
		t.Fatal(actions)
	}
	if _, err := SyncDirectories(context.Background(), nil, nil, source, filepath.Join(dir, "missing", "child"), opts); err == nil {
		t.Fatal("accepted missing parent")
	}
}
