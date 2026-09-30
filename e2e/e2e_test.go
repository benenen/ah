//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// TestLab runs the scenarios in order against one lab; later steps rely on the
// connections and files earlier ones created.
func TestLab(t *testing.T) {
	l := newLab(t)
	steps := []struct {
		name string
		run  func(*testing.T, *lab)
	}{
		{"HostTrust", testHostTrust},
		{"RemoteCommand", testRemoteCommand},
		{"InteractiveShell", testInteractiveShell},
		{"PasswordLogin", testPasswordLogin},
		{"CopyFile", testCopyFile},
		{"CopyTree", testCopyTree},
		{"History", testHistory},
		{"Forwards", testForwards},
		{"SudoWithoutPassword", testSudoWithoutPassword},
		{"SudoWithPassword", testSudoWithPassword},
	}
	for _, step := range steps {
		// Each step works on the shared lab, so stop at the first failure
		// instead of reporting its knock-on effects.
		if !t.Run(step.name, func(t *testing.T) { l.t = t; step.run(t, l) }) {
			t.FailNow()
		}
	}
}

func testHostTrust(t *testing.T, l *lab) {
	l.ok("new", "lab", "--host", "127.0.0.1", "--port", l.sshPort, "--user", "tester", "--identity-file", l.identity)
	l.ok("version")

	// Without a terminal an unknown host is refused, never trusted silently.
	if r := l.run("c", "lab", "true"); r.code == 0 {
		t.Fatalf("unknown host accepted without a terminal: %s", r)
	}
	if _, err := os.Stat(l.known); err == nil {
		t.Fatal("known_hosts written without consent")
	}
	// Answering no keeps it untrusted.
	term := l.terminal("c", "lab", "uname -s")
	term.expect("Key fingerprint is SHA256:")
	term.expect("(yes/no)? ")
	term.send("no\n")
	if code, out := term.wait(); code == 0 {
		t.Fatalf("declined host key still connected:\n%s", out)
	}
	// Answering yes records the key and runs the command.
	term = l.terminal("c", "lab", "uname -s")
	term.expect("(yes/no)? ")
	term.send("yes\n")
	term.expect("Linux")
	if code, out := term.wait(); code != 0 {
		t.Fatalf("trusted connection failed:\n%s", out)
	}
	known := readFile(t, l.known)
	if !strings.Contains(known, "[127.0.0.1]:"+l.sshPort+" ssh-ed25519 ") {
		t.Fatalf("known_hosts: %q", known)
	}
	if got := l.remote("lab", "whoami"); got != "tester" {
		t.Fatalf("whoami %q", got)
	}

	// A changed host key is always refused, even with --trust-new-host.
	saved := known
	other := writeKey(t, l.path("impostor_key"))
	fake := strings.Fields(known)[0] + " " + other + "\n"
	writeFile(t, l.known, fake, 0600)
	l.fails("key mismatch", "--trust-new-host", "c", "lab", "true")
	if readFile(t, l.known) != fake {
		t.Fatal("changed host key overwrote known_hosts")
	}
	writeFile(t, l.known, saved, 0600)
}

func testRemoteCommand(t *testing.T, l *lab) {
	r := l.run("c", "lab", "echo out; echo err >&2; exit 7")
	if r.code != 7 || strings.TrimSpace(r.stdout) != "out" || !strings.Contains(r.stderr, "err") {
		t.Fatalf("streams and exit code: %s", r)
	}
	// Arguments after the name are joined like ssh and parsed by the remote shell.
	if got := l.remote("lab", "printf '%s|' a 'b c'"); got != "a|b c|" {
		t.Fatalf("quoting %q", got)
	}
	r = l.runInput("piped stdin\n", "c", "lab", "cat")
	if r.code != 0 || r.stdout != "piped stdin\n" {
		t.Fatalf("stdin: %s", r)
	}
	var inspected map[string]any
	if err := json.Unmarshal([]byte(l.ok("inspect", "lab")), &inspected); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(inspected["user"]) != "tester" || fmt.Sprint(inspected["port"]) != l.sshPort {
		t.Fatalf("inspect %v", inspected)
	}
}

