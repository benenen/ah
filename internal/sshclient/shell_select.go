package sshclient

import (
	"encoding/base64"
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

// remoteCommand returns the exec command for a non-interactive command. Windows
// OpenSSH hands commands to its DefaultShell (cmd.exe by default), so a
// connection that chose PowerShell would otherwise find cmd parsing them.
// Other shells keep the server's handling.
func remoteCommand(shell string, windows bool, command string) string {
	if !windows || !isPowerShell(shell) {
		return command
	}
	return powerShellCommand(shell, command)
}

func isPowerShell(shell string) bool {
	name := strings.ToLower(shell[strings.LastIndexAny(shell, `/\`)+1:])
	name = strings.TrimSuffix(name, ".exe")
	return name == "pwsh" || name == "powershell"
}

// powerShellCommand passes the script as base64 inside -Command because cmd.exe
// and Windows argument parsing would otherwise reinterpret quotes, & and %VAR%.
// -EncodedCommand is avoided: endpoint security commonly blocks it, and
// Windows PowerShell 5 then exits with -1 and no output.
//
// The wrapper prints UTF-8 without ANSI styling, flushes formatted output
// through Out-Default before exiting (PowerShell delays table output), and
// exits with the native exit code when the last statement failed.
func powerShellCommand(shell, command string) string {
	script := `$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false); ` +
		`if ($PSStyle) { $PSStyle.OutputRendering = "PlainText" }; $global:ahOk = $false; & {` + "\n" +
		command + "\n" +
		`$global:ahOk = $?` + "\n" +
		`} | Out-Default` + "\n" +
		`if ($global:ahOk) { exit 0 }; if ($LASTEXITCODE) { exit $LASTEXITCODE }; exit 1`
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	return shell + ` -NoLogo -NoProfile -NonInteractive -Command "& ([scriptblock]::Create([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + encoded + `'))))"`
}
