package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestMissingAndRoundTrip(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "private", "connections.toml")}
	got, err := s.Load()
	if err != nil || len(got) != 0 {
		t.Fatalf("missing: %v %v", got, err)
	}
	if err := s.Update(context.Background(), func(m map[string]Connection) error {
		m["A"] = Connection{Host: "a.example.com", User: "alice", IdentityFile: "~/.ssh/id_ed25519"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got["A"].Port != 22 || got["A"].IdentityFile != "~/.ssh/id_ed25519" {
		t.Fatalf("round trip: %#v", got)
	}
	if err := s.Update(context.Background(), func(m map[string]Connection) error { c := m["A"]; c.Host = "b.example.com"; m["A"] = c; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load()
	if err != nil || got["A"].Host != "b.example.com" {
		t.Fatalf("edit: %v %v", got, err)
	}
	if err := s.Update(context.Background(), func(m map[string]Connection) error { delete(m, "A"); return nil }); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load()
	if err != nil || len(got) != 0 {
		t.Fatalf("delete: %v %v", got, err)
	}
	for path, mode := range map[string]os.FileMode{s.Path: 0600, filepath.Dir(s.Path): 0700} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != mode {
			t.Errorf("%s mode %o want %o", path, st.Mode().Perm(), mode)
		}
	}
}

func TestLoadDefaultAndRejectInvalid(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		valid      bool
	}{
		{"default", "[connections.A]\nhost='a.example.com'\nuser='alice'", true},
		{"unknown", "[connections.A]\nhost='a.example.com'\nuser='alice'\nporrt=22", false},
		{"password", "[connections.A]\nhost='a.example.com'\nuser='alice'\npassword='not-a-real-secret'", false},
		{"unknown top", "other=true", false},
		{"duplicate", "[connections.A]\nhost='a'\nhost='b'\nuser='alice'", false},
		{"syntax", "[connections", false},
		{"missing host", "[connections.A]\nuser='alice'", false},
		{"port", "[connections.A]\nhost='a'\nuser='alice'\nport=65536", false},
		{"bad alias", "[connections.'a:b']\nhost='a'\nuser='alice'", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := Store{Path: filepath.Join(t.TempDir(), "config.toml")}
			if err := os.WriteFile(s.Path, []byte(tt.body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := s.Load()
			if (err == nil) != tt.valid {
				t.Fatalf("Load %v valid=%v", err, tt.valid)
			}
			if tt.valid && got["A"].Port != 22 {
				t.Errorf("port %d", got["A"].Port)
			}
			if !tt.valid {
				called := false
				err = s.Update(context.Background(), func(m map[string]Connection) error { called = true; return nil })
				if err == nil || called {
					t.Fatalf("bad config update: err=%v callback=%v", err, called)
				}
				b, _ := os.ReadFile(s.Path)
				if string(b) != tt.body {
					t.Fatal("corrupt config overwritten")
				}
			}
		})
	}
}

func TestRejectedUpdatePreservesConfig(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "config.toml")}
	if err := s.Update(context.Background(), func(m map[string]Connection) error { m["A"] = Connection{Host: "a", User: "alice"}; return nil }); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.Path)
	sentinel := errors.New("duplicate")
	if err := s.Update(context.Background(), func(m map[string]Connection) error { delete(m, "A"); return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("callback error %v", err)
	}
	if err := s.Update(context.Background(), func(m map[string]Connection) error { m["B"] = Connection{}; return nil }); err == nil {
		t.Fatal("invalid update accepted")
	}
	after, _ := os.ReadFile(s.Path)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected update changed file")
	}
}

func TestConcurrentUpdates(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "config.toml")}
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- s.Update(context.Background(), func(m map[string]Connection) error {
				time.Sleep(time.Millisecond)
				m[fmt.Sprintf("host%d", i)] = Connection{Host: "example.com", User: "alice"}
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Load()
	if err != nil || len(got) != n {
		t.Fatalf("lost update: %d %v", len(got), err)
	}
}

func TestCancellationWhileLocked(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "config.toml")}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.Update(context.Background(), func(m map[string]Connection) error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := s.Update(ctx, func(m map[string]Connection) error { t.Error("canceled callback ran"); return nil })
	close(release)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel error %v", err)
	}
}

func TestExistingParentPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	s := Store{Path: filepath.Join(dir, "config.toml")}
	if err := s.Update(context.Background(), func(map[string]Connection) error { return nil }); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(dir)
	if st.Mode().Perm() != 0755 {
		t.Fatalf("shared parent chmod: %o", st.Mode().Perm())
	}
}

func TestValidation(t *testing.T) {
	for _, name := range []string{"", "-a", "a:b", "a b", "a\n", "a/b", "中"} {
		if ValidateName(name) == nil {
			t.Errorf("accepted alias %q", name)
		}
	}
	for _, name := range []string{"A", "a-b_2", "0"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("alias %q: %v", name, err)
		}
	}
	for _, c := range []Connection{{Host: "", Port: 22, User: "alice"}, {Host: "a b", Port: 22, User: "alice"}, {Host: "a", Port: 22, User: "a\nb"}, {Host: "a", Port: 0, User: "alice"}, {Host: "a", Port: -1, User: "alice"}, {Host: "a", Port: 65536, User: "alice"}} {
		if c.Validate() == nil {
			t.Errorf("accepted invalid connection %#v", c)
		}
	}
	if err := (Connection{Host: "::1", Port: 22, User: "alice"}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, proxy := range []string{"http://127.0.0.1:8080", "socks5://127.0.0.1", "socks5://:1080", "127.0.0.1:1080", "socks5://\x00:1"} {
		if (Connection{Host: "a", Port: 22, User: "alice", Proxy: proxy}).Validate() == nil {
			t.Errorf("accepted invalid proxy %q", proxy)
		}
	}
	for _, proxy := range []string{"", "socks5://127.0.0.1:1080", "socks5h://host:1080", "socks5://user:pass@127.0.0.1:1080"} {
		if err := (Connection{Host: "a", Port: 22, User: "alice", Proxy: proxy}).Validate(); err != nil {
			t.Errorf("rejected valid proxy %q: %v", proxy, err)
		}
	}
}

func TestCancellationDuringUpdatePreservesOriginal(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "config.toml")}
	if err := s.Update(context.Background(), func(m map[string]Connection) error {
		m["A"] = Connection{Host: "example.com", User: "alice"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = s.Update(ctx, func(m map[string]Connection) error { delete(m, "A"); cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error %v", err)
	}
	after, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("canceled update replaced original")
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(s.Path), ".ah-config-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary config leaked: %v", temps)
	}
}

func TestDefaultPath(t *testing.T) {
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Skip(err)
	}
	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "ah", "connections.toml") {
		t.Fatalf("default config path %s", got)
	}
}
