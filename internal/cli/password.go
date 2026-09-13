package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/credentials"
	"github.com/benenen/ah/internal/sshclient"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (a *app) passwordStore() (credentials.Store, error) {
	p := a.keyPath
	if p == "" {
		var err error
		p, err = credentials.DefaultPath()
		if err != nil {
			return credentials.Store{}, err
		}
	}
	configStore, err := a.store()
	if err != nil {
		return credentials.Store{}, err
	}
	keyPath, err := canonicalPasswordPath(p)
	if err != nil {
		return credentials.Store{}, err
	}
	configPath, err := canonicalPasswordPath(configStore.Path)
	if err != nil {
		return credentials.Store{}, err
	}
	if keyPath == configPath {
		return credentials.Store{}, fmt.Errorf("--key-file must differ from --config")
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil && !os.IsNotExist(err) {
		return credentials.Store{}, err
	}
	configInfo, err := os.Stat(configPath)
	if err != nil && !os.IsNotExist(err) {
		return credentials.Store{}, err
	}
	if keyInfo != nil && configInfo != nil && os.SameFile(keyInfo, configInfo) {
		return credentials.Store{}, fmt.Errorf("--key-file must differ from --config")
	}
	return credentials.Store{Path: p}, nil
}

// canonicalPasswordPath resolves existing ancestors without creating the file.
// A new key and a new config may share a destination through directory symlinks.
func canonicalPasswordPath(path string) (string, error) {
	current, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		// Do not treat a dangling symlink as a nonexistent path component.
		if _, statErr := os.Lstat(current); !os.IsNotExist(statErr) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		tail = append(tail, filepath.Base(current))
		current = parent
	}
}

func (a *app) promptPassword(cmd *cobra.Command, name string) (string, error) {
	input, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return "", fmt.Errorf("--password requires an interactive terminal")
	}
	store, err := a.passwordStore()
	if err != nil {
		return "", err
	}
	secret, err := readSecret(cmd.Context(), input, cmd.ErrOrStderr(), fmt.Sprintf("SSH password: "))
	if err != nil {
		return "", err
	}
	defer clear(secret)
	if len(secret) == 0 {
		return "", fmt.Errorf("password must not be empty")
	}
	return store.Encrypt(name, secret)
}

func (a *app) connectionSSHOptions(cmd *cobra.Command, name string, c config.Connection, interactive bool) sshclient.Options {
	opts := sshclient.Options{KnownHosts: a.knownHosts, Timeout: a.timeout}
	if interactive {
		opts = a.sshOptions(cmd)
	}
	if c.Password != "" {
		opts.PasswordOnly = true
		opts.Passphrase = nil
		// Decrypt only when a verified server requests authentication. This callback
		// never prompts, so stored passwords also work for noninteractive completion.
		opts.Password = func() (string, error) {
			store, err := a.passwordStore()
			if err != nil {
				return "", err
			}
			secret, err := store.Decrypt(name, c.Password)
			if err != nil {
				return "", fmt.Errorf("decrypt password for %s: %w", name, err)
			}
			defer clear(secret)
			return string(secret), nil
		}
	}
	return opts
}
