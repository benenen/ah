package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPasswordStoreRejectsDirectoryAliasCollision(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	for _, tail := range []string{"connections.toml", filepath.Join("not-created", "nested", "connections.toml")} {
		t.Run(tail, func(t *testing.T) {
			a := app{configPath: filepath.Join(real, tail), keyPath: filepath.Join(alias, tail)}
			if _, err := a.passwordStore(); err == nil {
				t.Fatal("accepted configuration and key pointing to the same nonexistent file")
			}
			if _, err := os.Stat(filepath.Join(real, tail)); !os.IsNotExist(err) {
				t.Fatal("validation created a file")
			}
		})
	}
}

func TestPasswordStoreRejectsHardlinkCollision(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "connections.toml")
	keyPath := filepath.Join(dir, "master.key")
	original := []byte("synthetic config")
	if err := os.WriteFile(configPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(configPath, keyPath); err != nil {
		t.Fatal(err)
	}
	a := app{configPath: configPath, keyPath: keyPath}
	if _, err := a.passwordStore(); err == nil {
		t.Fatal("accepted configuration and key sharing the same inode")
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("validation changed config contents")
	}
}

func TestPasswordStoreAllowsDistinctPathsWithDirectoryAlias(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	a := app{configPath: filepath.Join(real, "connections.toml"), keyPath: filepath.Join(alias, "nested", "master.key")}
	store, err := a.passwordStore()
	if err != nil {
		t.Fatal(err)
	}
	if store.Path != a.keyPath {
		t.Fatal("key path changed")
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("validation created filesystem entries")
	}
}
