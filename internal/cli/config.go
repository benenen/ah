package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/credentials"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (a *app) configCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Export and import connection configurations"}
	var withSecrets, overwrite bool
	var exportPassphraseFile string
	export := &cobra.Command{Use: "export FILE", Short: "Export credential-free TOML, or encrypt a portable backup with --with-secrets", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !withSecrets && exportPassphraseFile != "" {
			return fmt.Errorf("--passphrase-file requires --with-secrets")
		}
		if err := a.protectConfigExport(args[0], exportPassphraseFile); err != nil {
			return err
		}
		entries, err := a.connections()
		if err != nil {
			return err
		}
		var passphrase []byte
		if withSecrets {
			passphrase, err = exportPassphrase(cmd, exportPassphraseFile, true)
			if err != nil {
				return err
			}
			defer clear(passphrase)
		}
		var keys credentials.Store
		if withSecrets {
			keys, err = a.passwordStore()
			if err != nil {
				return err
			}
		}
		data, err := config.Export(entries, keys, passphrase)
		if err != nil {
			return err
		}
		defer clear(data)
		if err := writeConfigExport(cmd.Context(), args[0], data, overwrite); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Exported %d connections to %s\n", len(entries), args[0])
		return err
	}}
	export.Flags().BoolVar(&withSecrets, "with-secrets", false, "encrypt the export with a passphrase and include SSH, sudo and proxy credentials")
	export.Flags().BoolVarP(&overwrite, "force", "f", false, "replace an existing export file")
	export.Flags().StringVar(&exportPassphraseFile, "passphrase-file", "", "read the encryption passphrase from a private file instead of prompting")
	var force, dryRun bool
	var importPassphraseFile string
	imp := &cobra.Command{Use: "import FILE", Short: "Merge exported connections; reject duplicate names unless --force is set", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		data, err := readExportFile(args[0], config.MaxExportSize)
		if err != nil {
			return err
		}
		defer clear(data)
		var passphrase []byte
		if config.EncryptedExport(data) {
			passphrase, err = exportPassphrase(cmd, importPassphraseFile, false)
			if err != nil {
				return err
			}
			defer clear(passphrase)
		} else if importPassphraseFile != "" {
			return fmt.Errorf("--passphrase-file is only used for encrypted exports")
		}
		bundle, err := config.DecodeExport(data, passphrase)
		if err != nil {
			return err
		}
		defer bundle.Clear()
		store, err := a.store()
		if err != nil {
			return err
		}
		var plan []string
		apply := func(entries map[string]config.Connection) error {
			var err error
			plan, err = planConfigImport(entries, bundle.Connections, force)
			if err != nil {
				return err
			}
			if dryRun {
				return cmd.Context().Err()
			}
			// Key creation and password re-encryption follow all merge validation.
			keys, err := a.passwordStore()
			if err != nil {
				return err
			}
			imported, err := bundle.EncryptPasswords(keys)
			if err != nil {
				return err
			}
			for name, c := range imported {
				entries[name] = c
			}
			return cmd.Context().Err()
		}
		if dryRun {
			entries, err := store.Load()
			if err != nil {
				return err
			}
			if err := apply(entries); err != nil {
				return err
			}
		} else if err := store.Update(cmd.Context(), apply); err != nil {
			return err
		}
		for _, line := range plan {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
				return err
			}
		}
		if !dryRun {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Imported %d connections\n", len(bundle.Connections))
		}
		return err
	}}
	imp.Flags().BoolVarP(&force, "force", "f", false, "replace conflicting connection definitions, including their saved credentials")
	imp.Flags().BoolVar(&dryRun, "dry-run", false, "validate and preview additions and replacements without changing configuration or keys")
	imp.Flags().StringVar(&importPassphraseFile, "passphrase-file", "", "read the decryption passphrase from a private file instead of prompting")
	cmd.AddCommand(export, imp)
	return cmd
}

