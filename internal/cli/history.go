package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

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
	search := func(cmd *cobra.Command, args []string) error {
		store, err := a.openHistory()
		if err != nil {
			return err
		}
		defer store.Close()
		records, err := store.Search(cmd.Context(), strings.Join(args, " "), limit)
		if err != nil {
			return err
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
	cmd.AddCommand(&cobra.Command{Use: "run ID", Short: "Repeat a recorded copy and append a new history entry", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		// Structured invocation, never shell evaluation of data from SQLite.
		return replay.copyWithHistory(cmd, record.Source, record.Destination, record.Force, record.Cwd)
	}})
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
	args = append(args, "--", source, target)
	for i := 1; i < len(args); i++ {
		args[i] = "'" + strings.ReplaceAll(args[i], "'", "'\\''") + "'"
	}
	return "(cd " + "'" + strings.ReplaceAll(r.Cwd, "'", "'\\''") + "' && " + strings.Join(args, " ") + ")", nil
}
