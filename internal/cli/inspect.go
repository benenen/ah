package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/benenen/ah/internal/config"
	"github.com/spf13/cobra"
)

type inspectedProxy struct {
	Address                  string `json:"address"`
	AuthenticationConfigured bool   `json:"authentication_configured"`
}

type inspectedConnection struct {
	Jump                   string           `json:"jump"`
	Name                   string           `json:"name"`
	ConfigFile             string           `json:"config_file"`
	Host                   string           `json:"host"`
	Port                   int              `json:"port"`
	User                   string           `json:"user"`
	IdentityFile           string           `json:"identity_file"`
	PasswordConfigured     bool             `json:"password_configured"`
	Proxies                []inspectedProxy `json:"proxies"`
	Sudo                   bool             `json:"sudo"`
	SudoPasswordConfigured bool             `json:"sudo_password_configured"`
	SudoShell              string           `json:"sudo_shell"`
	SFTPServer             string           `json:"sftp_server"`
	Term                   string           `json:"term"`
	EffectiveTerm          string           `json:"effective_term"`
	Tags                   []string         `json:"tags"`
	Note                   string           `json:"note"`
}

func (a *app) inspectCommand() *cobra.Command {
	return &cobra.Command{
		Use: "inspect NAME", Short: "Show connection configuration as JSON with credentials hidden",
		Args: cobra.ExactArgs(1), ValidArgsFunction: a.completeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connection(args[0])
			if err != nil {
				return err
			}
			store, err := a.store()
			if err != nil {
				return err
			}
			path, err := filepath.Abs(store.Path)
			if err != nil {
				return err
			}
			proxies := make([]inspectedProxy, 0, len(c.ProxyChain()))
			for i, raw := range c.ProxyChain() {
				address, err := config.ProxyAddress(raw)
				if err != nil {
					return fmt.Errorf("proxy hop %d: %w", i+1, err)
				}
				p := inspectedProxy{Address: address}
				if strings.Contains(raw, "://") {
					u, err := url.Parse(raw)
					if err != nil {
						return fmt.Errorf("invalid proxy hop %d", i+1)
					}
					p.Address = u.Scheme + "://" + address
					p.AuthenticationConfigured = u.User != nil
				}
				proxies = append(proxies, p)
			}
			terminal := a.term
			if terminal == "" {
				terminal = c.Term
			}
			if terminal == "" {
				terminal = os.Getenv("TERM")
			}
			if terminal == "" {
				terminal = "xterm-256color"
			}
			// Use an explicit output model so newly added credential fields cannot leak.
			info := inspectedConnection{
				Jump: c.Jump, Name: args[0], ConfigFile: path, Host: c.Host, Port: c.Port, User: c.User,
				IdentityFile: c.IdentityFile, PasswordConfigured: c.Password != "", Proxies: proxies,
				Sudo: c.Sudo, SudoPasswordConfigured: c.SudoPassword != "", SudoShell: c.SudoShell,
				SFTPServer: c.SFTPServer, Term: c.Term, EffectiveTerm: terminal,
				Tags: append([]string{}, c.Tags...), Note: c.Note,
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(info)
		},
	}
}
