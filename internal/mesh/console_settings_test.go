package mesh

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestConsoleSettings_SavedAndCommittedInThePoolRepo(t *testing.T) {
	gitIdentity(t)
	root := legacyRoot(t)
	if _, err := UpgradePoolDir(root, time.Now()); err != nil {
		t.Fatal(err)
	}
	if s := ReadConsoleSettings(root); s != DefaultConsoleSettings() {
		t.Fatalf("no file: defaults, got %+v", s)
	}
	// Something else uncommitted in the pool repo stays out of the commit.
	_ = os.WriteFile(PoolConfigPath(root), []byte("knowledge: perspective\n"), 0o644)

	want := ConsoleSettings{Layout: "workspace", AgentOrder: "name", AttentionOnly: true}
	committed, err := WriteConsoleSettings(root, want)
	if err != nil || !committed {
		t.Fatalf("write: %v %v", committed, err)
	}
	if got := ReadConsoleSettings(root); got != want {
		t.Fatalf("read back %+v", got)
	}
	pool := PoolDir(root)
	if out, _ := git(pool, "log", "-1", "--name-only", "--format=%s"); !strings.Contains(out, "console: settings") || !strings.Contains(out, ConsoleSettingsFile) || strings.Contains(out, "config.yaml") {
		t.Errorf("last commit: %s", out)
	}
	if out, _ := git(pool, "status", "--porcelain"); !strings.Contains(out, "config.yaml") {
		t.Errorf("config.yaml must stay uncommitted: %q", out)
	}
	if committed, err := WriteConsoleSettings(root, want); err != nil || committed {
		t.Errorf("unchanged settings make no commit: %v %v", committed, err)
	}
	if _, err := WriteConsoleSettings(root, ConsoleSettings{Layout: "grid", AgentOrder: "name"}); err == nil {
		t.Error("an unknown layout must be refused")
	}
}
