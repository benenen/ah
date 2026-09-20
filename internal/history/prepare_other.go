//go:build !unix

package history

import (
	"errors"
	"fmt"
	"os"
)

// prepareFile creates path with private permissions if needed. Platforms without
// O_NOFOLLOW cannot refuse a symlink at the history path, so the regular-file
// check below only rejects devices and directories.
func prepareFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open history file: %w", err)
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("history database must be a regular file")
	}
	if err == nil {
		err = f.Chmod(0600)
	}
	return errors.Join(err, f.Close())
}
