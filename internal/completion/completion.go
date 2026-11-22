// Package completion queries remote path candidates without executing a shell.
package completion

import (
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"unicode"

	"github.com/benenen/ah/internal/transfer"
	"github.com/pkg/sftp"
)

// RemotePaths preserves the user's path spelling so shell quoting is left to Cobra.
// The caller supplies a noninteractive connection with a bounded lifetime.
func RemotePaths(client *sftp.Client, partial string) ([]string, error) {
	if strings.ContainsFunc(partial, unicode.IsControl) {
		return nil, fmt.Errorf("control characters in path")
	}
	if partial == "~" {
		return []string{"~/"}, nil
	}
	displayDir, prefix := path.Split(partial)
	dir := displayDir
	if dir == "" {
		dir = "."
	}
	resolved, err := transfer.ResolvePath(client, dir)
	if err != nil {
		return nil, err
	}
	// Reject ~user even before it contains a slash.
	if strings.HasPrefix(partial, "~") && !strings.HasPrefix(partial, "~/") {
		return nil, fmt.Errorf("use ~/ or an absolute path")
	}
	entries, err := client.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("read remote directory: %w", err)
	}
	candidates := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || strings.ContainsFunc(name, unicode.IsControl) {
			continue
		}
		candidate := displayDir + name
		isDir := entry.IsDir()
		if entry.Mode()&os.ModeSymlink != 0 {
			target, statErr := client.Stat(path.Join(resolved, name))
			if statErr == nil {
				isDir = target.IsDir()
			} else {
				var statusErr *sftp.StatusError
				if !os.IsNotExist(statErr) && !os.IsPermission(statErr) && !errors.As(statErr, &statusErr) {
					return nil, fmt.Errorf("stat remote path: %w", statErr)
				}
			}
		}
		if isDir {
			candidate += "/"
		}
		candidates = append(candidates, candidate)
	}
	sort.Strings(candidates)
	return candidates, nil
}
