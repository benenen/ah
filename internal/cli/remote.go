package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/benenen/ah/internal/sshclient"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (a *app) sshOptions(cmd *cobra.Command) sshclient.Options {
	opts := sshclient.Options{KnownHosts: a.knownHosts, Timeout: a.timeout, Term: a.term}
	if input, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(input.Fd())) {
		read := func(prompt string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout)
			defer cancel()
			return readSecret(ctx, input, cmd.ErrOrStderr(), prompt)
		}
		opts.Password = func() (string, error) { b, err := read("SSH password: "); defer clear(b); return string(b), err }
		opts.Passphrase = func(p string) ([]byte, error) { return read(fmt.Sprintf("Passphrase for %s: ", p)) }
	}
	if a.trustNewHost {
		opts.TrustHost = func(host, fingerprint string) (bool, error) {
			_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Trusting new host %s: %s\n", host, fingerprint)
			return err == nil, err
		}
	}
	return opts
}
func (a *app) connectCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "connect NAME [COMMAND [ARG...]]", Aliases: []string{"c"}, Short: "Open an SSH shell or execute a remote command", Args: cobra.MinimumNArgs(1), ValidArgsFunction: a.completeNames, RunE: func(cmd *cobra.Command, args []string) (err error) {
		input, ok := cmd.InOrStdin().(*os.File)
		if !ok {
			return fmt.Errorf("connect requires terminal input")
		}
		c, err := a.connection(args[0])
		if err != nil {
			return err
		}
		client, err := sshclient.Dial(cmd.Context(), c, a.connectionSSHOptions(cmd, args[0], c, true))
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, client.Close()) }()
		if len(args) > 1 {
			// OpenSSH sends a space-joined command for the remote shell to parse.
			return client.Exec(strings.Join(args[1:], " "), input, cmd.OutOrStdout(), cmd.ErrOrStderr())
		}
		return client.Shell(input, cmd.OutOrStdout(), cmd.ErrOrStderr())
	}}
	// Options following NAME belong to the remote command, even if they match ah flags.
	cmd.Flags().SetInterspersed(false)
	return cmd
}
