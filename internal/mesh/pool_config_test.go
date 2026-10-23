package mesh

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadPoolConfig_IdentityAndSettingsFromSplitFiles(t *testing.T) {
	root := migratedRoot(t)
	os.WriteFile(PoolConfigPath(root), []byte(`knowledge: perspective
outbound:
  tracker:
    tools: ['create.*', 'transition.*']
    commands: ['curl .*api\.example\.test']
reconcile:
  order: [architect, developer]
tools:
  - { cmd: tea, use: "Gitea CLI" }
`), 0o644)

	c := LoadPoolConfig(root)
	if c.Name != "legacy-pool" {
		t.Fatalf("identity must come from pool.yaml, got name %q", c.Name)
	}
	if c.Settings.Knowledge != "perspective" {
		t.Fatalf("knowledge = %q", c.Settings.Knowledge)
	}
	s, ok := c.Settings.Outbound["tracker"]
	if !ok || len(s.Tools) != 2 || s.Tools[1] != "transition.*" || len(s.Commands) != 1 || s.Commands[0] != `curl .*api\.example\.test` {
		t.Fatalf("outbound surface not parsed: %+v", c.Settings.Outbound)
	}
	if strings.Join(c.Settings.Reconcile.Order, ",") != "architect,developer" {
		t.Fatalf("reconcile.order = %v", c.Settings.Reconcile.Order)
	}
	if len(c.Tools) != 1 || c.Tools[0].Cmd != "tea" || len(c.IdentityTools) != 0 {
		t.Fatalf("tools from config.yaml not exposed: tools=%+v identity=%+v", c.Tools, c.IdentityTools)
	}
	if c.ConfigErr != nil {
		t.Fatalf("unexpected config error: %v", c.ConfigErr)
	}
}

func TestLoadPoolConfig_MissingConfigFileIsEmptySettings(t *testing.T) {
	root := migratedRoot(t)
	os.Remove(PoolConfigPath(root))
	c := LoadPoolConfig(root)
	if c.ConfigErr != nil {
		t.Fatalf("a missing config file is not an error: %v", c.ConfigErr)
	}
	if c.Name != "legacy-pool" || c.Settings.Knowledge != "" || len(c.Settings.Outbound) != 0 || len(c.Settings.Reconcile.Order) != 0 || len(c.Tools) != 0 {
		t.Fatalf("want identity only, got %+v", c)
	}
}

func TestLoadPoolConfig_UnparseableConfigIsReported(t *testing.T) {
	root := migratedRoot(t)
	os.WriteFile(PoolConfigPath(root), []byte("outbound: [not: a: map\n"), 0o644)
	c := LoadPoolConfig(root)
	if c.ConfigErr == nil {
		t.Fatal("a broken config file must surface as ConfigErr so the outbound gate can fail closed")
	}
	findings, _ := Doctor(root)
	if !hasFinding(findings, "config.yaml cannot be parsed") {
		t.Fatalf("doctor must report the broken config: %+v", findings)
	}
}

func TestLoadPoolConfig_ToolsStillInIdentityFileAreHonouredWithDoctorWarning(t *testing.T) {
	root := migratedRoot(t)
	os.WriteFile(PoolMarkerPath(root), []byte("name: legacy-pool\ncreated: 2026-07-25T00:00:00Z\ndoctrine_version: v0\ntools:\n  - { cmd: tea, use: \"Gitea CLI\" }\n"), 0o644)
	os.WriteFile(PoolConfigPath(root), []byte("knowledge: canonical\ntools:\n  - { cmd: myx, use: \"deploy\" }\n"), 0o644)

	c := LoadPoolConfig(root)
	cmds := []string{}
	for _, tr := range c.Tools {
		cmds = append(cmds, tr.Cmd)
	}
	if strings.Join(cmds, ",") != "myx,tea" {
		t.Fatalf("effective tools must be config first, then the identity-file leftovers: %v", cmds)
	}
	if len(c.IdentityTools) != 1 || c.IdentityTools[0].Cmd != "tea" {
		t.Fatalf("identity-file tools not exposed for the transition: %+v", c.IdentityTools)
	}
	findings, _ := Doctor(root)
	if !hasFinding(findings, "pool.yaml still carries `tools`") {
		t.Fatalf("doctor must warn that tools belong in config.yaml: %+v", findings)
	}

	// A pool with no tools in the identity file gets no such warning.
	root2 := migratedRoot(t)
	if findings, _ := Doctor(root2); hasFinding(findings, "still carries `tools`") {
		t.Fatalf("no transition warning on a clean pool: %+v", findings)
	}
}

