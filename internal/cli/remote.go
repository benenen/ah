package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/benenen/ah/internal/sshclient"
	"github.com/benenen/ah/internal/transfer"
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
	return &cobra.Command{Use: "connect NAME", Short: "Open an interactive SSH shell", Args: cobra.ExactArgs(1), ValidArgsFunction: a.completeNames, RunE: func(cmd *cobra.Command, args []string) (err error) {
		input, ok := cmd.InOrStdin().(*os.File)
		if !ok {
			return fmt.Errorf("connect requires terminal input")
		}
		c, err := a.connection(args[0])
		if err != nil {
			return err
		}
		client, err := sshclient.Dial(cmd.Context(), c, a.sshOptions(cmd))
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, client.Close()) }()
		return client.Shell(input, cmd.OutOrStdout(), cmd.ErrOrStderr())
	}}
}
func (a *app) copyCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{Use: "cp SOURCE:PATH DESTINATION:PATH", Short: "Copy a regular file between two saved connections using SFTP", Args: cobra.ExactArgs(2), ValidArgsFunction: a.completeRemote, RunE: func(cmd *cobra.Command, args []string) (err error) {
		source, err := transfer.ParseEndpoint(args[0])
		if err != nil {
			return err
		}
		target, err := transfer.ParseEndpoint(args[1])
		if err != nil {
			return err
		}
		sourceConn, err := a.connection(source.Name)
		if err != nil {
			return err
		}
		targetConn, err := a.connection(target.Name)
		if err != nil {
			return err
		}
		src, err := sshclient.Dial(cmd.Context(), sourceConn, a.sshOptions(cmd))
		if err != nil {
			return fmt.Errorf("source %s: %w", source.Name, err)
		}
		defer src.Close()
		dst, err := sshclient.Dial(cmd.Context(), targetConn, a.sshOptions(cmd))
		if err != nil {
			return fmt.Errorf("destination %s: %w", target.Name, err)
		}
		defer dst.Close()
		srcSFTP, err := src.SFTP()
		if err != nil {
			return fmt.Errorf("source SFTP: %w", err)
		}
		defer srcSFTP.Close()
		dstSFTP, err := dst.SFTP()
		if err != nil {
			return fmt.Errorf("destination SFTP: %w", err)
		}
		defer dstSFTP.Close()
		n, err := transfer.Copy(cmd.Context(), srcSFTP, dstSFTP, source.Path, target.Path, force)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Copied %d bytes: %s -> %s\n", n, args[0], args[1])
		return err
	}}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "atomically replace an existing destination file")
	return cmd
}
