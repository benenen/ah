package cli

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/benenen/ah/internal/config"
)

func TestShellQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ls -lah", "'ls -lah'"},
		{"cd /root && ls", "'cd /root && ls'"},
		{"echo 'hi'", `'echo '\''hi'\'''`},
		{"", "''"},
	} {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// sudoTestApp builds an app with an isolated key and config so passwords encrypt
// and decrypt through the real credentials store.
func sudoTestApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	return &app{
		configPath: filepath.Join(dir, "connections.toml"),
		keyPath:    filepath.Join(dir, "master.key"),
	}
}

func (a *app) encryptForTest(t *testing.T, name, secret string) string {
	t.Helper()
	store, err := a.passwordStore()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := store.Encrypt(name, []byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func TestSudoExecCommandReusesSSHPassword(t *testing.T) {
	a := sudoTestApp(t)
	c := config.Connection{Sudo: true, Password: a.encryptForTest(t, "h", "s3cr3t")}
	cmd, prefix, err := a.sudoExecCommand("h", c, "cd /root && ls")
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "sudo -S -p '' -- /bin/sh -c 'cd /root && ls'" {
		t.Fatalf("command = %q", cmd)
	}
	if string(prefix) != "s3cr3t\n" {
		t.Fatalf("prefix = %q", prefix)
	}
}

func TestSudoExecCommandPrefersSudoPassword(t *testing.T) {
	a := sudoTestApp(t)
	c := config.Connection{
		Sudo:         true,
		Password:     a.encryptForTest(t, "h", "ssh-pw"),
		SudoPassword: a.encryptForTest(t, "h", "sudo-pw"),
	}
	_, prefix, err := a.sudoExecCommand("h", c, "id")
	if err != nil {
		t.Fatal(err)
	}
	if string(prefix) != "sudo-pw\n" {
		t.Fatalf("prefix = %q, want the dedicated sudo password", prefix)
	}
}

func TestSudoExecCommandWithoutPasswordUsesNonInteractive(t *testing.T) {
	a := sudoTestApp(t)
	c := config.Connection{Sudo: true}
	cmd, prefix, err := a.sudoExecCommand("h", c, "id")
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "sudo -n -- /bin/sh -c 'id'" {
		t.Fatalf("command = %q", cmd)
	}
	if prefix != nil {
		t.Fatalf("prefix = %q, want nil without a stored password", prefix)
	}
}

func TestSudoShellCommand(t *testing.T) {
	a := sudoTestApp(t)
	withPw := config.Connection{Sudo: true, Password: a.encryptForTest(t, "h", "pw")}
	cmd, prefix, err := a.sudoShellCommand("h", withPw)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "sudo -p '' -i" || string(prefix) != "pw\n" {
		t.Fatalf("with password: cmd=%q prefix=%q", cmd, prefix)
	}
	cmd, prefix, err = a.sudoShellCommand("h", config.Connection{Sudo: true})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "sudo -p '' -i" || prefix != nil {
		t.Fatalf("without password: cmd=%q prefix=%v", cmd, prefix)
	}
}

func TestSudoExecCommandDecryptFailure(t *testing.T) {
	a := sudoTestApp(t)
	// Ciphertext bound to a different name must fail authentication for "h".
	c := config.Connection{Sudo: true, Password: a.encryptForTest(t, "other", "pw")}
	if _, _, err := a.sudoExecCommand("h", c, "id"); err == nil {
		t.Fatal("expected decrypt failure for mismatched connection name")
	}
}

func TestSudoPasswordBytesNotAliasedInPrefix(t *testing.T) {
	a := sudoTestApp(t)
	c := config.Connection{Sudo: true, Password: a.encryptForTest(t, "h", "abc")}
	_, prefix, err := a.sudoExecCommand("h", c, "id")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(prefix, []byte("\n")) {
		t.Fatalf("prefix must end with a newline: %q", prefix)
	}
}
