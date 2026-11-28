package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/forward"
)

func TestForwardLogsAndFollow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)

	r, err := forward.Allocate(forward.Local, "db", "127.0.0.1:15432", "127.0.0.1:5432")
	if err != nil {
		t.Fatal(err)
	}
	r.Alias = "pg"
	r.Group = "dev"
	r.Status = "stopped"
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	logPath, err := forward.LogPath(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("line1\nline2\nline3\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Read last 2 lines by alias.
	out, err := execute(t, "forward", "logs", "pg", "-n", "2")
	if err != nil {
		t.Fatal(err)
	}
	if out != "line2\nline3\n" {
		t.Fatalf("logs -n 2 = %q", out)
	}

	// Follow appended output and cancel.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := New()
	var followOut bytes.Buffer
	cmd.SetOut(&followOut)
	cmd.SetErr(&followOut)
	cmd.SetArgs([]string{"forward", "logs", r.ID, "-f", "-n", "1"})
	done := make(chan error, 1)
	go func() {
		done <- cmd.ExecuteContext(ctx)
	}()
	time.Sleep(50 * time.Millisecond)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("line4\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	time.Sleep(180 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(followOut.String(), "line3\n") || !strings.Contains(followOut.String(), "line4\n") {
		t.Fatalf("follow output=%q", followOut.String())
	}
}

func TestForwardProfilesCRUDAndLsFilter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	cfg := filepath.Join(t.TempDir(), "connections.toml")
	if err := (config.Store{Path: cfg}).Update(context.Background(), func(m map[string]config.Connection) error {
		m["bastion"] = config.Connection{Host: "127.0.0.1", Port: 22, User: "alice"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := execute(t, "--config", cfg, "forward", "profile", "add", "pg", "bastion", "15432", "db.internal:5432", "--group", "dev", "--reconnect"); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "--config", cfg, "forward", "p", "add", "socks", "bastion", "1080", "-D", "--group", "ops"); err != nil {
		t.Fatal(err)
	}
	// Duplicate alias rejected.
	if _, err := execute(t, "--config", cfg, "forward", "profile", "add", "pg", "bastion", "15433", "5432"); err == nil {
		t.Fatal("accepted duplicate profile alias")
	}

	out, err := execute(t, "--config", cfg, "forward", "profile", "ls", "--group", "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var items []struct {
		Alias string `json:"alias"`
		Group string `json:"group"`
	}
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Alias != "pg" || items[0].Group != "dev" {
		t.Fatalf("unexpected profile ls json: %+v", items)
	}

	if _, err := execute(t, "--config", cfg, "forward", "profile", "rm", "pg"); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "--config", cfg, "forward", "profile", "rm", "pg"); err == nil {
		t.Fatal("removed missing profile")
	}

	// Verify forward ls --group filters records.
	r1, err := forward.Allocate(forward.Local, "bastion", "127.0.0.1:8080", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	r1.Alias, r1.Group, r1.Status = "web", "dev", "stopped"
	if err := r1.Save(); err != nil {
		t.Fatal(err)
	}
	r2, err := forward.Allocate(forward.Local, "bastion", "127.0.0.1:9090", "127.0.0.1:90")
	if err != nil {
		t.Fatal(err)
	}
	r2.Alias, r2.Group, r2.Status = "metrics", "ops", "stopped"
	if err := r2.Save(); err != nil {
		t.Fatal(err)
	}
	lsOut, err := execute(t, "--config", cfg, "forward", "ls", "--group", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lsOut, r1.ID) || strings.Contains(lsOut, r2.ID) {
		t.Fatalf("group filter failed: %q", lsOut)
	}
}
