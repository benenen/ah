//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"
)

func testConfigMigration(t *testing.T, l *lab) {
	plain, encrypted, passfile := l.path("export.toml"), l.path("export.ah"), l.path("export-passphrase")
	writeFile(t, passfile, "fixture-portable-passphrase\n", 0600)
	originalConfig, originalKey := readFile(t, l.path("connections.toml")), readFile(t, l.path("master.key"))
	l.ok("config", "export", plain)
	if data := readFile(t, plain); strings.Contains(data, "enc:v1:") || strings.Contains(data, opsPassword) {
		t.Fatal("plain export retained saved credentials")
	}
	l.ok("config", "export", encrypted, "--with-secrets", "--passphrase-file", passfile)
	imported, importedKey := l.path("imported.toml"), l.path("imported.key")
	flags := []string{"--config", imported, "--key-file", importedKey, "config", "import", encrypted, "--passphrase-file", passfile}
	if out := l.ok(append(append([]string{}, flags...), "--dry-run")...); !strings.Contains(out, "add\tops") {
		t.Fatal(out)
	}
	for _, filename := range []string{imported, importedKey} {
		if _, err := os.Stat(filename); !os.IsNotExist(err) {
			t.Fatal("preview wrote state")
		}
	}
	l.ok(flags...)
	if got := strings.TrimSpace(l.ok("--config", imported, "--key-file", importedKey, "c", "ops", "whoami")); got != "ops" {
		t.Fatal("migrated password did not authenticate", got)
	}
	if readFile(t, importedKey) == originalKey {
		t.Fatal("migration copied source master key")
	}
	l.fails("already exists", flags...)
	if readFile(t, l.path("connections.toml")) != originalConfig || readFile(t, l.path("master.key")) != originalKey {
		t.Fatal("migration modified source state")
	}
}
