//go:build !linux && !darwin

package cli

import (
	"fmt"

	"github.com/benenen/ah/internal/forward"
	"github.com/spf13/cobra"
)

func (a *app) startForwardDaemon(_ *cobra.Command, _ forward.Record) error {
	return fmt.Errorf("background forwarding is supported on Linux and macOS")
}
