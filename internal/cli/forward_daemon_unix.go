//go:build linux || darwin

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/benenen/ah/internal/forward"
	"github.com/spf13/cobra"
)

func (a *app) startForwardDaemon(cmd *cobra.Command, record forward.Record) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	configPath, err := filepath.Abs(store.Path)
	if err != nil {
		return err
	}
	args := []string{"--config", configPath, "--timeout", a.timeout.String()}
	for _, option := range [][2]string{{"--known-hosts", a.knownHosts}, {"--key-file", a.keyPath}} {
		if option[1] != "" {
			args = append(args, option[0], option[1])
		}
	}
	if a.trustNewHost {
		args = append(args, "--trust-new-host")
	}
	args = append(args, "forward", record.Name, record.Listen, record.Target, "--forward-worker", record.ID)
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(root, "ah", "forwards", record.ID+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	child := exec.Command(executable, args...)
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Stderr = log
	// Nil stdin/stdout become /dev/null; the daemon cannot prompt or retain
	// the caller's terminal/pipes. FD 3 is only for startup acknowledgment.
	child.ExtraFiles = []*os.File{write}
	if err = child.Start(); err != nil {
		return err
	}
	_ = write.Close()
	started := false
	defer func() {
		if !started {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	result := make(chan startupResult, 1)
	go func() {
		var status startupResult
		if err := json.NewDecoder(read).Decode(&status); err != nil {
			status.Error = fmt.Sprintf("worker startup failed: %v (log: %s)", err, log.Name())
		}
		result <- status
	}()
	ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout+5*time.Second)
	defer cancel()
	select {
	case status := <-result:
		if status.Error != "" {
			return fmt.Errorf("start forward: %s", status.Error)
		}
		if status.ID != record.ID {
			return fmt.Errorf("forward startup ID mismatch")
		}
		started = true
		return child.Process.Release()
	case <-ctx.Done():
		return fmt.Errorf("start forward: %w", ctx.Err())
	}
}
