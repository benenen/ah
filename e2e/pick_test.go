//go:build e2e

package e2e

import "testing"

func testPick(t *testing.T, l *lab) {
	terminal := l.terminal("pick")
	terminal.expect("Search: ")
	terminal.send("lab\r")
	terminal.send("echo picker-$((6*7))\n")
	terminal.expect("picker-42")
	terminal.send("exit\n")
	if code, out := terminal.wait(); code != 0 {
		t.Fatalf("picked shell exited %d: %s", code, out)
	}
	l.ok("edit", "ops", "--note", "picker-note-target", "--tag", "pick-only")
	terminal = l.terminal("pick", "--tag", "pick-only", "picker-note-target")
	terminal.expect("Search: picker-note-target")
	terminal.send("\r")
	terminal.send("echo picker-user-$(id -un)\n")
	terminal.expect("picker-user-ops")
	terminal.send("exit\n")
	if code, out := terminal.wait(); code != 0 {
		t.Fatalf("picked password connection exited %d: %s", code, out)
	}
	terminal = l.terminal("pick")
	terminal.expect("Search: ")
	terminal.send("\x1b")
	if code, out := terminal.wait(); code != 0 {
		t.Fatalf("Esc cancellation exited %d: %s", code, out)
	}
}
