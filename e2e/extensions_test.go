//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testFeatureExtensions(t *testing.T, l *lab) {
	if out := l.ok("doctor", "lab"); !strings.Contains(out, "host-key\tOK") || !strings.Contains(out, "sftp\tOK") {
		t.Fatal(out)
	}
	l.ok("clone", "lab", "feature-bastion")
	l.ok("new", "feature-private", "--host", "127.0.0.1", "--port", "22", "--user", "tester", "--identity-file", l.identity, "--jump", "feature-bastion")
	l.ok("rename", "feature-bastion", "feature-renamed")
	// This uses the container's loopback SSH port, available through the jump.
	if out := l.ok("--trust-new-host", "c", "feature-private", "whoami"); strings.TrimSpace(out) != "tester" {
		t.Fatal(out)
	}
	if out := l.ok("doctor", "feature-private"); !strings.Contains(out, "jump-feature-renamed/host-key\tOK") {
		t.Fatal(out)
	}
	tree := l.path("feature-tree")
	if err := os.MkdirAll(filepath.Join(tree, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tree, "keep.txt"), "payload", 0600)
	writeFile(t, filepath.Join(tree, "skip.log"), "ignored", 0600)
	writeFile(t, filepath.Join(tree, ".git", "config"), "ignored", 0600)
	out := l.ok("cp", "-r", "--dry-run", "--exclude", ".git", "--exclude", "*.log", tree, "feature-private:~/feature-destination")
	if !strings.Contains(out, "exclude") || !strings.Contains(out, "add") {
		t.Fatal(out)
	}
	if got := l.remote("feature-private", "test ! -e ~/feature-destination && printf absent"); got != "absent" {
		t.Fatal("preview created target", got)
	}
	l.ok("cp", "-r", "--resume", "--exclude", ".git", "--exclude", "*.log", tree, "feature-private:~/feature-destination")
	if got := l.remote("feature-private", "cat ~/feature-destination/keep.txt && test ! -e ~/feature-destination/skip.log && test ! -e ~/feature-destination/.git"); got != "payload" {
		t.Fatal(got)
	}
	downloaded := l.path("feature-downloaded")
	l.ok("cp", "--resume", "feature-private:~/feature-destination/keep.txt", downloaded)
	if readFile(t, downloaded) != "payload" {
		t.Fatal("resume download content")
	}
	var connections []map[string]any
	if err := json.Unmarshal([]byte(l.ok("list", "--json")), &connections); err != nil || len(connections) < 3 {
		t.Fatal(connections, err)
	}
	var records []struct {
		ID      int64    `json:"id"`
		Status  string   `json:"status"`
		Resume  bool     `json:"resume"`
		Exclude []string `json:"exclude"`
	}
	if err := json.Unmarshal([]byte(l.ok("history", "--json", "--status", "success", "--connection", "feature-private")), &records); err != nil || len(records) != 2 {
		t.Fatal(records, err)
	}
	found := false
	for _, r := range records {
		if !r.Resume || r.Status != "success" {
			t.Fatal(records)
		}
		if len(r.Exclude) == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("recursive exclusion metadata missing", records)
	}
}

func checkForwardOptions(t *testing.T, l *lab, id string, enabled bool) {
	t.Helper()
	var record struct {
		ContinueOnError, Reconnect bool
		KeepAlive                  int64
	}
	p := filepath.Join(l.dir, "home", ".config", "ah", "forwards", id+".json")
	if err := json.Unmarshal([]byte(readFile(t, p)), &record); err != nil {
		t.Fatal(err)
	}
	if record.ContinueOnError != enabled || record.Reconnect != enabled || (record.KeepAlive > 0) != enabled {
		t.Fatalf("forward options not retained/overridden: %+v", record)
	}
}
