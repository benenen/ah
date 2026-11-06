package cli

import (
	"fmt"
	"os"

	"github.com/benenen/ah/internal/transfer"
	"github.com/spf13/cobra"
)

func (a *app) directoryCommand(sync bool) *cobra.Command {
	mode := copyMode{Sync: true, Force: true, Recursive: true}
	var dryRun bool
	name, short := "diff", "Compare directory contents, including entries present only at the destination"
	if sync {
		name, short = "sync", "Update destination directory contents; retain extra entries unless --delete is set"
	}
	cmd := &cobra.Command{Use: name + " SOURCE DESTINATION", Short: short, Args: cobra.ExactArgs(2), ValidArgsFunction: a.completeRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := transfer.ValidateExcludes(mode.Exclude); err != nil {
				return err
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
	if sync {
		cmd.Flags().BoolVar(&mode.Delete, "delete", false, "delete destination-only regular files and empty directories after updates succeed")
		cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview updates and deletions without modifying files or recording history")
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
	opts := transfer.SyncOptions{TreeOptions: transfer.TreeOptions{SameFilesystem: src.Name == dst.Name, Exclude: mode.Exclude}, Delete: mode.Delete}
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
