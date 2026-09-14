package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestHistoryRunForceOverride(t *testing.T) {
	for _, tc := range []struct {
		name     string
		original bool
		flags    []string
		want     bool
	}{
		{"default rejects", false, nil, false},
		{"explicit force", false, []string{"--force"}, true},
		{"short force", false, []string{"-f"}, true},
		{"inherits force", true, nil, true},
		{"explicit no force", true, []string{"--force=false"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src")
			dst := filepath.Join(dir, "dst")
			db := filepath.Join(dir, "history.db")
			for path, value := range map[string]string{src: "original", dst: "existing"} {
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			base := []string{"--history-file", db, "--config", filepath.Join(dir, "config.toml")}
			args := append(append([]string{}, base...), "cp", src, dst)
			if tc.original {
				args = append(args, "--force")
			} else {
				if err := os.Remove(dst); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := execute(t, args...); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(src, []byte("updated"), 0600); err != nil {
				t.Fatal(err)
			}
			args = append(append([]string{}, base...), "h", "run", "1")
			args = append(args, tc.flags...)
			_, err := execute(t, args...)
			if (err == nil) != tc.want {
				t.Fatalf("run success=%v want %v: %v", err == nil, tc.want, err)
			}
			data, err := os.ReadFile(dst)
			if err != nil {
				t.Fatal(err)
			}
			want := "original"
			if tc.want {
				want = "updated"
			}
			if string(data) != want {
				t.Fatalf("destination=%q want %q", data, want)
			}
			store, err := history.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			record, err := store.Get(context.Background(), 2)
			if err != nil {
				t.Fatal(err)
			}
			if record.Force != tc.want {
				t.Fatalf("recorded force=%v want %v", record.Force, tc.want)
			}
		})
	}
}

func TestHistoryClean(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "history.db")
	store, err := history.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, status := range []string{"success", "failed", "canceled"} {
		id, err := store.Begin(ctx, history.Record{Source: "source", Destination: "target"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Finish(ctx, id, 0, status, ""); err != nil {
			t.Fatal(err)
		}
	}
	running, err := store.Begin(ctx, history.Record{Source: "active", Destination: "target"})
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--history-file", db, "h", "clean"}
	out, err := execute(t, args...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "3") {
		t.Fatalf("missing removal count: %s", out)
	}
	records, err := store.Search(ctx, "", 20)
	if err != nil || len(records) != 1 || records[0].ID != running {
		t.Fatalf("running record not retained: %v %v", records, err)
	}
	if err := store.Finish(ctx, running, 0, "success", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, args...); err != nil {
		t.Fatal(err)
	}
	next, err := store.Begin(ctx, history.Record{})
	if err != nil || next <= running {
		t.Fatal("history IDs reused", err)
	}
	if _, err := execute(t, append(args, "unexpected")...); err == nil {
		t.Fatal("accepted extra arguments")
	}
}

func TestHistoryCleanFilters(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   []string
		want    int
		invalid bool
	}{
		{"failed", []string{"--failed"}, 3, false},
		{"retention", []string{"--keep-days", "7"}, 3, false},
		{"combined", []string{"--failed", "--keep-days", "7"}, 4, false},
		{"zero", []string{"--keep-days", "0"}, 5, true},
		{"negative", []string{"--keep-days", "-1"}, 5, true},
		{"overflow", []string{"--keep-days", "999999999"}, 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "history.db")
			store, err := history.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			for _, entry := range []struct {
				status string
				days   int
			}{{"failed", 10}, {"failed", 1}, {"success", 10}, {"canceled", 1}, {"running", 10}} {
				id, err := store.Begin(context.Background(), history.Record{StartedAt: time.Now().Add(-time.Duration(entry.days) * 24 * time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				if entry.status != "running" {
					if err := store.Finish(context.Background(), id, 0, entry.status, ""); err != nil {
						t.Fatal(err)
					}
				}
			}
			args := append([]string{"--history-file", db, "h", "clean"}, tc.flags...)
			_, err = execute(t, args...)
			if (err != nil) != tc.invalid {
				t.Fatalf("unexpected clean error: %v", err)
			}
			records, err := store.Search(context.Background(), "", 20)
			if err != nil || len(records) != tc.want {
				t.Fatalf("remaining %d want %d: %v", len(records), tc.want, err)
			}
			if _, err := store.Get(context.Background(), 5); err != nil {
				t.Fatal("removed running record", err)
			}
			if !tc.invalid {
				if _, err := store.Get(context.Background(), 1); err == nil {
					t.Fatal("old failed record retained")
				}
			}
		})
	}
}
