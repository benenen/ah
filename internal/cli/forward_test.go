package cli

import (
	"testing"
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
	} {
		if _, err := execute(t, args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
