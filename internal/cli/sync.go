package cli

import (
	"fmt"
	"os"

	"github.com/benenen/ah/internal/transfer"
	"github.com/spf13/cobra"
)

func (a *app) directoryCommand(sync bool) *cobra.Command {
	mode := copyMode{Sync: true, Force: true, Recursive: true}
	var dryRun, backup bool
	var bwlimit string
	name, short := "diff", "Compare directory contents, including entries present only at the destination"
	if sync {
		name, short = "sync", "Update destination directory contents; retain extra entries unless --delete is set"
	}
	cmd := &cobra.Command{Use: name + " SOURCE DESTINATION", Short: short, Args: cobra.ExactArgs(2), ValidArgsFunction: a.completeRemotePair,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := transfer.ValidateExcludes(mode.Exclude); err != nil {
				return err
			}
			if sync {
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
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if !sync || dryRun {
				return a.diffDirectories(cmd, args[0], args[1], mode, cwd, sync)
			}
			return a.copyWithHistory(cmd, args[0], args[1], mode, cwd)
		},
	}
	cmd.Flags().StringArrayVar(&mode.Exclude, "exclude", nil, "exclude and protect a relative glob on both sides (repeatable)")
	cmd.Flags().BoolVar(&mode.Fast, "fast", false, "skip byte comparison when size, permissions and mtime match")
	if sync {
		cmd.Flags().BoolVar(&mode.Delete, "delete", false, "delete destination-only regular files and empty directories after updates succeed")
		cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview updates and deletions without modifying files or recording history")
		cmd.Flags().BoolVarP(&mode.PreserveTimes, "preserve-times", "p", false, "preserve source modification timestamps")
		cmd.Flags().StringVar(&bwlimit, "bwlimit", "", "limit transfer rate (e.g. 500K, 10M, 0 disables)")
		cmd.Flags().BoolVar(&backup, "backup", false, "before replacing or deleting a file, keep the old one as bak.NAME.bak-TIMESTAMP beside it")
		cmd.Flags().IntVar(&mode.BackupKeep, "backup-keep", 5, "with --backup, keep at most this many backups per file, deleting the oldest")
	}
	return cmd
}

func (a *app) diffDirectories(cmd *cobra.Command, source, target string, mode copyMode, cwd string, syncing bool) error {
	src, err := resolveCopyEndpoint(source, cwd)
	if err != nil {
		return err
	}
	dst, err := resolveCopyEndpoint(target, cwd)
	if err != nil {
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
	opts := transfer.SyncOptions{
		TreeOptions: transfer.TreeOptions{
			Options:        transfer.Options{Fast: mode.Fast, BackupKeep: mode.BackupKeep},
			SameFilesystem: src.Name == dst.Name,
			Exclude:        mode.Exclude,
		},
		Delete: mode.Delete,
	}
	return transfer.DiffDirectories(cmd.Context(), sc, tc, src.Path, dst.Path, opts, func(e transfer.PlanEntry) error {
		if syncing && e.Action == "target-only" {
			e.Action = "retain"
			if mode.Delete {
				e.Action = "delete"
			}
		}
		if syncing && dst.Name == "" && (e.Action == "add" || e.Action == "modify" || e.Action == "mkdir" || e.Action == "delete") {
			if err := a.protectHistoryPath(e.Target); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%q -> %q\n", e.Action, e.Source, e.Target)
		if err == nil && syncing && e.Action == "conflict" {
			return fmt.Errorf("file type conflict: %s", e.Target)
		}
		return err
	})
}
