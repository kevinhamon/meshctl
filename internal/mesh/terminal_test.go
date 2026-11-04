package mesh

import "testing"

// fakeProcs installs a fake process tree for the terminal walk.
func fakeProcs(t *testing.T, tree map[int]struct {
	ppid      int
	tty, comm string
}) {
	t.Helper()
	oldTTY, oldComm := psParentTTY, psComm
	psParentTTY = func(pid int) (int, string, bool) {
		n, ok := tree[pid]
		return n.ppid, n.tty, ok
	}
	psComm = func(pid int) string { return tree[pid].comm }
	t.Cleanup(func() { psParentTTY, psComm = oldTTY, oldComm })
}

// The walk passes through the harness's own processes (opencode's launcher
// and its native binary) to the one holding the terminal, as measured in the
// opencode rig.
func TestHarnessTerminal_ThroughHarnessChain(t *testing.T) {
	fakeProcs(t, map[int]struct {
		ppid      int
		tty, comm string
	}{
		500: {400, "", "/bin/sh"},
		400: {300, "", "/opt/homebrew/Cellar/opencode-v2/2.2.5/bin/opencode"},
		300: {200, "ttys006", "opencode"},
		200: {1, "ttys006", "-zsh"},
	})
	if pid, tty := harnessTerminal(500); pid != 300 || tty != "/dev/ttys006" {
		t.Fatalf("got %d %q", pid, tty)
	}
}

// A harness with no terminal (a background service, a headless run) records
// none: the walk never borrows the terminal of a process above the harness.
func TestHarnessTerminal_StopsAboveTheHarness(t *testing.T) {
	fakeProcs(t, map[int]struct {
		ppid      int
		tty, comm string
	}{
		500: {400, "", "/bin/sh"},
		400: {300, "", "opencode"},
		300: {200, "", "timeout"},
		200: {100, "", "/bin/zsh"},
		100: {1, "ttys005", "claude"}, // someone else's session, higher up
		600: {550, "", "meshctl"},
		550: {1, "", "/opt/homebrew/Cellar/opencode-v2/2.2.5/bin/opencode"}, // the background service
	})
	if pid, tty := harnessTerminal(500); pid != 0 || tty != "" {
		t.Fatalf("borrowed a terminal from above the harness: %d %q", pid, tty)
	}
	if pid, tty := harnessTerminal(600); pid != 0 || tty != "" {
		t.Fatalf("service-run hook got a terminal: %d %q", pid, tty)
	}
}
