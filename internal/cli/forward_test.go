package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/ah/internal/forward"
)

func TestParseLocalForward(t *testing.T) {
	local, target, err := parseLocalForward("8080", "80")
	if err != nil || local != "127.0.0.1:8080" || target != "127.0.0.1:80" {
		t.Fatalf("default addresses: %q %q %v", local, target, err)
	}

	for _, tc := range []struct{ local, target, want string }{
		{"8080", "127.0.0.1:80", "127.0.0.1:8080"},
		{"0.0.0.0:8080", "database.internal:5432", "0.0.0.0:8080"},
		{"[::1]:8080", "[::1]:80", "[::1]:8080"},
	} {
		listen, target, err := parseLocalForward(tc.local, tc.target)
		if err != nil || listen != tc.want || target != tc.target {
			t.Fatalf("parse %q %q: %q %q %v", tc.local, tc.target, listen, target, err)
		}
	}
	for _, args := range [][2]string{
		{"0", "host:80"}, {"65536", "host:80"}, {"8080", "host:0"},
		{"8080", ":80"}, {":8080", "host:80"}, {"8080", "host"},
		{"8080:127.0.0.1:80", "host:80"}, {"8080", "::1:80"},
		{"8080", "host:http"}, {"-1", "host:80"},
	} {
		if _, _, err := parseLocalForward(args[0], args[1]); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestForwardArguments(t *testing.T) {
	for _, args := range [][]string{
		{"forward", "dev"}, {"forward", "dev", "8080"},
		{"forward", "dev", "-L", "8080:localhost:80"},
		{"forward", "dev", "8080", "localhost:80", "extra"},
		{"forward", "dev", "1080", "80", "-D"},
		{"forward", "dev", "9000", "-R"},
		{"forward", "dev", "8080", "80", "-L", "-R"},
		{"forward", "dev", "1080", "-D", "-R"},
	} {
		if _, err := execute(t, args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestForwardListJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	out, err := execute(t, "forward", "ls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty listing must stay valid JSON: %q", out)
	}
	if _, err := forward.Allocate(forward.Local, "dev", "127.0.0.1:8080", "127.0.0.1:80"); err != nil {
		t.Fatal(err)
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	// Documented layout: <user config dir>/ah/forwards/<ID>.json.
	corrupt := filepath.Join(configDir, "ah", "forwards", "0123456789abcdef.json")
	if err := os.WriteFile(corrupt, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = execute(t, "forward", "ls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var listed []struct {
		ID      string    `json:"id"`
		Type    string    `json:"type"`
		Name    string    `json:"name"`
		Listen  string    `json:"listen"`
		Target  string    `json:"target"`
		PID     int       `json:"pid"`
		Status  string    `json:"status"`
		Started time.Time `json:"started"`
		Error   string    `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	status := make(map[string]string, len(listed))
	for _, r := range listed {
		status[r.ID] = r.Status
	}
	if len(listed) != 2 || status["0123456789abcdef"] != "corrupt" {
		t.Fatalf("unexpected records: %v", listed)
	}
	if listed[1].Name != "dev" || listed[1].Type != "local" || listed[1].Status != "starting" || listed[1].Listen != "127.0.0.1:8080" {
		t.Fatalf("readable record: %v", listed[1])
	}
	if strings.Contains(out, "socket") || strings.Contains(out, "key_path") {
		t.Fatalf("internal fields leaked into JSON: %q", out)
	}
	table, err := execute(t, "forward", "ls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table, "STATUS") || !strings.Contains(table, "corrupt") {
		t.Fatalf("table view lost the corrupt record: %q", table)
	}
}

func TestForwardRemoveCorruptRecord(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(configDir, "ah", "forwards", "0123456789abcdef.json")
	if err := os.MkdirAll(filepath.Dir(corrupt), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "forward", "rm", "0123456789abcdef"); err == nil {
		t.Fatal("plain rm deleted an unreadable record")
	}
	if _, err := os.Stat(corrupt); err != nil {
		t.Fatalf("record removed without --force: %v", err)
	}
	out, err := execute(t, "forward", "rm", "-f", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Warning") || !strings.Contains(out, "Removed 0123456789abcdef") {
		t.Fatalf("output: %q", out)
	}
	if _, err := os.Stat(corrupt); !os.IsNotExist(err) {
		t.Fatalf("record survived --force: %v", err)
	}
}

func TestForwardRemoveAll(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	record := func(status string) forward.Record {
		t.Helper()
		r, err := forward.Allocate(forward.Local, "fixture", "127.0.0.1:8080", "127.0.0.1:80")
		if err != nil {
			t.Fatal(err)
		}
		r.Status = status
		if err := r.Save(); err != nil {
			t.Fatal(err)
		}
		return r
	}
	stopped, failed := record("stopped"), record("failed")
	// A running record whose control socket is gone lists as stale and cannot be stopped.
	stale := record("running")
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(configDir, "ah", "forwards", "0123456789abcdef.json")
	if err := os.WriteFile(corrupt, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	exists := func(id string) bool {
		_, err := os.Stat(filepath.Join(configDir, "ah", "forwards", id+".json"))
		return err == nil
	}

	if _, err := execute(t, "forward", "rm", "--all", stopped.ID); err == nil {
		t.Fatal("--all accepted an ID")
	}
	if _, err := execute(t, "forward", "rm"); err == nil {
		t.Fatal("rm without ID or --all succeeded")
	}

	out, err := execute(t, "forward", "rm", "--all")
	if err != nil {
		t.Fatalf("rm --all: %v: %s", err, out)
	}
	for _, want := range []string{"Removed " + stopped.ID, "Removed " + failed.ID, "Skipped " + stale.ID + ": stale", "Skipped 0123456789abcdef: corrupt"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
	}
	if exists(stopped.ID) || exists(failed.ID) || !exists(stale.ID) || !exists("0123456789abcdef") {
		t.Fatal("rm --all removed the wrong records")
	}

	out, err = execute(t, "forward", "rm", "--all", "-f", "--timeout", "100ms")
	if err != nil {
		t.Fatalf("rm --all -f: %v: %s", err, out)
	}
	if !strings.Contains(out, "Removed 0123456789abcdef") || !strings.Contains(out, "Removed "+stale.ID) || !strings.Contains(out, "removing stale record without signaling PID") {
		t.Fatalf("output: %q", out)
	}
	if exists("0123456789abcdef") || exists(stale.ID) {
		t.Fatal("rm --all -f must delete both corrupt and stale records")
	}
}

func TestForwardKillCancelsStartingBeforeLock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	r, err := forward.Allocate(forward.Local, "fixture", "127.0.0.1:8080", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := forward.Lock(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	startup, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg, err := forward.RegisterStarting(r, cancel)
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		<-startup.Done()
		// A real launcher only releases its lock once cancellation finishes.
		closeErr := reg.Close(context.Canceled)
		done <- errors.Join(closeErr, lock.Close())
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	cmd := New()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"forward", "kill", r.ID})
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err := forward.Read(r.ID)
	if err != nil || saved.Status != "stopped" {
		t.Fatalf("record after kill: %+v %v", saved, err)
	}
}

func TestForwardRestartCancelsStartingBeforeLock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	cfgPath := filepath.Join(dir, "connections.toml")
	if out, err := execute(t, "--config", cfgPath, "new", "fixture", "--host", "127.0.0.1", "--user", "tester"); err != nil {
		t.Fatalf("new connection: %s %v", out, err)
	}
	r, err := forward.Allocate(forward.Local, "fixture", "127.0.0.1:0", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	r.ConfigPath = cfgPath
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	lock, err := forward.Lock(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	startup, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg, err := forward.RegisterStarting(r, cancel)
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		<-startup.Done()
		closeErr := reg.Close(context.Canceled)
		done <- errors.Join(closeErr, lock.Close())
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	cmd := New()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	// No SSH server runs on port 22, so restart should cancel starting quickly and
	// fail on SSH dial rather than deadlocking on the startup lock.
	cmd.SetArgs([]string{"--timeout", "150ms", "forward", "restart", r.ID})
	err = cmd.ExecuteContext(ctx)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected fast dial error instead of lock deadlock, got: %v", err)
	}
}

func TestForwardFlagsScopedAndErrorSanitized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	if _, err := execute(t, "forward", "ls", "--reconnect"); err == nil {
		t.Fatal("forward ls accepted --reconnect")
	}
	r, err := forward.Allocate(forward.Local, "demo", "127.0.0.1:8080", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	r.Status = "failed"
	r.Error = "first line\nsecond\tline"
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "forward", "ls")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "second\tline") || !strings.Contains(out, "first line second line") {
		t.Fatalf("multiline error was not sanitized:\n%s", out)
	}
}

func TestForwardListFilterRemoveStaleAndCompletion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	live, err := forward.Allocate(forward.Local, "live", "127.0.0.1:8080", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := forward.Register(live, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close(nil) })

	stopped, err := forward.Allocate(forward.Local, "stopped-one", "127.0.0.1:8081", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	stopped.Status = "stopped"
	if err := stopped.Save(); err != nil {
		t.Fatal(err)
	}

	stale, err := forward.Allocate(forward.Local, "stale-one", "127.0.0.1:8082", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	stale.Status = "running"
	stale.Socket = filepath.Join(dir, "missing.sock")
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}

	// Filtering: --running and --status
	out, err := execute(t, "forward", "ls", "--running")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, live.ID) || strings.Contains(out, stopped.ID) || strings.Contains(out, stale.ID) {
		t.Fatalf("--running filter unexpected:\n%s", out)
	}
	out, err = execute(t, "forward", "ls", "--status", "stale")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, stale.ID) || strings.Contains(out, live.ID) || strings.Contains(out, stopped.ID) {
		t.Fatalf("--status stale filter unexpected:\n%s", out)
	}
	if _, err := execute(t, "forward", "ls", "--status", "bogus"); err == nil {
		t.Fatal("accepted invalid --status")
	}
	if _, err := execute(t, "forward", "ls", "--running", "--status", "stopped"); err == nil {
		t.Fatal("accepted --running with --status")
	}

	// Completion: kill/start/restart/rm filter by appropriate status.
	compKill, err := execute(t, "__complete", "forward", "kill", "")
	if err != nil || !strings.Contains(compKill, live.ID) || strings.Contains(compKill, stopped.ID) {
		t.Fatalf("kill completion unexpected: %q %v", compKill, err)
	}
	compStart, err := execute(t, "__complete", "forward", "start", "")
	if err != nil || !strings.Contains(compStart, stopped.ID) || strings.Contains(compStart, live.ID) {
		t.Fatalf("start completion unexpected: %q %v", compStart, err)
	}
	compRm, err := execute(t, "__complete", "forward", "rm", "")
	if err != nil || !strings.Contains(compRm, live.ID) || !strings.Contains(compRm, stopped.ID) || !strings.Contains(compRm, stale.ID) {
		t.Fatalf("rm completion unexpected: %q %v", compRm, err)
	}

	// Plain rm on stale refuses and hints --force/--stale.
	if _, err := execute(t, "forward", "rm", stale.ID); err == nil || !strings.Contains(err.Error(), "--stale") {
		t.Fatalf("expected stale hint, got: %v", err)
	}

	// rm --stale removes only stale records and leaves live & stopped intact.
	out, err = execute(t, "forward", "rm", "--stale")
	if err != nil {
		t.Fatalf("rm --stale: %v: %s", err, out)
	}
	if !strings.Contains(out, "Removed "+stale.ID) || !strings.Contains(out, "without signaling PID") {
		t.Fatalf("rm --stale output: %q", out)
	}
	if _, err := forward.Read(stale.ID); !os.IsNotExist(err) {
		t.Fatalf("stale record survived rm --stale: %v", err)
	}
	if _, err := forward.Read(live.ID); err != nil {
		t.Fatalf("live record removed by rm --stale: %v", err)
	}
	if _, err := forward.Read(stopped.ID); err != nil {
		t.Fatalf("stopped record removed by rm --stale: %v", err)
	}
}

func TestForwardDefaultsContinueOnErrorAndKeepAlive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	cfgPath := filepath.Join(dir, "connections.toml")
	if _, err := execute(t, "--config", cfgPath, "new", "demo", "--host", "127.0.0.1", "--user", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "--config", cfgPath, "new", "custom", "--host", "127.0.0.1", "--user", "alice", "--keepalive", "15s"); err != nil {
		t.Fatal(err)
	}

	// Verify captureForwardOptions & default resolution via forwardCommand flags.
	cmd := New()
	fCmd, _, err := cmd.Find([]string{"forward"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := fCmd.Flags().GetBool("continue-on-error")
	if err != nil || !v {
		t.Fatalf("forward --continue-on-error default = %v, %v; want true", v, err)
	}

	// Verify connection keepalive is stored and used when resolving SSH options.
	connDemo, err := (&app{configPath: cfgPath}).connection("demo")
	if err != nil {
		t.Fatal(err)
	}
	connCustom, err := (&app{configPath: cfgPath}).connection("custom")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{configPath: cfgPath}
	if got := a.effectiveKeepAlive(fCmd, connCustom); got != 15*time.Second {
		t.Fatalf("effectiveKeepAlive(custom) = %v; want 15s", got)
	}
	if got := a.effectiveKeepAlive(fCmd, connDemo); got != 0 {
		t.Fatalf("effectiveKeepAlive(demo) = %v; want 0", got)
	}
}
