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
	if _, err := New(pool, spec, ScaffoldOptions{}); err != nil {
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
	if after, _ := os.ReadFile(v1); string(after) != string(before) {
		t.Fatal("--check must not write")
	}
	if fileExists(filepath.Join(a.Dir(), filepath.FromSlash(opencodePluginV2Rel))) {
		t.Fatal("--check must not write the V2 plugin")
	}
}

func TestPoolUpgrade_ApplyRewiresThenConverges(t *testing.T) {
	pool, a := v1Pool(t)
	if _, err := UpgradeDoctrine(pool, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := UpgradeAgents(pool)
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
	dest := filepath.Join(pool, DoctrineDir)
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
