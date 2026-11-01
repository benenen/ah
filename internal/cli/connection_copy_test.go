package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/credentials"
)

func TestCloneRenameCredentials(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			dir := t.TempDir()
			key := credentials.Store{Path: filepath.Join(dir, "key")}
			password, err := key.Encrypt("old", []byte("ssh-secret"))
			if err != nil {
				t.Fatal(err)
			}
			binding := "old/sudo"
			if legacy {
				binding = "old"
			}
			sudo, err := key.Encrypt(binding, []byte("sudo-secret"))
			if err != nil {
				t.Fatal(err)
			}
			store := config.Store{Path: filepath.Join(dir, "connections.toml")}
			err = store.Update(context.Background(), func(m map[string]config.Connection) error {
				m["old"] = config.Connection{Host: "example.com", Port: 22, User: "alice", Password: password, SudoPassword: sudo}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) error {
				_, err := execute(t, append([]string{"--config", store.Path, "--key-file", key.Path}, args...)...)
				return err
			}
			if err := run("clone", "old", "copy"); err != nil {
				t.Fatal(err)
			}
			if err := run("rename", "copy", "renamed"); err != nil {
				t.Fatal(err)
			}
			entries, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 {
				t.Fatal("wrong aliases")
			}
			if _, ok := entries["copy"]; ok {
				t.Fatal("old alias retained")
			}
			for _, alias := range []string{"old", "renamed"} {
				c := entries[alias]
				plain, err := key.Decrypt(alias, c.Password)
				if err != nil || string(plain) != "ssh-secret" {
					t.Fatal("SSH binding", err)
				}
			}
			plain, err := key.Decrypt("renamed/sudo", entries["renamed"].SudoPassword)
			if err != nil || string(plain) != "sudo-secret" {
				t.Fatal("sudo binding", err)
			}
			before, err := os.ReadFile(store.Path)
			if err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"rename", "old", "renamed"}, {"clone", "missing", "new"}, {"clone", "old", "bad name"}} {
				if run(args...) == nil {
					t.Fatal("invalid copy accepted")
				}
			}
			after, err := os.ReadFile(store.Path)
			if err != nil || string(before) != string(after) {
				t.Fatal("failed operation changed config", err)
			}
			if err := os.Remove(key.Path); err != nil {
				t.Fatal(err)
			}
			if run("rename", "old", "new") == nil {
				t.Fatal("missing key accepted")
			}
			after, err = os.ReadFile(store.Path)
			if err != nil || string(before) != string(after) {
				t.Fatal("missing key changed config", err)
			}
		})
	}
}
