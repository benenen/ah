package cli

import (
	"fmt"
	"strings"

	"github.com/benenen/ah/internal/config"
)

// shellQuote wraps s in single quotes for safe parsing by the remote login
// shell, so the escalated command keeps the caller's original word splitting.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sudoPassword returns the decrypted sudo password for the connection, or nil
// when none is stored. It prefers the dedicated sudo password and otherwise
// reuses the saved SSH password; an empty result means rely on NOPASSWD sudo.
func (a *app) sudoPassword(name string, c config.Connection) ([]byte, error) {
	cipher := c.SudoPassword
	if cipher == "" {
		cipher = c.Password
	}
	if cipher == "" {
		return nil, nil
	}
	store, err := a.passwordStore()
	if err != nil {
		return nil, err
	}
	secret, err := store.Decrypt(name, cipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt sudo password for %s: %w", name, err)
	}
	return secret, nil
}

// sudoExecCommand builds the remote command and stdin prefix that run userCmd
// as root. With a stored password it uses sudo -S and feeds the password as the
// first stdin line; otherwise it relies on passwordless sudo.
func (a *app) sudoExecCommand(name string, c config.Connection, userCmd string) (string, []byte, error) {
	secret, err := a.sudoPassword(name, c)
	if err != nil {
		return "", nil, err
	}
	// Run the original command under a shell so pipes and && keep working as root.
	inner := "/bin/sh -c " + shellQuote(userCmd)
	if len(secret) == 0 {
		return "sudo -n -- " + inner, nil, nil
	}
	prefix := append(secret, '\n')
	return "sudo -S -p '' -- " + inner, prefix, nil
}

// sudoShellCommand builds the remote command and stdin prefix for an escalated
// interactive login. It avoids sudo -S so the terminal (not -S) reads the
// password with echo disabled, and passes an empty prompt to stay quiet.
func (a *app) sudoShellCommand(name string, c config.Connection) (string, []byte, error) {
	secret, err := a.sudoPassword(name, c)
	if err != nil {
		return "", nil, err
	}
	if len(secret) == 0 {
		return "sudo -p '' -i", nil, nil
	}
	return "sudo -p '' -i", append(secret, '\n'), nil
}
