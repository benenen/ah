//go:build !darwin && !linux

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
)

func readSecret(_ context.Context, _ *os.File, _ io.Writer, _ string) ([]byte, error) {
	return nil, fmt.Errorf("interactive authentication currently requires macOS or Linux; use SSH keys or an agent")
}

func confirmHost(_ context.Context, _ *os.File, _ io.Writer, host, _ string) (bool, error) {
	return false, fmt.Errorf("host key confirmation currently requires macOS or Linux; verify the fingerprint for %s and use --trust-new-host", host)
}
