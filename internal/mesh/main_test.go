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
	os.Exit(m.Run())
}
