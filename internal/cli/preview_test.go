package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewDoesNotCreateHistory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	dst := filepath.Join(dir, "destination")
	db := filepath.Join(dir, "history.db")
	if err := os.WriteFile(src, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "--history-file", db, "cp", "--dry-run", src, dst)
	if err != nil || !strings.Contains(out, "add") {
		t.Fatal(out, err)
	}
	for _, p := range []string{dst, db} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("preview wrote file", p, err)
		}
	}
	if _, err := execute(t, "--history-file", db, "cp", "--dry-run", src, filepath.Join(dir, "missing", "file")); err == nil {
		t.Fatal("missing parent accepted")
	}
}
