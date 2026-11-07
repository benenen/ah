package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

type execResult struct {
	name, status string
	exit         int // -1 when no remote exit status was received
	err          error
}

func (a *app) execCommand() *cobra.Command {
	var tags []string
	var jobs int
	var timeout time.Duration
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "exec --tag TAG [--jobs N] -- COMMAND [ARG...]",
		Short: "Execute a remote command on connections carrying every selected tag",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() != 0 {
				return fmt.Errorf("exec requires -- before the remote command")
			}
			command := strings.Join(args, " ")
			if strings.TrimSpace(command) == "" {
				return fmt.Errorf("remote command must not be empty")
			}
			tags = normalizeTags(tags)
			if len(tags) == 0 {
				return fmt.Errorf("exec requires at least one --tag")
			}
			for _, tag := range tags {
				if err := config.ValidateTag(tag); err != nil {
					return err
				}
			}
			if jobs < 1 || jobs > 64 {
				return fmt.Errorf("--jobs must be between 1 and 64")
			}
			if timeout < 0 {
				return fmt.Errorf("--exec-timeout must not be negative")
			}
			entries, err := a.connections()
			if err != nil {
				return err
			}
			names := []string{}
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
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), name); err != nil {
						return err
					}
				}
				return cmd.Context().Err()
			}
			return a.execBatch(cmd, names, entries, command, jobs, timeout)
		},
	}
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "select connections carrying every tag (repeatable or comma-separated, required)")
	cmd.Flags().IntVar(&jobs, "jobs", 4, "maximum concurrent hosts (1-64)")
	cmd.Flags().DurationVar(&timeout, "exec-timeout", 0, "maximum time per host, including connection establishment (0 disables)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list selected connection names without connecting or executing")
	_ = cmd.RegisterFlagCompletionFunc("tag", a.completeTags(false))
	return cmd
}

func (a *app) execBatch(cmd *cobra.Command, names []string, entries map[string]config.Connection, command string, jobs int, timeout time.Duration) error {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	output := &execOutput{cancel: cancel}
	results := make([]execResult, len(names))
	queue := make(chan int, len(names))
	for i := range names {
		queue <- i
	}
	close(queue)
	var workers sync.WaitGroup
	for range min(jobs, len(names)) {
		workers.Go(func() {
			for i := range queue {
				if ctx.Err() != nil {
					results[i] = execResult{name: names[i], status: "not-run", exit: -1, err: ctx.Err()}
					continue
				}
				results[i] = a.execHost(ctx, names[i], entries[names[i]], command, timeout, output, cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
		})
	}
	workers.Wait()
	counts := map[string]int{}
	table := tabwriter.NewWriter(cmd.ErrOrStderr(), 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "NAME\tSTATUS\tEXIT\tERROR"); err != nil {
		return err
	}
	for _, result := range results {
		counts[result.status]++
		exit := "-"
		if result.exit >= 0 {
			exit = fmt.Sprint(result.exit)
		}
		message := ""
		if result.err != nil {
			message = result.err.Error()
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%q\n", result.name, result.status, exit, message); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Summary: %d succeeded, %d failed, %d canceled, %d not-run\n", counts["success"], counts["failed"], counts["canceled"], counts["not-run"]); err != nil {
		return err
	}
	if output.err != nil {
		return fmt.Errorf("write batch output: %w", output.err)
	}
	if cmd.Context().Err() != nil {
		return cmd.Context().Err()
	}
	if counts["success"] != len(names) {
		return fmt.Errorf("batch command did not succeed on %d of %d connections", len(names)-counts["success"], len(names))
	}
	return nil
}

func (a *app) execHost(ctx context.Context, name string, c config.Connection, command string, timeout time.Duration, output *execOutput, stdout, stderr io.Writer) (result execResult) {
	result = execResult{name: name, status: "failed", exit: -1}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	out := &execPrefixWriter{output: output, writer: stdout, prefix: "[" + name + "] "}
	errOut := &execPrefixWriter{output: output, writer: stderr, prefix: "[" + name + "] "}
	var err error
	defer func() {
		err = errors.Join(err, out.flush(), errOut.flush())
		result.err = err
		if ctx.Err() != nil {
			result.status = "canceled"
			result.err = errors.Join(err, ctx.Err())
			return
		}
		if err == nil {
			result.status = "success"
			result.exit = 0
			return
		}
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			result.exit = exit.ExitStatus()
		}
	}()
	// An empty nonterminal input prevents prompts on every hop while preserving
	// explicit --trust-new-host and the existing credential/sudo callbacks.
	worker := &cobra.Command{}
	worker.SetContext(ctx)
	worker.SetIn(strings.NewReader(""))
	worker.SetOut(out)
	worker.SetErr(errOut)
	client, err := a.dialSSH(ctx, worker, name, c, a.connectionSSHOptions(worker, name, c, true), true)
	if err != nil {
		return result
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	err = client.Exec(command, nil, out, errOut)
	return result
}

// All streams share a lock, including when stdout and stderr use one writer.
// A write failure cancels active SSH sessions so streaming commands can exit.
type execOutput struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	err    error
}

type execPrefixWriter struct {
	output  *execOutput
	writer  io.Writer
	prefix  string
	pending []byte
}

const maxExecLine = 32 * 1024

func (w *execPrefixWriter) emit() error {
	if w.output.err != nil {
		return w.output.err
	}
	line := make([]byte, 0, len(w.prefix)+len(w.pending)+1)
	line = append(line, w.prefix...)
	line = append(line, w.pending...)
	line = append(line, '\n')
	n, err := w.writer.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	w.pending = w.pending[:0]
	if err != nil {
		w.output.err = err
		w.output.cancel()
	}
	return err
}

func (w *execPrefixWriter) Write(data []byte) (int, error) {
	w.output.mu.Lock()
	defer w.output.mu.Unlock()
	if w.output.err != nil {
		return 0, w.output.err
	}
	total := len(data)
	for len(data) > 0 {
		// Keep a full fragment until the next byte arrives so a newline exactly
		// at the limit does not turn into an extra empty prefixed line.
		if len(w.pending) == maxExecLine {
			if data[0] == '\n' {
				data = data[1:]
			}
			if err := w.emit(); err != nil {
				return total - len(data), err
			}
			continue
		}
		take := min(len(data), maxExecLine-len(w.pending))
		newline := bytes.IndexByte(data[:take], '\n')
		if newline >= 0 {
			take = newline
		}
		w.pending = append(w.pending, data[:take]...)
		data = data[take:]
		if newline >= 0 {
			data = data[1:]
		}
		if newline >= 0 {
			if err := w.emit(); err != nil {
				return total - len(data), err
			}
		}
	}
	return total, nil
}

func (w *execPrefixWriter) flush() error {
	w.output.mu.Lock()
	defer w.output.mu.Unlock()
	if len(w.pending) == 0 {
		return nil
	}
	return w.emit()
}
