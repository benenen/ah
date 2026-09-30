//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

// sshdImage is built from ./sshd and kept between runs as a build cache.
const sshdImage = "ah-e2e-sshd"

// opsPassword is the throwaway password baked into the disposable image.
const opsPassword = "ops-e2e-pass"

// lab is one isolated environment: a Docker network holding the OpenSSH
// target ("bastion") and an internal web server reachable only through it,
// plus a private HOME, config, known_hosts, master key and history for ah.
type lab struct {
	t        *testing.T
	dir      string
	ah       string
	env      []string
	flags    []string
	identity string
	known    string
	sshPort  string
	network  string
	sshd     string
	web      string
}

// result is one ah invocation.
type result struct {
	stdout, stderr string
	code           int
}

func (r result) String() string {
	return fmt.Sprintf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
}

func newLab(t *testing.T) *lab {
	t.Helper()
	// CI sets AH_E2E_REQUIRE_DOCKER so a missing daemon fails instead of
	// passing as a skip.
	skip := t.Skipf
	if os.Getenv("AH_E2E_REQUIRE_DOCKER") != "" {
		skip = t.Fatalf
	}
	if _, err := exec.LookPath("docker"); err != nil {
		skip("docker not installed")
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		skip("docker daemon unavailable: %s", out)
	}
	l := &lab{t: t, dir: t.TempDir()}
	suffix := randomHex(t, 4)
	l.network, l.sshd, l.web = "ah-e2e-"+suffix, "ah-e2e-sshd-"+suffix, "ah-e2e-web-"+suffix

	docker(t, "build", "-q", "-t", sshdImage, "sshd")
	l.ah = filepath.Join(l.dir, "ah")
	build := exec.Command("go", "build", "-o", l.ah, "../cmd/ah")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build ah: %s: %v", out, err)
	}

	l.identity = filepath.Join(l.dir, "id_ed25519")
	public := writeKey(t, l.identity)
	docker(t, "network", "create", l.network)
	t.Cleanup(func() {
		// Stop background forwards before their SSH server disappears.
		_ = l.run("f", "rm", "--all", "-f")
		_, _ = exec.Command("docker", "rm", "-f", l.sshd, l.web).CombinedOutput()
		_, _ = exec.Command("docker", "network", "rm", l.network).CombinedOutput()
	})
	docker(t, "run", "-d", "--name", l.sshd, "--network", l.network, "--network-alias", "bastion",
		"-p", "127.0.0.1::22", "-e", "AUTHORIZED_KEY="+public, sshdImage)
	// Only the bastion can reach this server; nothing publishes its port.
	docker(t, "run", "-d", "--name", l.web, "--network", l.network, "--network-alias", "web.internal",
		"--entrypoint", "sh", sshdImage, "-c",
		"mkdir -p /www && echo internal-web-ok > /www/index.html && exec httpd -f -p 8080 -h /www")
	address := strings.TrimSpace(docker(t, "port", l.sshd, "22/tcp"))
	address = strings.Split(address, "\n")[0]
	_, l.sshPort, _ = net.SplitHostPort(address)
	waitForBanner(t, net.JoinHostPort("127.0.0.1", l.sshPort))

	home := filepath.Join(l.dir, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	l.known = filepath.Join(l.dir, "known_hosts")
	// A private HOME keeps forward records and the default paths off the
	// developer's own ah state; SSH_AUTH_SOCK is cleared so only the lab key
	// authenticates.
	l.env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "SSH_AUTH_SOCK=", "TERM=xterm")
	l.flags = []string{
		"--config", filepath.Join(l.dir, "connections.toml"),
		"--known-hosts", l.known,
		"--key-file", filepath.Join(l.dir, "master.key"),
		"--history-file", filepath.Join(l.dir, "history.db"),
		"--timeout", "10s",
	}
	return l
}

// run executes ah with the lab's global flags; stdin is empty.
func (l *lab) run(args ...string) result {
	return l.runInput("", args...)
}

