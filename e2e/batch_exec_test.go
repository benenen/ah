//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func testBatchExec(t *testing.T, l *lab) {
	for _, name := range []string{"lab", "ops", "feature-private"} {
		l.ok("edit", name, "--tag", "batch-prod", "--tag", "batch-web")
	}
	if out := l.ok("exec", "--tag", "batch-prod,batch-web", "--dry-run", "--", "true"); out != "feature-private\nlab\nops\n" {
		t.Fatal(out)
	}
	// The selection covers keys, a saved password and a named SSH jump chain.
	command := `printf 'out\n'; printf 'err\n' >&2; if [ "$(id -un)" = ops ]; then exit 7; fi`
	result := l.run("exec", "--tag", "BATCH-prod", "--jobs", "2", "--", command)
	if result.code != 1 || !strings.Contains(result.stderr, "2 succeeded, 1 failed") {
		t.Fatal(result)
	}
	for _, name := range []string{"lab", "ops", "feature-private"} {
		if !strings.Contains(result.stdout, "["+name+"] out\n") || !strings.Contains(result.stderr, "["+name+"] err\n") {
			t.Fatal(result)
		}
	}
	if !strings.Contains(result.stderr, "7") {
		t.Fatal("missing remote exit code", result)
	}
	result = l.run("exec", "--tag", "batch-prod", "--jobs", "1", "--", "id -un")
	if result.code != 0 || !strings.Contains(result.stdout, "[ops] ops\n") || !strings.Contains(result.stderr, "3 succeeded, 0 failed") {
		t.Fatal(result)
	}
}
