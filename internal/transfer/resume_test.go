package transfer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

func TestResumeInterruptedCopy(t *testing.T) {
	for _, mode := range []string{"local", "upload", "download", "remote-remote"} {
		t.Run(mode, func(t *testing.T) {
			var sc, tc *sftp.Client
			if mode == "download" || mode == "remote-remote" {
				sc = localSFTP(t)
			}
			if mode == "upload" || mode == "remote-remote" {
				tc = localSFTP(t)
			}
			dir := t.TempDir()
			source := filepath.Join(dir, "source")
			target := filepath.Join(dir, "target")
			data := bytes.Repeat([]byte("resume-content-"), 100000)
			if err := os.WriteFile(source, data, 0640); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			_, err := Copy(ctx, sc, tc, source, target, Options{Resume: true, Progress: func(n, total int64) {
				if n >= 256*1024 {
					cancel()
				}
			}})
			if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "partial retained") {
				t.Fatal(err)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("partial was published", err)
			}
			parts, err := filepath.Glob(filepath.Join(dir, ".ah-resume-*"))
			if err != nil || len(parts) != 1 {
				t.Fatal(parts, err)
			}
			partial, err := os.ReadFile(parts[0])
			if err != nil || len(partial) == 0 || len(partial) >= len(data) {
				t.Fatal("no partial payload", len(partial), err)
			}
			// Preview reads the reusable prefix without creating another staging file.
			var planned []PlanEntry
			if err := Plan(context.Background(), nil, nil, source, target, false, TreeOptions{Options: Options{Resume: true}}, func(e PlanEntry) error { planned = append(planned, e); return nil }); err != nil {
				t.Fatal(err)
			}
			if len(planned) != 2 || planned[0].Action != "resume-prefix" {
				t.Fatal(planned)
			}
			afterPlan, e := filepath.Glob(filepath.Join(dir, ".ah-resume-*"))
			if e != nil || len(afterPlan) != 1 {
				t.Fatal("preview wrote staging files", afterPlan, e)
			}
			// Cancellation owns and closes SFTP clients, so retry opens new sessions.
			if sc != nil {
				sc = localSFTP(t)
			}
			if tc != nil {
				tc = localSFTP(t)
			}
			n, err := Copy(context.Background(), sc, tc, source, target, Options{Resume: true})
			if err != nil || n != int64(len(data)) {
				t.Fatal(n, err)
			}
			got, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatal("resumed content differs", err)
			}
			gotPartial, err := os.ReadFile(parts[0])
			if err != nil || !bytes.Equal(partial, gotPartial) {
				t.Fatal("retry modified shared partial", err)
			}
			info, err := os.Stat(target)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatal("wrong final permissions", err)
			}
		})
	}
}

func TestResumeRejectsCorruptPartialAndChangedSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	data := bytes.Repeat([]byte("a"), 1024*1024)
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Copy(ctx, nil, nil, source, target, Options{Resume: true, Progress: func(n, total int64) {
		if n >= 256*1024 {
			cancel()
		}
	}})
	if err == nil {
		t.Fatal("interruption succeeded")
	}
	parts, err := filepath.Glob(filepath.Join(dir, ".ah-resume-*"))
	if err != nil || len(parts) != 1 {
		t.Fatal(parts, err)
	}
	partial, err := os.OpenFile(parts[0], os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.WriteAt([]byte("tampered"), 0); err != nil {
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Copy(context.Background(), nil, nil, source, target, Options{Resume: true})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal("corrupt partial accepted", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("corrupt partial published", err)
	}
	// Different source contents select a different fingerprint and cannot reuse it.
	data[0] = 'b'
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Copy(context.Background(), nil, nil, source, target, Options{Resume: true}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("old partial reused for changed source", err)
	}
}

func TestResumeDetectsMutationDuringTransfer(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(source, bytes.Repeat([]byte("a"), 1024*1024), 0600); err != nil {
		t.Fatal(err)
	}
	changed := false
	_, err := Copy(context.Background(), nil, nil, source, target, Options{Resume: true, Progress: func(n, total int64) {
		if !changed && n > 0 {
			changed = true
			f, e := os.OpenFile(source, os.O_WRONLY, 0600)
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.WriteAt([]byte("b"), 900000)
			if e != nil {
				t.Fatal(e)
			}
			if e := f.Close(); e != nil {
				t.Fatal(e)
			}
		}
	}})
	if err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatal("source mutation accepted", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("mutated data published", err)
	}
}
