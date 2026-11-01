package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/history"
)

func TestHistoryFiltersBeforeLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.db")
	store, err := history.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, src := range []string{"dev:/a", "dev2:/a", "./dev:/a", "dev:/a"} {
		id, err := store.Begin(context.Background(), history.Record{Source: src, Destination: "./out", StartedAt: base.Add(time.Duration(i) * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		status := "failed"
		if i == 3 {
			status = "success"
		}
		if err := store.Finish(context.Background(), id, 0, status, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "--history-file", p, "history", "--json", "--connection", "dev", "--status", "failed", "--limit", "1", "--after", "2026-01-01T00:00:00Z", "--before", "2026-01-01T03:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	var rows []history.Record
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != 1 {
		t.Fatalf("unexpected matches %s", out)
	}
	for _, args := range [][]string{{"--status", "bad"}, {"--after", "yesterday"}, {"--after", "2026-01-02T00:00:00Z", "--before", "2026-01-01T00:00:00Z"}} {
		if _, err := execute(t, append([]string{"--history-file", p, "history"}, args...)...); err == nil {
			t.Fatal("invalid filter accepted")
		}
	}
}

func TestListJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "connections.toml")
	if _, err := execute(t, "--config", p, "new", "dev", "--host", "example.com", "--user", "alice", "--proxy", "socks5://secret:password@127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "--config", p, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["name"] != "dev" || strings.Contains(out, "secret") || strings.Contains(out, "password") {
		t.Fatal(out)
	}
}
