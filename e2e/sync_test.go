//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDirectorySync(t *testing.T, l *lab) {
	source := l.path("sync-source")
	writeFile(t, filepath.Join(source, "file"), "v1", 0640)
	writeFile(t, filepath.Join(source, "空 格", "nested"), "nested", 0600)
	remote := "lab:~/sync-target"
	if out := l.ok("diff", source, remote); !strings.Contains(out, "add") {
		t.Fatal(out)
	}
	l.ok("sync", source, remote)
	l.remote("lab", "printf extra > ~/sync-target/extra; mkdir ~/sync-target/old; printf keep > ~/sync-target/old/keep.log; printf remove > ~/sync-target/old/remove")
	writeFile(t, filepath.Join(source, "file"), "v2", 0640)
	out := l.ok("sync", "--delete", "--dry-run", "--exclude", "*.log", source, remote)
	if !strings.Contains(out, "modify") || !strings.Contains(out, "delete") {
		t.Fatal(out)
	}
	if got := l.remote("lab", "cat ~/sync-target/file ~/sync-target/extra"); got != "v1extra" {
		t.Fatal("preview wrote files", got)
	}
	l.ok("sync", source, remote)
	if got := l.remote("lab", "cat ~/sync-target/extra"); got != "extra" {
		t.Fatal("default deleted extra", got)
	}
	l.ok("sync", "--delete", "--exclude", "*.log", source, remote)
	if got := l.remote("lab", "cat ~/sync-target/file ~/sync-target/old/keep.log; test ! -e ~/sync-target/extra && test ! -e ~/sync-target/old/remove && printf pruned"); got != "v2keeppruned" {
		t.Fatal(got)
	}
	l.ok("sync", remote, "lab:~/sync-mirror")
	download := l.path("sync-download")
	l.ok("sync", "lab:~/sync-mirror", download)
	if readFile(t, filepath.Join(download, "file")) != "v2" || readFile(t, filepath.Join(download, "空 格", "nested")) != "nested" {
		t.Fatal("download differs")
	}
	if _, err := os.Stat(filepath.Join(download, "sync-mirror")); !os.IsNotExist(err) {
		t.Fatal("unexpected source basename")
	}
}
