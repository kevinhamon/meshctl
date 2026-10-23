package mesh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitIdentity pins the git identity meshctl's own commits pick up from the
// environment, so first-commit authorship is deterministic in tests. Production
// code sets no identity: the user's configured one is the point (ADR-0066).
func gitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@e")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@e")
}

// legacyRoot is meshRoot made a real legacy pool: the hidden directory carries
// a marker, a local doctrine addendum and runtime files, the shape every pool
// on a machine had before ADR-0066.
func legacyRoot(t *testing.T) string {
	t.Helper()
	dir := meshRoot(t)
	if err := WritePoolMarker(dir, "legacy-pool", "v0", time.Now(), false); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(DoctrinePath(dir), 0o755)
	os.WriteFile(DoctrineLocalPath(dir), []byte("# house rules\n"), 0o644)
	os.WriteFile(DispatchLogPath(dir), []byte("old dispatch\n"), 0o644)
	os.WriteFile(DispatchCountPath(dir), []byte("1\n"), 0o644)
	os.MkdirAll(filepath.Join(WorktreesPath(dir), "architect-s1"), 0o755)
	os.WriteFile(filepath.Join(WorktreesPath(dir), "architect-s1", "f"), []byte("wt"), 0o644)
	return dir
}

// migratedRoot is the pool fixture for everything after ADR-0066: a pool whose
// directory is the visible pool/ git repo on main with the ignore file and the
// first commit in place. Later tickets (pool writes, PostToolUse commit, repo
// nag) build on it.
func migratedRoot(t *testing.T) string {
	t.Helper()
	gitIdentity(t)
	dir := legacyRoot(t)
	if _, err := UpgradePoolDir(dir, time.Now()); err != nil {
		t.Fatalf("migrate fixture: %v", err)
	}
	return dir
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPoolUpgrade_CheckPreviewsPoolDirMigrationWithoutWriting(t *testing.T) {
	root := legacyRoot(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	plan, err := PlanPoolUpgrade(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Pending() {
		t.Fatalf("a legacy pool directory must make the upgrade pending: %+v", plan)
	}
	joined := strings.Join(plan.Pool, "\n")
	for _, want := range []string{"rename .agentmesh/ → pool/", "git init", "pool/.gitignore", "first commit"} {
		if !strings.Contains(joined, want) {
			t.Errorf("plan missing %q:\n%s", want, joined)
		}
	}
	if !dirExists(filepath.Join(root, ".agentmesh")) || dirExists(filepath.Join(root, "pool")) {
		t.Fatal("--check must not move the pool directory")
	}
}

func TestPoolUpgrade_ApplyMigratesPoolDir(t *testing.T) {
	gitIdentity(t)
	root := legacyRoot(t)
	now := time.Now()

	did, err := UpgradePoolDir(root, now)
	if err != nil {
		t.Fatal(err)
	}
	pool := filepath.Join(root, "pool")
	if dirExists(filepath.Join(root, ".agentmesh")) || PoolDir(root) != pool {
		t.Fatalf("pool directory not renamed: did=%v", did)
	}
	// Knowledge moved with the directory; runtime files moved too.
	if b, _ := os.ReadFile(DoctrineLocalPath(root)); string(b) != "# house rules\n" {
		t.Fatal("local doctrine lost in the rename")
	}
	if b, _ := os.ReadFile(DispatchLogPath(root)); string(b) != "old dispatch\n" {
		t.Fatal("dispatch log lost in the rename")
	}
	// A git repo on main with the user's identity on the first commit.
	if gitOut(t, pool, "rev-parse", "--abbrev-ref", "HEAD") != "main" {
		t.Fatal("pool repo is not on main")
	}
	if got := gitOut(t, pool, "log", "-1", "--format=%an <%ae>"); got != "t <t@e>" {
		t.Fatalf("first commit author = %q, want the user's git identity", got)
	}
	if n := gitOut(t, pool, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("want exactly one commit, got %s", n)
	}
	// Ignore file covers runtime state; the first commit tracks knowledge only.
	ig, err := os.ReadFile(PoolIgnorePath(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dispatch.log", "dispatch.count", "dispatch.lock", "worktrees/"} {
		if !strings.Contains(string(ig), want) {
			t.Errorf("ignore file lacks %s:\n%s", want, ig)
		}
	}
	tracked := gitOut(t, pool, "ls-files")
	for _, want := range []string{"pool.yaml", "doctrine/local.md", ".gitignore"} {
		if !strings.Contains(tracked, want) {
			t.Errorf("first commit does not track %s:\n%s", want, tracked)
		}
	}
	for _, no := range []string{"dispatch", "worktrees"} {
		if strings.Contains(tracked, no) {
			t.Errorf("runtime file %s must not be tracked:\n%s", no, tracked)
		}
	}
	if st := gitOut(t, pool, "status", "--porcelain"); st != "" {
		t.Fatalf("pool repo dirty right after migration:\n%s", st)
	}
	if !fileExists(PoolGlossaryPath(root)) {
		t.Fatal("pool glossary not created")
	}

	// Already migrated: nothing pending, apply is a no-op.
	plan, err := PlanPoolUpgrade(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pool) != 0 {
		t.Fatalf("migrated pool still plans directory steps: %v", plan.Pool)
	}
	did2, err := UpgradePoolDir(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(did2) != 0 {
		t.Fatalf("second upgrade must be a no-op, did %v", did2)
	}
	if n := gitOut(t, pool, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("no-op upgrade added a commit: %s", n)
	}
}

func TestPoolUpgrade_RefusesToClobberForeignPoolDir(t *testing.T) {
	root := legacyRoot(t)
	os.MkdirAll(filepath.Join(root, "pool", "src"), 0o755)
	plan, err := PlanPoolUpgrade(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Warnings, "\n"), "pool/ already exists") {
		t.Fatalf("plan must warn about the foreign pool/ dir: %v", plan.Warnings)
	}
	if _, err := UpgradePoolDir(root, time.Now()); err == nil {
		t.Fatal("apply must refuse to rename over a foreign pool/ dir")
	}
	if !dirExists(filepath.Join(root, ".agentmesh")) || !dirExists(filepath.Join(root, "pool", "src")) {
		t.Fatal("refusal must leave both directories untouched")
	}
}

func TestOnboard_RefreshesRenderedPointersAfterMigration(t *testing.T) {
	root := legacyRoot(t)
	a := newTestAgent(t, root, "architect", true, nil)
	claude := filepath.Join(a.Dir(), "CLAUDE.md")
	if b, _ := os.ReadFile(claude); !strings.Contains(string(b), "../.agentmesh/doctrine/agent-comms.md") {
		t.Fatalf("legacy pool must render the legacy pointer:\n%s", b)
	}

	gitIdentity(t)
	if _, err := UpgradePoolDir(root, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := Onboard(root, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"CLAUDE.md",
		filepath.Join(IntakeDir, "README.md"),
		filepath.Join(".claude", "skills", "mesh-cognition", "SKILL.md"),
		filepath.Join(".claude", "agents", "distiller.md"),
	} {
		b, err := os.ReadFile(filepath.Join(a.Dir(), f))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), ".agentmesh") {
			t.Errorf("%s still points at the legacy directory after re-onboard", f)
		}
		if !strings.Contains(string(b), "pool/doctrine/") {
			t.Errorf("%s does not point at pool/doctrine/:\n%s", f, b)
		}
	}
}

func TestMigratedRootFixture_IsAPoolRepo(t *testing.T) {
	root := migratedRoot(t)
	if PoolDirName(root) != "pool" || !IsPool(root) {
		t.Fatal("fixture must resolve to pool/")
	}
	if gitOut(t, PoolDir(root), "status", "--porcelain") != "" {
		t.Fatal("fixture repo must start clean")
	}
	a := newTestAgent(t, root, "dev", true, nil)
	if findings, _ := Doctor(root); hasFinding(findings, "legacy hidden") {
		t.Fatalf("doctor must not flag a migrated pool: %+v", findings)
	}
	_ = a
}

func TestDoctor_WarnsOnLegacyPoolDir(t *testing.T) {
	root := legacyRoot(t)
	newTestAgent(t, root, "dev", true, nil)
	findings, _ := Doctor(root)
	if !hasFinding(findings, "meshctl pool upgrade") {
		t.Fatalf("doctor should point a legacy pool at pool upgrade: %+v", findings)
	}
}
