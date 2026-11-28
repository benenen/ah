package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/forward"
	"github.com/spf13/cobra"
)

func (a *app) forwardLogsCommand() *cobra.Command {
	var follow bool
	var lines int
	cmd := &cobra.Command{
		Use:               "logs ID_OR_ALIAS",
		Short:             "Show logs for a background forward",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.completeForwardIDs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if lines < 0 {
				return fmt.Errorf("--lines must not be negative")
			}
			id, err := forward.ResolveID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			logPath, err := forward.LogPath(id)
			if err != nil {
				return err
			}
			f, err := os.Open(logPath)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("no log file for forward %s (foreground forwards write directly to stderr)", id)
				}
				return err
			}
			defer func() { _ = f.Close() }()

			if lines > 0 {
				data, err := io.ReadAll(f)
				if err != nil {
					return err
				}
				out := tailLines(string(data), lines)
				if _, err := io.WriteString(cmd.OutOrStdout(), out); err != nil {
					return err
				}
			} else {
				if _, err := io.Copy(cmd.OutOrStdout(), f); err != nil {
					return err
				}
			}

			if !follow {
				return nil
			}
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			buf := make([]byte, 4096)
			for {
				for {
					n, readErr := f.Read(buf)
					if n > 0 {
						if _, writeErr := cmd.OutOrStdout().Write(buf[:n]); writeErr != nil {
							return writeErr
						}
					}
					if readErr == io.EOF {
						break
					}
					if readErr != nil {
						return readErr
					}
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-ticker.C:
				}
			}
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow appended log output until interrupted")
	cmd.Flags().IntVarP(&lines, "lines", "n", 0, "number of trailing lines to show (0 shows all)")
	return cmd
}

func tailLines(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	trimmed := strings.TrimSuffix(s, "\n")
	parts := strings.Split(trimmed, "\n")
	if len(parts) <= n {
		return s
	}
	suffix := ""
	if strings.HasSuffix(s, "\n") {
		suffix = "\n"
	}
	return strings.Join(parts[len(parts)-n:], "\n") + suffix
}

func (a *app) forwardProfileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "profile",
		Aliases: []string{"p"},
		Short:   "Manage saved port-forward profiles",
	}

	var local, dynamic, remote, reconnect, continueOnError bool
	var group, keepAlive string
	addCmd := &cobra.Command{
		Use:   "add ALIAS NAME LISTEN [TARGET] [-L | -D | -R]",
		Short: "Save a reusable forward profile",
		Args: func(cmd *cobra.Command, args []string) error {
			if dynamic {
				return cobra.ExactArgs(3)(cmd, args)
			}
			return cobra.ExactArgs(4)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			alias, connName := args[0], args[1]
			if err := config.ValidateName(alias); err != nil {
				return fmt.Errorf("invalid alias: %w", err)
			}
			if _, err := a.connection(connName); err != nil {
				return err
			}
			kind, listen, target := forward.Local, "", ""
			var err error
			switch {
			case dynamic:
				kind = forward.Dynamic
				listen, err = parseForwardAddress(args[2])
			case remote:
				kind = forward.Remote
				listen, target, err = parseLocalForward(args[2], args[3])
			default:
				listen, target, err = parseLocalForward(args[2], args[3])
			}
			if err != nil {
				return err
			}
			prof := forward.Profile{
				Connection: connName,
				Type:       kind,
				Listen:     listen,
				Target:     target,
				Group:      group,
				KeepAlive:  keepAlive,
				Reconnect:  reconnect,
			}
			if cmd.Flags().Changed("continue-on-error") {
				v := continueOnError
				prof.ContinueOnError = &v
			}
			if err := prof.Validate(); err != nil {
				return err
			}
			if err := forward.UpdateProfiles(cmd.Context(), func(m map[string]forward.Profile) error {
				if _, exists := m[alias]; exists {
					return fmt.Errorf("forward profile %q already exists", alias)
				}
				m[alias] = prof
				return nil
			}); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved forward profile %s\n", alias)
			return err
		},
	}
	addCmd.Flags().BoolVarP(&local, "local", "L", false, "local forward (default)")
	addCmd.Flags().BoolVarP(&dynamic, "dynamic", "D", false, "dynamic SOCKS5 proxy")
	addCmd.Flags().BoolVarP(&remote, "remote", "R", false, "remote forward")
	addCmd.MarkFlagsMutuallyExclusive("local", "dynamic", "remote")
	addCmd.Flags().StringVar(&group, "group", "", "optional group name")
	addCmd.Flags().StringVar(&keepAlive, "keepalive", "", "optional SSH keepalive duration")
	addCmd.Flags().BoolVar(&reconnect, "reconnect", false, "reconnect automatically on failure")
	addCmd.Flags().BoolVar(&continueOnError, "continue-on-error", true, "keep listening after individual target errors")

	var lsGroup string
	var lsJSON bool
	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "List saved forward profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			profiles, err := forward.LoadProfiles()
			if err != nil {
				return err
			}
			aliases := make([]string, 0, len(profiles))
			for alias, p := range profiles {
				if lsGroup != "" && !strings.EqualFold(p.Group, lsGroup) {
					continue
				}
				aliases = append(aliases, alias)
			}
			sort.Strings(aliases)
			if lsJSON {
				type profileJSON struct {
					Alias      string `json:"alias"`
					Group      string `json:"group,omitempty"`
					Type       string `json:"type"`
					Connection string `json:"connection"`
					Listen     string `json:"listen"`
					Target     string `json:"target,omitempty"`
					Reconnect  bool   `json:"reconnect,omitempty"`
				}
				out := make([]profileJSON, 0, len(aliases))
				for _, alias := range aliases {
					p := profiles[alias]
					out = append(out, profileJSON{
						Alias: alias, Group: p.Group, Type: p.Kind(),
						Connection: p.Connection, Listen: p.Listen, Target: p.Target, Reconnect: p.Reconnect,
					})
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetEscapeHTML(false)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(out, "ALIAS\tGROUP\tTYPE\tNAME\tLISTEN\tTARGET"); err != nil {
				return err
			}
			for _, alias := range aliases {
				p := profiles[alias]
				grp := p.Group
				if grp == "" {
					grp = "-"
				}
				target := p.Target
				if p.Kind() == forward.Dynamic {
					target = "(socks5)"
				}
				if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%s\n", alias, grp, forwardFlags[p.Kind()], p.Connection, p.Listen, target); err != nil {
					return err
				}
			}
			return out.Flush()
		},
	}
	lsCmd.Flags().StringVar(&lsGroup, "group", "", "filter by group name")
	lsCmd.Flags().BoolVar(&lsJSON, "json", false, "print profiles as JSON")

	rmCmd := &cobra.Command{
		Use:     "rm ALIAS",
		Aliases: []string{"remove"},
		Short:   "Remove a saved forward profile",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := forward.UpdateProfiles(cmd.Context(), func(m map[string]forward.Profile) error {
				if _, ok := m[args[0]]; !ok {
					return fmt.Errorf("forward profile %q does not exist", args[0])
				}
				delete(m, args[0])
				return nil
			}); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Removed forward profile %s\n", args[0])
			return err
		},
	}

	cmd.AddCommand(addCmd, lsCmd, rmCmd)
	return cmd
}

