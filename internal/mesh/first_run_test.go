package mesh

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Findings from a newcomer's first run against the public README.

func TestFindAgent_SuggestsSuffixedName(t *testing.T) {
	dir := meshRoot(t)
	spec := AgentSpec{Name: "steward", Title: "t", Role: "r", Accepts: []Accept{{Type: "fyi", Desc: "x"}}}
	if _, err := New(dir, spec); err != nil { // not bare: becomes steward-agent
		t.Fatal(err)
	}
	_, err := FindAgent(dir, "steward")
	if err == nil || !strings.Contains(err.Error(), `did you mean "steward-agent"`) {
		t.Fatalf("want a did-you-mean hint, got %v", err)
	}
	if _, err := FindAgent(dir, "nobody"); err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("no hint without a match: %v", err)
	}
}

func gitClean(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestInitPool_StewardStartsClean(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	if _, err := InitPool(root, "demo", now); err != nil {
		t.Fatal(err)
	}
	dir, _, err := ScaffoldSteward(root, HarnessClaude, now)
	if err != nil {
		t.Fatal(err)
	}
	if !isGitRepo(dir) {
		t.Skip("no git")
	}
	if dirty := gitClean(t, dir); dirty != "" {
		t.Fatalf("a fresh steward must start clean:\n%s", dirty)
	}
	b, _ := os.ReadFile(dir + "/CLAUDE.md")
	if !strings.Contains(string(b), "Steward") {
		t.Fatalf("steward role not in CLAUDE.md:\n%s", b)
	}
}

func TestRefreshPeerRoster_NewAgentReachesEarlierOnes(t *testing.T) {
	dir := meshRoot(t)
	arch := newTestAgent(t, dir, "architect", true, nil)
	dev := newTestAgent(t, dir, "developer", true, nil)
	if fileExists(PeerAgentFile(arch.Dir(), "developer")) {
		t.Skip("scaffold already refreshes earlier agents")
	}
	if PlanPeerAgents(dir, arch.Dir()).Empty() {
		t.Fatal("architect should be stale after developer joined")
	}
	lines, err := RefreshPeerRoster(dir, dev.Dir())
	if err != nil || len(lines) != 1 || !strings.HasPrefix(lines[0], "architect:") {
		t.Fatalf("%v %v", lines, err)
	}
	if !fileExists(PeerAgentFile(arch.Dir(), "developer")) {
		t.Fatal("architect has no in-session peer for developer")
	}
	if isGitRepo(arch.Dir()) {
		if dirty := gitClean(t, arch.Dir()); dirty != "" {
			t.Fatalf("the refresh must commit what it wrote:\n%s", dirty)
		}
	}
	if again, _ := RefreshPeerRoster(dir, dev.Dir()); len(again) != 0 {
		t.Fatalf("second run is a no-op: %v", again)
	}
}

func TestDoctor_WarnsWhenMeshctlNotOnPath(t *testing.T) {
	dir := meshRoot(t)
	newTestAgent(t, dir, "architect", true, nil)
	old := meshctlOnPath
	defer func() { meshctlOnPath = old }()
	meshctlOnPath = func() bool { return false }
	found := false
	for _, f := range mustDoctor(dir) {
		if strings.Contains(f.Message, "not on PATH") {
			found = true
		}
	}
	if !found {
		t.Fatal("doctor must warn when meshctl is not on PATH")
	}
}

func mustDoctor(dir string) []DoctorFinding {
	f, _ := Doctor(dir)
	return f
}
