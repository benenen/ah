package forward

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlIdentityAndStaleRecords(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	r, err := Allocate("test", "127.0.0.1:8080", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := recordPath(r.ID)
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("record permissions: %v %v", info, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg, err := Register(r, cancel)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		<-ctx.Done()
		closed <- reg.Close(nil)
	}()
	t.Cleanup(func() { cancel(); <-closed })
	wrong := reg.Record
	wrong.ID = "0000000000000000"
	if err := control(context.Background(), wrong, "stop"); err == nil {
		t.Fatal("accepted wrong control identity")
	}
	if ctx.Err() != nil {
		t.Fatal("wrong ID stopped forward")
	}
	records, err := List(context.Background())
	if err != nil || len(records) != 1 || records[0].Status != "running" {
		t.Fatalf("running list: %v %v", records, err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := Stop(stopCtx, r.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := Read(r.ID)
	if err != nil || saved.Status != "stopped" {
		t.Fatalf("saved stop: %v %v", saved, err)
	}
	// Emulate a crashed process whose PID now belongs to this live test.
	stale := reg.Record
	stale.Status, stale.Socket, stale.PID = "running", "/nonexistent-ah-forward/control.sock", os.Getpid()
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	if err := Stop(stopCtx, stale.ID); err == nil {
		t.Fatal("stale record stopped a process")
	}
	records, err = List(context.Background())
	if err != nil || records[0].Status != "stale" {
		t.Fatalf("stale list: %v %v", records, err)
	}
	if _, err := Read("../not-an-id"); err == nil {
		t.Fatal("accepted path traversal")
	}
}

func TestRemoveAndLifecycleLock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	r, err := Allocate("test", "127.0.0.1:8080", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := Lock(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if second, err := Lock(ctx, r.ID); err == nil {
		_ = second.Close()
		t.Fatal("concurrent lifecycle command acquired the lock")
	}
	for _, status := range []string{"starting", "running", "stale"} {
		r.Status = status
		if err := r.Save(); err != nil {
			t.Fatal(err)
		}
		if err := Remove(r.ID); err == nil {
			t.Fatalf("removed %s record", status)
		}
		if _, err := Read(r.ID); err != nil {
			t.Fatal(err)
		}
	}
	r.Status = "failed"
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if err := Remove(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(r.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed record: %v", err)
	}
	if _, err := Read("../invalid"); err == nil {
		t.Fatal("accepted invalid ID")
	}
}

func TestListReportsCorruptRecords(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	good, err := Allocate("test", "127.0.0.1:8080", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	root, err := directory()
	if err != nil {
		t.Fatal(err)
	}
	// A truncated record, and a stray file whose name is not a forward ID.
	for name, body := range map[string]string{
		"0123456789abcdef.json": "{",
		"notes.json":            "not json",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	records, err := List(context.Background())
	if err != nil {
		t.Fatalf("a single unreadable record must not fail the listing: %v", err)
	}
	status := make(map[string]string, len(records))
	for _, r := range records {
		status[r.ID] = r.Status
		if r.Status == "corrupt" && r.Error == "" {
			t.Fatalf("corrupt record without a reason: %v", r)
		}
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d: %v", len(records), records)
	}
	if status[good.ID] != "starting" {
		t.Fatalf("readable record status: %v", status)
	}
	for _, id := range []string{"0123456789abcdef", "notes"} {
		if status[id] != "corrupt" {
			t.Fatalf("record %s: status %q, want corrupt", id, status[id])
		}
	}
}

func TestRemoveCorrupt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	r, err := Allocate("test", "127.0.0.1:8080", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	// Readable records must keep going through Remove so its status check applies.
	if err := RemoveCorrupt(r.ID); err == nil {
		t.Fatal("RemoveCorrupt deleted a readable record")
	}
	p, err := recordPath(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	log := strings.TrimSuffix(p, ".json") + ".log"
	// The CLI holds the lifecycle lock while it removes a record.
	lock, err := Lock(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := os.WriteFile(p, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte("log"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveCorrupt(r.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p, log} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s survived: %v", path, err)
		}
	}
	// Concurrent lifecycles must keep locking the same inode.
	if _, err := os.Stat(p + ".lock"); err != nil {
		t.Fatalf("lock file removed: %v", err)
	}
	if err := RemoveCorrupt("../not-an-id"); err == nil {
		t.Fatal("accepted path traversal")
	}
}
