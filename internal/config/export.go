package config

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/benenen/ah/internal/credentials"
	"golang.org/x/crypto/scrypt"
)

const exportMagic = "AH-CONFIG-ENC-V1\n"

// MaxExportSize bounds imported files and decrypted payloads.
const MaxExportSize = 16 << 20

type exportedPassword struct {
	SSH  []byte `json:"ssh,omitempty"`
	Sudo []byte `json:"sudo,omitempty"`
}

type exportPayload struct {
	Config    []byte                      `json:"config"`
	Passwords map[string]exportedPassword `json:"passwords"`
}

// ImportBundle holds decoded connections and optional decrypted passwords.
// Clear must be called after use. Passwords are never saved as plaintext TOML.
type ImportBundle struct {
	Connections map[string]Connection
	passwords   map[string]exportedPassword
}

func (b *ImportBundle) Clear() {
	for name, p := range b.passwords {
		clear(p.SSH)
		clear(p.Sudo)
		delete(b.passwords, name)
	}
}

// EncryptPasswords binds imported secrets to the destination's local master key.
func (b *ImportBundle) EncryptPasswords(store credentials.Store) (map[string]Connection, error) {
	result := make(map[string]Connection, len(b.Connections))
	for name, c := range b.Connections {
		p := b.passwords[name]
		var err error
		if len(p.SSH) > 0 {
			c.Password, err = store.Encrypt(name, p.SSH)
			if err != nil {
				return nil, err
			}
		}
		if len(p.Sudo) > 0 {
			c.SudoPassword, err = store.Encrypt(name+"/sudo", p.Sudo)
			if err != nil {
				return nil, err
			}
		}
		result[name] = c
	}
	return result, nil
}

// EncryptedExport recognizes the versioned binary archive header.
func EncryptedExport(data []byte) bool { return bytes.HasPrefix(data, []byte(exportMagic)) }

// Export emits credential-free TOML when passphrase is empty. Otherwise it
// encrypts configuration and portable passwords without including the master key.
func Export(entries map[string]Connection, store credentials.Store, passphrase []byte) ([]byte, error) {
	portable := make(map[string]Connection, len(entries))
	bundle := ImportBundle{passwords: map[string]exportedPassword{}}
	defer bundle.Clear()
	for name, c := range entries {
		if err := ValidateName(name); err != nil {
			return nil, err
		}
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("connection %s: %w", name, err)
		}
		if len(passphrase) > 0 {
			p := exportedPassword{}
			var err error
			if c.Password != "" {
				p.SSH, err = store.Decrypt(name, c.Password)
				if err != nil {
					return nil, fmt.Errorf("decrypt SSH password for %s: %w", name, err)
				}
			}
			bundle.passwords[name] = p
			if c.SudoPassword != "" {
				p.Sudo, err = store.Decrypt(name+"/sudo", c.SudoPassword)
				if err != nil {
					p.Sudo, err = store.Decrypt(name, c.SudoPassword)
				}
				if err != nil {
					return nil, fmt.Errorf("decrypt sudo password for %s: %w", name, err)
				}
			}
			bundle.passwords[name] = p
		} else {
			var err error
			c.Proxy, err = withoutProxyCredentials(c.Proxy)
			if err != nil {
				return nil, err
			}
			c.Proxies = append([]string(nil), c.Proxies...)
			for i, v := range c.Proxies {
				c.Proxies[i], err = withoutProxyCredentials(v)
				if err != nil {
					return nil, err
				}
			}
		}
		c.Password, c.SudoPassword = "", ""
		portable[name] = c
	}
	data, err := Encode(portable)
	if err != nil {
		return nil, err
	}
	if len(passphrase) == 0 {
		if len(data) > MaxExportSize {
			return nil, fmt.Errorf("export exceeds size limit")
		}
		return data, nil
	}
	defer clear(data)
	plaintext, err := json.Marshal(exportPayload{Config: data, Passwords: bundle.passwords})
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	aead, err := exportCipher(passphrase, salt)
	if err != nil {
		return nil, err
	}
	header := append([]byte(exportMagic), salt...)
	if len(header)+aead.Overhead()+len(plaintext) > MaxExportSize {
		return nil, fmt.Errorf("export exceeds size limit")
	}
	return append(header, aead.Seal(nil, nil, plaintext, header)...), nil
}

// DecodeExport accepts credential-free TOML or an authenticated encrypted archive.
func DecodeExport(data, passphrase []byte) (_ *ImportBundle, err error) {
	if len(data) > MaxExportSize {
		return nil, fmt.Errorf("import exceeds size limit")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("import file is empty")
	}
	b := &ImportBundle{}
	defer func() {
		if err != nil {
			b.Clear()
		}
	}()
	configData := data
	if EncryptedExport(data) {
		if len(passphrase) == 0 {
			return nil, fmt.Errorf("encrypted export requires a passphrase")
		}
		offset := len(exportMagic) + 16
		if len(data) < offset+28 {
			return nil, fmt.Errorf("invalid encrypted export")
		}
		aead, err := exportCipher(passphrase, data[len(exportMagic):offset])
		if err != nil {
			return nil, err
		}
		plaintext, err := aead.Open(nil, nil, data[offset:], data[:offset])
		if err != nil {
			return nil, fmt.Errorf("cannot decrypt export: incorrect passphrase or damaged file")
		}
		defer clear(plaintext)
		var payload exportPayload
		decoder := json.NewDecoder(bytes.NewReader(plaintext))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&payload)
		b.passwords = payload.Passwords
		if decodeErr != nil {
			return nil, fmt.Errorf("invalid encrypted export payload")
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("invalid encrypted export payload")
		}
		configData = payload.Config
		defer clear(configData)
	} else if bytes.HasPrefix(data, []byte("AH-CONFIG-")) {
		return nil, fmt.Errorf("unsupported export version")
	}
	b.Connections, err = Decode(configData)
	if err != nil {
		return nil, fmt.Errorf("invalid import configuration: %w", err)
	}
	for _, c := range b.Connections {
		if c.Password != "" || c.SudoPassword != "" {
			return nil, fmt.Errorf("imported TOML must not contain saved passwords; use an encrypted export")
		}
	}
	for name, p := range b.passwords {
		if _, ok := b.Connections[name]; !ok {
			return nil, fmt.Errorf("export contains passwords for an unknown connection")
		}
		if len(p.SSH) > 4096 || len(p.Sudo) > 4096 {
			return nil, fmt.Errorf("invalid exported password length")
		}
	}
	return b, nil
}

func exportCipher(passphrase, salt []byte) (cipher.AEAD, error) {
	if len(passphrase) == 0 || len(passphrase) > 4096 {
		return nil, fmt.Errorf("export passphrase must contain between 1 and 4096 bytes")
	}
	key, err := scrypt.Key(passphrase, salt, 65536, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func withoutProxyCredentials(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.Contains(value, "://") {
		if _, err := ProxyAddress(value); err != nil {
			return "", err
		}
		return value, nil
	}
	if _, err := ProxyAddress(value); err != nil {
		return "", err
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid proxy URL")
	}
	if u.Scheme == "socks5" || u.Scheme == "socks5h" {
		u.User = nil
		return u.String(), nil
	}
	return value, nil
}
