//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

type configPromptWriter struct {
	output  *bytes.Buffer
	prompts chan struct{}
}

func (w *configPromptWriter) Write(data []byte) (int, error) {
	n, err := w.output.Write(data)
	if strings.Contains(string(data), "passphrase:") {
		w.prompts <- struct{}{}
	}
	return n, err
}

func configWithTerminal(t *testing.T, secrets []string, args ...string) (string, error) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	writer := &configPromptWriter{output: &output, prompts: make(chan struct{}, 2)}
	cmd := New()
	cmd.SetArgs(args)
	cmd.SetIn(slave)
	cmd.SetOut(&output)
	cmd.SetErr(writer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, secret := range secrets {
			select {
			case <-writer.prompts:
				_, _ = master.Write([]byte(secret + "\r"))
			case <-ctx.Done():
				return
			}
		}
	}()
	err = cmd.ExecuteContext(ctx)
	cancel()
	<-done
	return output.String(), err
}

func TestConfigInteractiveExportAndImport(t *testing.T) {
	dir := t.TempDir()
	source, key := seedPortableConfig(t, dir)
	archive := filepath.Join(dir, "backup.ah")
	args := []string{"--config", source.Path, "--key-file", key.Path, "config", "export", archive, "--with-secrets"}
	if _, err := execute(t, args...); err == nil {
		t.Fatal("noninteractive export prompted")
	}
	out, err := configWithTerminal(t, []string{"fixture-passphrase", "different-passphrase"}, args...)
	if err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatal("mismatched passphrase accepted", err)
	}
	if strings.Contains(out, "fixture-passphrase") {
		t.Fatal("passphrase echoed")
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("mismatched passphrase wrote export")
	}
	if out, err := configWithTerminal(t, []string{"fixture-passphrase", "fixture-passphrase"}, args...); err != nil {
		t.Fatal(out, err)
	}
	target, targetKey := filepath.Join(dir, "target.toml"), filepath.Join(dir, "target.key")
	out, err = configWithTerminal(t, []string{"fixture-passphrase"}, "--config", target, "--key-file", targetKey, "config", "import", archive)
	if err != nil || !strings.Contains(out, "Imported 2 connections") {
		t.Fatal(out, err)
	}
	if strings.Contains(out, "fixture-passphrase") {
		t.Fatal("import passphrase echoed")
	}
}
