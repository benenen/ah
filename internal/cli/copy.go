package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/benenen/ah/internal/history"
	"github.com/benenen/ah/internal/sshclient"
	"github.com/benenen/ah/internal/transfer"
	"github.com/pkg/sftp"
	"github.com/spf13/cobra"
)

func (a *app) copyCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{Use: "cp SOURCE DESTINATION", Short: "Copy a local or NAME:PATH remote file and record its history", Args: cobra.ExactArgs(2), ValidArgsFunction: a.completeRemote, RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return a.copyWithHistory(cmd, args[0], args[1], force, cwd)
	}}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "atomically replace an existing destination file")
	return cmd
}

func (a *app) copyWithHistory(cmd *cobra.Command, source, target string, force bool, cwd string) (err error) {
	store, err := a.openHistory()
	if err != nil {
		return fmt.Errorf("open copy history (copy not started): %w", err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	record, err := a.copyRecord(source, target, force, cwd)
	if err != nil {
		return err
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 5*time.Second)
	id, err := store.Begin(logCtx, record)
	cancel()
	if err != nil {
		return fmt.Errorf("record copy history (copy not started): %w", err)
	}
	var n int64
	defer func() {
		if err != nil && cmd.Context().Err() != nil {
			err = errors.Join(err, cmd.Context().Err())
		}
		status := "success"
		errorText := ""
		if err != nil {
			status = "failed"
			errorText = err.Error()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				status = "canceled"
			}
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 5*time.Second)
		defer cancel()
		if logErr := store.Finish(finishCtx, id, n, status, errorText); logErr != nil {
			err = errors.Join(err, fmt.Errorf("finish copy history #%d: %w", id, logErr))
		}
		if err != nil {
			err = fmt.Errorf("copy history #%d: %w", id, err)
		} else {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Copied %d bytes: %s -> %s (history #%d)\n", n, source, target, id)
		}
	}()
	n, err = a.performCopy(cmd, source, target, force, cwd)
	if err != nil {
		return err
	}
	return nil
}

func (a *app) performCopy(cmd *cobra.Command, source, target string, force bool, cwd string) (int64, error) {
	src, err := resolveCopyEndpoint(source, cwd)
	if err != nil {
		return 0, err
	}
	dst, err := resolveCopyEndpoint(target, cwd)
	if err != nil {
		return 0, err
	}
	if err := a.protectHistoryDestination(src, dst); err != nil {
		return 0, err
	}
	sourceClient, closeSource, err := a.openCopyEndpoint(cmd, src, cwd)
	if err != nil {
		return 0, fmt.Errorf("source: %w", err)
	}
	defer closeSource()
	targetClient, closeTarget, err := a.openCopyEndpoint(cmd, dst, cwd)
	if err != nil {
		return 0, fmt.Errorf("destination: %w", err)
	}
	defer closeTarget()
	return transfer.Copy(cmd.Context(), sourceClient, targetClient, src.Path, dst.Path, force)
}

func resolveCopyEndpoint(value, cwd string) (transfer.Endpoint, error) {
	e, err := transfer.ParseEndpoint(value)
	if err != nil {
		return e, err
	}
	if e.Name == "" {
		if !filepath.IsAbs(e.Path) && !strings.HasPrefix(e.Path, "~") {
			e.Path = filepath.Join(cwd, e.Path)
		}
		e.Path, err = transfer.ResolveLocalPath(e.Path)
	}
	return e, err
}

func (a *app) openCopyEndpoint(cmd *cobra.Command, e transfer.Endpoint, cwd string) (*sftp.Client, func(), error) {
	if e.Name == "" {
		return nil, func() {}, nil
	}
	c, err := a.connection(e.Name)
	if err != nil {
		return nil, nil, err
	}
	if c.IdentityFile != "" && !filepath.IsAbs(c.IdentityFile) && !strings.HasPrefix(c.IdentityFile, "~") {
		c.IdentityFile = filepath.Join(cwd, c.IdentityFile)
	}
	client, err := sshclient.Dial(cmd.Context(), c, a.connectionSSHOptions(cmd, e.Name, c, true))
	if err != nil {
		return nil, nil, err
	}
	files, err := client.SFTP()
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return files, func() { _ = files.Close(); _ = client.Close() }, nil
}

func (a *app) copyRecord(source, target string, force bool, cwd string) (history.Record, error) {
	record := history.Record{Source: source, Destination: target, Force: force, Cwd: cwd}
	store, err := a.store()
	if err != nil {
		return record, err
	}
	record.ConfigPath, err = filepath.Abs(store.Path)
	if err != nil {
		return record, err
	}
	keys, err := a.passwordStore()
	if err != nil {
		return record, err
	}
	record.KeyPath, err = filepath.Abs(keys.Path)
	if err != nil {
		return record, err
	}
	known := a.knownHosts
	if known == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return record, err
		}
		known = filepath.Join(home, ".ssh", "known_hosts")
	}
	record.KnownHosts, err = transfer.ResolveLocalPath(known)
	return record, err
}

// Protect the database and SQLite sidecars from an explicit local destination.
func (a *app) protectHistoryDestination(src, dst transfer.Endpoint) error {
	if dst.Name != "" {
		return nil
	}
	target := dst.Path
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		base := filepath.Base(src.Path)
		if src.Name != "" {
			base = path.Base(src.Path)
		}
		target = filepath.Join(target, base)
	}
	target, err := canonicalPasswordPath(target)
	if err != nil {
		return err
	}
	database, err := a.historyFilePath()
	if err != nil {
		return err
	}
	database, err = canonicalPasswordPath(database)
	if err != nil {
		return err
	}
	targetInfo, _ := os.Stat(target)
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		protected := database + suffix
		info, _ := os.Stat(protected)
		if target == protected || (targetInfo != nil && info != nil && os.SameFile(targetInfo, info)) {
			return fmt.Errorf("destination must not replace the active copy history database or its sidecars")
		}
	}
	return nil
}
