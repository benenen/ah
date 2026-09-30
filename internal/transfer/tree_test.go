package transfer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// writeTree creates src/{a.txt, 空 格/b.txt, empty/, ro/c.txt} plus a symlink.
func writeTree(t *testing.T, src string) {
	t.Helper()
	for name, data := range map[string]string{"a.txt": "alpha", "空 格/b.txt": "beta", "ro/c.txt": "gamma"} {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(src, "empty"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	// A read-only directory must still be filled before its mode applies.
	if err := os.Chmod(filepath.Join(src, "ro"), 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(src, "ro"), 0755) })
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCopyTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	for _, mode := range []string{"local-local", "local-remote", "remote-local"} {
		t.Run(mode, func(t *testing.T) {
			var src, dst *sftp.Client
			if mode == "local-remote" {
				dst = localSFTP(t)
			}
			if mode == "remote-local" {
				src = localSFTP(t)
			}
			dir := t.TempDir()
			source := filepath.Join(dir, "src")
			writeTree(t, source)
			var skipped []string
			var last, total int64
			opts := TreeOptions{
				SameFilesystem: mode == "local-local",
				Skipped:        func(p string, _ os.FileMode) { skipped = append(skipped, p) },
				Options:        Options{Progress: func(n, all int64) { last, total = n, all }},
			}

			// A missing target becomes the copy itself.
			copyRoot := filepath.Join(dir, "copy")
			result, err := CopyTree(context.Background(), src, dst, source, copyRoot, opts)
			if err != nil {
				t.Fatal(err)
			}
			if result.Files != 3 || result.Dirs != 4 || result.Bytes != 14 || last != 14 || total != 14 {
				t.Fatalf("result %+v progress %d/%d", result, last, total)
			}
			if readFile(t, filepath.Join(copyRoot, "空 格", "b.txt")) != "beta" || readFile(t, filepath.Join(copyRoot, "ro", "c.txt")) != "gamma" {
				t.Fatal("wrong contents")
			}
			if len(skipped) != 1 || filepath.Base(skipped[0]) != "link" {
				t.Fatalf("skipped %v", skipped)
			}
			if _, err := os.Lstat(filepath.Join(copyRoot, "link")); !os.IsNotExist(err) {
				t.Fatal("symlink was copied")
			}
			for name, want := range map[string]os.FileMode{"": 0755, "ro": 0555, "empty": 0750} {
				info, err := os.Stat(filepath.Join(copyRoot, name))
				if err != nil || info.Mode().Perm() != want {
					t.Fatalf("%q mode %v %v", name, info.Mode().Perm(), err)
				}
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(copyRoot, "ro"), 0755) })

			// An existing directory target receives target/BASE.
			into := filepath.Join(dir, "into")
			if err := os.Mkdir(into, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := CopyTree(context.Background(), src, dst, source, into, opts); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(into, "src", "ro"), 0755) })
			if readFile(t, filepath.Join(into, "src", "a.txt")) != "alpha" {
				t.Fatal("not copied under target/BASE")
			}

			// Without --force an existing copy root is refused before any write.
			if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("ALPHA"), 0640); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "new.txt"), []byte("new"), 0640); err != nil {
				t.Fatal(err)
			}
			if _, err := CopyTree(context.Background(), src, dst, source, into, opts); err == nil || !strings.Contains(err.Error(), "--force") {
				t.Fatalf("existing destination: %v", err)
			}
			if _, err := os.Stat(filepath.Join(into, "src", "new.txt")); !os.IsNotExist(err) {
				t.Fatal("refused copy wrote files")
			}

			// --force merges: files replaced, extra destination files kept.
			if err := os.WriteFile(filepath.Join(into, "src", "extra"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(into, "src", "ro"), 0755); err != nil {
				t.Fatal(err)
			}
			opts.Force = true
			if _, err := CopyTree(context.Background(), src, dst, source, into, opts); err != nil {
				t.Fatal(err)
			}
			if readFile(t, filepath.Join(into, "src", "a.txt")) != "ALPHA" || readFile(t, filepath.Join(into, "src", "new.txt")) != "new" || readFile(t, filepath.Join(into, "src", "extra")) != "keep" {
				t.Fatal("merge lost or kept wrong files")
			}
			entries, _ := os.ReadDir(filepath.Join(into, "src"))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".ah-copy-") {
					t.Fatalf("leaked staging file %s", e.Name())
				}
			}
		})
	}
}

