package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/history"
	"github.com/benenen/ah/internal/transfer"
	"github.com/pkg/sftp"
	"github.com/spf13/cobra"
)

// copyMode holds the options that change what a copy writes; history stores
// them so a rerun behaves the same.
type copyMode struct {
	Force, Recursive, Resume bool
	PreserveTimes, Fast      bool
	BandwidthLimit           int64
	Sync, Delete             bool
	BackupKeep               int // 0 disables backups
	Exclude                  []string
}

func (a *app) copyCommand() *cobra.Command {
	var mode copyMode
	var backup, dryRun bool
	var bwlimit string
	var tags []string
	var jobs int
	cmd := &cobra.Command{Use: "copy SOURCE... DESTINATION", Aliases: []string{"cp"}, Short: "Copy local or NAME:PATH remote files or directories and record history", Args: cobra.MinimumNArgs(2), ValidArgsFunction: a.completeRemote, RunE: func(cmd *cobra.Command, args []string) error {
		if err := transfer.ValidateExcludes(mode.Exclude); err != nil {
			return err
		}
		if len(mode.Exclude) > 0 && !mode.Recursive {
			return fmt.Errorf("--exclude requires --recursive")
		}
		if !backup {
			if cmd.Flags().Changed("backup-keep") {
				return fmt.Errorf("--backup-keep requires --backup")
			}
			mode.BackupKeep = 0
		} else if mode.BackupKeep < 1 || mode.BackupKeep > 1000 {
			return fmt.Errorf("--backup-keep must be between 1 and 1000")
		}
		var err error
		mode.BandwidthLimit, err = transfer.ParseBandwidthLimit(bwlimit)
		if err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		sources := args[:len(args)-1]
		target := args[len(args)-1]
		tags = normalizeTags(tags)
		if len(tags) > 0 {
			for _, tag := range tags {
				if err := config.ValidateTag(tag); err != nil {
					return err
				}
			}
			if jobs < 1 || jobs > 64 {
				return fmt.Errorf("--jobs must be between 1 and 64")
			}
			return a.copyByTag(cmd, tags, sources, target, mode, cwd, jobs, dryRun)
		}
		if len(sources) > 1 {
			return a.copyMultiple(cmd, sources, target, mode, cwd, dryRun)
		}
		if dryRun {
			return a.previewCopy(cmd, sources[0], target, mode, cwd)
		}
		return a.copyWithHistory(cmd, sources[0], target, mode, cwd)
	}}
	cmd.Flags().BoolVar(&mode.Resume, "resume", false, "retain and verify partial files for resumable copying")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview destination changes without copying or recording history")
	cmd.Flags().StringArrayVar(&mode.Exclude, "exclude", nil, "exclude a relative glob (repeatable, requires --recursive)")
	cmd.Flags().BoolVarP(&mode.Force, "force", "f", false, "atomically replace existing destination files; with -r, merge into an existing directory")
	cmd.Flags().BoolVarP(&mode.Recursive, "recursive", "r", false, "copy a directory tree (symbolic links and special files are skipped)")
	cmd.Flags().BoolVarP(&mode.PreserveTimes, "preserve-times", "p", false, "preserve source modification timestamps")
	cmd.Flags().BoolVar(&mode.Fast, "fast", false, "skip byte comparison when size, permissions and mtime match")
	cmd.Flags().StringVar(&bwlimit, "bwlimit", "", "limit transfer rate (e.g. 500K, 10M, 0 disables)")
	cmd.Flags().BoolVar(&backup, "backup", false, "before replacing a file, keep the old one as bak.NAME.bak-TIMESTAMP beside it (implies --force)")
	cmd.Flags().IntVar(&mode.BackupKeep, "backup-keep", 5, "with --backup, keep at most this many backups per file, deleting the oldest")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "distribute local sources to all connections carrying every tag (destination must start with ':')")
	cmd.Flags().IntVar(&jobs, "jobs", 4, "maximum concurrent hosts when using --tag (1-64)")
	_ = cmd.RegisterFlagCompletionFunc("tag", a.completeTags(false))
	return cmd
}

