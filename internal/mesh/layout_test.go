package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFreshPoolFootprint_ConsolidatedUnderAgentmesh(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "poly")
	if _, err := InitPool(root, "poly", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Everything under .agentmesh/.
	for _, p := range []string{
		".agentmesh/pool.yaml",
		".agentmesh/doctrine/" + CommsFile,
		".agentmesh/doctrine/distiller.md",
		".agentmesh/worktrees",
	} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatalf("expected %s: %v", p, err)
		}
	}
	// No legacy top-level agent-mesh/ dir.
	if dirExists(filepath.Join(root, "agent-mesh")) {
		t.Fatal("fresh pool must not create a top-level agent-mesh/ dir")
	}
	if IsLegacyLayout(root) {
		t.Fatal("fresh pool should not read as legacy layout")
	}
}

func TestNaming_AgentSuffix(t *testing.T) {
	dir := meshRoot(t)
	// Default: -agent suffix.
	if _, err := New(dir, AgentSpec{Name: "scribe", Title: "S", Accepts: []Accept{{Type: "fyi", Desc: "x"}}, Badge: Badge{Label: "S"}}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if !dirExists(filepath.Join(dir, "scribe-agent")) {
		t.Fatal("expected dir scribe-agent")
	}
	if _, err := FindAgent(dir, "scribe-agent"); err != nil {
		t.Fatalf("agent should resolve by scribe-agent: %v", err)
	}
	// --bare: exact name, no suffix.
	if _, err := New(dir, AgentSpec{Name: "raw", Title: "R", Accepts: []Accept{{Type: "fyi", Desc: "x"}}, Badge: Badge{Label: "R"}, Bare: true}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if !dirExists(filepath.Join(dir, "raw")) || dirExists(filepath.Join(dir, "raw-agent")) {
		t.Fatal("--bare should produce dir 'raw', not 'raw-agent'")
	}
	// No double-suffix.
	if _, err := New(dir, AgentSpec{Name: "ops-agent", Title: "O", Accepts: []Accept{{Type: "fyi", Desc: "x"}}, Badge: Badge{Label: "O"}}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if dirExists(filepath.Join(dir, "ops-agent-agent")) {
		t.Fatal("must not double-suffix an already -agent name")
	}
}

func TestMigratePool_LegacyToConsolidated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "legacy")
	// Hand-build a legacy pool: marker + top-level agent-mesh/ doctrine + .worktrees + .dispatch.log.
	os.MkdirAll(filepath.Join(root, PoolMarkerDir), 0o755)
	os.MkdirAll(filepath.Join(root, "agent-mesh"), 0o755)
	os.WriteFile(filepath.Join(root, "agent-mesh", CommsFile), []byte("old doctrine"), 0o644)
	os.MkdirAll(filepath.Join(root, ".worktrees", "x"), 0o755)
	os.WriteFile(filepath.Join(root, ".dispatch.log"), []byte("l\n"), 0o644)

	if !IsLegacyLayout(root) {
		t.Fatal("should detect legacy layout")
	}
	if _, err := MigratePool(root); err != nil {
		t.Fatal(err)
	}
	// Doctrine now under .agentmesh/doctrine; worktrees + dispatch moved; legacy dir gone.
	if !fileExists(filepath.Join(root, DoctrineDir, CommsFile)) {
		t.Fatal("doctrine not emitted under .agentmesh/doctrine")
	}
	if !dirExists(filepath.Join(root, WorktreesDir)) {
		t.Fatal(".worktrees not moved under .agentmesh")
	}
	if dirExists(filepath.Join(root, "agent-mesh")) {
		t.Fatal("legacy agent-mesh/ (non-repo) should be removed")
	}
	if IsLegacyLayout(root) {
		t.Fatal("still reads as legacy after migrate")
	}
}

func TestMigratePool_KeepsGitRepoDoctrineDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "poollike")
	os.MkdirAll(filepath.Join(root, PoolMarkerDir), 0o755)
	// agent-mesh/ is a git repo (the real meshctl clone) — must be preserved.
	os.MkdirAll(filepath.Join(root, "agent-mesh", ".git"), 0o755)
	os.WriteFile(filepath.Join(root, "agent-mesh", CommsFile), []byte("real repo"), 0o644)

	if _, err := MigratePool(root); err != nil {
		t.Fatal(err)
	}
	if !dirExists(filepath.Join(root, "agent-mesh", ".git")) {
		t.Fatal("must NOT delete a git-repo agent-mesh/ dir")
	}
	if !fileExists(filepath.Join(root, DoctrineDir, CommsFile)) {
		t.Fatal("doctrine should still be emitted under .agentmesh even when legacy dir kept")
	}
	// A kept git-repo agent-mesh/ must NOT read as legacy (no doctor false-positive).
	if IsLegacyLayout(root) {
		t.Fatal("git-repo agent-mesh/ kept by migrate should not trip IsLegacyLayout")
	}
}

func TestTeardownPool(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "keeper", false, nil)
	RegisterPool(dir, "tp", time.Now())

	did, err := TeardownPool(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if IsPool(dir) {
		t.Fatal(".agentmesh should be removed after teardown")
	}
	if !dirExists(a.Dir()) {
		t.Fatal("teardown without --purge must keep agent repos")
	}
	if !strings.Contains(strings.Join(did, " "), "deregistered") {
		t.Fatalf("expected deregistration note, got %v", did)
	}
	pools, _ := RegisteredPools()
	for _, e := range pools {
		if e.Name == "tp" {
			t.Fatal("pool should be deregistered")
		}
	}
}
