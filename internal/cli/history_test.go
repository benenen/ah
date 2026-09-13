package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/history"
	"github.com/benenen/ah/internal/testutil"
)

func TestLocalRemoteCopyHistorySearchAndReplay(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartSSH(t)
	workdir := t.TempDir()
	key, err := os.ReadFile(server.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "identity"), key, 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workdir)
	connection := server.Connection
	connection.IdentityFile = "identity"
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	dbPath := filepath.Join(dir, "history.db")
	if err := (config.Store{Path: configPath}).Update(context.Background(), func(m map[string]config.Connection) error { m["nas"] = connection; return nil }); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "README 中文 $(literal).md")
	if err := os.WriteFile(source, []byte("upload data"), 0600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, source)
	if err != nil {
		t.Fatal(err)
	}
	flags := []string{"--config", configPath, "--history-file", dbPath, "--known-hosts", server.KnownHosts}
	run := func(args ...string) (string, error) {
		return execute(t, append(append([]string{}, flags...), args...)...)
	}
	if out, err := run("cp", rel, "nas:~/upload.md"); err != nil {
		t.Fatalf("upload %s %v", out, err)
	}
	download := filepath.Join(dir, "download.md")
	if out, err := run("cp", "nas:~/upload.md", download); err != nil {
		t.Fatalf("download %s %v", out, err)
	}
	if data, err := os.ReadFile(download); err != nil || string(data) != "upload data" {
		t.Fatal("download bytes", err)
	}
	// Repeating a no-clobber copy must produce a failed history row.
	if _, err := run("cp", rel, "nas:~/upload.md"); err == nil {
		t.Fatal("overwrite accepted")
	}
	store, err := history.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.Search(context.Background(), "", 20)
	if err != nil || len(rows) != 3 {
		t.Fatalf("history rows=%d err=%v", len(rows), err)
	}
	if rows[0].Status != "failed" || rows[1].Status != "success" || rows[1].Bytes != 11 {
		t.Fatalf("wrong history outcomes: %+v", rows)
	}
	first := rows[2]
	if first.Cwd == "" || first.Source != rel {
		t.Fatalf("relative context not saved: %+v", first)
	}
	if out, err := run("history", "README", "中文"); err != nil || !strings.Contains(out, "upload.md") {
		t.Fatalf("search %s %v", out, err)
	}
	out, err := run("history", "show", strconv.FormatInt(first.ID, 10))
	if err != nil || !strings.Contains(out, "ah ") || !strings.Contains(out, source) {
		t.Fatalf("show command %q %v", out, err)
	}
	if err := os.Remove(filepath.Join(server.Root, "upload.md")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if out, err := run("history", "run", strconv.FormatInt(first.ID, 10)); err != nil {
		t.Fatalf("replay %s %v", out, err)
	}
	rows, err = store.Search(context.Background(), "", 20)
	if err != nil || len(rows) != 4 || rows[0].Status != "success" {
		t.Fatal("replay not recorded", err)
	}
	if out, err := run("history", "run", "999999"); err == nil {
		t.Fatalf("missing history accepted %s", out)
	}
}

func TestLocalCopyDoesNotNeedConnections(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "file")
	dst := filepath.Join(dir, "copy")
	if err := os.WriteFile(src, []byte("local"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "--config", filepath.Join(dir, "absent.toml"), "--history-file", filepath.Join(dir, "history.db"), "cp", src, dst)
	if err != nil {
		t.Fatal(out, err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "local" {
		t.Fatal("local copy", err)
	}
}

func TestCopyCannotReplaceHistoryDatabase(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "payload")
	db := filepath.Join(dir, "history.db")
	if err := os.WriteFile(source, []byte("must not replace database"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := execute(t, "--history-file", db, "cp", "--force", source, db)
	if err == nil {
		t.Fatal("history database overwritten")
	}
	store, err := history.Open(db)
	if err != nil {
		t.Fatalf("history database corrupted: %v", err)
	}
	defer store.Close()
	rows, err := store.Search(context.Background(), "", 20)
	if err != nil || len(rows) != 1 || rows[0].Status != "failed" {
		t.Fatalf("failed attempt not preserved: %+v %v", rows, err)
	}
}