func (a *app) forwardUpCommand() *cobra.Command {
	var group string
	cmd := &cobra.Command{
		Use:   "up ALIAS | --group GROUP",
		Short: "Start a saved forward profile or group in the background",
		Args: func(cmd *cobra.Command, args []string) error {
			if group != "" {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles, err := forward.LoadProfiles()
			if err != nil {
				return err
			}
			var selected []string
			if group != "" {
				for alias, p := range profiles {
					if strings.EqualFold(p.Group, group) {
						selected = append(selected, alias)
					}
				}
				sort.Strings(selected)
				if len(selected) == 0 {
					return fmt.Errorf("no forward profiles in group %q", group)
				}
			} else {
				if _, ok := profiles[args[0]]; !ok {
					return fmt.Errorf("forward profile %q does not exist", args[0])
				}
				selected = []string{args[0]}
			}

			existing, err := forward.List(cmd.Context())
			if err != nil {
				return err
			}
			activeAliases := map[string]string{}
			for _, r := range existing {
				if r.Alias != "" && (r.Status == "running" || r.Status == "starting") {
					activeAliases[r.Alias] = r.ID
				}
			}

			var failed []error
			for _, alias := range selected {
				if activeID, ok := activeAliases[alias]; ok {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s already active (%s)\n", alias, activeID)
					continue
				}
				if err := a.startProfileDaemon(cmd, alias, profiles[alias]); err != nil {
					failed = append(failed, fmt.Errorf("%s: %w", alias, err))
				}
			}
			return errors.Join(failed...)
		},
	}
	cmd.Flags().StringVar(&group, "group", "", "start all profiles in a group")
	return cmd
}

func (a *app) startProfileDaemon(cmd *cobra.Command, alias string, prof forward.Profile) (err error) {
	conn, err := a.connection(prof.Connection)
	if err != nil {
		return err
	}
	record, err := forward.Allocate(prof.Kind(), prof.Connection, prof.Listen, prof.Target)
	if err != nil {
		return err
	}
	record.Alias = alias
	record.Group = prof.Group
	lock, err := forward.Lock(cmd.Context(), record.ID)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if err = a.captureForwardOptions(&record); err != nil {
		return err
	}
	record.Reconnect = prof.Reconnect
	if prof.ContinueOnError != nil {
		record.ContinueOnError = *prof.ContinueOnError
	} else {
		record.ContinueOnError = true
	}
	if prof.KeepAlive != "" {
		d, kerr := time.ParseDuration(prof.KeepAlive)
		if kerr != nil {
			return kerr
		}
		record.KeepAlive = d
	} else if d, kerr := conn.KeepAliveDuration(); kerr == nil && d > 0 {
		record.KeepAlive = d
	} else {
		record.KeepAlive = 30 * time.Second
	}
	if err = record.Save(); err != nil {
		return err
	}
	if err = a.startForwardDaemon(cmd, record); err != nil {
		record.Status, record.Error = "failed", err.Error()
		return errors.Join(err, record.Save())
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", alias, record.ID)
	return err
}

func (a *app) forwardDownCommand() *cobra.Command {
	var group string
	cmd := &cobra.Command{
		Use:   "down ALIAS | --group GROUP",
		Short: "Stop active forwards by alias or group",
		Args: func(cmd *cobra.Command, args []string) error {
			if group != "" {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout+5*time.Second)
			defer cancel()
			if group != "" {
				return a.stopForwardGroup(ctx, cmd, group)
			}
			records, err := forward.List(ctx)
			if err != nil {
				return err
			}
			var matched []forward.Record
			for _, r := range records {
				if r.Alias == args[0] && (r.Status == "running" || r.Status == "starting") {
					matched = append(matched, r)
				}
			}
			if len(matched) == 0 {
				return fmt.Errorf("no active forward with alias %q", args[0])
			}
			var failed []error
			for _, r := range matched {
				if err := a.stopForwardByID(ctx, cmd, r.ID); err != nil {
					failed = append(failed, fmt.Errorf("%s: %w", r.ID, err))
				}
			}
			return errors.Join(failed...)
		},
	}
	cmd.Flags().StringVar(&group, "group", "", "stop all active forwards in a group")
	return cmd
}
