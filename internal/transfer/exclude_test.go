package transfer

import (
	"context"
	"github.com/pkg/sftp"
	"os"
	"path/filepath"
	"testing"
)

func TestExcludedPatterns(t *testing.T) {
	for _, tc := range []struct {
		rel     string
		dir     bool
		pattern string
		want    bool
	}{
		{"sub/.git", true, ".git", true}, {"a/debug.log", false, "*.log", true}, {"a/keep.txt", false, "*.log", false},
		{"build", true, "build/", true}, {"build", false, "build/", false}, {"a/x.txt", false, "a/*.txt", true}, {"b/a/x.txt", false, "a/*.txt", false},
	} {
		if got := excluded(tc.rel, tc.dir, []string{tc.pattern}); got != tc.want {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
	for _, p := range []string{"", "/etc", "../x", "a/../b", "[", "a\\b"} {
		if ValidateExcludes([]string{p}) == nil {
			t.Fatalf("accepted %q", p)
		}
	}
}

func TestCopyTreeExcludes(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "sftp"}[remote], func(t *testing.T) {
			var src, dst *sftp.Client
			if remote {
				src = localSFTP(t)
				dst = localSFTP(t)
			}
			dir := t.TempDir()
			source := filepath.Join(dir, "src")
			target := filepath.Join(dir, "copy")
			for _, name := range []string{"keep.txt", "sub/keep.txt", "sub/debug.log", ".git/config", "sub/.git/config"} {
				p := filepath.Join(source, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("hello"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var excludedPaths []string
			var completed, total int64
			result, err := CopyTree(context.Background(), src, dst, source, target, TreeOptions{Exclude: []string{".git", "*.log"}, Excluded: func(p string) { excludedPaths = append(excludedPaths, p) }, Options: Options{Progress: func(n, all int64) { completed, total = n, all }}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Files != 2 || result.Bytes != 10 || total != 10 || completed != 10 || len(excludedPaths) != 3 {
				t.Fatalf("%+v %d/%d exclusions %v", result, completed, total, excludedPaths)
			}
			for _, name := range []string{".git", "sub/.git", "sub/debug.log"} {
				if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(name))); !os.IsNotExist(err) {
					t.Fatal("excluded path copied", name, err)
				}
			}
		})
	}
}
