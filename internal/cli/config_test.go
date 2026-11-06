package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/credentials"
)

func seedPortableConfig(t *testing.T, dir string) (config.Store, credentials.Store) {
	t.Helper()
	store := config.Store{Path: filepath.Join(dir, "connections.toml")}
	key := credentials.Store{Path: filepath.Join(dir, "master.key")}
	password, err := key.Encrypt("private", []byte("portable-ssh-secret"))
	if err != nil {
		t.Fatal(err)
	}
	sudo, err := key.Encrypt("private/sudo", []byte("portable-sudo-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(entries map[string]config.Connection) error {
		entries["jump"] = config.Connection{Host: "bastion.example.com", Port: 22, User: "alice"}
		entries["private"] = config.Connection{Host: "10.0.0.8", Port: 22, User: "bob", Jump: "jump", Password: password, SudoPassword: sudo, Tags: []string{"prod"}, Note: "迁移"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return store, key
}

func TestConfigCommandsPlainMergeAndConflicts(t *testing.T) {
	dir := t.TempDir()
	source, key := seedPortableConfig(t, dir)
	archive := filepath.Join(dir, "export.toml")
	run := func(args ...string) (string, error) {
		return execute(t, append([]string{"--config", source.Path, "--key-file", key.Path}, args...)...)
	}
	if out, err := run("config", "export", archive); err != nil {
		t.Fatal(out, err)
	}
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("export is not private")
	}
	if _, err := run("config", "export", archive); err == nil {
		t.Fatal("export overwrote without force")
	}
	if _, err := run("config", "export", "--force", archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "destination", "connections.toml")
	targetKey := filepath.Join(dir, "destination", "master.key")
	args := []string{"--config", target, "--key-file", targetKey, "config", "import", archive}
	out, err := execute(t, append(args, "--dry-run")...)
	if err != nil || !strings.Contains(out, "add\tprivate") {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Dir(target)); !os.IsNotExist(err) {
		t.Fatal("preview created configuration directory", err)
	}
	if out, err := execute(t, args...); err != nil {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(targetKey); !os.IsNotExist(err) {
		t.Fatal("plain import created a master key")
	}
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, args...); err == nil {
		t.Fatal("accepted duplicate names")
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("conflict changed config", err)
	}
	if err := os.WriteFile(target, []byte("invalid TOML ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, append(args, "--force")...); err == nil {
		t.Fatal("overwrote damaged destination")
	}
	after, err = os.ReadFile(target)
	if err != nil || string(after) != "invalid TOML [" {
		t.Fatal("damaged config changed", err)
	}
}

func TestConfigCommandsEncryptedMigration(t *testing.T) {
	dir := t.TempDir()
	source, key := seedPortableConfig(t, dir)
	passfile := filepath.Join(dir, "passphrase")
	if err := os.WriteFile(passfile, []byte("portable-archive-passphrase\n"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "backup.ah")
	if out, err := execute(t, "--config", source.Path, "--key-file", key.Path, "config", "export", archive, "--with-secrets", "--passphrase-file", passfile); err != nil {
		t.Fatal(out, err)
	}
	target, targetKey := filepath.Join(dir, "target.toml"), filepath.Join(dir, "target.key")
	args := []string{"--config", target, "--key-file", targetKey, "config", "import", archive, "--passphrase-file", passfile}
	if out, err := execute(t, append(args, "--dry-run")...); err != nil {
		t.Fatal(out, err)
	}
	for _, p := range []string{target, targetKey} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("encrypted preview wrote a file")
		}
	}
	if err := os.WriteFile(passfile, []byte("wrong-passphrase"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, args...)
	if err == nil || strings.Contains(out, "portable-ssh-secret") {
		t.Fatal("wrong passphrase accepted or leaked secret")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("wrong passphrase wrote config")
	}
	if err := os.WriteFile(passfile, []byte("portable-archive-passphrase\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := execute(t, args...); err != nil {
		t.Fatal(out, err)
	}
	entries, err := (config.Store{Path: target}).Load()
	if err != nil {
		t.Fatal(err)
	}
	localKey := credentials.Store{Path: targetKey}
	for _, tc := range []struct{ name, value, want string }{{"private", entries["private"].Password, "portable-ssh-secret"}, {"private/sudo", entries["private"].SudoPassword, "portable-sudo-secret"}} {
		plain, err := localKey.Decrypt(tc.name, tc.value)
		if err != nil || string(plain) != tc.want {
			t.Fatal("migrated password", err)
		}
		clear(plain)
	}
	// Explicit replacement using a credential-free export clears the imported passwords.
	plainArchive := filepath.Join(dir, "plain.toml")
	if _, err := execute(t, "--config", source.Path, "config", "export", plainArchive); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "--config", target, "config", "import", plainArchive, "--force"); err != nil {
		t.Fatal(err)
	}
	entries, err = (config.Store{Path: target}).Load()
	if err != nil || entries["private"].Password != "" || entries["private"].SudoPassword != "" {
		t.Fatal("replacement did not clear credentials", err)
	}
}

func TestConfigExportProtectsActiveFiles(t *testing.T) {
	dir := t.TempDir()
	store, key := seedPortableConfig(t, dir)
	db, known, passfile := filepath.Join(dir, "history.db"), filepath.Join(dir, "known_hosts"), filepath.Join(dir, "passphrase")
	for _, p := range []string{db, known, passfile} {
		if err := os.WriteFile(p, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dest := range []string{store.Path, store.Path + ".lock", key.Path, db, db + "-wal", known, passfile} {
		_, err := execute(t, "--config", store.Path, "--key-file", key.Path, "--history-file", db, "--known-hosts", known, "config", "export", dest, "--force", "--with-secrets", "--passphrase-file", passfile)
		if err == nil {
			t.Fatal("accepted protected export destination", dest)
		}
	}
	alias := filepath.Join(dir, "hardlink")
	if err := os.Link(key.Path, alias); err == nil {
		if _, err := execute(t, "--config", store.Path, "--key-file", key.Path, "config", "export", alias, "--force"); err == nil {
			t.Fatal("overwrote hardlink to master key")
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(passfile, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := execute(t, "--config", store.Path, "--key-file", key.Path, "config", "export", filepath.Join(dir, "backup"), "--with-secrets", "--passphrase-file", passfile); err == nil {
			t.Fatal("accepted public passphrase file")
		}
	}
}

func TestConfigImportRejectsInvalidJumpRoutes(t *testing.T) {
	for _, incoming := range []map[string]config.Connection{
		{"a": {Host: "example.com", Port: 22, User: "alice", Jump: "missing"}},
		{"a": {Host: "example.com", Port: 22, User: "alice", Jump: "b"}, "b": {Host: "example.com", Port: 22, User: "alice", Jump: "a"}},
	} {
		dir := t.TempDir()
		archive := filepath.Join(dir, "import.toml")
		target := filepath.Join(dir, "target.toml")
		data, err := config.Encode(incoming)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archive, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := execute(t, "--config", target, "config", "import", archive); err == nil {
			t.Fatal("accepted invalid jump route")
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("invalid jump wrote config")
		}
	}
}
