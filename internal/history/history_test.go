package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestPersistenceAndLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "history.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Minute)
	want := Record{Source: "a:/source", Destination: "b:/dest", Cwd: "/cwd", ConfigPath: "/config", KnownHosts: "/hosts", KeyPath: "/key", Force: true, StartedAt: start}
	id, err := s.Begin(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, path)
	r, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "running" || !r.FinishedAt.IsZero() || !r.StartedAt.Equal(start) || r.Source != want.Source || r.Destination != want.Destination || r.Cwd != want.Cwd || r.ConfigPath != want.ConfigPath || r.KnownHosts != want.KnownHosts || r.KeyPath != want.KeyPath || !r.Force {
		t.Fatalf("unexpected record: %+v", r)
	}
	for _, status := range []string{"success", "failed", "canceled"} {
		id, err := s.Begin(ctx, Record{Source: status})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(ctx, id, 42, status, "example failure"); err != nil {
			t.Fatal(err)
		}
		r, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != status || r.Bytes != 42 || r.Error != "example failure" || r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
			t.Fatalf("unexpected completed record: %+v", r)
		}
		if err := s.Finish(ctx, id, 0, "success", ""); err == nil {
			t.Fatal("completed record overwritten")
		}
	}
	if _, err := s.Get(ctx, 9999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing record: %v", err)
	}
	if err := s.Finish(ctx, 9999, 0, "success", ""); err == nil {
		t.Fatal("missing finish accepted")
	}
	if err := s.Finish(ctx, id, 0, "running", ""); err == nil {
		t.Fatal("invalid terminal status accepted")
	}
}

func TestSearchKeywordsLiteralWildcardsAndLimit(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "history.db"))
	ctx := context.Background()
	for _, source := range []string{"ALPHA report_100%.csv", `alpha folder\quoted'`, "beta alpha", "beta unrelated"} {
		id, err := s.Begin(ctx, Record{Source: source, Destination: "remote:/archive"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(ctx, id, 0, "failed", "permission denied"); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query string
		count int
	}{{"alpha", 3}, {"ALPHA archive denied", 3}, {"%", 1}, {"_", 1}, {`\`, 1}, {"quoted'", 1}, {"' OR 1=1 --", 0}, {"success", 0}, {"failed", 4}, {"beta alpha", 1}, {"", 4}} {
		rows, err := s.Search(ctx, tc.query, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != tc.count {
			t.Errorf("query %q got %d want %d", tc.query, len(rows), tc.count)
		}
	}
	rows, err := s.Search(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != 4 || rows[1].ID != 3 {
		t.Fatalf("order/limit: %+v", rows)
	}
	for _, limit := range []int{-1, 0, 1001} {
		if _, err := s.Search(ctx, "", limit); err == nil {
			t.Errorf("accepted limit %d", limit)
		}
	}
}

func TestIndependentConcurrentHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	stores := []*Store{openTestStore(t, path), openTestStore(t, path)}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := stores[i%len(stores)]
			id, err := s.Begin(context.Background(), Record{Source: fmt.Sprint(i)})
			if err != nil {
				t.Error(err)
				return
			}
			if err := s.Finish(context.Background(), id, int64(i), "success", ""); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	rows, err := stores[0].Search(context.Background(), "success", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 40 {
		t.Fatalf("lost writes: %d", len(rows))
	}
}

func TestPermissionsAndUnsafePaths(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "new", "history.db")
	openTestStore(t, path)
	for _, tc := range []struct {
		path string
		mode os.FileMode
	}{{parent, 0755}, {filepath.Dir(path), 0700}, {path, 0600}} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != tc.mode {
			t.Errorf("%s permissions %o", tc.path, info.Mode().Perm())
		}
	}
	link := filepath.Join(parent, "link.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(link); err == nil {
		s.Close()
		t.Fatal("symlink accepted")
	}
	if s, err := Open(parent); err == nil {
		s.Close()
		t.Fatal("directory accepted")
	}
}

func TestCanceledContextDoesNotInsert(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "history.db"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Begin(ctx, Record{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	rows, err := s.Search(context.Background(), "", 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows %v err %v", rows, err)
	}
}

func TestDefaultPath(t *testing.T) {
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "ah", "history.db") {
		t.Fatalf("unexpected default path %q", path)
	}
}

func TestConcurrentInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(path)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			if _, err := s.Begin(context.Background(), Record{Source: "ÉTÉ 中文"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s := openTestStore(t, path)
	rows, err := s.Search(context.Background(), "été 中文", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 8 {
		t.Fatalf("concurrent initialization lost records: %d", len(rows))
	}
}

func TestExistingFilePermissionsAreRestricted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	openTestStore(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("database permissions %o", info.Mode().Perm())
	}
}
