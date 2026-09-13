// Package credentials encrypts saved SSH passwords with a local master key.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	prefix      = "enc:v1:"
	maxPassword = 4096
	nonceSize   = 12
	tagSize     = 16
)

// Store keeps the encryption key separate from connection configuration.
// An empty Path uses DefaultPath.
type Store struct{ Path string }

// DefaultPath returns the per-user master key location.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate credential directory: %w", err)
	}
	return filepath.Join(dir, "ah", "master.key"), nil
}

// Validate checks the versioned envelope without loading a key or decrypting.
func Validate(ciphertext string) error { _, err := decode(ciphertext); return err }

func decode(value string) ([]byte, error) {
	if !strings.HasPrefix(value, prefix) {
		return nil, errors.New("saved password must use the enc:v1 encrypted format")
	}
	encoded := strings.TrimPrefix(value, prefix)
	if len(encoded) > base64.StdEncoding.EncodedLen(nonceSize+tagSize+maxPassword) {
		return nil, errors.New("invalid encrypted password length")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != encoded || len(raw) < nonceSize+tagSize+1 || len(raw) > nonceSize+tagSize+maxPassword {
		return nil, errors.New("invalid encrypted password envelope")
	}
	return raw, nil
}

// Encrypt creates a missing master key and binds the ciphertext to name.
// Passwords must contain between 1 and 4096 bytes.
func (s Store) Encrypt(name string, plaintext []byte) (string, error) {
	if len(plaintext) == 0 || len(plaintext) > maxPassword {
		return "", errors.New("password must contain between 1 and 4096 bytes")
	}
	aead, err := s.aead(true)
	if err != nil {
		return "", err
	}
	encrypted := aead.Seal(nil, nil, plaintext, []byte("ah/password/enc:v1\x00"+name))
	return prefix + base64.StdEncoding.EncodeToString(encrypted), nil
}

// Decrypt authenticates the saved password; it never creates a master key.
func (s Store) Decrypt(name, ciphertext string) ([]byte, error) {
	encrypted, err := decode(ciphertext)
	if err != nil {
		return nil, err
	}
	aead, err := s.aead(false)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nil, encrypted, []byte("ah/password/enc:v1\x00"+name))
	if err != nil {
		return nil, errors.New("cannot decrypt saved password: key, connection name, or ciphertext does not match")
	}
	return plaintext, nil
}

func (s Store) aead(create bool) (cipher.AEAD, error) {
	path := s.Path
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	key, err := readKey(path)
	if errors.Is(err, os.ErrNotExist) && create {
		key, err = createKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("load password master key: %w", err)
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize password cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("initialize password encryption: %w", err)
	}
	return aead, nil
}

func readKey(path string) (_ []byte, err error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("master key must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return nil, errors.New("master key changed while opening")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("master key permissions must exclude group and other access (use chmod 600)")
	}
	if info.Size() != 32 {
		return nil, errors.New("master key must contain exactly 32 bytes")
	}
	key, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil {
		clear(key)
		return nil, err
	}
	if len(key) != 32 {
		clear(key)
		return nil, errors.New("master key must contain exactly 32 bytes")
	}
	return key, nil
}

func createKey(path string) (_ []byte, err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create credential directory: %w", err)
	}
	key := make([]byte, 32)
	defer clear(key)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	file, err := os.CreateTemp(dir, ".master-key-*")
	if err != nil {
		return nil, err
	}
	temp := file.Name()
	defer func() {
		if removeErr := os.Remove(temp); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary master key: %w", removeErr))
		}
	}()
	// CreateTemp opens with 0600. Publish only after the complete key is synced
	// and closed; hard linking never overwrites a concurrently published key.
	_, writeErr := file.Write(key)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		return nil, errors.Join(writeErr, closeErr)
	}
	if err := os.Link(temp, path); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("publish master key: %w", err)
	}
	return readKey(path)
}
