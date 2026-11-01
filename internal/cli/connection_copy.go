package cli

import (
	"fmt"

	"github.com/benenen/ah/internal/config"
	"github.com/spf13/cobra"
)

// connectionCopyCommand rebinds encrypted credentials inside the config update
// lock, so an error leaves both aliases unchanged.
func (a *app) connectionCopyCommand(move bool) *cobra.Command {
	name, description := "clone", "Clone a connection under a new name"
	if move {
		name, description = "rename", "Rename a connection"
	}
	return &cobra.Command{Use: name + " OLD NEW", Short: description, Args: cobra.ExactArgs(2), ValidArgsFunction: a.completeNames, RunE: func(cmd *cobra.Command, args []string) error {
		old, next := args[0], args[1]
		if err := config.ValidateName(next); err != nil {
			return err
		}
		s, err := a.store()
		if err != nil {
			return err
		}
		return s.Update(cmd.Context(), func(entries map[string]config.Connection) error {
			c, ok := entries[old]
			if !ok {
				return fmt.Errorf("connection %q does not exist", old)
			}
			if _, ok := entries[next]; ok {
				return fmt.Errorf("connection %q already exists", next)
			}
			if c.Password != "" || c.SudoPassword != "" {
				secrets, err := a.passwordStore()
				if err != nil {
					return err
				}
				rebind := func(value, suffix string) (string, error) {
					if value == "" {
						return "", nil
					}
					plain, err := secrets.Decrypt(old+suffix, value)
					if err != nil && suffix != "" {
						plain, err = secrets.Decrypt(old, value)
					}
					if err != nil {
						return "", err
					}
					defer clear(plain)
					return secrets.Encrypt(next+suffix, plain)
				}
				c.Password, err = rebind(c.Password, "")
				if err != nil {
					return err
				}
				c.SudoPassword, err = rebind(c.SudoPassword, "/sudo")
				if err != nil {
					return err
				}
			}
			entries[next] = c
			if move {
				delete(entries, old)
				for alias, dependent := range entries {
					if dependent.Jump == old {
						dependent.Jump = next
						entries[alias] = dependent
					}
				}
			}
			return nil
		})
	}}
}
