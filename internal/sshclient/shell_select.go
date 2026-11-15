package sshclient

import (
	"strings"

	"github.com/benenen/ah/internal/config"
)

// Platform defaults when neither --shell nor the connection chooses one.
const (
	windowsDefaultShell = "cmd.exe"
	linuxDefaultShell   = "bash"
	darwinDefaultShell  = "zsh"
)

// interactiveCommand returns the exec command that starts the chosen shell, or
// "" to request the server's own shell. Windows servers are recognized by the
// OpenSSH_for_Windows version banner; others are treated as POSIX, where the
// platform is read from uname on the remote side so no extra round trip is
// needed. A missing shell falls back to the login shell instead of failing.
func interactiveCommand(shell string, windows bool) string {
	if shell == config.DefaultShell {
		return ""
	}
	if windows {
		if shell == "" {
			return windowsDefaultShell
		}
		return shell
	}
	// The user's login shell runs this command and may not be POSIX (fish), so
	// hand the selection to /bin/sh. ValidateShell keeps shell free of quotes
	// and metacharacters.
	pick := `case "$(uname -s)" in Darwin) s=` + darwinDefaultShell + `;; *) s=` + linuxDefaultShell + `;; esac; `
	missing := ``
	if shell != "" {
		pick = `s=` + shell + `; `
		missing = `printf "ah: shell %s not found, starting the login shell\n" "$s" >&2; `
	}
	return `exec /bin/sh -c '` + pick + `if command -v "$s" >/dev/null 2>&1; then exec "$s" -l; fi; ` + missing + `exec "${SHELL:-/bin/sh}" -l'`
}

func isWindowsServer(version []byte) bool {
	return strings.Contains(string(version), "Windows")
}