func planConfigImport(current, incoming map[string]config.Connection, force bool) ([]string, error) {
	merged := make(map[string]config.Connection, len(current)+len(incoming))
	for name, c := range current {
		merged[name] = c
	}
	var plan []string
	for _, name := range sortedNames(incoming) {
		action := "add"
		if _, exists := current[name]; exists {
			if !force {
				return nil, fmt.Errorf("connection %q already exists (use --force to replace)", name)
			}
			action = "replace"
		}
		plan = append(plan, action+"\t"+name)
		merged[name] = incoming[name]
	}
	for _, name := range sortedNames(merged) {
		seen := map[string]bool{}
		next := name
		for next != "" {
			if seen[next] {
				return nil, fmt.Errorf("SSH jump cycle at %q", next)
			}
			if len(seen) >= 32 {
				return nil, fmt.Errorf("SSH jump chain exceeds 32 connections")
			}
			seen[next] = true
			c, ok := merged[next]
			if !ok {
				return nil, fmt.Errorf("jump connection %q does not exist", next)
			}
			next = c.Jump
		}
	}
	return plan, nil
}

func exportPassphrase(cmd *cobra.Command, filename string, confirm bool) ([]byte, error) {
	if filename != "" {
		info, err := os.Lstat(filename)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return nil, fmt.Errorf("passphrase file must be a regular file accessible only by its owner")
		}
		secret, err := readExportFile(filename, 4098)
		if err != nil {
			return nil, err
		}
		secret = bytes.TrimSuffix(secret, []byte("\n"))
		secret = bytes.TrimSuffix(secret, []byte("\r"))
		if len(secret) == 0 || len(secret) > 4096 {
			clear(secret)
			return nil, fmt.Errorf("export passphrase must contain between 1 and 4096 bytes")
		}
		return secret, nil
	}
	input, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return nil, fmt.Errorf("export passphrase requires an interactive terminal or --passphrase-file")
	}
	secret, err := readSecret(cmd.Context(), input, cmd.ErrOrStderr(), "Export passphrase: ")
	if err != nil {
		return nil, err
	}
	if len(secret) == 0 {
		return nil, fmt.Errorf("export passphrase must not be empty")
	}
	if confirm {
		again, err := readSecret(cmd.Context(), input, cmd.ErrOrStderr(), "Confirm export passphrase: ")
		defer clear(again)
		if err != nil {
			clear(secret)
			return nil, err
		}
		if !bytes.Equal(secret, again) {
			clear(secret)
			return nil, fmt.Errorf("export passphrases do not match")
		}
	}
	return secret, nil
}

func readExportFile(filename string, limit int) (data []byte, err error) {
	before, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("import must be a regular file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			clear(data)
			data = nil
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("import must be a regular file")
	}
	data, err = io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return data, err
	}
	if len(data) > limit {
		return data, fmt.Errorf("file exceeds size limit")
	}
	return data, nil
}

func (a *app) protectConfigExport(filename, passphraseFile string) error {
	target, err := canonicalPasswordPath(filename)
	if err != nil {
		return err
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	key := a.keyPath
	if key == "" {
		key, err = credentials.DefaultPath()
		if err != nil {
			return err
		}
	}
	known := a.knownHosts
	if known == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		known = filepath.Join(home, ".ssh", "known_hosts")
	}
	paths := []string{store.Path, store.Path + ".lock", key, known}
	if passphraseFile != "" {
		paths = append(paths, passphraseFile)
	}
	targetInfo, _ := os.Stat(filename)
	for _, p := range paths {
		protected, err := canonicalPasswordPath(p)
		if err != nil {
			return err
		}
		info, _ := os.Stat(p)
		if target == protected || (targetInfo != nil && info != nil && os.SameFile(targetInfo, info)) {
			return fmt.Errorf("export destination must differ from active configuration, keys, known_hosts and passphrase files")
		}
	}
	return a.protectHistoryPath(filename)
}

func writeConfigExport(ctx context.Context, filename string, data []byte, force bool) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, statErr := os.Lstat(filename)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if statErr == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("export destination must be a regular file")
		}
		if !force {
			return fmt.Errorf("export destination exists (use --force to replace)")
		}
	}
	file, err := os.CreateTemp(filepath.Dir(filename), ".ah-export-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() {
		if removeErr := os.Remove(name); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if force {
		return os.Rename(name, filename)
	}
	if err := os.Link(name, filename); err != nil {
		return fmt.Errorf("publish export: %w", err)
	}
	return nil
}