func TestPoolInit_WritesConfigWithOutboundExample(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gitIdentity(t)
	root := t.TempDir() + "/fresh"
	if _, err := InitPool(root, "fresh", time.Now()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(PoolConfigPath(root))
	if err != nil {
		t.Fatalf("pool init must write config.yaml: %v", err)
	}
	for _, want := range []string{"# outbound:", "#   <surface>:", "must equal a `mutates` tag", "knowledge: canonical"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config example lacks %q:\n%s", want, b)
		}
	}
	// The identity file is binary-managed only: no tools example left in it.
	if mb, _ := os.ReadFile(PoolMarkerPath(root)); strings.Contains(string(mb), "tools:") {
		t.Fatalf("pool.yaml must not carry the tools example any more:\n%s", mb)
	}
	// The example parses back as a pool with no outbound surfaces.
	c := LoadPoolConfig(root)
	if c.ConfigErr != nil || c.Settings.Knowledge != "canonical" || len(c.Settings.Outbound) != 0 || len(c.Tools) != 0 {
		t.Fatalf("init example must parse as canonical with no surfaces: %+v (err %v)", c.Settings, c.ConfigErr)
	}
	// Tracked by the first commit, so the human's edits have history from the start.
	if tracked := gitOut(t, PoolDir(root), "ls-files"); !strings.Contains(tracked, PoolConfigFile) {
		t.Fatalf("config.yaml must be tracked:\n%s", tracked)
	}
}

func TestPoolUpgrade_SplitsToolsOutOfIdentityFile(t *testing.T) {
	root := migratedRoot(t)
	pool := PoolDir(root)
	// A pool migrated before the split: tools still live in the identity file.
	os.WriteFile(PoolMarkerPath(root), []byte("name: legacy-pool\ncreated: 2026-07-25T00:00:00Z\ndoctrine_version: v0\ntools:\n  - { cmd: tea, use: \"Gitea CLI\", help: \"tea --help\" }\n  - { cmd: gh, avoid: \"use tea\" }\n"), 0o644)
	gitCommitAll(t, pool, "fixture: tools in pool.yaml")
	before := gitOut(t, pool, "rev-list", "--count", "HEAD")

	// --check previews without writing.
	plan, err := PlanPoolUpgrade(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Pool, "\n")
	if !strings.Contains(joined, "move `tools` from pool/pool.yaml to pool/config.yaml") || !strings.Contains(joined, "config: split from pool.yaml") {
		t.Fatalf("plan must preview the split:\n%s", joined)
	}
	if !plan.Pending() {
		t.Fatal("the split must make the upgrade pending")
	}
	if mb, _ := os.ReadFile(PoolMarkerPath(root)); !strings.Contains(string(mb), "tools:") {
		t.Fatal("--check must not touch pool.yaml")
	}
	if gitOut(t, pool, "rev-list", "--count", "HEAD") != before {
		t.Fatal("--check must not commit")
	}

	did, err := UpgradePoolDir(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if mb, _ := os.ReadFile(PoolMarkerPath(root)); strings.Contains(string(mb), "tools:") || !strings.Contains(string(mb), "name: legacy-pool") || !strings.Contains(string(mb), "created: 2026-07-25T00:00:00Z") {
		t.Fatalf("pool.yaml must keep its identity and lose tools (did %v):\n%s", did, mb)
	}
	c := LoadPoolConfig(root)
	if len(c.IdentityTools) != 0 || len(c.Tools) != 2 || c.Tools[0].Cmd != "tea" || c.Tools[0].Help != "tea --help" || c.Tools[1].Avoid != "use tea" {
		t.Fatalf("tools must now load from config.yaml: tools=%+v identity=%+v", c.Tools, c.IdentityTools)
	}
	if c.Settings.Knowledge != "canonical" {
		t.Fatalf("the split must keep the rest of config.yaml: %+v", c.Settings)
	}
	if got := gitOut(t, pool, "log", "-1", "--format=%s|%an <%ae>"); got != "config: split from pool.yaml|t <t@e>" {
		t.Fatalf("split commit = %q", got)
	}
	if st := gitOut(t, pool, "status", "--porcelain"); st != "" {
		t.Fatalf("pool repo dirty after the split:\n%s", st)
	}
	if findings, _ := Doctor(root); hasFinding(findings, "still carries `tools`") {
		t.Fatalf("doctor warning must clear after the split: %+v", findings)
	}
	plan, err = PlanPoolUpgrade(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pool) != 0 {
		t.Fatalf("split must converge: %v", plan.Pool)
	}
}

func TestPoolUpgrade_SplitRefusesWhenConfigAlreadyHasOtherTools(t *testing.T) {
	root := migratedRoot(t)
	pool := PoolDir(root)
	os.WriteFile(PoolMarkerPath(root), []byte("name: legacy-pool\ncreated: 2026-07-25T00:00:00Z\ndoctrine_version: v0\ntools:\n  - { cmd: tea, use: \"Gitea CLI\" }\n"), 0o644)
	os.WriteFile(PoolConfigPath(root), []byte("knowledge: canonical\ntools:\n  - { cmd: myx, use: \"deploy\" }\n"), 0o644)
	gitCommitAll(t, pool, "fixture: tools in both files")

	plan, err := PlanPoolUpgrade(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Warnings, "\n"), "by hand") {
		t.Fatalf("two tools blocks need a human: %v", plan.Warnings)
	}
	if strings.Contains(strings.Join(plan.Pool, "\n"), "move `tools`") {
		t.Fatalf("no automatic split when config.yaml already declares tools: %v", plan.Pool)
	}
	if _, err := UpgradePoolDir(root, time.Now()); err != nil {
		t.Fatal(err)
	}
	if mb, _ := os.ReadFile(PoolMarkerPath(root)); !strings.Contains(string(mb), "tea") {
		t.Fatal("the identity file must be left alone when the split cannot be automatic")
	}
}

