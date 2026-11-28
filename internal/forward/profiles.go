package forward

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/benenen/ah/internal/config"
	"github.com/gofrs/flock"
	"github.com/pelletier/go-toml/v2"
)

// Profile defines a reusable port-forward or SOCKS5 configuration.
type Profile struct {
	Connection      string `toml:"connection"`
	Type            string `toml:"type,omitempty"`
	Listen          string `toml:"listen"`
	Target          string `toml:"target,omitempty"`
	Group           string `toml:"group,omitempty"`
	KeepAlive       string `toml:"keepalive,omitempty"`
	Reconnect       bool   `toml:"reconnect,omitempty"`
	ContinueOnError *bool  `toml:"continue_on_error,omitempty"`
}

func (p Profile) Kind() string {
	if p.Type == "" {
		return Local
	}
	return p.Type
}

func (p Profile) Validate() error {
	if err := config.ValidateName(p.Connection); err != nil {
		return fmt.Errorf("invalid connection: %w", err)
	}
	switch p.Kind() {
	case Local, Remote:
		if p.Listen == "" || p.Target == "" {
			return fmt.Errorf("%s forward profile requires listen and target", p.Kind())
		}
	case Dynamic:
		if p.Listen == "" {
			return fmt.Errorf("dynamic forward profile requires listen")
		}
		if p.Target != "" {
			return fmt.Errorf("dynamic forward profile must not set target")
		}
	default:
		return fmt.Errorf("invalid forward type %q (expected local, remote or dynamic)", p.Type)
	}
	if p.Group != "" {
		if err := config.ValidateTag(p.Group); err != nil {
			return fmt.Errorf("invalid group: %w", err)
		}
	}
	if p.KeepAlive != "" {
		if strings.ContainsFunc(p.KeepAlive, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return fmt.Errorf("keepalive must not contain whitespace or control characters")
		}
		d, err := time.ParseDuration(p.KeepAlive)
		if err != nil {
			return fmt.Errorf("invalid keepalive %q: %w", p.KeepAlive, err)
		}
		if d < 0 {
			return fmt.Errorf("keepalive must not be negative")
		}
	}
	return nil
}

type profileDocument struct {
	Profiles map[string]Profile `toml:"profiles"`
}

// ProfilesPath returns the TOML file storing named forward profiles.
func ProfilesPath() (string, error) {
	dir, err := directory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "profiles.toml"), nil
}

// LoadProfiles reads all saved forward profiles.
func LoadProfiles() (map[string]Profile, error) {
	p, err := ProfilesPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]Profile), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read forward profiles: %w", err)
	}
	d := profileDocument{Profiles: make(map[string]Profile)}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&d); err != nil {
		return nil, fmt.Errorf("decode forward profiles: %w", err)
	}
	for alias, prof := range d.Profiles {
		if err := config.ValidateName(alias); err != nil {
			return nil, fmt.Errorf("profile %q: %w", alias, err)
		}
		if err := prof.Validate(); err != nil {
			return nil, fmt.Errorf("profile %q: %w", alias, err)
		}
	}
	return d.Profiles, nil
}

// UpdateProfiles atomically updates the forward profiles file under a file lock.
func UpdateProfiles(ctx context.Context, fn func(map[string]Profile) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("profile update callback is nil")
	}
	p, err := ProfilesPath()
	if err != nil {
		return err
	}
	parent := filepath.Dir(p)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return fmt.Errorf("create forward directory: %w", err)
	}
	lock := flock.New(p+".lock", flock.SetPermissions(0600))
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock forward profiles: %w", err)
	}
	if !locked {
		return fmt.Errorf("lock forward profiles: %w", ctx.Err())
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("release forward profiles lock: %w", closeErr))
		}
	}()
	profiles, err := LoadProfiles()
	if err != nil {
		return err
	}
	if err := fn(profiles); err != nil {
		return err
	}
	for alias, prof := range profiles {
		if err := config.ValidateName(alias); err != nil {
			return fmt.Errorf("profile %q: %w", alias, err)
		}
		if err := prof.Validate(); err != nil {
			return fmt.Errorf("profile %q: %w", alias, err)
		}
	}
	data, err := toml.Marshal(profileDocument{Profiles: profiles})
	if err != nil {
		return fmt.Errorf("encode forward profiles: %w", err)
	}
	tmp, err := os.CreateTemp(parent, ".ah-profiles-*")
	if err != nil {
		return fmt.Errorf("create temporary profiles: %w", err)
	}
	tempPath := tmp.Name()
	defer func() {
		if removeErr := os.Remove(tempPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary profiles: %w", removeErr))
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write profiles: %w", err), tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync profiles: %w", err), tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close profiles: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tempPath, p)
}
