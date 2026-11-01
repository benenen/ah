package cli

import (
	"context"
	"github.com/benenen/ah/internal/history"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestExcludeHistoryReplay(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	db := filepath.Join(dir, "history.db")
	if err := os.Mkdir(src, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"keep", "skip.log"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	flags := []string{"--config", filepath.Join(dir, "config"), "--key-file", filepath.Join(dir, "key"), "--history-file", db}
	run := func(args ...string) (string, error) {
		return execute(t, append(append([]string{}, flags...), args...)...)
	}
	if out, err := run("cp", "-r", "--resume", "--exclude", "*.log", src, dst); err != nil {
		t.Fatal(out, err)
	}
	store, err := history.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records, err := store.Search(context.Background(), "", 20)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	r := records[0]
	if !r.Resume || len(r.Exclude) != 1 || r.Exclude[0] != "*.log" {
		t.Fatal("exclude not stored", r)
	}
	out, err := run("history", "show", strconv.FormatInt(r.ID, 10))
	if err != nil || (!strings.Contains(out, "--exclude") || !strings.Contains(out, "--resume")) {
		t.Fatal(out, err)
	}
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
	if out, err := run("history", "run", strconv.FormatInt(r.ID, 10)); err != nil {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "skip.log")); !os.IsNotExist(err) {
		t.Fatal("replay copied exclusion", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "keep")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"cp", "--exclude", "*.log", src, dst}, {"cp", "-r", "--exclude", "[", src, dst}} {
		if _, err := run(args...); err == nil {
			t.Fatal("invalid exclusion accepted")
		}
	}
}