func TestPoolUpgrade_LegacyPoolWithToolsMigratesAndSplitsInFirstCommit(t *testing.T) {
	gitIdentity(t)
	root := legacyRoot(t)
	os.WriteFile(PoolMarkerPath(root), []byte("name: legacy-pool\ncreated: 2026-07-25T00:00:00Z\ndoctrine_version: v0\ntools:\n  - { cmd: tea, use: \"Gitea CLI\" }\n"), 0o644)

	plan, err := PlanPoolUpgrade(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Pool, "\n"), "move `tools`") {
		t.Fatalf("the legacy migration must preview the split too:\n%s", strings.Join(plan.Pool, "\n"))
	}
	if _, err := UpgradePoolDir(root, time.Now()); err != nil {
		t.Fatal(err)
	}
	c := LoadPoolConfig(root)
	if len(c.IdentityTools) != 0 || len(c.Tools) != 1 || c.Tools[0].Cmd != "tea" || c.Name != "legacy-pool" {
		t.Fatalf("migrated pool must carry tools in config.yaml only: %+v", c)
	}
	pool := PoolDir(root)
	if n := gitOut(t, pool, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("migration and split land in the first commit, got %s commits", n)
	}
	if st := gitOut(t, pool, "status", "--porcelain"); st != "" {
		t.Fatalf("pool repo dirty after migration:\n%s", st)
	}
}

func TestPoolUpgrade_SplitIsManualWhenConfigDeclaresAnEmptyToolsKey(t *testing.T) {
	root := migratedRoot(t)
	pool := PoolDir(root)
	os.WriteFile(PoolMarkerPath(root), []byte("name: legacy-pool\ncreated: 2026-07-25T00:00:00Z\ndoctrine_version: v0\ntools:\n  - { cmd: tea, use: \"Gitea CLI\" }\n"), 0o644)
	os.WriteFile(PoolConfigPath(root), []byte("knowledge: canonical\ntools: []\n"), 0o644)
	gitCommitAll(t, pool, "fixture: empty tools key in config")

	if _, err := UpgradePoolDir(root, time.Now()); err != nil {
		t.Fatal(err)
	}
	if c := LoadPoolConfig(root); c.ConfigErr != nil {
		t.Fatalf("appending a second tools key would break config.yaml: %v", c.ConfigErr)
	}
}
