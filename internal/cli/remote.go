package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/benenen/ah/internal/sshclient"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (a *app) sshOptions(cmd *cobra.Command) sshclient.Options {
	opts := sshclient.Options{KnownHosts: a.knownHosts, Timeout: a.timeout}
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
	return &cobra.Command{Use: "connect NAME", Aliases: []string{"c"}, Short: "Open an interactive SSH shell", Args: cobra.ExactArgs(1), ValidArgsFunction: a.completeNames, RunE: func(cmd *cobra.Command, args []string) (err error) {
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
		return client.Shell(input, cmd.OutOrStdout(), cmd.ErrOrStderr())
	}}
}
