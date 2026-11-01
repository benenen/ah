package transfer

import (
	"context"
	"github.com/pkg/sftp"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPlanReadOnly(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "sftp"}[remote], func(t *testing.T) {
			var sc, tc *sftp.Client
			if remote {
				sc = localSFTP(t)
				tc = localSFTP(t)
			}
			dir := t.TempDir()
			src := filepath.Join(dir, "src")
			dst := filepath.Join(dir, "dst")
			if err := os.Mkdir(src, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dst, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dst, "src")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string]string{"same": "same", "changed": "new", "added": "added", "skip.log": "skip"} {
				if err := os.WriteFile(filepath.Join(src, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for name, data := range map[string]string{"same": "same", "changed": "old"} {
				if err := os.WriteFile(filepath.Join(target, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			backup := filepath.Join(target, "bak.changed.bak-20260101T000000.000000000Z")
			if err := os.WriteFile(backup, []byte("older"), 0600); err != nil {
				t.Fatal(err)
			}
			snapshot := func() map[string]string {
				m := map[string]string{}
				err := filepath.WalkDir(dst, func(p string, d os.DirEntry, e error) error {
					if e != nil {
						return e
					}
					if !d.IsDir() {
						b, e := os.ReadFile(p)
						if e != nil {
							return e
						}
						m[p] = string(b)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			before := snapshot()
			var entries []PlanEntry
			err := Plan(context.Background(), sc, tc, src, dst, true, TreeOptions{Options: Options{BackupKeep: 1}, Exclude: []string{"*.log"}}, func(e PlanEntry) error { entries = append(entries, e); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, snapshot()) {
				t.Fatal("plan modified target")
			}
			want := []string{"merge", "add", "prune-backup", "backup", "replace", "unchanged", "exclude"}
			var actions []string
			for _, e := range entries {
				actions = append(actions, e.Action)
			}
			if !reflect.DeepEqual(want, actions) {
				t.Fatalf("got %v", actions)
			}
			if entries[1].Target != filepath.Join(target, "added") {
				t.Fatal("wrong cp destination", entries)
			}
			newTarget := filepath.Join(dir, "missing")
			if err := Plan(context.Background(), sc, tc, src, newTarget, true, TreeOptions{}, func(PlanEntry) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(newTarget); !os.IsNotExist(err) {
				t.Fatal("preview created root", err)
			}
		})
	}
}
