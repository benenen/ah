// Package config persists named SSH connections without authentication secrets.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/benenen/ah/internal/credentials"
	"github.com/gofrs/flock"
	"github.com/pelletier/go-toml/v2"
)

type Connection struct {
	Host         string `toml:"host"`
	Port         int    `toml:"port"`
	User         string `toml:"user"`
	IdentityFile string `toml:"identity_file,omitempty"`
	Password     string `toml:"password,omitempty"`
	// Sudo escalates remote commands and interactive shells to root via sudo.
	Sudo bool `toml:"sudo,omitempty"`
	// SudoPassword is an encrypted sudo password; when empty, Sudo reuses Password.
	SudoPassword string `toml:"sudo_password,omitempty"`
	// Proxy tunnels the SSH connection through a SOCKS5 proxy, e.g.
	// "socks5://127.0.0.1:1080" or "socks5://user:pass@host:1080".
	Proxy string `toml:"proxy,omitempty"`
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return errors.New("connection name must begin with an ASCII letter or digit and contain only letters, digits, underscores or hyphens")
	}
	return nil
}

func (c Connection) Validate() error {
	if c.Password != "" {
		if err := credentials.Validate(c.Password); err != nil {
			return fmt.Errorf("invalid encrypted password: %w", err)
		}
	}
	if c.SudoPassword != "" {
		if err := credentials.Validate(c.SudoPassword); err != nil {
			return fmt.Errorf("invalid encrypted sudo password: %w", err)
		}
	}
	invalidText := func(s string) bool {
		return strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	}
	if c.Host == "" || invalidText(c.Host) || strings.ContainsAny(c.Host, "/@\\") {
		return errors.New("host must be a nonempty hostname or IP address without whitespace")
	}
	if c.User == "" || invalidText(c.User) {
		return errors.New("user must be nonempty and contain no whitespace or control characters")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if strings.ContainsFunc(c.IdentityFile, unicode.IsControl) {
		return errors.New("identity_file must contain no control characters")
	}
	if err := validateProxy(c.Proxy); err != nil {
		return err
	}
	return nil
}

// validateProxy accepts an empty proxy or a socks5 URL with an explicit
// host:port; other schemes are rejected so misconfiguration fails loudly.
func validateProxy(proxy string) error {
	if proxy == "" {
		return nil
	}
	if strings.ContainsFunc(proxy, unicode.IsControl) {
		return errors.New("proxy must contain no control characters")
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return fmt.Errorf("invalid proxy URL: %w", err)
	}
	if u.Scheme != "socks5" && u.Scheme != "socks5h" {
		return fmt.Errorf("proxy scheme %q not supported (use socks5://host:port)", u.Scheme)
	}
	if u.Hostname() == "" || u.Port() == "" {
		return errors.New("proxy must include host and port, e.g. socks5://127.0.0.1:1080")
	}
	return nil
}

type Store struct{ Path string }

type document struct {
	Connections map[string]Connection `toml:"connections"`
}

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(dir, "ah", "connections.toml"), nil
}

func normalize(connections map[string]Connection) error {
	for name, c := range connections {
		if err := ValidateName(name); err != nil {
			return err
		}
		if c.Port == 0 {
			c.Port = 22
		}
		if err := c.Validate(); err != nil {
			return fmt.Errorf("connection %s: %w", name, err)
		}
		connections[name] = c
	}
	return nil
}

func (s Store) Load() (map[string]Connection, error) {
	if s.Path == "" {
		return nil, errors.New("config path is empty")
	}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]Connection), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	d := document{Connections: make(map[string]Connection)}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&d); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if err := normalize(d.Connections); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return d.Connections, nil
}

// Update serializes cooperating writers and commits only a valid callback result.
// The callback runs while holding the file lock and must not call Update itself.
func (s Store) Update(ctx context.Context, fn func(map[string]Connection) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Path == "" {
		return errors.New("config path is empty")
	}
	if fn == nil {
		return errors.New("config update callback is nil")
	}
	parent := filepath.Dir(s.Path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	lock := flock.New(s.Path+".lock", flock.SetPermissions(0600))
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock config: %w", err)
	}
	if !locked {
		return fmt.Errorf("lock config: %w", ctx.Err())
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("release config lock: %w", closeErr))
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	connections, err := s.Load()
	if err != nil {
		return err
	}
	if err := fn(connections); err != nil {
		return err
	}
	if err := normalize(connections); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	data, err := toml.Marshal(document{Connections: connections})
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	temp, err := os.CreateTemp(parent, ".ah-config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if removeErr := os.Remove(tempPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary config: %w", removeErr))
		}
	}()
	if _, err := temp.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write config: %w", err), temp.Close())
	}
	if err := temp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync config: %w", err), temp.Close())
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, s.Path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
