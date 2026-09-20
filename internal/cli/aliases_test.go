package cli

import (
	"strings"
	"testing"
)

func TestCommandAliasesAndHelp(t *testing.T) {
	root := New()
	help, err := execute(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for name, alias := range map[string]string{"connect": "c", "copy": "cp", "edit": "e", "forward": "f", "history": "h", "list": "ls", "new": "n", "remove": "rm", "version": "v"} {
		full, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		short, _, err := root.Find([]string{alias})
		if err != nil || full != short || full.Name() != name {
			t.Fatalf("%s/%s did not resolve to same command", name, alias)
		}
		found := false
		for _, line := range strings.Split(help, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), name+" ") && strings.Contains(line, "alias: "+alias+")") {
				found = true
			}
		}
		if !found {
			t.Fatalf("help omitted %s alias %s: %s", name, alias, help)
		}
		if _, err := execute(t, alias, "--help"); err != nil {
			t.Fatal(err)
		}
	}
	// h is a command, while -h continues to request root help.
	if out, err := execute(t, "-h"); err != nil || !strings.Contains(out, "Available Commands:") {
		t.Fatal("root help flag changed", err)
	}
}