func TestCopyTreeRefusals(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(source, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	same := TreeOptions{SameFilesystem: true, Options: Options{Force: true}}
	if _, err := CopyTree(context.Background(), nil, nil, source, filepath.Join(source, "sub"), same); err == nil || !strings.Contains(err.Error(), "into itself") {
		t.Fatalf("copied into itself: %v", err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyTree(context.Background(), nil, nil, source, file, same); err == nil {
		t.Fatal("merged a directory over a file")
	}
	// A source file where the merge target holds a directory is not nested.
	if err := os.WriteFile(filepath.Join(source, "x"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	into := filepath.Join(dir, "into")
	if err := os.MkdirAll(filepath.Join(into, "src", "x"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyTree(context.Background(), nil, nil, source, into, same); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("file over directory: %v", err)
	}
	denied := TreeOptions{Options: Options{Force: true}, CheckTarget: func(p string) error {
		if filepath.Base(p) == "x" {
			return os.ErrPermission
		}
		return nil
	}}
	if _, err := CopyTree(context.Background(), nil, nil, source, filepath.Join(dir, "checked"), denied); err == nil {
		t.Fatal("CheckTarget was ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CopyTree(ctx, nil, nil, source, filepath.Join(dir, "canceled"), TreeOptions{}); err == nil {
		t.Fatal("canceled copy succeeded")
	}
	// A regular file copies like Copy.
	result, err := CopyTree(context.Background(), nil, nil, filepath.Join(source, "x"), filepath.Join(dir, "single"), TreeOptions{})
	if err != nil || result.Files != 1 || readFile(t, filepath.Join(dir, "single")) != "x" {
		t.Fatalf("single file: %+v %v", result, err)
	}
}

func TestCopyBackupKeepsNewest(t *testing.T) {
	for _, mode := range []string{"local", "remote"} {
		t.Run(mode, func(t *testing.T) {
			var dst *sftp.Client
			if mode == "remote" {
				dst = localSFTP(t)
			}
			dir := t.TempDir()
			source, target := filepath.Join(dir, "source"), filepath.Join(dir, "config.toml")
			if err := os.WriteFile(target, []byte("v0"), 0600); err != nil {
				t.Fatal(err)
			}
			// An unrelated file sharing the prefix is never pruned.
			other := filepath.Join(dir, "bak.config.toml.bak-notes")
			if err := os.WriteFile(other, nil, 0600); err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 4; i++ {
				if err := os.WriteFile(source, []byte("v"+string(rune('0'+i))), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := Copy(context.Background(), nil, dst, source, target, Options{BackupKeep: 2}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Millisecond)
			}
			if readFile(t, target) != "v4" {
				t.Fatal("target not replaced")
			}
			backups, err := filesystem{}.Backups(target)
			if err != nil {
				t.Fatal(err)
			}
			if len(backups) != 2 || !strings.HasPrefix(filepath.Base(backups[0]), "bak.config.toml.bak-") || readFile(t, backups[0]) != "v2" || readFile(t, backups[1]) != "v3" {
				t.Fatalf("backups %v", backups)
			}
			if _, err := os.Stat(other); err != nil {
				t.Fatal("pruned an unrelated file")
			}
			// No existing target means no backup.
			fresh := filepath.Join(dir, "fresh")
			if _, err := Copy(context.Background(), nil, dst, source, fresh, Options{BackupKeep: 2}); err != nil {
				t.Fatal(err)
			}
			if backups, _ := (filesystem{}).Backups(fresh); len(backups) != 0 {
				t.Fatalf("backed up a new file: %v", backups)
			}
		})
	}
}

func TestUnchangedFilesAreSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	for _, mode := range []string{"local", "remote"} {
		t.Run(mode, func(t *testing.T) {
			var dst *sftp.Client
			if mode == "remote" {
				dst = localSFTP(t)
			}
			dir := t.TempDir()
			source := filepath.Join(dir, "src")
			if err := os.MkdirAll(filepath.Join(source, "sub"), 0700); err != nil {
				t.Fatal(err)
			}
			big := strings.Repeat("0123456789", 30000) // spans several compare buffers
			files := map[string]string{"same.txt": big, "sub/changed.txt": big, "grown.txt": "v1"}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(name)), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			into := filepath.Join(dir, "into")
			if err := os.Mkdir(into, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := CopyTree(context.Background(), nil, dst, source, into, TreeOptions{}); err != nil {
				t.Fatal(err)
			}
			// Same size, last byte differs; and a size change.
			changed := big[:len(big)-1] + "X"
			if err := os.WriteFile(filepath.Join(source, "sub", "changed.txt"), []byte(changed), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "grown.txt"), []byte("v2 longer"), 0600); err != nil {
				t.Fatal(err)
			}
			var unchanged []string
			var last, total int64
			opts := TreeOptions{Options: Options{BackupKeep: 3,
				Unchanged: func(p string) { unchanged = append(unchanged, filepath.Base(p)) },
				Progress:  func(n, all int64) { last, total = n, all },
			}}
			result, err := CopyTree(context.Background(), nil, dst, source, into, opts)
			if err != nil {
				t.Fatal(err)
			}
			if result.Files != 2 || result.Unchanged != 1 || result.Bytes != int64(len(changed)+len("v2 longer")) || len(unchanged) != 1 || unchanged[0] != "same.txt" {
				t.Fatalf("result %+v unchanged %v", result, unchanged)
			}
			if last != total || total != int64(2*len(big)+len("v2 longer")) {
				t.Fatalf("progress %d/%d", last, total)
			}
			root := filepath.Join(into, "src")
			if readFile(t, filepath.Join(root, "sub", "changed.txt")) != changed || readFile(t, filepath.Join(root, "grown.txt")) != "v2 longer" {
				t.Fatal("changed files not replaced")
			}
			for name, want := range map[string]int{"same.txt": 0, "sub/changed.txt": 1, "grown.txt": 1} {
				backups, err := filesystem{}.Backups(filepath.Join(root, filepath.FromSlash(name)))
				if err != nil || len(backups) != want {
					t.Fatalf("%s backups %v %v", name, backups, err)
				}
			}

			// A permission change alone still replaces the file.
			if err := os.Chmod(filepath.Join(source, "same.txt"), 0640); err != nil {
				t.Fatal(err)
			}
			unchanged = nil
			n, err := Copy(context.Background(), nil, dst, filepath.Join(source, "same.txt"), filepath.Join(root, "same.txt"), Options{Force: true, Unchanged: opts.Unchanged})
			if err != nil || n != int64(len(big)) || len(unchanged) != 0 {
				t.Fatalf("mode change: %d %v %v", n, err, unchanged)
			}
			if info, _ := os.Stat(filepath.Join(root, "same.txt")); info.Mode().Perm() != 0640 {
				t.Fatalf("mode %v", info.Mode().Perm())
			}
			n, err = Copy(context.Background(), nil, dst, filepath.Join(source, "same.txt"), filepath.Join(root, "same.txt"), Options{Force: true, Unchanged: opts.Unchanged})
			if err != nil || n != 0 || len(unchanged) != 1 {
				t.Fatalf("identical single copy: %d %v %v", n, err, unchanged)
			}
		})
	}
}
