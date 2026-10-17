package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/testutil"
)

func TestBackupListAndRestore(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartSSH(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := (config.Store{Path: configPath}).Update(context.Background(), func(m map[string]config.Connection) error {
		m["nas"] = server.Connection
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		flags := []string{"--config", configPath, "--history-file", filepath.Join(dir, "history.db"), "--known-hosts", server.KnownHosts}
		return execute(t, append(flags, args...)...)
	}
	for _, target := range []struct{ name, endpoint, file string }{
		{"remote", "nas:~/app.conf", filepath.Join(server.Root, "app.conf")},
		{"local", filepath.Join(dir, "local", "app.conf"), filepath.Join(dir, "local", "app.conf")},
	} {
		t.Run(target.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(target.file), 0700); err != nil {
				t.Fatal(err)
			}
			if out, err := run("backup", "ls", target.endpoint); err != nil || !strings.Contains(out, "No backups of "+target.endpoint) {
				t.Fatalf("empty ls: %s %v", out, err)
			}
			if _, err := run("backup", "restore", target.endpoint); err == nil {
				t.Fatal("restored without backups")
			}
			source := filepath.Join(dir, "source-"+target.name)
			for _, v := range []string{"v1", "v2", "v3"} {
				if err := os.WriteFile(source, []byte(v), 0600); err != nil {
					t.Fatal(err)
				}
				if out, err := run("cp", "--backup", source, target.endpoint); err != nil {
					t.Fatalf("cp: %s %v", out, err)
				}
				time.Sleep(time.Millisecond)
			}
			out, err := run("b", "ls", target.endpoint)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) != 3 || !strings.HasPrefix(lines[0], "#") || !strings.HasPrefix(lines[1], "1 ") || !strings.Contains(lines[1], "bak.app.conf.bak-") {
				t.Fatalf("ls:\n%s", out)
			}
			newest := strings.Fields(lines[1])[4]
			oldest := strings.Fields(lines[2])[4]
			stamp := strings.TrimPrefix(oldest, "bak.app.conf.bak-")

			// Default: the newest backup (v2); the replaced v3 is saved.
			out, err = run("backup", "restore", target.endpoint)
			if err != nil || !strings.Contains(out, "Restored "+target.endpoint+" from "+newest) || !strings.Contains(out, "replaced version saved as bak.app.conf.bak-") {
				t.Fatalf("restore newest: %s %v", out, err)
			}
			if data, _ := os.ReadFile(target.file); string(data) != "v2" {
				t.Fatalf("after restore: %q", data)
			}
			// By timestamp prefix: the oldest (v1).
			if out, err := run("backup", "restore", target.endpoint, stamp[:len(stamp)-3]); err != nil {
				t.Fatalf("restore by stamp: %s %v", out, err)
			}
			if data, _ := os.ReadFile(target.file); string(data) != "v1" {
				t.Fatalf("after stamp restore: %q", data)
			}
			if out, err := run("backup", "restore", target.endpoint, oldest); err != nil || !strings.Contains(out, "nothing changed") {
				t.Fatalf("identical restore: %s %v", out, err)
			}
			// Undo: the newest backup now holds v2, replaced by the last restore.
			if out, err := run("backup", "restore", target.endpoint, "1"); err != nil {
				t.Fatalf("undo: %s %v", out, err)
			}
			if data, _ := os.ReadFile(target.file); string(data) != "v2" {
				t.Fatalf("after undo: %q", data)
			}
			for _, bad := range []string{"99", "0", "2026", "19990101", "bak.other.bak-1"} {
				if _, err := run("backup", "restore", target.endpoint, bad); err == nil {
					t.Fatalf("accepted version %q", bad)
				}
			}
			// The date alone matches every backup taken today.
			if _, err := run("backup", "restore", target.endpoint, stamp[:8]); err == nil || !strings.Contains(err.Error(), "matches 5 backups") {
				t.Fatalf("ambiguous prefix: %v", err)
			}
			if _, err := run("backup", "restore", "--backup-keep", "0", target.endpoint); err == nil {
				t.Fatal("accepted --backup-keep 0")
			}
		})
	}
}