func (l *lab) runInput(stdin string, args ...string) result {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, l.ah, append(append([]string{}, l.flags...), args...)...)
	cmd.Env = l.env
	cmd.Dir = l.dir
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.code = exit.ExitCode()
	case err != nil:
		l.t.Fatalf("run ah %v: %v", args, err)
	}
	return r
}

// ok runs ah and fails the test unless it exits 0; it returns stdout.
func (l *lab) ok(args ...string) string {
	l.t.Helper()
	r := l.run(args...)
	if r.code != 0 {
		l.t.Fatalf("ah %s: %s", strings.Join(args, " "), r)
	}
	return r.stdout
}

// fails runs ah, requires a non-zero exit and a message in its output.
func (l *lab) fails(want string, args ...string) result {
	l.t.Helper()
	r := l.run(args...)
	if r.code == 0 || !strings.Contains(r.stdout+r.stderr, want) {
		l.t.Fatalf("ah %s: want failure mentioning %q, got %s", strings.Join(args, " "), want, r)
	}
	return r
}

// remote runs one shell command on the lab connection and returns its stdout.
func (l *lab) remote(name, command string) string {
	l.t.Helper()
	return strings.TrimSpace(l.ok("c", name, command))
}

func (l *lab) path(parts ...string) string {
	return filepath.Join(append([]string{l.dir}, parts...)...)
}

// terminal drives ah on a pseudo-terminal for prompts that refuse pipes.
type terminal struct {
	t    *testing.T
	cmd  *exec.Cmd
	tty  *os.File
	mu   sync.Mutex
	out  bytes.Buffer
	seen int
	done chan struct{}
}

func (l *lab) terminal(args ...string) *terminal {
	l.t.Helper()
	cmd := exec.Command(l.ah, append(append([]string{}, l.flags...), args...)...)
	cmd.Env = l.env
	cmd.Dir = l.dir
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		l.t.Fatal(err)
	}
	term := &terminal{t: l.t, cmd: cmd, tty: tty, done: make(chan struct{})}
	go func() {
		defer close(term.done)
		buf := make([]byte, 4096)
		for {
			n, err := tty.Read(buf)
			term.mu.Lock()
			term.out.Write(buf[:n])
			term.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	l.t.Cleanup(func() { _ = cmd.Process.Kill(); _ = tty.Close() })
	return term
}

// expect waits for text that appears after the previous match.
func (term *terminal) expect(text string) {
	term.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		term.mu.Lock()
		output := term.out.String()
		term.mu.Unlock()
		if i := strings.Index(output[term.seen:], text); i >= 0 {
			term.seen += i + len(text)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	term.t.Fatalf("terminal never showed %q; output:\n%s", text, term.out.String())
}

func (term *terminal) send(text string) {
	term.t.Helper()
	if _, err := term.tty.Write([]byte(text)); err != nil {
		term.t.Fatal(err)
	}
}

// wait returns the exit code and everything the terminal showed.
func (term *terminal) wait() (int, string) {
	term.t.Helper()
	err := term.cmd.Wait()
	select {
	case <-term.done:
	case <-time.After(5 * time.Second):
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), term.out.String()
	}
	if err != nil {
		term.t.Fatal(err)
	}
	return 0, term.out.String()
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %s: %v", strings.Join(args, " "), out, err)
	}
	return string(out)
}

func randomHex(t *testing.T, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// writeKey saves a fresh private key and returns its authorized_keys line.
func writeKey(t *testing.T, path string) string {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "ah e2e")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func waitForBanner(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			banner := make([]byte, 8)
			_, err = io.ReadFull(conn, banner)
			_ = conn.Close()
			if err == nil && string(banner) == "SSH-2.0-" {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("sshd at %s never became ready", address)
}

// freePort returns a currently unused local TCP port.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

func httpGet(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %q %v", url, resp.StatusCode, body, err)
	}
	return strings.TrimSpace(string(body))
}

func writeFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
