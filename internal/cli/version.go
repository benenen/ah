package cli

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// Build metadata for released binaries, injected with -ldflags -X at build time
// (see .github/workflows/release.yml). A plain go build leaves the version at
// "dev" and the rest empty, and the version command then omits them.
var (
	version   = "dev"
	commit    = ""
	buildDate = ""
)

func versionCommand() *cobra.Command {
	return &cobra.Command{Use: "version", Aliases: []string{"v"}, Short: "Print the ah version and build information", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), versionLine())
		return err
	}}
}

func versionLine() string {
	details := make([]string, 0, 3)
	if commit != "" {
		details = append(details, "commit "+commit)
	}
	if buildDate != "" {
		details = append(details, "built "+buildDate)
	}
	details = append(details, runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH)
	return fmt.Sprintf("ah %s (%s)", version, strings.Join(details, ", "))
}
