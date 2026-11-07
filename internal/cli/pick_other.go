//go:build !darwin && !linux

package cli

import (
	"context"
	"fmt"
	"os"
)

func selectConnection(_ context.Context, _, _ *os.File, _ *connectionPicker) (string, error) {
	return "", fmt.Errorf("interactive connection selection currently requires macOS or Linux")
}