func (a *app) copyWithHistory(cmd *cobra.Command, source, target string, mode copyMode, cwd string) (err error) {
	store, err := a.openHistory()
	if err != nil {
		return fmt.Errorf("open copy history (copy not started): %w", err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	return a.copyWithStore(cmd, store, source, target, mode, cwd, nil, nil, true)
}

func (a *app) copyWithStore(cmd *cobra.Command, store *history.Store, source, target string, mode copyMode, cwd string, sourceClient, targetClient *sftp.Client, showProgress bool) (err error) {
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
			if mode.Sync {
				summary = fmt.Sprintf("Synced %d files, %d bytes, %d deleted", tree.Files, n, tree.Deleted)
			}
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
	if sourceClient != nil || targetClient != nil {
		src, resolveErr := resolveCopyEndpoint(source, cwd)
		if resolveErr != nil {
			return resolveErr
		}
		dst, resolveErr := resolveCopyEndpoint(target, cwd)
		if resolveErr != nil {
			return resolveErr
		}
		if !mode.Sync {
			if protectErr := a.protectHistoryDestination(src, dst); protectErr != nil {
				return protectErr
			}
		}
		n, tree, unchanged, err = a.performCopyWithClients(cmd, src, dst, sourceClient, targetClient, mode, showProgress)
		return err
	}
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
	if !mode.Sync {
		if err := a.protectHistoryDestination(src, dst); err != nil {
			return 0, nil, false, err
		}
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
	return a.performCopyWithClients(cmd, src, dst, sourceClient, targetClient, mode, true)
}

func (a *app) performCopyWithClients(cmd *cobra.Command, src, dst transfer.Endpoint, sourceClient, targetClient *sftp.Client, mode copyMode, showProgress bool) (int64, *transfer.TreeResult, bool, error) {
	var progress func(int64, int64)
	var finishBar = func() {}
	if showProgress {
		bar := newProgressBar(cmd.ErrOrStderr())
		progress = bar.update
		finishBar = bar.finish
	}
	options := transfer.Options{
		Resume:         mode.Resume,
		PreserveTimes:  mode.PreserveTimes,
		Fast:           mode.Fast,
		BandwidthLimit: mode.BandwidthLimit,
		Force:          mode.Force,
		BackupKeep:     mode.BackupKeep,
		Progress:       progress,
	}
	if !mode.Recursive {
		unchanged := false
		options.Unchanged = func(string) { unchanged = true }
		n, err := transfer.Copy(cmd.Context(), sourceClient, targetClient, src.Path, dst.Path, options)
		finishBar()
		return n, nil, unchanged, err
	}
	tree := transfer.TreeOptions{
		Options:  options,
		Exclude:  mode.Exclude,
		Excluded: func(p string) { finishBar(); _, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Excluded %s\n", p) },
		// Two endpoints on one connection name share a filesystem.
		SameFilesystem: src.Name == dst.Name,
		Skipped: func(p string, m os.FileMode) {
			finishBar()
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Skipped %s (%s)\n", p, skippedKind(m))
		},
	}
	if dst.Name == "" {
		tree.CheckTarget = a.protectHistoryPath
	}
	var result transfer.TreeResult
	var err error
	if mode.Sync {
		result, err = transfer.SyncDirectories(cmd.Context(), sourceClient, targetClient, src.Path, dst.Path, transfer.SyncOptions{TreeOptions: tree, Delete: mode.Delete})
	} else {
		result, err = transfer.CopyTree(cmd.Context(), sourceClient, targetClient, src.Path, dst.Path, tree)
	}
	finishBar()
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
	client, err := a.dialSSH(cmd.Context(), cmd, e.Name, c, a.connectionSSHOptions(cmd, e.Name, c, true), true)
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
	record := history.Record{
		Sync:           mode.Sync,
		Delete:         mode.Delete,
		Resume:         mode.Resume,
		PreserveTimes:  mode.PreserveTimes,
		Fast:           mode.Fast,
		BandwidthLimit: mode.BandwidthLimit,
		Exclude:        append([]string(nil), mode.Exclude...),
		Source:         source,
		Destination:    target,
		Force:          mode.Force,
		Recursive:      mode.Recursive,
		BackupKeep:     mode.BackupKeep,
		Cwd:            cwd,
	}
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

func (a *app) previewCopy(cmd *cobra.Command, source, target string, mode copyMode, cwd string) error {
	src, err := resolveCopyEndpoint(source, cwd)
	if err != nil {
		return err
	}
	dst, err := resolveCopyEndpoint(target, cwd)
	if err != nil {
		return err
	}
	if err := a.protectHistoryDestination(src, dst); err != nil {
		return err
	}
	sc, closeSource, err := a.openCopyEndpoint(cmd, src, cwd)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	defer closeSource()
	tc, closeTarget, err := a.openCopyEndpoint(cmd, dst, cwd)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	defer closeTarget()
	opts := transfer.TreeOptions{Options: transfer.Options{Resume: mode.Resume, PreserveTimes: mode.PreserveTimes, Fast: mode.Fast, Force: mode.Force, BackupKeep: mode.BackupKeep}, Exclude: mode.Exclude, SameFilesystem: src.Name == dst.Name}
	if dst.Name == "" {
		opts.CheckTarget = a.protectHistoryPath
	}
	return transfer.Plan(cmd.Context(), sc, tc, src.Path, dst.Path, mode.Recursive, opts, func(e transfer.PlanEntry) error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%q -> %q\n", e.Action, e.Source, e.Target)
		return err
	})
}

func requireDestinationDirectory(client *sftp.Client, dst transfer.Endpoint) error {
	if client == nil {
		info, err := os.Stat(dst.Path)
		if err != nil {
			return fmt.Errorf("destination %s: %w", dst.Path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("multi-source destination must be a directory: %s", dst.Path)
		}
		return nil
	}
	resolved, err := transfer.ResolvePath(client, dst.Path)
	if err != nil {
		return err
	}
	info, err := client.Stat(resolved)
	if err != nil {
		return fmt.Errorf("destination %s: %w", dst.Path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("multi-source destination must be a directory: %s", dst.Path)
	}
	return nil
}

func (a *app) copyMultiple(cmd *cobra.Command, sources []string, target string, mode copyMode, cwd string, dryRun bool) error {
	dst, err := resolveCopyEndpoint(target, cwd)
	if err != nil {
		return err
	}
	targetClient, closeTarget, err := a.openCopyEndpoint(cmd, dst, cwd)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	defer closeTarget()
	if err := requireDestinationDirectory(targetClient, dst); err != nil {
		return err
	}
	sourceClients := map[string]*sftp.Client{}
	var closers []func()
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}()
	getSourceClient := func(src transfer.Endpoint) (*sftp.Client, error) {
		if src.Name == "" {
			return nil, nil
		}
		if src.Name == dst.Name {
			return targetClient, nil
		}
		if c, ok := sourceClients[src.Name]; ok {
			return c, nil
		}
		c, closeFn, err := a.openCopyEndpoint(cmd, src, cwd)
		if err != nil {
			return nil, err
		}
		sourceClients[src.Name] = c
		closers = append(closers, closeFn)
		return c, nil
	}
	if dryRun {
		for _, source := range sources {
			src, err := resolveCopyEndpoint(source, cwd)
			if err != nil {
				return err
			}
			if err := a.protectHistoryDestination(src, dst); err != nil {
				return err
			}
			sc, err := getSourceClient(src)
			if err != nil {
				return fmt.Errorf("source %s: %w", source, err)
			}
			opts := transfer.TreeOptions{Options: transfer.Options{Resume: mode.Resume, PreserveTimes: mode.PreserveTimes, Fast: mode.Fast, Force: mode.Force, BackupKeep: mode.BackupKeep}, Exclude: mode.Exclude, SameFilesystem: src.Name == dst.Name}
			if dst.Name == "" {
				opts.CheckTarget = a.protectHistoryPath
			}
			if err := transfer.Plan(cmd.Context(), sc, targetClient, src.Path, dst.Path, mode.Recursive, opts, func(e transfer.PlanEntry) error {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%q -> %q\n", e.Action, e.Source, e.Target)
				return err
			}); err != nil {
				return err
			}
		}
		return nil
	}
	store, err := a.openHistory()
	if err != nil {
		return fmt.Errorf("open copy history (copy not started): %w", err)
	}
	defer store.Close()
	for _, source := range sources {
		src, err := resolveCopyEndpoint(source, cwd)
		if err != nil {
			return err
		}
		sc, err := getSourceClient(src)
		if err != nil {
			return fmt.Errorf("source %s: %w", source, err)
		}
		if err := a.copyWithStore(cmd, store, source, target, mode, cwd, sc, targetClient, true); err != nil {
			return err
		}
	}
	return nil
}

type batchCopyResult struct {
	name   string
	status string
	err    error
}

func (a *app) copyByTag(cmd *cobra.Command, tags, sources []string, target string, mode copyMode, cwd string, jobs int, dryRun bool) error {
	if !strings.HasPrefix(target, ":") || len(target) <= 1 {
		return fmt.Errorf("destination with --tag must be :REMOTE_PATH (for example :/tmp/)")
	}
	remotePath := target[1:]
	for _, s := range sources {
		ep, err := resolveCopyEndpoint(s, cwd)
		if err != nil {
			return err
		}
		if ep.Name != "" {
			return fmt.Errorf("sources with --tag must be local paths: %s", s)
		}
	}
	entries, err := a.connections()
	if err != nil {
		return err
	}
	var names []string
	filter := connectionFilter{tags: tags}
	for _, name := range sortedNames(entries) {
		if filter.match(name, entries[name]) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("no connections match the selected tags")
	}
	if dryRun {
		for _, name := range names {
			worker := &cobra.Command{}
			worker.SetContext(cmd.Context())
			worker.SetIn(strings.NewReader(""))
			worker.SetOut(cmd.OutOrStdout())
			worker.SetErr(cmd.ErrOrStderr())
			dst := transfer.Endpoint{Name: name, Path: remotePath}
			tc, closeTarget, err := a.openCopyEndpoint(worker, dst, cwd)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if len(sources) > 1 {
				if err := requireDestinationDirectory(tc, dst); err != nil {
					closeTarget()
					return fmt.Errorf("%s: %w", name, err)
				}
			}
			for _, source := range sources {
				src, err := resolveCopyEndpoint(source, cwd)
				if err != nil {
					closeTarget()
					return err
				}
				opts := transfer.TreeOptions{Options: transfer.Options{Resume: mode.Resume, PreserveTimes: mode.PreserveTimes, Fast: mode.Fast, Force: mode.Force, BackupKeep: mode.BackupKeep}, Exclude: mode.Exclude}
				if err := transfer.Plan(cmd.Context(), nil, tc, src.Path, dst.Path, mode.Recursive, opts, func(e transfer.PlanEntry) error {
					_, err := fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s\t%q -> %q\n", name, e.Action, e.Source, name+":"+e.Target)
					return err
				}); err != nil {
					closeTarget()
					return fmt.Errorf("%s: %w", name, err)
				}
			}
			closeTarget()
		}
		return nil
	}
	ctx := cmd.Context()
	results := make([]batchCopyResult, len(names))
	queue := make(chan int, len(names))
	for i := range names {
		queue <- i
	}
	close(queue)
	var outMu sync.Mutex
	var workers sync.WaitGroup
	for range min(jobs, len(names)) {
		workers.Go(func() {
			for i := range queue {
				name := names[i]
				if ctx.Err() != nil {
					results[i] = batchCopyResult{name: name, status: "not-run", err: ctx.Err()}
					continue
				}
				results[i] = a.copyHost(ctx, cmd, name, sources, remotePath, mode, cwd, &outMu)
			}
		})
	}
	workers.Wait()
	counts := map[string]int{}
	table := tabwriter.NewWriter(cmd.ErrOrStderr(), 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "NAME\tSTATUS\tERROR"); err != nil {
		return err
	}
	for _, r := range results {
		counts[r.status]++
		msg := ""
		if r.err != nil {
			msg = r.err.Error()
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%q\n", r.name, r.status, msg); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Summary: %d succeeded, %d failed, %d canceled, %d not-run\n", counts["success"], counts["failed"], counts["canceled"], counts["not-run"]); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if counts["success"] != len(names) {
		return fmt.Errorf("batch copy did not succeed on %d of %d connections", len(names)-counts["success"], len(names))
	}
	return nil
}

func (a *app) copyHost(ctx context.Context, cmd *cobra.Command, name string, sources []string, remotePath string, mode copyMode, cwd string, outMu *sync.Mutex) batchCopyResult {
	res := batchCopyResult{name: name, status: "failed"}
	worker := &cobra.Command{}
	worker.SetContext(ctx)
	worker.SetIn(strings.NewReader(""))
	var outBuf, errBuf strings.Builder
	worker.SetOut(&outBuf)
	worker.SetErr(&errBuf)
	dst := transfer.Endpoint{Name: name, Path: remotePath}
	tc, closeTarget, err := a.openCopyEndpoint(worker, dst, cwd)
	if err != nil {
		res.err = err
		if ctx.Err() != nil {
			res.status = "canceled"
		}
		return res
	}
	defer closeTarget()
	if len(sources) > 1 {
		if err := requireDestinationDirectory(tc, dst); err != nil {
			res.err = err
			return res
		}
	}
	store, err := a.openHistory()
	if err != nil {
		res.err = fmt.Errorf("open copy history: %w", err)
		return res
	}
	defer store.Close()
	for _, source := range sources {
		if err := a.copyWithStore(worker, store, source, name+":"+remotePath, mode, cwd, nil, tc, false); err != nil {
			res.err = err
			if ctx.Err() != nil {
				res.status = "canceled"
			}
			return res
		}
	}
	outMu.Lock()
	defer outMu.Unlock()
	if outBuf.Len() > 0 {
		for _, line := range strings.Split(strings.TrimSuffix(outBuf.String(), "\n"), "\n") {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s\n", name, line)
		}
	}
	if errBuf.Len() > 0 {
		for _, line := range strings.Split(strings.TrimSuffix(errBuf.String(), "\n"), "\n") {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[%s] %s\n", name, line)
		}
	}
	res.status = "success"
	return res
}