func testInteractiveShell(t *testing.T, l *lab) {
	term := l.terminal("c", "lab")
	term.send("echo shell-$((6*7))\n")
	term.expect("shell-42")
	// The local terminal size travels with the PTY request.
	term.send("stty size\n")
	term.expect("40 120")
	term.send("exit 3\n")
	if code, out := term.wait(); code != 3 {
		t.Fatalf("interactive exit code %d:\n%s", code, out)
	}
}

func testPasswordLogin(t *testing.T, l *lab) {
	term := l.terminal("new", "ops", "--host", "127.0.0.1", "--port", l.sshPort, "--user", "ops", "--password")
	term.expect("assword")
	term.send(opsPassword + "\n")
	if code, out := term.wait(); code != 0 {
		t.Fatalf("save password:\n%s", out)
	}
	config := readFile(t, l.path("connections.toml"))
	if strings.Contains(config, opsPassword) {
		t.Fatal("password stored in plain text")
	}
	// Non-interactive use decrypts the saved password.
	if got := l.remote("ops", "whoami"); got != "ops" {
		t.Fatalf("whoami %q", got)
	}
	inspected := l.ok("inspect", "ops")
	if strings.Contains(inspected, opsPassword) || !strings.Contains(inspected, `"password_configured": true`) {
		t.Fatalf("inspect leaked or lost the password: %s", inspected)
	}
}

func testCopyFile(t *testing.T, l *lab) {
	local := l.path("files", "报告 v1.txt")
	writeFile(t, local, "version 1\n", 0640)
	out := l.ok("cp", local, "lab:~/")
	if !strings.Contains(out, "Copied 10 bytes") {
		t.Fatalf("upload: %s", out)
	}
	if got := l.remote("lab", "cat ~/'报告 v1.txt'; stat -c %a ~/'报告 v1.txt'"); got != "version 1\n640" {
		t.Fatalf("remote file and mode: %q", got)
	}
	download := l.path("files", "download.txt")
	l.ok("cp", "lab:~/报告 v1.txt", download)
	if readFile(t, download) != "version 1\n" {
		t.Fatal("download differs")
	}
	// Remote to remote streams through this machine over two SFTP sessions.
	l.ok("cp", "lab:~/报告 v1.txt", "ops:/tmp/relay.txt")
	if got := l.remote("ops", "cat /tmp/relay.txt"); got != "version 1" {
		t.Fatalf("relay %q", got)
	}

	target := "lab:~/app.conf"
	writeFile(t, local, "v1\n", 0600)
	l.ok("cp", local, target)
	writeFile(t, local, "v2\n", 0600)
	l.fails("destination exists", "cp", local, target)
	l.ok("cp", "-f", local, target)
	if got := l.remote("lab", "cat ~/app.conf"); got != "v2" {
		t.Fatalf("force %q", got)
	}
	// Backups keep the newest --backup-keep versions beside the file.
	for _, v := range []string{"v3", "v4", "v5"} {
		writeFile(t, local, v+"\n", 0600)
		l.ok("cp", "--backup", "--backup-keep", "2", local, target)
		time.Sleep(10 * time.Millisecond)
	}
	backups := strings.Fields(l.remote("lab", "cd ~ && ls bak.app.conf.bak-*"))
	if len(backups) != 2 {
		t.Fatalf("backups %v", backups)
	}
	if got := l.remote("lab", "cd ~ && cat "+backups[0]+" "+backups[1]+" app.conf"); got != "v3\nv4\nv5" {
		t.Fatalf("backup contents %q", got)
	}
	// Restoring is renaming a backup back.
	l.remote("lab", "cd ~ && cp "+backups[0]+" app.conf")
	if got := l.remote("lab", "cat ~/app.conf"); got != "v3" {
		t.Fatalf("restore %q", got)
	}
	// Identical content is left alone and makes no backup.
	writeFile(t, local, "v3\n", 0600)
	if out := l.ok("cp", "--backup", "--backup-keep", "2", local, target); !strings.Contains(out, "Unchanged") {
		t.Fatalf("identical copy: %s", out)
	}
	if n := len(strings.Fields(l.remote("lab", "cd ~ && ls bak.app.conf.bak-*"))); n != 2 {
		t.Fatalf("identical copy changed backups: %d", n)
	}
	l.fails("--backup-keep", "cp", "--backup", "--backup-keep", "0", local, target)
}

