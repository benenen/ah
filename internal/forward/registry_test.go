package forward

import (
	"context"
	"os"
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
