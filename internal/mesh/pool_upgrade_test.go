package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// v1Pool builds a pool with one opencode agent wired for opencode V1 — the shape
// of a pool created before opencode V2 — with the machine now on V2.
func v1Pool(t *testing.T) (pool string, a *Agent) {
	t.Helper()
	pool = meshRoot(t)
	spec := AgentSpec{Name: "oc", Title: "OC", Role: "r", Accepts: []Accept{{Type: "fyi", Desc: "x"}},
		Badge: Badge{Label: "OC"}, Harness: HarnessOpencode, HarnessVersion: 1, Bare: true}
	if _, err := New(pool, spec); err != nil {
		t.Fatal(err)
	}
	a, err := FindAgent(pool, "oc")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(opencodeMajorEnv, "2")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no global opencode config
	return pool, a
}

func TestPoolUpgrade_CheckPreviewsWithoutWriting(t *testing.T) {
	pool, a := v1Pool(t)
	v1 := filepath.Join(a.Dir(), filepath.FromSlash(opencodePluginV1Rel))
	before, _ := os.ReadFile(v1)

	plan, err := PlanPoolUpgrade(pool, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Pending() || plan.OpencodeMajor != 2 {
		t.Fatalf("want a pending V2 upgrade, got %+v", plan)
	}
	var ap AgentUpgradePlan
	for _, x := range plan.Agents {
		if x.Name == "oc" {
			ap = x
		}
	}
	joined := strings.Join(ap.Actions, "\n")
	if !strings.Contains(joined, "write opencode V2 plugin") || !strings.Contains(joined, "remove "+opencodePluginV1Rel) {
		t.Fatalf("plan missing the plugin swap: %v", ap.Actions)
	}
	if !strings.Contains(joined, "add the mesh status panel "+opencodeTUIRel) {
		t.Fatalf("plan missing the status panel: %v", ap.Actions)
	}
	if after, _ := os.ReadFile(v1); string(after) != string(before) {
		t.Fatal("--check must not write")
	}
	if fileExists(filepath.Join(a.Dir(), filepath.FromSlash(opencodePluginV2Rel))) {
		t.Fatal("--check must not write the V2 plugin")
	}
}

func TestPoolUpgrade_ApplyRewiresThenConverges(t *testing.T) {
	pool, a := v1Pool(t)
	// The apply path in order: pool directory, doctrine, agents.
	gitIdentity(t)
	if _, err := UpgradePoolDir(pool, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeDoctrine(pool, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := UpgradeAgents(pool, "vtest")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("no agents upgraded")
	}
	if !fileExists(filepath.Join(a.Dir(), filepath.FromSlash(opencodePluginV2Rel))) ||
		fileExists(filepath.Join(a.Dir(), filepath.FromSlash(opencodePluginV1Rel))) {
		t.Fatal("upgrade must swap the V1 plugin for the V2 one")
	}
	if opencodePluginState(a.Dir(), opencodeTUIRel, opencodeTUIPlugin) != pluginCurrent {
		t.Fatal("upgrade must add the opencode status panel")
	}
	if fs := opencodeGateFindings(a); len(fs) != 0 {
		t.Fatalf("doctor should be clean after upgrade: %+v", fs)
	}
	plan, err := PlanPoolUpgrade(pool, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Pending() {
		t.Fatalf("a second check must report nothing pending: %+v", plan)
	}
}

func TestPoolUpgrade_UnstampedDoctrineIsAConflictNotAnUpdate(t *testing.T) {
	pool := meshRoot(t)
	dest := DoctrinePath(pool)
	os.MkdirAll(dest, 0o755)
	os.WriteFile(filepath.Join(dest, "agent-comms.md"), []byte("old pool copy"), 0o644)
	_, conflicts, err := planDoctrine(pool)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range conflicts {
		found = found || (strings.Contains(c, "agent-comms.md") && strings.Contains(c, "unstamped"))
	}
	if !found {
		t.Fatalf("unstamped differing doctrine must be a flagged conflict: %v", conflicts)
	}
}

func TestPoolUpgrade_GlobalOpencodeConfigV1Keys(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	os.MkdirAll(filepath.Join(cfg, "opencode"), 0o755)
	os.WriteFile(filepath.Join(cfg, "opencode", "opencode.jsonc"), []byte(`{
  // global config — comments and trailing commas are JSONC
  "$schema": "https://opencode.ai/config.json", /* url has // inside a string */
  "plugin": ["x"],
  "mcp": { "fs": { "type": "local" } },
  "model": "a/b",
}`), 0o644)
	ws := opencodeGlobalConfigWarnings()
	if len(ws) != 1 || !strings.Contains(ws[0], "plugin → plugins") || !strings.Contains(ws[0], "mcp → mcp.servers") ||
		strings.Contains(ws[0], "model") {
		t.Fatalf("want plugin + mcp flagged, model not: %v", ws)
	}
	os.WriteFile(filepath.Join(cfg, "opencode", "opencode.jsonc"), []byte(`{"plugins": [], "mcp": {"servers": {}}}`), 0o644)
	if ws := opencodeGlobalConfigWarnings(); len(ws) != 0 {
		t.Fatalf("V2 config must be clean: %v", ws)
	}
}

func TestPoolUpgrade_AddsMissingStatusLine(t *testing.T) {
	pool := meshRoot(t)
	a := newTestAgent(t, pool, "architect", true, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	settings := filepath.Join(a.Dir(), ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	const action = "add the mesh status line to .claude/settings.json"
	hasAction := func() bool {
		plan, err := PlanPoolUpgrade(pool, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range plan.Agents {
			if x.Name == a.Name {
				return strings.Contains(strings.Join(x.Actions, "\n"), action)
			}
		}
		return false
	}
	if !hasAction() {
		t.Fatal("--check must report the missing status line")
	}
	if _, err := UpgradeAgents(pool, "vtest"); err != nil {
		t.Fatal(err)
	}
	if !hasStatusLine(a.Dir()) {
		t.Fatal("upgrade did not wire the status line")
	}
	if hasAction() {
		t.Fatal("a second check must not report the status line again")
	}
}

// Issue #10: the upgrade commits exactly what it wrote in each agent repo,
// leaves the person's uncommitted work alone, and pushes nothing.
func TestPoolUpgrade_CommitsOnlyWhatItWrote(t *testing.T) {
	pool, a := v1Pool(t)
	gitIdentity(t)
	initAgentRepo(a.Dir())
	if _, err := git(a.Dir(), "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := git(a.Dir(), "commit", "-q", "--allow-empty", "-m", "baseline"); err != nil {
		t.Fatal(err)
	}
	// The person's own uncommitted work: one file the upgrade never touches,
	// one it rewrites (opencode.json gains ask rules).
	notes := filepath.Join(a.Dir(), "notes.md")
	if err := os.WriteFile(notes, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ocPath := filepath.Join(a.Dir(), "opencode.json")
	oc, _ := os.ReadFile(ocPath)
	if err := os.WriteFile(ocPath, []byte(strings.Replace(string(oc), "{", "{\n  \"username\": \"mine\",", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := UpgradeAgents(pool, "vtest")
	if err != nil {
		t.Fatal(err)
	}
	var ap AgentUpgradePlan
	for _, x := range res {
		if x.Name == "oc" {
			ap = x
		}
	}
	if !strings.Contains(strings.Join(ap.Actions, "\n"), "committed") {
		t.Fatalf("want a commit action, got %v", ap.Actions)
	}
	if !strings.Contains(strings.Join(ap.Warnings, "\n"), "opencode.json") {
		t.Fatalf("the hand-edited file the upgrade changed must be named, got %v", ap.Warnings)
	}
	subject, _ := git(a.Dir(), "log", "-1", "--format=%s")
	if strings.TrimSpace(subject) != UpgradeWiringSubject("vtest") {
		t.Fatalf("want the upgrade subject, got %q", subject)
	}
	files, _ := git(a.Dir(), "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(files, opencodePluginV2Rel) || strings.Contains(files, "notes.md") || strings.Contains(files, "opencode.json") {
		t.Fatalf("commit must hold only the upgrade's own files:\n%s", files)
	}
	status, _ := git(a.Dir(), "status", "--porcelain")
	if !strings.Contains(status, "notes.md") || !strings.Contains(status, "opencode.json") {
		t.Fatalf("the person's file must stay uncommitted:\n%s", status)
	}
}

// symlinkedSkillRepo is an agent repo whose tracked skill file the upgrade
// has just replaced with a symlinked directory into the pool (issue #23).
func symlinkedSkillRepo(t *testing.T) (dir string, before map[string]string) {
	t.Helper()
	gitIdentity(t)
	root := t.TempDir()
	dir = filepath.Join(root, "agent")
	skill := filepath.Join(dir, ".opencode", "skills", "mesh-cognition")
	poolSkill := filepath.Join(root, "pool", "skills", "mesh-cognition")
	for _, d := range []string{skill, poolSkill} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(skill, "SKILL.md"), "old copy\n")
	mustWrite(filepath.Join(poolSkill, "SKILL.md"), "pool copy\n")
	mustWrite(filepath.Join(dir, "notes.md"), "mine\n")
	initAgentRepo(dir) // commits everything above as the baseline
	if out, _ := git(dir, "ls-files"); !strings.Contains(out, "SKILL.md") {
		t.Fatalf("baseline must track the old skill file, got %q", out)
	}
	before = dirtySnapshot(dir)
	// What the upgrade writes: the link replaces the directory, and an
	// ignore line keeps the link itself out of the repo.
	if err := os.RemoveAll(skill); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../pool/skills/mesh-cognition", skill); err != nil {
		t.Fatal(err)
	}
	mustWrite(filepath.Join(dir, ".gitignore"), ".opencode/skills/mesh-*\n")
	return dir, before
}

func TestCommitChangedPaths_RemovalBehindSymlink(t *testing.T) {
	dir, before := symlinkedSkillRepo(t)
	action, warnings := commitChangedPaths(dir, before, "upgrade")
	if action == "" || len(warnings) > 0 {
		t.Fatalf("want a clean commit, got action %q warnings %v", action, warnings)
	}
	if status, _ := git(dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("repo must be clean after the upgrade commit:\n%s", status)
	}
	files, _ := git(dir, "show", "--name-status", "--format=", "HEAD")
	if !strings.Contains(files, "D\t.opencode/skills/mesh-cognition/SKILL.md") || !strings.Contains(files, ".gitignore") {
		t.Fatalf("commit must remove the old skill and add the ignore line:\n%s", files)
	}
	// Git must not have followed the link and committed the pool's copy.
	if out, err := git(dir, "ls-files", ".opencode"); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("nothing under .opencode may stay tracked, got %q (%v)", out, err)
	}
}

func TestCommitChangedPaths_RemovalBehindSymlinkWithPersonStaged(t *testing.T) {
	dir, before := symlinkedSkillRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("mine, edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := git(dir, "add", "notes.md"); err != nil {
		t.Fatal(err)
	}
	before["notes.md"] = dirtySnapshot(dir)["notes.md"] // the person's edit predates the upgrade
	action, warnings := commitChangedPaths(dir, before, "upgrade")
	if action == "" {
		t.Fatalf("the ignore line must still be committed, warnings %v", warnings)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "SKILL.md") {
		t.Fatalf("the removal left staged must be named, got %v", warnings)
	}
	files, _ := git(dir, "show", "--name-only", "--format=", "HEAD")
	if strings.Contains(files, "notes.md") || strings.Contains(files, "SKILL.md") {
		t.Fatalf("commit must not take the person's staged work or follow the link:\n%s", files)
	}
	status, _ := git(dir, "status", "--porcelain")
	if !strings.Contains(status, "M  notes.md") || !strings.Contains(status, "D  .opencode/skills/mesh-cognition/SKILL.md") {
		t.Fatalf("the person's edit and the removal must stay staged:\n%s", status)
	}
}