func testCopyTree(t *testing.T, l *lab) {
	tree := l.path("site")
	writeFile(t, filepath.Join(tree, "index.html"), "home v1\n", 0644)
	writeFile(t, filepath.Join(tree, "css", "app.css"), "body{}\n", 0644)
	writeFile(t, filepath.Join(tree, "中文 目录", "说明.txt"), "你好\n", 0600)
	writeFile(t, filepath.Join(tree, "readonly", "fixed.txt"), "fixed\n", 0444)
	if err := os.MkdirAll(filepath.Join(tree, "empty"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("index.html", filepath.Join(tree, "link.html")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(tree, "readonly"), 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(tree, "readonly"), 0755) })

	l.fails("regular file", "cp", tree, "lab:~/")
	r := l.run("cp", "-r", tree, "lab:~/")
	if r.code != 0 || !strings.Contains(r.stdout, "Copied 4 files") || !strings.Contains(r.stderr, "link.html (symbolic link)") {
		t.Fatalf("upload tree: %s", r)
	}
	listing := l.remote("lab", "cd ~/site && find . | sort")
	want := ". ./css ./css/app.css ./empty ./index.html ./readonly ./readonly/fixed.txt ./中文 目录 ./中文 目录/说明.txt"
	if strings.Join(strings.Split(listing, "\n"), " ") != want {
		t.Fatalf("remote tree:\n%s", listing)
	}
	if got := l.remote("lab", "cd ~/site && stat -c '%n %a' readonly empty readonly/fixed.txt"); got != "readonly 555\nempty 750\nreadonly/fixed.txt 444" {
		t.Fatalf("modes:\n%s", got)
	}

	// Downloading reproduces the tree; a missing target becomes the copy.
	download := l.path("site-copy")
	l.ok("cp", "-r", "lab:~/site", download)
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(download, "readonly"), 0755) })
	if readFile(t, filepath.Join(download, "中文 目录", "说明.txt")) != "你好\n" {
		t.Fatal("download differs")
	}

	// An existing destination needs --force (or --backup) to merge.
	writeFile(t, filepath.Join(tree, "index.html"), "home v2\n", 0644)
	l.fails("use --force to merge", "cp", "-r", tree, "lab:~/")
	l.remote("lab", "echo keep > ~/site/extra.txt")
	out := l.ok("cp", "-r", "--backup", tree, "lab:~/")
	if !strings.Contains(out, "Copied 1 files, 8 bytes, 3 unchanged") {
		t.Fatalf("merge: %s", out)
	}
	if got := l.remote("lab", "cd ~/site && cat index.html extra.txt bak.index.html.bak-*"); got != "home v2\nkeep\nhome v1" {
		t.Fatalf("merge result:\n%s", got)
	}
	if got := l.remote("lab", "cd ~/site && find . -name 'bak.*' | wc -l"); got != "1" {
		t.Fatalf("unchanged files were backed up: %s", got)
	}
	l.fails("into itself", "cp", "-r", "-f", "lab:~/site", "lab:~/site/css")
}

func testHistory(t *testing.T, l *lab) {
	out := l.ok("history", "search", "site", "--limit", "50")
	if !strings.Contains(out, "success") || !strings.Contains(out, "failed") {
		t.Fatalf("search:\n%s", out)
	}
	var id string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "success") && strings.Contains(line, "lab:~/") {
			id = strings.Fields(line)[0]
			break
		}
	}
	if id == "" {
		t.Fatalf("no recursive upload in:\n%s", out)
	}
	shown := l.ok("history", "show", id)
	if !strings.Contains(shown, "'--recursive'") {
		t.Fatalf("show: %s", shown)
	}
	// Replaying the recorded merge is a no-op now that nothing changed.
	replay := l.ok("history", "run", id)
	if !strings.Contains(replay, "0 bytes") || !strings.Contains(replay, "unchanged") {
		t.Fatalf("replay: %s", replay)
	}
	l.fails("positive integer", "history", "show", "abc")
}

