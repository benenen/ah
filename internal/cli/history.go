package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/history"
	"github.com/spf13/cobra"
)

func (a *app) historyFilePath() (string, error) {
	if a.historyPath != "" {
		return filepath.Abs(a.historyPath)
	}
	return history.DefaultPath()
}
func (a *app) openHistory() (*history.Store, error) {
	p, err := a.historyFilePath()
	if err != nil {
		return nil, err
	}
	return history.Open(p)
}
func (a *app) historyCommand() *cobra.Command {
	limit := 20
	var jsonOutput bool
	var status, connection, after, before string
	search := func(cmd *cobra.Command, args []string) error {
		store, err := a.openHistory()
		if err != nil {
			return err
		}
		defer store.Close()
		opts := history.SearchOptions{Status: status, Connection: connection}
		if connection != "" {
			if err := config.ValidateName(connection); err != nil {
				return err
			}
		}
		for _, bound := range []struct {
			raw  string
			dest *time.Time
		}{{after, &opts.After}, {before, &opts.Before}} {
			raw, dest := bound.raw, bound.dest
			if raw == "" {
				continue
			}
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return fmt.Errorf("history time must be RFC3339: %w", err)
			}
			*dest = parsed
		}
		if !opts.After.IsZero() && !opts.Before.IsZero() && !opts.After.Before(opts.Before) {
			return fmt.Errorf("--after must precede --before")
		}
		records, err := store.SearchFiltered(cmd.Context(), strings.Join(args, " "), limit, opts)
		if err != nil {
			return err
		}
		if jsonOutput {
			if records == nil {
				records = []history.Record{}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(records)
		}
		out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		if _, err = fmt.Fprintln(out, "ID\tSTARTED (UTC)\tSTATUS\tBYTES\tFORCE\tSOURCE\tDESTINATION\tERROR"); err != nil {
			return err
		}
		for _, r := range records {
			if _, err = fmt.Fprintf(out, "%d\t%s\t%s\t%d\t%t\t%q\t%q\t%q\n", r.ID, r.StartedAt.UTC().Format(time.RFC3339), r.Status, r.Bytes, r.Force, r.Source, r.Destination, r.Error); err != nil {
				return err
			}
		}
		return out.Flush()
	}
	cmd := &cobra.Command{Use: "history [QUERY...]", Aliases: []string{"h"}, Short: "Search copy history by keywords", RunE: search}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "print records as JSON")
	cmd.PersistentFlags().StringVar(&status, "status", "", "exact status: running, success, failed, canceled")
	cmd.PersistentFlags().StringVar(&connection, "connection", "", "exact source or destination connection alias")
	cmd.PersistentFlags().StringVar(&after, "after", "", "inclusive start time (RFC3339)")
	cmd.PersistentFlags().StringVar(&before, "before", "", "exclusive start time (RFC3339)")
	cmd.PersistentFlags().IntVar(&limit, "limit", 20, "maximum history records (1-1000)")
	cmd.AddCommand(&cobra.Command{Use: "search [QUERY...]", Short: "Search source, destination, status and errors", RunE: search})
	cmd.AddCommand(&cobra.Command{Use: "show ID", Short: "Print a safely quoted command for a history entry", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		record, err := a.historyRecord(cmd, args[0])
		if err != nil {
			return err
		}
		command, err := historyShellCommand(record, a.historyPath)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), command)
		return err
	}})
	var failedOnly bool
	var keepDays int
	clean := &cobra.Command{Use: "clean", Short: "Delete finished copy history records (keep running copies)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		opts := history.CleanOptions{FailedOnly: failedOnly}
		if cmd.Flags().Changed("keep-days") {
			if keepDays < 1 || keepDays > 36500 {
				return fmt.Errorf("--keep-days must be between 1 and 36500")
			}
			opts.Before = time.Now().Add(-time.Duration(keepDays) * 24 * time.Hour)
		}
		store, err := a.openHistory()
		if err != nil {
			return err
		}
		defer store.Close()
		count, err := store.Clean(cmd.Context(), opts)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed %d history records; running copies retained\n", count)
		return err
	}}
	clean.Flags().BoolVar(&failedOnly, "failed", false, "delete only failed records")
	clean.Flags().IntVar(&keepDays, "keep-days", 0, "retain the last N days; delete older records by start time (1-36500)")
	cmd.AddCommand(clean)
	var force bool
	run := &cobra.Command{Use: "run ID", Short: "Repeat a recorded copy and append a new history entry", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		record, err := a.historyRecord(cmd, args[0])
		if err != nil {
			return err
		}
		replay := *a
		if !cmd.Flags().Changed("config") {
			replay.configPath = record.ConfigPath
		}
		if !cmd.Flags().Changed("known-hosts") {
			replay.knownHosts = record.KnownHosts
		}
		if !cmd.Flags().Changed("key-file") {
			replay.keyPath = record.KeyPath
		}
		if cmd.Flags().Changed("force") {
			record.Force = force
		}
		// Structured invocation, never shell evaluation of data from SQLite.
		return replay.copyWithHistory(cmd, record.Source, record.Destination, copyMode{Resume: record.Resume, Force: record.Force, Recursive: record.Recursive, BackupKeep: record.BackupKeep, Exclude: record.Exclude}, record.Cwd)
	}}
	run.Flags().BoolVarP(&force, "force", "f", false, "override recorded overwrite behavior (use --force=false to prevent replacement)")
	cmd.AddCommand(run)
	return cmd
}
func (a *app) historyRecord(cmd *cobra.Command, value string) (history.Record, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return history.Record{}, fmt.Errorf("history ID must be a positive integer")
	}
	store, err := a.openHistory()
	if err != nil {
		return history.Record{}, err
	}
	defer store.Close()
	record, err := store.Get(cmd.Context(), id)
	if err != nil {
		return record, fmt.Errorf("load history #%d: %w", id, err)
	}
	return record, nil
}
func historyShellCommand(r history.Record, historyPath string) (string, error) {
	src, err := resolveCopyEndpoint(r.Source, r.Cwd)
	if err != nil {
		return "", err
	}
	dst, err := resolveCopyEndpoint(r.Destination, r.Cwd)
	if err != nil {
		return "", err
	}
	source, target := src.Path, dst.Path
	if src.Name != "" {
		source = src.Name + ":" + src.Path
	}
	if dst.Name != "" {
		target = dst.Name + ":" + dst.Path
	}
	args := []string{"ah", "--config", r.ConfigPath, "--known-hosts", r.KnownHosts, "--key-file", r.KeyPath}
	if historyPath != "" {
		absolute, err := filepath.Abs(historyPath)
		if err != nil {
			return "", err
		}
		args = append(args, "--history-file", absolute)
	}
	args = append(args, "cp")
	if r.Force {
		args = append(args, "--force")
	}
	if r.Recursive {
		args = append(args, "--recursive")
	}
	if r.BackupKeep > 0 {
		args = append(args, "--backup", "--backup-keep", strconv.Itoa(r.BackupKeep))
	}
	if r.Resume {
		args = append(args, "--resume")
	}
	for _, pattern := range r.Exclude {
		args = append(args, "--exclude", pattern)
	}
	args = append(args, "--", source, target)
	for i := 1; i < len(args); i++ {
		args[i] = "'" + strings.ReplaceAll(args[i], "'", "'\\''") + "'"
	}
	return "(cd " + "'" + strings.ReplaceAll(r.Cwd, "'", "'\\''") + "' && " + strings.Join(args, " ") + ")", nil
}
