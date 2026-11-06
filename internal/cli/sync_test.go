package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benenen/ah/internal/history"
)

func TestSyncCLIHistoryAndPreview(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	for _, p := range []string{source, target} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for filename, data := range map[string]string{filepath.Join(source, "file"): "new", filepath.Join(target, "file"): "old", filepath.Join(target, "extra"): "extra", filepath.Join(target, "keep.log"): "keep"} {
		if err := os.WriteFile(filename, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db := filepath.Join(dir, "history.db")
	for _, command := range [][]string{{"diff", source, target}, {"sync", "--delete", "--dry-run", source, target}} {
		out, err := execute(t, append([]string{"--history-file", db}, command...)...)
		if err != nil || !strings.Contains(out, "modify") {
			t.Fatal(out, err)
		}
		if _, err := os.Stat(db); !os.IsNotExist(err) {
			t.Fatal("preview created history")
		}
		data, err := os.ReadFile(filepath.Join(target, "file"))
		if err != nil || string(data) != "old" {
			t.Fatal("preview wrote files")
		}
	}
	out, err := execute(t, "--history-file", db, "sync", "--delete", "--exclude", "*.log", source, target)
	if err != nil || !strings.Contains(out, "Synced 1 files") || !strings.Contains(out, "1 deleted") {
		t.Fatal(out, err)
	}
	store, err := history.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if !record.Sync || !record.Delete || len(record.Exclude) != 1 || record.Status != "success" {
		t.Fatalf("%+v", record)
	}
	out, err = execute(t, "--history-file", db, "history", "show", "1")
	if err != nil || !strings.Contains(out, "'sync'") || !strings.Contains(out, "'--delete'") || strings.Contains(out, "'--recursive'") {
		t.Fatal(out, err)
	}
	if err := os.WriteFile(filepath.Join(target, "again"), []byte("extra"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := execute(t, "--history-file", db, "history", "run", "1"); err != nil {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Join(target, "again")); !os.IsNotExist(err) {
		t.Fatal("rerun lost delete option")
	}
	if _, err := os.Stat(filepath.Join(target, "keep.log")); err != nil {
		t.Fatal("rerun lost exclude", err)
	}
}

func TestSyncProtectsHistoryFromDeletion(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	for _, p := range []string{source, target} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "new"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(target, "history.db")
	_, err := execute(t, "--history-file", db, "sync", "--delete", source, target)
	if err == nil || !strings.Contains(err.Error(), "history database") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "new")); !os.IsNotExist(err) {
		t.Fatal("wrote before checking history protection")
	}
	store, err := history.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.Get(context.Background(), 1)
	if err != nil || record.Status != "failed" {
		t.Fatal(record, err)
	}
}
