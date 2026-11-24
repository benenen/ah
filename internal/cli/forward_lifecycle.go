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
	r.KeepAlive = a.keepAlive
	r.ContinueOnError = a.forwardContinue
	r.Reconnect = a.forwardReconnect
	return err
}

func (a *app) forwardRemoveCommand() *cobra.Command {
	var force, all, staleOnly bool
	cmd := &cobra.Command{
		Use: "rm ID | --all | --stale", Aliases: []string{"remove"}, Short: "Remove a stopped forward; --force stops it first",
		Long: "Remove a stopped or failed forward record and its log.\n\n" +
			"--force stops a running or starting forward first and also deletes unreadable or stale records.\n" +
			"--stale removes every stale forward record without signaling its PID.\n" +
			"--all removes every stopped or failed forward and skips the rest; with --force it removes all of them.",
		Args: func(cmd *cobra.Command, args []string) error {
			if all || staleOnly {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		ValidArgsFunction: a.completeForwardIDs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if staleOnly {
				records, err := forward.List(cmd.Context())
				if err != nil {
					return err
				}
				var failed []error
				for _, r := range records {
					if r.Status != "stale" {
						continue
					}
					if err := a.removeStaleForward(cmd, r.ID); err != nil {
						failed = append(failed, err)
						if _, werr := fmt.Fprintf(cmd.ErrOrStderr(), "Failed %s: %v\n", r.ID, err); werr != nil {
							return werr
						}
					}
				}
				if len(failed) > 0 {
					return fmt.Errorf("%d stale forwards not removed: %w", len(failed), errors.Join(failed...))
				}
				return nil
			}
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
	cmd.Flags().BoolVar(&staleOnly, "stale", false, "remove every stale forward record without signaling its PID")
	cmd.MarkFlagsMutuallyExclusive("all", "stale")
	return cmd
}

func (a *app) removeStaleForward(cmd *cobra.Command, id string) (err error) {
	ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout+5*time.Second)
	defer cancel()
	lock, err := forward.Lock(ctx, id)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	r, err := forward.Read(id)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: forward %s control socket is unreachable; removing stale record without signaling PID %d\n", r.ID, r.PID); err != nil {
		return err
	}
	if err := forward.RemoveStale(ctx, r.ID); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", r.ID)
	return err
}

func (a *app) removeForward(cmd *cobra.Command, id string, force bool) (err error) {
	ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout+5*time.Second)
	defer cancel()
	// The launcher holds the lifecycle lock until startup completes.
	// Cancel its control socket first so --force also interrupts SSH setup.
	if force {
		if current, readErr := forward.Read(id); readErr == nil && current.Status == "starting" {
			_ = forward.Stop(ctx, current.ID)
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
	if r.Status == "running" || r.Status == "starting" {
		if !force {
			if stale, staleErr := forward.IsStale(ctx, r); staleErr == nil && stale {
				return fmt.Errorf("forward %s is stale (control socket unreachable); use --force or --stale to remove", r.ID)
			}
		} else {
			if err := forward.Stop(ctx, r.ID); err != nil {
				if stale, staleErr := forward.IsStale(ctx, r); staleErr == nil && stale {
					if _, warnErr := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: forward %s control socket is unreachable; removing stale record without signaling PID %d\n", r.ID, r.PID); warnErr != nil {
						return warnErr
					}
					if err := forward.RemoveStale(ctx, r.ID); err != nil {
						return err
					}
					_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", r.ID)
					return err
				}
				return err
			}
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
	statuses := []string{"stopped", "failed"}
	if restart {
		name, description = "restart", "Restart a forward in the background using the same ID"
		statuses = []string{"running", "starting", "stopped", "failed"}
	}
	cmd := &cobra.Command{
		Use: name + " ID", Short: description, Args: cobra.ExactArgs(1),
		ValidArgsFunction: a.completeForwardIDs(statuses...),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout+5*time.Second)
			defer cancel()
			validateRestartConfig := func(r forward.Record) (app, error) {
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
				if !cmd.Flags().Changed("keepalive") {
					options.keepAlive = r.KeepAlive
				}
				if !cmd.Flags().Changed("continue-on-error") {
					options.forwardContinue = r.ContinueOnError
				}
				if !cmd.Flags().Changed("reconnect") {
					options.forwardReconnect = r.Reconnect
				}
				// Validate configuration before stopping a working forward.
				c, err := options.connection(r.Name)
				if err != nil {
					return options, err
				}
				if _, err := options.resolveSSHRoute(r.Name, c); err != nil {
					return options, err
				}
				return options, nil
			}
			stoppedStartup := false
			if restart {
				current, err := forward.Read(args[0])
				if err != nil {
					return err
				}
				if current.Status == "starting" {
					if _, err := validateRestartConfig(current); err != nil {
						return err
					}
					if err := forward.Stop(ctx, current.ID); err != nil {
						return err
					}
					stoppedStartup = true
				}
			}
			lock, err := forward.Lock(ctx, args[0])
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, lock.Close()) }()
			r, err := forward.Read(args[0])
			if err != nil {
				return err
			}
			options, err := validateRestartConfig(r)
			if err != nil {
				return err
			}
			if restart && (!stoppedStartup && (r.Status == "running" || r.Status == "starting")) {
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
	a.bindForwardRunFlags(cmd)
	return cmd
}

func (a *app) completeForwardIDs(statuses ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	allowed := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		allowed[s] = true
	}
	return func(cmd *cobra.Command, args []string, partial string) ([]string, cobra.ShellCompDirective) {
		directive := cobra.ShellCompDirectiveNoFileComp
		if len(args) > 0 {
			return nil, directive
		}
		timeout := min(a.timeout, 2*time.Second)
		if timeout <= 0 {
			timeout = 2 * time.Second
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		records, err := forward.List(ctx)
		if err != nil {
			return nil, directive
		}
		var candidates []string
		for _, r := range records {
			if len(allowed) > 0 && !allowed[r.Status] {
				continue
			}
			if !strings.HasPrefix(r.ID, partial) {
				continue
			}
			flag := forwardFlags[r.Kind()]
			target := r.Target
			if r.Kind() == forward.Dynamic {
				target = "(socks5)"
			}
			desc := fmt.Sprintf("%s %s %s->%s (%s)", flag, r.Name, r.Listen, target, r.Status)
			if r.Status == "corrupt" {
				desc = "corrupt"
			}
			candidates = append(candidates, r.ID+"\t"+desc)
		}
		return candidates, directive
	}
}
