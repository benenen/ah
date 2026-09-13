package completion

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLocalPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local 中文.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "local-dir"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := LocalPaths(filepath.Join(dir, "loc"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "local 中文.txt"), filepath.Join(dir, "local-dir") + "/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("local completion %q want %q", got, want)
	}
}
