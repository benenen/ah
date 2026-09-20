//go:build unix

package history

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// prepareFile creates path with private permissions if needed. O_NOFOLLOW keeps
// a symlink planted at the history path from redirecting the database, and the
// non-blocking flag stops a FIFO there from hanging the open.
func prepareFile(path string) error {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("open history file: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("history database must be a regular file")
	}
	if err == nil {
		err = f.Chmod(0600)
	}
	return errors.Join(err, f.Close())
}
