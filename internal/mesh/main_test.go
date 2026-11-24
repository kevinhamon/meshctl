package mesh

import (
	"os"
	"testing"
)

// TestMain clears the markers a harness exports to its tool subprocesses
// (RunningHarness, sessionIDEnv), so the suite behaves the same run from a
// Claude Code session, an opencode session or a plain terminal. A test that
// needs one sets it with t.Setenv.
func TestMain(m *testing.M) {
	for _, k := range []string{"CLAUDECODE", "OPENCODE", "OPENCODE_SESSION_ID", "OPENCODE_CONFIG_CONTENT"} {
		os.Unsetenv(k)
	}
	meshctlOnPath = func() bool { return true } // CI has no installed meshctl
	// Host-level state (pool registry, installed-harness cache) lives under
	// XDG_CONFIG_HOME: point it at a throwaway dir so no test can write the
	// real host's files. Tests that need their own still t.Setenv over it.
	xdg, err := os.MkdirTemp("", "meshctl-test-xdg-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", xdg)
	code := m.Run()
	os.RemoveAll(xdg)
	os.Exit(code)
}
