package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectionShell(t *testing.T) {
	p := filepath.Join(t.TempDir(), "connections.toml")
	run := func(args ...string) (string, error) { return execute(t, append([]string{"--config", p}, args...)...) }
	if _, err := run("new", "win", "--host", "win.example", "--user", "alice", "--shell", "pwsh"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("new", "linux", "--host", "linux.example", "--user", "bob"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"pwsh -NoLogo", "a;rm", "$(id)", "bash'"} {
		if _, err := run("edit", "linux", "--shell", bad); err == nil {
			t.Errorf("accepted shell %q", bad)
		}
		if _, err := run("--shell", bad, "list"); err == nil {
			t.Errorf("accepted --shell %q", bad)
		}
	}
	table, err := run("list")
	if err != nil {
		t.Fatal(err)
	}
	header := strings.Fields(strings.SplitN(table, "\n", 2)[0])
	if strings.Join(header, " ") != "NAME HOST PORT USER TERM SHELL TAGS NOTE" {
		t.Fatalf("header %q", header)
	}
	for _, line := range strings.Split(strings.TrimSpace(table), "\n")[1:] {
		fields := strings.Fields(line)
		want := map[string]string{"win": "pwsh", "linux": "-"}[fields[0]]
		if fields[5] != want {
			t.Fatalf("%s shell column %q want %q", fields[0], fields[5], want)
		}
	}
	out, err := run("list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Name, Shell string }
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Name != "linux" || rows[0].Shell != "" || rows[1].Shell != "pwsh" {
		t.Fatalf("json rows %+v", rows)
	}
	if _, err := run("edit", "win", "--shell", ""); err != nil {
		t.Fatal(err)
	}
	if out, err := run("inspect", "win"); err != nil || !strings.Contains(out, `"shell": ""`) {
		t.Fatal(out, err)
	}
	if out, err := run("--shell", "zsh", "inspect", "win"); err != nil || !strings.Contains(out, `"shell": "zsh"`) {
		t.Fatal(out, err)
	}
}
