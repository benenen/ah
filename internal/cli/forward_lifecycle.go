package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/benenen/ah/internal/forward"
	"github.com/spf13/cobra"
)

func (a *app) captureForwardOptions(r *forward.Record) error {
	store, err := a.store()
	if err != nil {
		return err
	}
	r.ConfigPath, err = filepath.Abs(store.Path)
	if err != nil {
		return err
	}
	r.KeyPath, r.KnownHosts = a.keyPath, a.knownHosts
	if r.KnownHosts == "~" || strings.HasPrefix(r.KnownHosts, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		r.KnownHosts = filepath.Join(home, strings.TrimPrefix(r.KnownHosts, "~/"))
		if a.knownHosts == "~" {
			r.KnownHosts = home
		}
	}
	for _, path := range []*string{&r.KeyPath, &r.KnownHosts} {
		if *path != "" {
			*path, err = filepath.Abs(*path)
			if err != nil {
				return err
			}
		}
	}
	r.WorkDir, err = os.Getwd()
	r.Timeout = a.timeout
	return err
}

func (a *app) forwardRemoveCommand() *cobra.Command {
	var force, all bool
	cmd := &cobra.Command{
		Use: "rm ID | --all", Short: "Remove a stopped forward; --force stops it first",
		Long: "Remove a stopped or failed forward record and its log.\n\n" +
			"--force stops a running or starting forward first and also deletes unreadable records.\n" +
			"--all removes every stopped or failed forward and skips the rest; with --force it removes all of them.",
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !all {
				return a.removeForward(cmd, args[0], force)
			}
			records, err := forward.List(cmd.Context())
			if err != nil {
				return err
			}
			var failed []error
			for _, r := range records {
				if !force && r.Status != "stopped" && r.Status != "failed" {
					if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Skipped %s: %s (use --force)\n", r.ID, r.Status); err != nil {
						return err
					}
					continue
				}
				// Keep going so that one stuck forward does not block the others.
				if err := a.removeForward(cmd, r.ID, force); err != nil {
					failed = append(failed, err)
					if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Failed %s: %v\n", r.ID, err); err != nil {
						return err
					}
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("%d of %d forwards not removed: %w", len(failed), len(records), errors.Join(failed...))
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "stop an active forward before removing it")
	cmd.Flags().BoolVar(&all, "all", false, "remove every stopped or failed forward (with --force, every forward)")
	return cmd
}

func (a *app) removeForward(cmd *cobra.Command, id string, force bool) (err error) {
	ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout+5*time.Second)
	defer cancel()
	// The launcher holds the lifecycle lock until startup completes.
	// Cancel its control socket first so --force also interrupts SSH setup.
	if force {
		if current, readErr := forward.Read(id); readErr == nil && current.Status == "starting" {
			if err := forward.Stop(ctx, current.ID); err != nil {
				return err
			}
		}
	}
	lock, err := forward.Lock(ctx, id)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	r, err := forward.Read(id)
	if err != nil {
		if !force {
			return fmt.Errorf("read forward %s: %w (use --force to delete an unreadable record)", id, err)
		}
		// The unreadable record hides its control socket, so a worker started
		// from it cannot be confirmed stopped; deleting may leave it listening.
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: forward %s is unreadable (%v); a worker started from it cannot be stopped and may keep listening\n", id, err); err != nil {
			return err
		}
		if err := forward.RemoveCorrupt(id); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", id)
		return err
	}
	if force && (r.Status == "running" || r.Status == "starting") {
		if err := forward.Stop(ctx, r.ID); err != nil {
			return err
		}
	}
	if err := forward.Remove(r.ID); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", r.ID)
	return err
}

func (a *app) forwardStartCommand(restart bool) *cobra.Command {
	name, description := "start", "Start a stopped forward in the background using the same ID"
	if restart {
		name, description = "restart", "Restart a forward in the background using the same ID"
	}
	return &cobra.Command{
		Use: name + " ID", Short: description, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout+5*time.Second)
			defer cancel()
			lock, err := forward.Lock(ctx, args[0])
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, lock.Close()) }()
			r, err := forward.Read(args[0])
			if err != nil {
				return err
			}
			options := *a
			if !cmd.Flags().Changed("config") && r.ConfigPath != "" {
				options.configPath = r.ConfigPath
			}
			if !cmd.Flags().Changed("key-file") && r.KeyPath != "" {
				options.keyPath = r.KeyPath
			}
			if !cmd.Flags().Changed("known-hosts") && r.KnownHosts != "" {
				options.knownHosts = r.KnownHosts
			}
			if !cmd.Flags().Changed("timeout") && r.Timeout > 0 {
				options.timeout = r.Timeout
			}
			// Validate configuration before stopping a working forward.
			if _, err := options.connection(r.Name); err != nil {
				return err
			}
			if restart && (r.Status == "running" || r.Status == "starting") {
				if err := forward.Stop(ctx, r.ID); err != nil {
					return err
				}
				r, err = forward.Read(r.ID)
				if err != nil {
					return err
				}
			}
			if r.Status != "stopped" && r.Status != "failed" {
				return fmt.Errorf("forward %s is %s; stop it before starting", r.ID, r.Status)
			}
			workDir := r.WorkDir
			if err := options.captureForwardOptions(&r); err != nil {
				return err
			}
			if workDir != "" {
				r.WorkDir = workDir
			}
			r.Status, r.Error, r.Socket, r.PID, r.Started = "starting", "", "", 0, time.Now()
			if err := r.Save(); err != nil {
				return err
			}
			if err := options.startForwardDaemon(cmd, r); err != nil {
				r.Status, r.Error = "failed", err.Error()
				return errors.Join(err, r.Save())
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.ID)
			return err
		},
	}
}
