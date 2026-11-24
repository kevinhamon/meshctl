package mesh

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The suite never probes this host's binaries for the wire set. By default
// no harness counts as installed, so scaffolds wire only the preferred
// harness (the no-harness fallback) — the one-harness shape the rest of the
// suite was written against. A test that needs a host shape calls
// stubInstalled (opencode at the major MESHCTL_OPENCODE_MAJOR names, else 2).
func init() { installedHarnessesFn = func() []InstalledHarness { return nil } }

func allRegisteredInstalled() []InstalledHarness {
	var out []InstalledHarness
	for _, s := range RegisteredHarnesses() {
		out = append(out, InstalledHarness{ID: s.ID, Variant: testVariant(s)})
	}
	return out
}

func testVariant(s HarnessSpec) int {
	if len(s.Variants) == 0 {
		return 0
	}
	if s.ID == HarnessOpencode {
		if v, err := strconv.Atoi(os.Getenv(opencodeMajorEnv)); err == nil && v > 0 {
			return v
		}
	}
	return s.Variants[0].Num
}

// stubInstalled makes only ids count as installed for the rest of the test.
func stubInstalled(t *testing.T, ids ...string) {
	t.Helper()
	prev := installedHarnessesFn
	installedHarnessesFn = func() []InstalledHarness {
		var out []InstalledHarness
		for _, h := range allRegisteredInstalled() {
			for _, id := range ids {
				if h.ID == id {
					out = append(out, h)
				}
			}
		}
		return out
	}
	t.Cleanup(func() { installedHarnessesFn = prev })
}

