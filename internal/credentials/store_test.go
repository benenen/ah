package credentials

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestRoundTripAndRandomNonce(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "private", "master.key")}
	secret := []byte("synthetic password 中文")
	a, err := s.Encrypt("alpha", secret)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Encrypt("alpha", secret)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("encryption reused nonce")
	}
	for _, v := range []string{a, b} {
		if err := Validate(v); err != nil {
			t.Fatal(err)
		}
		got, err := s.Decrypt("alpha", v)
		if err != nil || !bytes.Equal(got, secret) {
			t.Fatal("roundtrip failed", err)
		}
	}
	if _, err := s.Decrypt("beta", a); err == nil {
		t.Fatal("accepted another alias")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(a, "enc:v1:"))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	if _, err := s.Decrypt("alpha", "enc:v1:"+base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("accepted modified ciphertext")
	}
	for _, p := range []string{s.Path, filepath.Dir(s.Path)} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			t.Fatal("permissions expose key")
		}
	}
}

func TestValidationAndPasswordBounds(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "master.key")}
	for _, v := range []string{"", "plaintext", "enc:v2:AAAA", "enc:v1:!!!!", "enc:v1:" + base64.StdEncoding.EncodeToString(make([]byte, 28)), "enc:v1:" + base64.StdEncoding.EncodeToString(make([]byte, 4125))} {
		if Validate(v) == nil {
			t.Fatal("accepted invalid envelope")
		}
	}
	for _, v := range [][]byte{nil, make([]byte, 4097)} {
		if _, err := s.Encrypt("a", v); err == nil {
			t.Fatal("accepted invalid password size")
		}
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatal("invalid input created key")
	}
	v, err := s.Encrypt("a", bytes.Repeat([]byte("x"), 4096))
	if err != nil {
		t.Fatal(err)
	}
	if Validate(v+"\n") == nil {
		t.Fatal("accepted noncanonical base64")
	}
	if _, err := s.Decrypt("a", v); err != nil {
		t.Fatal(err)
	}
}

func TestMissingAndWrongKey(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "master.key")}
	v, err := s.Encrypt("a", []byte("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	missing := Store{Path: filepath.Join(t.TempDir(), "missing", "master.key")}
	if _, err := missing.Decrypt("a", v); err == nil {
		t.Fatal("accepted missing key")
	}
	if _, err := os.Stat(filepath.Dir(missing.Path)); !os.IsNotExist(err) {
		t.Fatal("decrypt created key directory")
	}
	wrong := Store{Path: filepath.Join(t.TempDir(), "master.key")}
	if _, err := wrong.Encrypt("a", []byte("other")); err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Decrypt("a", v); err == nil {
		t.Fatal("accepted wrong key")
	}
}

func TestRejectUnsafeOrInvalidKey(t *testing.T) {
	for _, kind := range []string{"short", "long", "directory", "symlink", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "permissions" && runtime.GOOS == "windows" {
				t.Skip("Unix mode check")
			}
			dir := t.TempDir()
			s := Store{Path: filepath.Join(dir, "key")}
			switch kind {
			case "directory":
				if err := os.Mkdir(s.Path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(dir, "target")
				if err := os.WriteFile(target, make([]byte, 32), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, s.Path); err != nil {
					t.Fatal(err)
				}
			default:
				n := 32
				if kind == "short" {
					n = 31
				}
				if kind == "long" {
					n = 33
				}
				if err := os.WriteFile(s.Path, make([]byte, n), 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "permissions" {
					if err := os.Chmod(s.Path, 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.Encrypt("a", []byte("synthetic")); err == nil {
				t.Fatal("accepted unsafe key")
			}
		})
	}
}

func TestConcurrentKeyCreation(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "private", "master.key")}
	const n = 24
	values := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() { <-start; values[i], errs[i] = s.Encrypt("a", []byte("synthetic")) })
	}
	close(start)
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		got, err := s.Decrypt("a", values[i])
		if err != nil || string(got) != "synthetic" {
			t.Fatal("concurrent value lost its key", err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(s.Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "master.key" {
		t.Fatal("temporary key leaked")
	}
}

func TestDefaultPath(t *testing.T) {
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Skip(err)
	}
	got, err := DefaultPath()
	if err != nil || got != filepath.Join(dir, "ah", "master.key") {
		t.Fatal("incorrect default key location", err)
	}
}
