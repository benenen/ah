// Package cli defines the ah command line interface.
package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/spf13/cobra"
)

type app struct {
	configPath   string
	historyPath  string
	keyPath      string
	knownHosts   string
	term         string
	timeout      time.Duration
	trustNewHost bool
}

func New() *cobra.Command {
	a := &app{}
	root := &cobra.Command{Use: "ah", Short: "Manage SSH connections and copy files between them", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&a.configPath, "config", "", "connection TOML file (default: user config directory/ah/connections.toml)")
	root.PersistentFlags().StringVar(&a.historyPath, "history-file", "", "copy history SQLite file (default: user config directory/ah/history.db)")
	root.PersistentFlags().StringVar(&a.keyPath, "key-file", "", "password encryption key file (default: user config directory/ah/master.key)")
	root.PersistentFlags().StringVar(&a.knownHosts, "known-hosts", "", "SSH known_hosts file (default: ~/.ssh/known_hosts)")
	root.PersistentFlags().StringVar(&a.term, "term", "", "TERM for interactive sessions, overriding the connection's saved value and $TERM")
	root.PersistentFlags().DurationVar(&a.timeout, "timeout", 10*time.Second, "SSH connection and handshake timeout")
	root.PersistentFlags().BoolVar(&a.trustNewHost, "trust-new-host", false, "explicitly trust and save previously unknown host keys (changed keys still fail)")
	root.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		if a.timeout <= 0 {
			return fmt.Errorf("--timeout must be positive")
		}
		return nil
	}
	root.AddCommand(a.listCommand(), a.newCommand(), a.editCommand(), a.removeCommand(), a.connectCommand(), a.forwardCommand(), a.copyCommand(), a.historyCommand(), completionCommand())
	for _, command := range root.Commands() {
		if len(command.Aliases) > 0 {
			command.Short += " (alias: " + strings.Join(command.Aliases, ", ") + ")"
		}
	}
	root.CompletionOptions.DisableDefaultCmd = true
	return root
}

func (a *app) store() (config.Store, error) {
	p := a.configPath
	if p == "" {
		var err error
		p, err = config.DefaultPath()
		if err != nil {
			return config.Store{}, err
		}
	}
	return config.Store{Path: p}, nil
}
func (a *app) connections() (map[string]config.Connection, error) {
	s, err := a.store()
	if err != nil {
		return nil, err
	}
	return s.Load()
}
func (a *app) connection(name string) (config.Connection, error) {
	m, err := a.connections()
	if err != nil {
		return config.Connection{}, err
	}
	c, ok := m[name]
	if !ok {
		return c, fmt.Errorf("connection %q does not exist", name)
	}
	return c, nil
}
func sortedNames(m map[string]config.Connection) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
