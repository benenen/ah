package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/credentials"
	"github.com/benenen/ah/internal/testutil"
)

func TestDoctorStages(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := testutil.StartSSH(t)
	for _, mode := range []string{"success", "identity", "host-key", "proxy", "authentication", "sudo"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config")
			keyPath := filepath.Join(dir, "key")
			known := server.KnownHosts
			c := server.Connection
			switch mode {
			case "sudo":
				c.Sudo = true
			case "identity":
				c.IdentityFile = filepath.Join(dir, "missing")
			case "host-key":
				known = filepath.Join(dir, "known_hosts")
				if err := os.WriteFile(known, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "proxy":
				c.Proxies = []string{"socks5://user:proxy-secret@127.0.0.1:1"}
			case "authentication":
				var err error
				c.Password, err = (credentials.Store{Path: keyPath}).Encrypt("fixture", []byte("invalid-secret"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := (config.Store{Path: p}).Update(context.Background(), func(m map[string]config.Connection) error { m["fixture"] = c; return nil }); err != nil {
				t.Fatal(err)
			}
			out, err := execute(t, "--config", p, "--key-file", keyPath, "--known-hosts", known, "doctor", "fixture")
			if mode == "success" {
				if err != nil {
					t.Fatal(out, err)
				}
				for _, stage := range []string{"configuration", "authentication-setup", "tcp", "ssh-key-exchange", "host-key", "authentication", "sftp", "hardlink@openssh.com", "posix-rename@openssh.com"} {
					if !strings.Contains(out, stage+"\tOK") {
						t.Fatal("missing stage", stage, out)
					}
				}
			} else {
				if err == nil {
					t.Fatal("doctor accepted failure", out)
				}
				stage := map[string]string{"identity": "authentication-setup", "host-key": "host-key", "proxy": "proxy-chain", "authentication": "authentication", "sudo": "sudo"}[mode]
				if !strings.Contains(out, stage+"\tFAIL") {
					t.Fatal("wrong stage", out, err)
				}
			}
			for _, secret := range []string{"proxy-secret", "invalid-secret"} {
				if strings.Contains(out, secret) || err != nil && strings.Contains(err.Error(), secret) {
					t.Fatal("diagnostics leaked credential")
				}
			}
		})
	}
}
