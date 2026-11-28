package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/spf13/cobra"
)

type doctorStage struct {
	Stage  string `json:"stage"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type doctorReport struct {
	Name      string        `json:"name"`
	Ready     bool          `json:"ready"`
	CheckedAt string        `json:"checked_at"`
	Stages    []doctorStage `json:"stages"`
	Error     string        `json:"error,omitempty"`
}

func (a *app) doctorCommand() *cobra.Command {
	var tags []string
	var jobs int
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:               "doctor [NAME]",
		Short:             "Check SSH authentication, sudo, SFTP and copy extensions",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: a.completeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			tags = normalizeTags(tags)
			if len(tags) > 0 && len(args) > 0 {
				return fmt.Errorf("cannot combine connection NAME with --tag")
			}
			if len(tags) == 0 && len(args) == 0 {
				return fmt.Errorf("doctor requires connection NAME or --tag")
			}
			if len(tags) == 0 {
				report, err := a.diagnoseHost(cmd.Context(), cmd, args[0], nil, !jsonOutput)
				if jsonOutput {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetEscapeHTML(false)
					enc.SetIndent("", "  ")
					if encErr := enc.Encode(report); encErr != nil {
						return errors.Join(err, encErr)
					}
				}
				return err
			}
			for _, tag := range tags {
				if err := config.ValidateTag(tag); err != nil {
					return err
				}
			}
			if jobs < 1 || jobs > 64 {
				return fmt.Errorf("--jobs must be between 1 and 64")
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
			return a.doctorBatch(cmd, names, entries, jobs, jsonOutput)
		},
	}
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "check all connections carrying every tag (repeatable or comma-separated)")
	cmd.Flags().IntVar(&jobs, "jobs", 4, "maximum concurrent hosts when using --tag (1-64)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print structured JSON diagnostics")
	_ = cmd.RegisterFlagCompletionFunc("tag", a.completeTags(false))
	return cmd
}

func (a *app) doctorBatch(cmd *cobra.Command, names []string, entries map[string]config.Connection, jobs int, jsonOutput bool) error {
	ctx := cmd.Context()
	reports := make([]doctorReport, len(names))
	errs := make([]error, len(names))
	queue := make(chan int, len(names))
	for i := range names {
		queue <- i
	}
	close(queue)
	var workers sync.WaitGroup
	for range min(jobs, len(names)) {
		workers.Go(func() {
			for i := range queue {
				name := names[i]
				if ctx.Err() != nil {
					reports[i] = doctorReport{Name: name, Error: ctx.Err().Error()}
					errs[i] = ctx.Err()
					continue
				}
				worker := &cobra.Command{}
				worker.SetContext(ctx)
				worker.SetIn(strings.NewReader(""))
				worker.SetOut(io.Discard)
				worker.SetErr(io.Discard)
				c := entries[name]
				reports[i], errs[i] = a.diagnoseHost(ctx, worker, name, &c, false)
			}
		})
	}
	workers.Wait()

	readyCount := 0
	for _, r := range reports {
		if r.Ready {
			readyCount++
		}
	}
	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reports); err != nil {
			return err
		}
	} else {
		out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(out, "NAME\tREADY\tERROR"); err != nil {
			return err
		}
		for _, r := range reports {
			status := "FAIL"
			if r.Ready {
				status = "OK"
			}
			if _, err := fmt.Fprintf(out, "%s\t%s\t%q\n", r.Name, status, r.Error); err != nil {
				return err
			}
		}
		if err := out.Flush(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Summary: %d ready, %d failed\n", readyCount, len(names)-readyCount); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readyCount != len(names) {
		return fmt.Errorf("doctor failed on %d of %d connections", len(names)-readyCount, len(names))
	}
	return nil
}

func (a *app) diagnoseHost(ctx context.Context, cmd *cobra.Command, name string, preloaded *config.Connection, streamText bool) (reportDoc doctorReport, err error) {
	reportDoc = doctorReport{Name: name}
	var c config.Connection
	if preloaded != nil {
		c = *preloaded
	} else {
		c, err = a.connection(name)
		if err != nil {
			err = fmt.Errorf("configuration: %w", err)
			reportDoc.Stages = append(reportDoc.Stages, doctorStage{Stage: "configuration", Status: "FAIL", Error: err.Error()})
			reportDoc.Error = err.Error()
			return reportDoc, err
		}
	}
	var outputErr error
	report := func(stage string, stageErr error) {
		status := "OK"
		errMsg := ""
		if stageErr != nil {
			status = "FAIL"
			errMsg = stageErr.Error()
		}
		reportDoc.Stages = append(reportDoc.Stages, doctorStage{Stage: stage, Status: status, Error: errMsg})
		if streamText {
			_, e := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", stage, status)
			outputErr = errors.Join(outputErr, e)
		}
	}
	report("configuration", nil)
	opts := a.connectionSSHOptions(cmd, name, c, true)
	opts.Observe = report
	client, err := a.dialSSH(ctx, cmd, name, c, opts, true)
	if err != nil {
		reportDoc.Error = err.Error()
		return reportDoc, errors.Join(err, outputErr)
	}
	defer func() { err = errors.Join(err, client.Close(), outputErr) }()
	// Bound subsystem startup too; the normal connection timeout covers only
	// transport and authentication, not a server that stalls after login.
	sftpCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	stop := context.AfterFunc(sftpCtx, func() { _ = client.Close() })
	defer stop()
	files, err := client.SFTP()
	report("sftp", err)
	if err != nil {
		reportDoc.Error = err.Error()
		return reportDoc, err
	}
	defer func() { err = errors.Join(err, files.Close()) }()
	if !c.Sudo {
		reportDoc.Stages = append(reportDoc.Stages, doctorStage{Stage: "sudo", Status: "SKIP"})
		if streamText {
			_, e := fmt.Fprintln(cmd.OutOrStdout(), "sudo\tSKIP (disabled)")
			outputErr = errors.Join(outputErr, e)
		}
	}
	var missing []string
	for _, extension := range []string{"hardlink@openssh.com", "posix-rename@openssh.com"} {
		_, ok := files.HasExtension(extension)
		var extErr error
		if !ok {
			extErr = fmt.Errorf("extension unavailable")
			missing = append(missing, extension)
		}
		report(extension, extErr)
	}
	if sftpCtx.Err() != nil {
		reportDoc.Error = sftpCtx.Err().Error()
		return reportDoc, sftpCtx.Err()
	}
	if len(missing) > 0 {
		err = fmt.Errorf("SFTP copy capabilities missing: %v", missing)
		reportDoc.Error = err.Error()
		return reportDoc, err
	}
	checkedAt := time.Now().UTC().Format(time.RFC3339)
	reportDoc.Ready = true
	reportDoc.CheckedAt = checkedAt
	if streamText {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Connection %s is ready for copying (checked %s)\n", name, checkedAt)
	}
	return reportDoc, err
}