func writePoolHarnesses(t *testing.T, pool string, ids ...string) {
	t.Helper()
	body := "harnesses: [" + strings.Join(ids, ", ") + "]\n"
	if err := os.MkdirAll(filepath.Dir(PoolConfigPath(pool)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PoolConfigPath(pool), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wireIDs(set []InstalledHarness) string {
	var ids []string
	for _, h := range set {
		ids = append(ids, h.ID)
	}
	return strings.Join(ids, ",")
}

func newWireAgent(t *testing.T, pool, name, harness string) *Agent {
	t.Helper()
	spec := AgentSpec{Name: name, Title: name, Role: "r", Accepts: []Accept{{Type: "fyi", Desc: "x"}},
		Badge: Badge{Label: strings.ToUpper(name)}, Harness: harness, Bare: true}
	if _, err := New(pool, spec); err != nil {
		t.Fatal(err)
	}
	a, err := FindAgent(pool, name)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestWireSet_InstalledAllowlistAndPreference(t *testing.T) {
	stubInstalled(t, HarnessClaude, HarnessOpencode)
	pool := meshRoot(t)
	if got := wireIDs(WireSet(pool)); got != "claude,opencode" {
		t.Fatalf("no allowlist ⇒ every installed harness, got %q", got)
	}
	writePoolHarnesses(t, pool, "claude")
	if got := wireIDs(WireSet(pool)); got != "claude" {
		t.Fatalf("allowlist must narrow the set, got %q", got)
	}
	if got := wireIDs(AgentWireSet(pool, HarnessOpencode)); got != "claude,opencode" {
		t.Fatalf("the preferred harness is wired even when the allowlist leaves it out, got %q", got)
	}
	stubInstalled(t, HarnessClaude)
	if got := wireIDs(AgentWireSet(pool, HarnessOpencode)); got != "claude" {
		t.Fatalf("a preferred harness that is not installed is never wired, got %q", got)
	}
}

func TestWireSet_UnknownAllowlistIDIsADoctorError(t *testing.T) {
	stubInstalled(t, HarnessClaude, HarnessOpencode)
	pool := meshRoot(t)
	writePoolHarnesses(t, pool, "claude", "nope")
	if got := wireIDs(WireSet(pool)); got != "claude" {
		t.Fatalf("unknown ids are ignored for wiring, got %q", got)
	}
	fs := poolConfigFindings(pool, LoadPoolConfig(pool))
	if !hasFinding(fs, "unknown harness") || fs[0].Level != "error" {
		t.Fatalf("unknown allowlist id must be a doctor error: %+v", fs)
	}
}

func TestScaffold_WiresEveryInstalledHarness(t *testing.T) {
	stubInstalled(t, HarnessClaude, HarnessOpencode)
	t.Setenv(opencodeMajorEnv, "2")
	pool := meshRoot(t)
	a := newWireAgent(t, pool, "both", "")
	if got := strings.Join(WiredHarnesses(a.Dir()), ","); got != "claude,opencode" {
		t.Fatalf("a new agent is wired for every installed harness, got %q", got)
	}
	if a.HarnessName() != HarnessClaude {
		t.Fatalf("the recorded preference stays the default, got %q", a.HarnessName())
	}
	for _, id := range []string{HarnessClaude, HarnessOpencode} {
		h := HarnessByNameVersion(id, 2)
		if !fileExists(filepath.Join(h.SkillDir(a.Dir(), "mesh-cognition"), "SKILL.md")) {
			t.Fatalf("mesh-cognition not linked for %s", id)
		}
	}
	if fs := gateWiringFindings(a); hasLevel(fs, "error") {
		t.Fatalf("a freshly wired agent must pass every harness's gate check: %+v", fs)
	}
}

func TestScaffold_OnlyOneHarnessInstalled(t *testing.T) {
	stubInstalled(t, HarnessClaude)
	pool := meshRoot(t)
	a := newWireAgent(t, pool, "solo", HarnessOpencode)
	if got := strings.Join(WiredHarnesses(a.Dir()), ","); got != "claude" {
		t.Fatalf("an uninstalled harness must not be wired, even when preferred; got %q", got)
	}
	if fileExists(filepath.Join(a.Dir(), "opencode.json")) {
		t.Fatal("no opencode files on a host without opencode")
	}
	if a.HarnessName() != HarnessOpencode {
		t.Fatal("the preference is still recorded")
	}
	res, err := Onboard(pool, a.Dir(), AgentSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Join(res.Skipped, "\n"); !strings.Contains(n, "preferred harness opencode is not installed") {
		t.Fatalf("onboard must report the skipped preferred harness: %v", res.Skipped)
	}
	fs := harnessCoverageFindings(a, installedHarnessesFn())
	if !hasFinding(fs, "preferred harness opencode (agent.yaml `harness:`) is not installed") {
		t.Fatalf("doctor must warn on an uninstalled preferred harness: %+v", fs)
	}
	plan, err := PlanPoolUpgrade(pool, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(plan.Skipped, "\n"); !strings.Contains(s, "opencode is not installed") || strings.Contains(s, "Claude") {
		t.Fatalf("the plan notes the skipped harness once: %v", plan.Skipped)
	}
	if strings.Contains(strings.Join(plan.Warnings, "\n"), "not installed") {
		t.Fatalf("a skipped harness is a note, not a warning (it must not hold up a headless upgrade): %v", plan.Warnings)
	}
}

func TestScaffold_NothingInstalledWiresThePreference(t *testing.T) {
	stubInstalled(t)
	pool := meshRoot(t)
	a := newWireAgent(t, pool, "bare", "")
	if got := strings.Join(WiredHarnesses(a.Dir()), ","); got != "claude" {
		t.Fatalf("with no harness installed the preferred one is still wired (never no gate), got %q", got)
	}
}

func TestScaffold_AllowlistExcludesAnInstalledHarness(t *testing.T) {
	stubInstalled(t, HarnessClaude, HarnessOpencode)
	pool := meshRoot(t)
	writePoolHarnesses(t, pool, "claude")
	a := newWireAgent(t, pool, "narrow", "")
	if got := strings.Join(WiredHarnesses(a.Dir()), ","); got != "claude" {
		t.Fatalf("allowlist must keep opencode out, got %q", got)
	}
	if fs := harnessCoverageFindings(a, installedHarnessesFn()); hasFinding(fs, "not wired") {
		t.Fatalf("a harness the allowlist excludes is not 'installed but not wired': %+v", fs)
	}
	plan, err := PlanPoolUpgrade(pool, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, ap := range plan.Agents {
		if len(ap.AddHarnesses) > 0 {
			t.Fatalf("upgrade must not add an excluded harness: %+v", ap)
		}
	}
}

func TestDoctor_InstalledButNotWiredAndUpgradeAddsIt(t *testing.T) {
	stubInstalled(t, HarnessClaude)
	pool := meshRoot(t)
	a := newWireAgent(t, pool, "late", "")
	// opencode is installed later.
	stubInstalled(t, HarnessClaude, HarnessOpencode)
	t.Setenv(opencodeMajorEnv, "2")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fs := harnessCoverageFindings(a, installedHarnessesFn())
	if !hasFinding(fs, "installed but this workspace is not wired for it") {
		t.Fatalf("doctor must flag an installed, unwired harness: %+v", fs)
	}
	plan, err := PlanPoolUpgrade(pool, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var ap AgentUpgradePlan
	for _, x := range plan.Agents {
		if x.Name == "late" {
			ap = x
		}
	}
	if strings.Join(ap.AddHarnesses, ",") != HarnessOpencode || !strings.Contains(strings.Join(ap.Actions, "\n"), "wire for opencode") {
		t.Fatalf("plan must list opencode as added: %+v", ap)
	}
	if fileExists(filepath.Join(a.Dir(), "opencode.json")) {
		t.Fatal("--check must not write")
	}
	if _, err := UpgradeAgents(pool, "test"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(WiredHarnesses(a.Dir()), ","); got != "claude,opencode" {
		t.Fatalf("upgrade must wire the newly installed harness, got %q", got)
	}
	if fs := harnessCoverageFindings(a, installedHarnessesFn()); len(fs) != 0 {
		t.Fatalf("after the upgrade coverage is clean: %+v", fs)
	}
	if fs := gateWiringFindings(a); hasLevel(fs, "error") {
		t.Fatalf("both harnesses' gates must check clean: %+v", fs)
	}
	plan, _ = PlanPoolUpgrade(pool, time.Now())
	for _, x := range plan.Agents {
		if x.Name == "late" && len(x.AddHarnesses)+len(x.RefreshHarnesses) > 0 {
			t.Fatalf("a second plan has nothing left to wire: %+v", x)
		}
	}
}

func TestDoctor_WiredButNotInstalledIsInfo(t *testing.T) {
	stubInstalled(t, HarnessClaude, HarnessOpencode)
	pool := meshRoot(t)
	a := newWireAgent(t, pool, "gone", "")
	stubInstalled(t, HarnessClaude)
	fs := harnessCoverageFindings(a, installedHarnessesFn())
	if len(fs) != 1 || fs[0].Level != "info" || !strings.Contains(fs[0].Message, "opencode, which is not installed") {
		t.Fatalf("want one info finding for wired-but-not-installed opencode: %+v", fs)
	}
}

func TestDoctor_ChecksEveryWiredHarnessGate(t *testing.T) {
	stubInstalled(t, HarnessClaude, HarnessOpencode)
	t.Setenv(opencodeMajorEnv, "2")
	pool := meshRoot(t)
	a := newWireAgent(t, pool, "multi", "") // prefers claude
	if err := os.Remove(filepath.Join(a.Dir(), filepath.FromSlash(opencodePluginV2Rel))); err != nil {
		t.Fatal(err)
	}
	if !hasFinding(gateWiringFindings(a), "mesh plugin not installed") {
		t.Fatal("doctor must check the opencode wiring of a claude-preferred agent")
	}
}

func hasLevel(fs []DoctorFinding, level string) bool {
	for _, f := range fs {
		if f.Level == level {
			return true
		}
	}
	return false
}
