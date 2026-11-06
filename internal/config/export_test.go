package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/benenen/ah/internal/credentials"
)

func exportFixture(t *testing.T) (map[string]Connection, credentials.Store) {
	t.Helper()
	key := credentials.Store{Path: filepath.Join(t.TempDir(), "source.key")}
	ssh, err := key.Encrypt("server", []byte("fixture-ssh-secret"))
	if err != nil {
		t.Fatal(err)
	}
	sudo, err := key.Encrypt("server/sudo", []byte("fixture-sudo-secret"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Connection{"server": {Host: "example.com", Port: 2222, User: "alice", Password: ssh, Sudo: true, SudoPassword: sudo, IdentityFile: "~/.ssh/custom", Proxies: []string{"socks5h://proxy-user:proxy-secret@proxy.example.com:1080", "[::1]:1080"}, Tags: []string{"prod"}, Note: "测试备注"}}, key
}

func TestPlainConfigExportRemovesCredentials(t *testing.T) {
	entries, key := exportFixture(t)
	original := append([]string(nil), entries["server"].Proxies...)
	if err := os.Remove(key.Path); err != nil {
		t.Fatal(err)
	} // Plain export never needs the source master key.
	data, err := Export(entries, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"enc:v1:", "fixture-ssh-secret", "fixture-sudo-secret", "proxy-user", "proxy-secret"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("credential escaped into plaintext export")
		}
	}
	b, err := DecodeExport(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Clear()
	c := b.Connections["server"]
	if c.Password != "" || c.SudoPassword != "" || c.Proxies[0] != "socks5h://proxy.example.com:1080" || c.Proxies[1] != "[::1]:1080" || c.Note != "测试备注" || c.Tags[0] != "prod" || c.IdentityFile != "~/.ssh/custom" {
		t.Fatal("configuration did not survive export")
	}
	if !reflect.DeepEqual(entries["server"].Proxies, original) || entries["server"].Password == "" {
		t.Fatal("export changed source entries")
	}
}

func TestEncryptedConfigExportMigratesToDifferentMasterKey(t *testing.T) {
	entries, source := exportFixture(t)
	// Legacy sudo envelopes used only the connection alias as additional data.
	legacy, err := source.Encrypt("server", []byte("legacy-sudo-secret"))
	if err != nil {
		t.Fatal(err)
	}
	entries["legacy"] = Connection{Host: "legacy.example.com", Port: 22, User: "bob"}
	c := entries["server"]
	c.SudoPassword = legacy
	entries["server"] = c
	passphrase := []byte("fixture-portable-passphrase")
	data, err := Export(entries, source, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if !EncryptedExport(data) {
		t.Fatal("missing archive header")
	}
	keyData, err := os.ReadFile(source.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte("example.com"), []byte("fixture-ssh-secret"), []byte("legacy-sudo-secret"), []byte("proxy-secret"), keyData} {
		if bytes.Contains(data, secret) {
			t.Fatal("unencrypted archive content")
		}
	}
	b, err := DecodeExport(data, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Clear()
	target := credentials.Store{Path: filepath.Join(t.TempDir(), "destination.key")}
	if _, err := target.Encrypt("existing", []byte("existing-secret")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := b.EncryptPasswords(target)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(target.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("destination master key replaced", err)
	}
	for _, tc := range []struct{ name, value, want string }{{"server", imported["server"].Password, "fixture-ssh-secret"}, {"server/sudo", imported["server"].SudoPassword, "legacy-sudo-secret"}} {
		plain, err := target.Decrypt(tc.name, tc.value)
		if err != nil || string(plain) != tc.want {
			t.Fatal("migrated password did not decrypt", err)
		}
		clear(plain)
		if _, err := source.Decrypt(tc.name, tc.value); err == nil {
			t.Fatal("destination reused source master key")
		}
	}
	if !reflect.DeepEqual(imported["server"].Proxies, entries["server"].Proxies) {
		t.Fatal("proxy credentials lost in encrypted migration")
	}
	encoded, err := Encode(imported)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("fixture-ssh-secret")) || bytes.Contains(encoded, []byte("legacy-sudo-secret")) {
		t.Fatal("plaintext password saved to TOML")
	}
	if _, err := DecodeExport(encoded, nil); err == nil {
		t.Fatal("accepted local saved passwords in a portable plain import")
	}
	b.Clear()
	if len(b.passwords) != 0 {
		t.Fatal("password buffers retained")
	}
}

func TestConfigExportRejectsWrongPasswordAndCorruption(t *testing.T) {
	entries, key := exportFixture(t)
	passphrase := []byte("fixture-archive-passphrase")
	data, err := Export(entries, key, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, []byte("wrong-passphrase")} {
		if _, err := DecodeExport(data, bad); err == nil {
			t.Fatal("accepted wrong passphrase")
		}
	}
	damaged := append([]byte(nil), data...)
	damaged[len(damaged)-1] ^= 1
	if _, err := DecodeExport(damaged, passphrase); err == nil {
		t.Fatal("accepted altered ciphertext")
	}
	for _, bad := range [][]byte{data[:len(exportMagic)+1], []byte("AH-CONFIG-ENC-V2\n"), []byte(" "), []byte("[connections.bad]\nhost='x'\nuser='y'\nunknown=true")} {
		if _, err := DecodeExport(bad, passphrase); err == nil {
			t.Fatal("accepted invalid export")
		}
	}
	if err := os.Remove(key.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(entries, key, passphrase); err == nil {
		t.Fatal("encrypted export accepted missing source key")
	}
}

func TestEmptyConfigExportRoundTrip(t *testing.T) {
	data, err := Export(map[string]Connection{}, credentials.Store{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := DecodeExport(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Clear()
	if len(bundle.Connections) != 0 {
		t.Fatal("nonempty import")
	}
}
