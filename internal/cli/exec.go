package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

const maxScriptBytes = 1 << 20

type execResult struct {
	name, status   string
	exit           int // -1 when no remote exit status was received
	stdout, stderr string
	err            error
}

type execJSONResult struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error,omitempty"`
}

func (a *app) execCommand() *cobra.Command {
	var tags []string
	var jobs int
	var timeout time.Duration
	var dryRun, jsonOutput bool
	var scriptPath string
	cmd := &cobra.Command{
		Use:   "exec --tag TAG [--jobs N] [--script FILE | -- COMMAND [ARG...]]",
		Short: "Execute a remote command or script on connections carrying every selected tag",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var command string
			var scriptData []byte
			if scriptPath != "" {
				if len(args) > 0 || cmd.ArgsLenAtDash() != -1 {
					return fmt.Errorf("--script cannot be combined with a remote command")
				}
				info, err := os.Stat(scriptPath)
				if err != nil {
					return fmt.Errorf("read script %s: %w", scriptPath, err)
				}
				if info.IsDir() {
					return fmt.Errorf("script path must be a regular file: %s", scriptPath)
				}
				if info.Size() == 0 {
					return fmt.Errorf("script file must not be empty: %s", scriptPath)
				}
				if info.Size() > maxScriptBytes {
					return fmt.Errorf("script file exceeds %d bytes limit: %s", maxScriptBytes, scriptPath)
				}
				scriptData, err = os.ReadFile(scriptPath)
				if err != nil {
					return fmt.Errorf("read script %s: %w", scriptPath, err)
				}
				command = "/bin/sh -s"
			} else {
				if len(args) == 0 {
					return fmt.Errorf("exec requires --script FILE or -- COMMAND")
				}
				if cmd.ArgsLenAtDash() != 0 {
					return fmt.Errorf("exec requires -- before the remote command")
				}
				command = strings.Join(args, " ")
				if strings.TrimSpace(command) == "" {
					return fmt.Errorf("remote command must not be empty")
				}
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
				if jsonOutput {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetEscapeHTML(false)
					enc.SetIndent("", "  ")
					return enc.Encode(names)
				}
				for _, name := range names {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), name); err != nil {
						return err
					}
				}
				return cmd.Context().Err()
			}
			return a.execBatch(cmd, names, entries, command, scriptData, jobs, timeout, jsonOutput)
		},
	}
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "select connections carrying every tag (repeatable or comma-separated, required)")
	cmd.Flags().IntVar(&jobs, "jobs", 4, "maximum concurrent hosts (1-64)")
	cmd.Flags().DurationVar(&timeout, "exec-timeout", 0, "maximum time per host, including connection establishment (0 disables)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list selected connection names without connecting or executing")
	cmd.Flags().StringVar(&scriptPath, "script", "", "read local script file and execute it via /bin/sh -s on each host")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print structured JSON results after all hosts finish")
	_ = cmd.RegisterFlagCompletionFunc("tag", a.completeTags(false))
	return cmd
}

func (a *app) execBatch(cmd *cobra.Command, names []string, entries map[string]config.Connection, command string, scriptData []byte, jobs int, timeout time.Duration, jsonOutput bool) error {
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
				results[i] = a.execHost(ctx, names[i], entries[names[i]], command, scriptData, timeout, output, cmd.OutOrStdout(), cmd.ErrOrStderr(), jsonOutput)
			}
		})
	}
	workers.Wait()
	counts := map[string]int{}
	for _, result := range results {
		counts[result.status]++
	}
	if jsonOutput {
		payload := make([]execJSONResult, 0, len(results))
		for _, r := range results {
			var exitPtr *int
			if r.exit >= 0 {
				v := r.exit
				exitPtr = &v
			}
			msg := ""
			if r.err != nil {
				msg = r.err.Error()
			}
			payload = append(payload, execJSONResult{
				Name:     r.name,
				Status:   r.status,
				ExitCode: exitPtr,
				Stdout:   r.stdout,
				Stderr:   r.stderr,
				Error:    msg,
			})
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			return err
		}
	} else {
		table := tabwriter.NewWriter(cmd.ErrOrStderr(), 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(table, "NAME\tSTATUS\tEXIT\tERROR"); err != nil {
			return err
		}
		for _, result := range results {
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

type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if remain := b.limit - b.buf.Len(); remain > 0 {
		take := min(len(p), remain)
		b.buf.Write(p[:take])
	}
	return len(p), nil
}

func (a *app) execHost(ctx context.Context, name string, c config.Connection, command string, scriptData []byte, timeout time.Duration, output *execOutput, stdout, stderr io.Writer, capture bool) (result execResult) {
	result = execResult{name: name, status: "failed", exit: -1}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var outStream, errStream io.Writer
	var flushStreams = func() error { return nil }
	var outCap, errCap *boundedBuffer
	if capture {
		outCap = &boundedBuffer{limit: maxScriptBytes}
		errCap = &boundedBuffer{limit: maxScriptBytes}
		outStream, errStream = outCap, errCap
	} else {
		out := &execPrefixWriter{output: output, writer: stdout, prefix: "[" + name + "] "}
		errOut := &execPrefixWriter{output: output, writer: stderr, prefix: "[" + name + "] "}
		outStream, errStream = out, errOut
		flushStreams = func() error { return errors.Join(out.flush(), errOut.flush()) }
	}
	var err error
	defer func() {
		err = errors.Join(err, flushStreams())
		if capture {
			result.stdout = outCap.buf.String()
			result.stderr = errCap.buf.String()
		}
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
	worker.SetOut(outStream)
	worker.SetErr(errStream)
	client, err := a.dialSSH(ctx, worker, name, c, a.connectionSSHOptions(worker, name, c, true), true)
	if err != nil {
		return result
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	if len(scriptData) > 0 {
		err = client.ExecWithInput(command, bytes.NewReader(scriptData), outStream, errStream)
	} else {
		err = client.Exec(command, nil, outStream, errStream)
	}
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