func testForwards(t *testing.T, l *lab) {
	direct := &http.Client{Timeout: 10 * time.Second}

	// -L: an internal-only web server through the bastion.
	localPort := freePort(t)
	localID := strings.TrimSpace(l.ok("f", "lab", localPort, "web.internal:8080", "-d"))
	if got := httpGet(t, direct, "http://127.0.0.1:"+localPort+"/"); got != "internal-web-ok" {
		t.Fatalf("-L body %q", got)
	}

	// -D: names resolve on the bastion, and one bad target leaves the proxy up.
	socksPort := freePort(t)
	socksID := strings.TrimSpace(l.ok("f", "lab", socksPort, "-D", "-d"))
	dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:"+socksPort, nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	viaSOCKS := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.(proxy.ContextDialer).DialContext(ctx, network, address)
		},
	}}
	if got := httpGet(t, viaSOCKS, "http://web.internal:8080/"); got != "internal-web-ok" {
		t.Fatalf("-D body %q", got)
	}
	if _, err := viaSOCKS.Get("http://missing.internal:8080/"); err == nil {
		t.Fatal("unresolvable target succeeded")
	}
	if got := httpGet(t, viaSOCKS, "http://web.internal:8080/"); got != "internal-web-ok" {
		t.Fatalf("-D after failure %q", got)
	}
	logs, _ := filepath.Glob(filepath.Join(l.dir, "home", ".config", "ah", "forwards", socksID+".log"))
	if len(logs) != 1 || !strings.Contains(readFile(t, logs[0]), "missing.internal:8080") {
		t.Fatalf("failed SOCKS request not logged: %v", logs)
	}

	// -R: the bastion reaches a server that only listens on this machine.
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("from-the-laptop\n"))
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	remotePort := strconv.Itoa(20000 + int(time.Now().UnixNano()%20000))
	remoteID := strings.TrimSpace(l.ok("f", "lab", remotePort, listener.Addr().String(), "-R", "-d"))
	if got := l.remote("lab", "wget -qO- http://127.0.0.1:"+remotePort+"/"); got != "from-the-laptop" {
		t.Fatalf("-R body %q", got)
	}
	// Loopback by default: other hosts on the network cannot reach it...
	if out, err := exec.Command("docker", "exec", l.web, "wget", "-qO-", "-T", "3", "http://bastion:"+remotePort+"/").CombinedOutput(); err == nil {
		t.Fatalf("loopback -R reachable from the network: %s", out)
	}
	// ...while an explicit 0.0.0.0 is honoured under GatewayPorts clientspecified.
	gatewayPort := strconv.Itoa(40000 + int(time.Now().UnixNano()%20000))
	gatewayID := strings.TrimSpace(l.ok("f", "lab", "0.0.0.0:"+gatewayPort, listener.Addr().String(), "-R", "-d"))
	if out := docker(t, "exec", l.web, "wget", "-qO-", "-T", "5", "http://bastion:"+gatewayPort+"/"); strings.TrimSpace(out) != "from-the-laptop" {
		t.Fatalf("gateway -R body %q", out)
	}

	list := l.ok("f", "ls")
	for _, want := range []string{localID + "  -L", socksID + "  -D", remoteID + "  -R", gatewayID + "  -R", "(socks5)"} {
		if !strings.Contains(list, want) {
			t.Fatalf("ls missing %q:\n%s", want, list)
		}
	}
	var records []struct{ ID, Type, Status string }
	if err := json.Unmarshal([]byte(l.ok("f", "ls", "--json")), &records); err != nil || len(records) != 4 {
		t.Fatalf("ls --json: %v %v", records, err)
	}
	for _, r := range records {
		if r.Status != "running" {
			t.Fatalf("record %+v", r)
		}
	}

	// Lifecycle: kill keeps the record, start and restart reuse the ID and type.
	l.ok("f", "kill", socksID)
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+socksPort, time.Second); err == nil {
		t.Fatal("killed SOCKS proxy still listening")
	}
	if got := strings.TrimSpace(l.ok("f", "start", socksID)); got != socksID {
		t.Fatalf("start printed %q", got)
	}
	if got := httpGet(t, viaSOCKS, "http://web.internal:8080/"); got != "internal-web-ok" {
		t.Fatalf("restarted -D body %q", got)
	}
	l.ok("f", "restart", remoteID)
	if got := l.remote("lab", "wget -qO- http://127.0.0.1:"+remotePort+"/"); got != "from-the-laptop" {
		t.Fatalf("restarted -R body %q", got)
	}

	l.ok("f", "kill", localID)
	r := l.run("f", "rm", "--all")
	if r.code != 0 || !strings.Contains(r.stdout, "Removed "+localID) || strings.Count(r.stderr, "Skipped") != 3 {
		t.Fatalf("rm --all: %s", r)
	}
	l.ok("f", "remove", "--all", "-f")
	if list := l.ok("f", "ls", "--json"); strings.TrimSpace(list) != "[]" {
		t.Fatalf("forwards left: %s", list)
	}
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+localPort, time.Second); err == nil {
		t.Fatal("-L listener survived removal")
	}
	if out, err := exec.Command("docker", "exec", l.sshd, "sh", "-c", "netstat -ltn | grep -c ':"+gatewayPort+" '").CombinedOutput(); err == nil {
		t.Fatalf("-R listener survived removal: %s", out)
	}
}

