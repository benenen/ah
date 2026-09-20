package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/forward"
)

func TestParseLocalForward(t *testing.T) {
	local, target, err := parseLocalForward("8080", "80")
	if err != nil || local != "127.0.0.1:8080" || target != "127.0.0.1:80" {
		t.Fatalf("default addresses: %q %q %v", local, target, err)
	}

	for _, tc := range []struct{ local, target, want string }{
		{"8080", "127.0.0.1:80", "127.0.0.1:8080"},
		{"0.0.0.0:8080", "database.internal:5432", "0.0.0.0:8080"},
		{"[::1]:8080", "[::1]:80", "[::1]:8080"},
	} {
		listen, target, err := parseLocalForward(tc.local, tc.target)
		if err != nil || listen != tc.want || target != tc.target {
			t.Fatalf("parse %q %q: %q %q %v", tc.local, tc.target, listen, target, err)
		}
	}
	for _, args := range [][2]string{
		{"0", "host:80"}, {"65536", "host:80"}, {"8080", "host:0"},
		{"8080", ":80"}, {":8080", "host:80"}, {"8080", "host"},
		{"8080:127.0.0.1:80", "host:80"}, {"8080", "::1:80"},
		{"8080", "host:http"}, {"-1", "host:80"},
	} {
		if _, _, err := parseLocalForward(args[0], args[1]); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestForwardArguments(t *testing.T) {
	for _, args := range [][]string{
		{"forward", "dev"}, {"forward", "dev", "8080"},
		{"forward", "dev", "-L", "8080:localhost:80"},
		{"forward", "dev", "8080", "localhost:80", "extra"},
	} {
		if _, err := execute(t, args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestForwardListJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	out, err := execute(t, "forward", "ls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty listing must stay valid JSON: %q", out)
	}
	if _, err := forward.Allocate("dev", "127.0.0.1:8080", "127.0.0.1:80"); err != nil {
		t.Fatal(err)
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	// Documented layout: <user config dir>/ah/forwards/<ID>.json.
	corrupt := filepath.Join(configDir, "ah", "forwards", "0123456789abcdef.json")
	if err := os.WriteFile(corrupt, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = execute(t, "forward", "ls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var listed []struct {
		ID      string    `json:"id"`
		Name    string    `json:"name"`
		Listen  string    `json:"listen"`
		Target  string    `json:"target"`
		PID     int       `json:"pid"`
		Status  string    `json:"status"`
		Started time.Time `json:"started"`
		Error   string    `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	status := make(map[string]string, len(listed))
	for _, r := range listed {
		status[r.ID] = r.Status
	}
	if len(listed) != 2 || status["0123456789abcdef"] != "corrupt" {
		t.Fatalf("unexpected records: %v", listed)
	}
	if listed[1].Name != "dev" || listed[1].Status != "starting" || listed[1].Listen != "127.0.0.1:8080" {
		t.Fatalf("readable record: %v", listed[1])
	}
	if strings.Contains(out, "socket") || strings.Contains(out, "key_path") {
		t.Fatalf("internal fields leaked into JSON: %q", out)
	}
	table, err := execute(t, "forward", "ls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table, "STATUS") || !strings.Contains(table, "corrupt") {
		t.Fatalf("table view lost the corrupt record: %q", table)
	}
}

func TestForwardRemoveCorruptRecord(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(configDir, "ah", "forwards", "0123456789abcdef.json")
	if err := os.MkdirAll(filepath.Dir(corrupt), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "forward", "rm", "0123456789abcdef"); err == nil {
		t.Fatal("plain rm deleted an unreadable record")
	}
	if _, err := os.Stat(corrupt); err != nil {
		t.Fatalf("record removed without --force: %v", err)
	}
	out, err := execute(t, "forward", "rm", "-f", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Warning") || !strings.Contains(out, "Removed 0123456789abcdef") {
		t.Fatalf("output: %q", out)
	}
	if _, err := os.Stat(corrupt); !os.IsNotExist(err) {
		t.Fatalf("record survived --force: %v", err)
	}
}
