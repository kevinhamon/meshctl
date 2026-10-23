package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFreshPoolFootprint_ConsolidatedUnderPoolDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gitIdentity(t)
	root := filepath.Join(t.TempDir(), "poly")
	if _, err := InitPool(root, "poly", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Everything under the visible pool/ (ADR-0066), which is a git repo.
	for _, p := range []string{
		"pool/pool.yaml",
		"pool/doctrine/" + CommsFile,
		"pool/doctrine/distiller.md",
		"pool/worktrees",
		"pool/glossary.md",
		"pool/.gitignore",
		"pool/.git",
	} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatalf("expected %s: %v", p, err)
		}
	}
	if dirExists(filepath.Join(root, ".agentmesh")) {
		t.Fatal("fresh pool must not create the legacy hidden dir")
	}
	// No legacy top-level agent-mesh/ dir.
	if dirExists(filepath.Join(root, "agent-mesh")) {
		t.Fatal("fresh pool must not create a top-level agent-mesh/ dir")
	}
	if IsLegacyLayout(root) || PoolDirIsLegacy(root) {
		t.Fatal("fresh pool should not read as legacy layout")
	}
	pool := filepath.Join(root, "pool")
	if gitOut(t, pool, "rev-parse", "--abbrev-ref", "HEAD") != "main" {
		t.Fatal("pool repo is not on main")
	}
	if got := gitOut(t, pool, "log", "-1", "--format=%an <%ae>"); got != "t <t@e>" {
		t.Fatalf("first commit author = %q, want the user's git identity", got)
	}
	// Runtime files are ignored: writing them leaves the repo clean.
	os.WriteFile(DispatchLogPath(root), []byte("x\n"), 0o644)
	os.MkdirAll(filepath.Join(WorktreesPath(root), "a-s1"), 0o755)
	os.WriteFile(filepath.Join(WorktreesPath(root), "a-s1", "f"), []byte("x"), 0o644)
	if st := gitOut(t, pool, "status", "--porcelain"); st != "" {
		t.Fatalf("runtime files must be ignored:\n%s", st)
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
	WritePoolMarker(root, "legacy", "v", time.Now(), false)
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
	// Doctrine now under the pool doctrine dir; worktrees + dispatch moved; legacy dir gone.
	if !fileExists(filepath.Join(DoctrinePath(root), CommsFile)) {
		t.Fatal("doctrine not emitted under the pool doctrine dir")
	}
	if !dirExists(WorktreesPath(root)) {
		t.Fatal(".worktrees not moved under the pool dir")
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
	WritePoolMarker(root, "poollike", "v", time.Now(), false)
	// agent-mesh/ is a git repo (the real meshctl clone) — must be preserved.
	os.MkdirAll(filepath.Join(root, "agent-mesh", ".git"), 0o755)
	os.WriteFile(filepath.Join(root, "agent-mesh", CommsFile), []byte("real repo"), 0o644)

	if _, err := MigratePool(root); err != nil {
		t.Fatal(err)
	}
	if !dirExists(filepath.Join(root, "agent-mesh", ".git")) {
		t.Fatal("must NOT delete a git-repo agent-mesh/ dir")
	}
	if !fileExists(filepath.Join(DoctrinePath(root), CommsFile)) {
		t.Fatal("doctrine should still be emitted under the pool dir even when legacy dir kept")
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
		t.Fatal("pool dir should be removed after teardown")
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