func testSudoWithoutPassword(t *testing.T, l *lab) {
	l.ok("edit", "lab", "--sudo")
	if got := l.remote("lab", "id -u"); got != "0" {
		t.Fatalf("escalated uid %q", got)
	}
	// SFTP runs as root too, so root-only paths are writable.
	local := l.path("root.txt")
	writeFile(t, local, "root owned\n", 0600)
	l.ok("cp", local, "lab:/root/root.txt")
	if got := l.remote("lab", "stat -c '%U %a' /root/root.txt; cat /root/root.txt"); got != "root 600\nroot owned" {
		t.Fatalf("root copy %q", got)
	}
	l.ok("cp", "-r", l.path("site"), "lab:/root/")
	if got := l.remote("lab", "cat /root/site/index.html"); got != "home v2" {
		t.Fatalf("root tree %q", got)
	}
	l.ok("edit", "lab", "--sudo=false")
	if got := l.remote("lab", "id -u"); got == "0" {
		t.Fatal("sudo still active after --sudo=false")
	}
	l.fails("", "cp", local, "lab:/root/denied.txt")
}

func testSudoWithPassword(t *testing.T, l *lab) {
	l.ok("edit", "ops", "--sudo")
	// A password-protected sudo cannot prompt without a terminal.
	l.fails("sudo needs a password", "c", "ops", "id -u")
	term := l.terminal("edit", "ops", "--sudo-password")
	term.expect("sudo password:")
	term.send(opsPassword + "\n")
	if code, out := term.wait(); code != 0 {
		t.Fatalf("save sudo password:\n%s", out)
	}
	if got := l.remote("ops", "id -u"); got != "0" {
		t.Fatalf("escalated uid %q", got)
	}
	// The password prompt never leaks into command output.
	if out := l.ok("c", "ops", "echo done"); strings.TrimSpace(out) != "done" {
		t.Fatalf("output %q", out)
	}
	local := l.path("ops.txt")
	writeFile(t, local, "via sudo sftp\n", 0600)
	l.ok("cp", local, "ops:/root/ops.txt")
	if got := l.remote("ops", "cat /root/ops.txt"); got != "via sudo sftp" {
		t.Fatalf("sudo copy %q", got)
	}
}
