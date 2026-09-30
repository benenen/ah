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

// copyMode holds the options that change what a copy writes; history stores
// them so a rerun behaves the same.
type copyMode struct {
	Force, Recursive bool
	BackupKeep       int // 0 disables backups
}

func (a *app) copyCommand() *cobra.Command {
	var mode copyMode
	var backup bool
	cmd := &cobra.Command{Use: "copy SOURCE DESTINATION", Aliases: []string{"cp"}, Short: "Copy a local or NAME:PATH remote file or directory and record its history", Args: cobra.ExactArgs(2), ValidArgsFunction: a.completeRemote, RunE: func(cmd *cobra.Command, args []string) error {
		if !backup {
			mode.BackupKeep = 0
		} else if mode.BackupKeep < 1 || mode.BackupKeep > 1000 {
			return fmt.Errorf("--backup-keep must be between 1 and 1000")
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return a.copyWithHistory(cmd, args[0], args[1], mode, cwd)
	}}
	cmd.Flags().BoolVarP(&mode.Force, "force", "f", false, "atomically replace existing destination files; with -r, merge into an existing directory")
	cmd.Flags().BoolVarP(&mode.Recursive, "recursive", "r", false, "copy a directory tree (symbolic links and special files are skipped)")
	cmd.Flags().BoolVar(&backup, "backup", false, "before replacing a file, keep the old one as bak.NAME.bak-TIMESTAMP beside it (implies --force)")
	cmd.Flags().IntVar(&mode.BackupKeep, "backup-keep", 5, "with --backup, keep at most this many backups per file, deleting the oldest")
	return cmd
}

func (a *app) copyWithHistory(cmd *cobra.Command, source, target string, mode copyMode, cwd string) (err error) {
	store, err := a.openHistory()
	if err != nil {
		return fmt.Errorf("open copy history (copy not started): %w", err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	record, err := a.copyRecord(source, target, mode, cwd)
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
	var tree *transfer.TreeResult
	var unchanged bool
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
		} else if tree != nil {
			summary := fmt.Sprintf("Copied %d files, %d bytes", tree.Files, n)
			if tree.Unchanged > 0 {
				summary += fmt.Sprintf(", %d unchanged", tree.Unchanged)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s -> %s (history #%d)\n", summary, source, target, id)
		} else if unchanged {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Unchanged (identical content): %s -> %s (history #%d)\n", source, target, id)
		} else {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Copied %d bytes: %s -> %s (history #%d)\n", n, source, target, id)
		}
	}()
	n, tree, unchanged, err = a.performCopy(cmd, source, target, mode, cwd)
	return err
}

// performCopy returns a tree summary only for recursive copies, and reports
// whether a single file was left alone because it already matched.
func (a *app) performCopy(cmd *cobra.Command, source, target string, mode copyMode, cwd string) (int64, *transfer.TreeResult, bool, error) {
	src, err := resolveCopyEndpoint(source, cwd)
	if err != nil {
		return 0, nil, false, err
	}
	dst, err := resolveCopyEndpoint(target, cwd)
	if err != nil {
		return 0, nil, false, err
	}
	if err := a.protectHistoryDestination(src, dst); err != nil {
		return 0, nil, false, err
	}
	sourceClient, closeSource, err := a.openCopyEndpoint(cmd, src, cwd)
	if err != nil {
		return 0, nil, false, fmt.Errorf("source: %w", err)
	}
	defer closeSource()
	targetClient, closeTarget, err := a.openCopyEndpoint(cmd, dst, cwd)
	if err != nil {
		return 0, nil, false, fmt.Errorf("destination: %w", err)
	}
	defer closeTarget()
	bar := newProgressBar(cmd.ErrOrStderr())
	options := transfer.Options{Force: mode.Force, BackupKeep: mode.BackupKeep, Progress: bar.update}
	if !mode.Recursive {
		unchanged := false
		options.Unchanged = func(string) { unchanged = true }
		n, err := transfer.Copy(cmd.Context(), sourceClient, targetClient, src.Path, dst.Path, options)
		bar.finish()
		return n, nil, unchanged, err
	}
	tree := transfer.TreeOptions{
		Options: options,
		// Two endpoints on one connection name share a filesystem.
		SameFilesystem: src.Name == dst.Name,
		Skipped: func(p string, m os.FileMode) {
			bar.finish()
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Skipped %s (%s)\n", p, skippedKind(m))
		},
	}
	if dst.Name == "" {
		tree.CheckTarget = a.protectHistoryPath
	}
	result, err := transfer.CopyTree(cmd.Context(), sourceClient, targetClient, src.Path, dst.Path, tree)
	bar.finish()
	return result.Bytes, &result, false, err
}

func skippedKind(m os.FileMode) string {
	switch {
	case m&os.ModeSymlink != 0:
		return "symbolic link"
	case m&os.ModeNamedPipe != 0:
		return "named pipe"
	case m&os.ModeSocket != 0:
		return "socket"
	case m&os.ModeDevice != 0:
		return "device"
	}
	return "special file"
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

func (a *app) copyRecord(source, target string, mode copyMode, cwd string) (history.Record, error) {
	record := history.Record{Source: source, Destination: target, Force: mode.Force, Recursive: mode.Recursive, BackupKeep: mode.BackupKeep, Cwd: cwd}
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
	return a.protectHistoryPath(target)
}

// protectHistoryPath rejects a local destination file that is the active
// history database or one of its SQLite sidecars.
func (a *app) protectHistoryPath(target string) error {
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
