package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func (a *app) doctorCommand() *cobra.Command {
	return &cobra.Command{Use: "doctor NAME", Short: "Check SSH authentication, sudo, SFTP and copy extensions", Args: cobra.ExactArgs(1), ValidArgsFunction: a.completeNames, RunE: func(cmd *cobra.Command, args []string) (err error) {
		c, err := a.connection(args[0])
		if err != nil {
			return fmt.Errorf("configuration: %w", err)
		}
		var outputErr error
		report := func(stage string, err error) {
			status := "OK"
			if err != nil {
				status = "FAIL"
			}
			_, e := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", stage, status)
			outputErr = errors.Join(outputErr, e)
		}
		report("configuration", nil)
		opts := a.connectionSSHOptions(cmd, args[0], c, true)
		opts.Observe = report
		client, err := a.dialSSH(cmd.Context(), cmd, args[0], c, opts, true)
		if err != nil {
			return errors.Join(err, outputErr)
		}
		defer func() { err = errors.Join(err, client.Close(), outputErr) }()
		// Bound subsystem startup too; the normal connection timeout covers only
		// transport and authentication, not a server that stalls after login.
		ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout)
		defer cancel()
		stop := context.AfterFunc(ctx, func() { _ = client.Close() })
		defer stop()
		files, err := client.SFTP()
		report("sftp", err)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, files.Close()) }()
		if !c.Sudo {
			_, e := fmt.Fprintln(cmd.OutOrStdout(), "sudo\tSKIP (disabled)")
			outputErr = errors.Join(outputErr, e)
		}
		var missing []string
		for _, extension := range []string{"hardlink@openssh.com", "posix-rename@openssh.com"} {
			_, ok := files.HasExtension(extension)
			var err error
			if !ok {
				err = fmt.Errorf("extension unavailable")
				missing = append(missing, extension)
			}
			report(extension, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(missing) > 0 {
			return fmt.Errorf("SFTP copy capabilities missing: %v", missing)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Connection %s is ready for copying (checked %s)\n", args[0], time.Now().UTC().Format(time.RFC3339))
		return err
	}}
}
