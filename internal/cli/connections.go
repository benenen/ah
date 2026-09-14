package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/benenen/ah/internal/config"
	"github.com/spf13/cobra"
)

func (a *app) listCommand() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List saved connections", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		entries, err := a.connections()
		if err != nil {
			return err
		}
		out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		if _, err = fmt.Fprintln(out, "NAME\tHOST\tPORT\tUSER"); err != nil {
			return err
		}
		for _, name := range sortedNames(entries) {
			c := entries[name]
			if _, err = fmt.Fprintf(out, "%s\t%s\t%d\t%s\n", name, c.Host, c.Port, c.User); err != nil {
				return err
			}
		}
		return out.Flush()
	}}
}
func connectionFlags(cmd *cobra.Command, c *config.Connection) {
	cmd.Flags().StringVar(&c.Host, "host", "", "hostname or IP address")
	cmd.Flags().IntVarP(&c.Port, "port", "p", 22, "SSH port")
	cmd.Flags().StringVarP(&c.User, "user", "u", "", "SSH username")
	cmd.Flags().StringVarP(&c.IdentityFile, "identity-file", "i", "", "private key path (empty to use agent/default keys)")
	cmd.Flags().StringVar(&c.Proxy, "proxy", "", "SOCKS5 proxy URL, e.g. socks5://127.0.0.1:1080")
}
func (a *app) newCommand() *cobra.Command {
	var c config.Connection
	var password, sudoPassword bool
	cmd := &cobra.Command{Use: "new NAME --host HOST --user USER", Short: "Save a new connection", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.ValidateName(args[0]); err != nil {
			return err
		}
		if err := c.Validate(); err != nil {
			return err
		}
		s, err := a.store()
		if err != nil {
			return err
		}
		existing, err := s.Load()
		if err != nil {
			return err
		}
		if _, ok := existing[args[0]]; ok {
			return fmt.Errorf("connection %q already exists", args[0])
		}
		if password {
			c.Password, err = a.promptPassword(cmd, args[0])
			if err != nil {
				return err
			}
		}
		if sudoPassword {
			c.SudoPassword, err = a.promptSecret(cmd, args[0], "sudo password: ")
			if err != nil {
				return err
			}
		}
		if err = s.Update(cmd.Context(), func(m map[string]config.Connection) error {
			if _, ok := m[args[0]]; ok {
				return fmt.Errorf("connection %q already exists", args[0])
			}
			m[args[0]] = c
			return nil
		}); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created %s\n", args[0])
		return err
	}}
	connectionFlags(cmd, &c)
	cmd.Flags().BoolVar(&password, "password", false, "prompt for a password and save it encrypted in TOML")
	cmd.Flags().BoolVar(&c.Sudo, "sudo", false, "escalate remote commands and shells to root via sudo")
	cmd.Flags().BoolVar(&sudoPassword, "sudo-password", false, "prompt for a separate sudo password (defaults to the SSH password)")
	return cmd
}
func (a *app) editCommand() *cobra.Command {
	var changes config.Connection
	var password, clearPassword, sudo, noSudo, sudoPassword, clearSudoPassword, clearProxy bool
	cmd := &cobra.Command{Use: "edit NAME [flags]", Short: "Update only the supplied connection fields", Args: cobra.ExactArgs(1), ValidArgsFunction: a.completeNames, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := a.store()
		if err != nil {
			return err
		}
		existing, err := s.Load()
		if err != nil {
			return err
		}
		if _, ok := existing[args[0]]; !ok {
			return fmt.Errorf("connection %q does not exist", args[0])
		}
		encrypted := ""
		if password {
			encrypted, err = a.promptPassword(cmd, args[0])
			if err != nil {
				return err
			}
		}
		encryptedSudo := ""
		if sudoPassword {
			encryptedSudo, err = a.promptSecret(cmd, args[0], "sudo password: ")
			if err != nil {
				return err
			}
		}
		if err = s.Update(cmd.Context(), func(m map[string]config.Connection) error {
			c, ok := m[args[0]]
			if !ok {
				return fmt.Errorf("connection %q does not exist", args[0])
			}
			if cmd.Flags().Changed("host") {
				c.Host = changes.Host
			}
			if cmd.Flags().Changed("user") {
				c.User = changes.User
			}
			if cmd.Flags().Changed("port") {
				c.Port = changes.Port
			}
			if cmd.Flags().Changed("identity-file") {
				c.IdentityFile = changes.IdentityFile
			}
			if cmd.Flags().Changed("proxy") {
				c.Proxy = changes.Proxy
			}
			if clearProxy {
				c.Proxy = ""
			}
			if password {
				c.Password = encrypted
			}
			if clearPassword {
				c.Password = ""
			}
			if sudo {
				c.Sudo = true
			}
			if noSudo {
				c.Sudo = false
			}
			if sudoPassword {
				c.SudoPassword = encryptedSudo
			}
			if clearSudoPassword {
				c.SudoPassword = ""
			}
			if err := c.Validate(); err != nil {
				return err
			}
			m[args[0]] = c
			return nil
		}); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Updated %s\n", args[0])
		return err
	}}
	connectionFlags(cmd, &changes)
	cmd.Flags().BoolVar(&password, "password", false, "prompt for a replacement password and save it encrypted")
	cmd.Flags().BoolVar(&clearPassword, "clear-password", false, "remove the saved encrypted password")
	cmd.Flags().BoolVar(&sudo, "sudo", false, "escalate remote commands and shells to root via sudo")
	cmd.Flags().BoolVar(&noSudo, "no-sudo", false, "disable sudo escalation for this connection")
	cmd.Flags().BoolVar(&sudoPassword, "sudo-password", false, "prompt for a separate sudo password and save it encrypted")
	cmd.Flags().BoolVar(&clearSudoPassword, "clear-sudo-password", false, "remove the saved sudo password (fall back to the SSH password)")
	cmd.Flags().BoolVar(&clearProxy, "clear-proxy", false, "remove the saved SOCKS5 proxy")
	cmd.MarkFlagsMutuallyExclusive("password", "clear-password")
	cmd.MarkFlagsMutuallyExclusive("sudo", "no-sudo")
	cmd.MarkFlagsMutuallyExclusive("sudo-password", "clear-sudo-password")
	cmd.MarkFlagsMutuallyExclusive("proxy", "clear-proxy")
	return cmd
}
func (a *app) removeCommand() *cobra.Command {
	return &cobra.Command{Use: "rm NAME", Short: "Remove a saved connection", Args: cobra.ExactArgs(1), ValidArgsFunction: a.completeNames, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := a.store()
		if err != nil {
			return err
		}
		if err = s.Update(cmd.Context(), func(m map[string]config.Connection) error {
			if _, ok := m[args[0]]; !ok {
				return fmt.Errorf("connection %q does not exist", args[0])
			}
			delete(m, args[0])
			return nil
		}); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", args[0])
		return err
	}}
}
