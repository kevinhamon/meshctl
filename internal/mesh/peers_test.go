package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// peerPool: a caller plus two dispatchable peers and one send-only peer.
func peerPool(t *testing.T) (dir string, caller *Agent) {
	t.Helper()
	dir = meshRoot(t)
	caller = newTestAgent(t, dir, "developer", false, []string{"code"})
	newTestAgent(t, dir, "architect", true, nil)
	newTestAgent(t, dir, "catalog-expert", true, nil)
	newTestAgent(t, dir, "pm", false, []string{"tracker"})
	return dir, caller
}

func TestPeers_RenderedForDispatchablePeersOnly(t *testing.T) {
	dir, caller := peerPool(t)
	// newTestAgent → New → WireAgent already rendered; the order of creation
	// means the caller saw no peers yet. Onboard refreshes.
	if _, err := Onboard(dir, caller.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"architect", "catalog-expert"} {
		if !fileExists(PeerAgentFile(caller.Dir(), p)) {
			t.Fatalf("missing peer definition for %s", p)
		}
	}
	for _, p := range []string{"pm", "developer"} {
		if fileExists(PeerAgentFile(caller.Dir(), p)) {
			t.Fatalf("%s must not be rendered (send-only or self)", p)
		}
	}
	if !PlanPeerAgents(dir, caller.Dir()).Empty() {
		t.Fatal("plan must be empty right after onboard")
	}
}

func TestPeers_FrontmatterContract(t *testing.T) {
	dir, caller := peerPool(t)
	arch, _ := FindAgent(dir, "architect")
	body, err := RenderPeerAgent(caller.Name, arch)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(body, "---\n", 3)
	if len(parts) < 3 {
		t.Fatalf("no frontmatter: %s", body)
	}
	var fm struct {
		Name        string         `yaml:"name"`
		Description string         `yaml:"description"`
		Tools       string         `yaml:"tools"`
		Model       string         `yaml:"model"`
		Omit        bool           `yaml:"omitClaudeMd"`
		Hooks       map[string]any `yaml:"hooks"`
	}
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		t.Fatalf("frontmatter is not YAML: %v\n%s", err, parts[1])
	}
	if fm.Name != "mesh-architect" || !fm.Omit || fm.Model != "inherit" {
		t.Fatalf("frontmatter: %+v", fm)
	}
	if strings.Contains(fm.Tools, "Agent") || strings.Contains(fm.Tools, "Write") || strings.Contains(fm.Tools, "Edit") {
		t.Fatalf("peer must not get Agent/Write/Edit: %q", fm.Tools)
	}
	if !strings.Contains(fm.Description, "guidance") || !strings.Contains(fm.Description, "--in-session") {
		t.Fatalf("description must list accepted types and the precondition: %q", fm.Description)
	}
	h, _ := json.Marshal(fm.Hooks)
	for _, want := range []string{"PreToolUse", "Stop", "meshctl hook pre-tool-use --agent architect --in-session", "meshctl hook session-end --agent architect --in-session"} {
		if !strings.Contains(string(h), want) {
			t.Fatalf("hooks missing %q: %s", want, h)
		}
	}
	if !strings.Contains(parts[2], distillerAgentMarker) || !strings.Contains(parts[2], "--agent architect") {
		t.Fatalf("body missing marker or --agent guidance: %s", parts[2])
	}
	if !strings.Contains(parts[2], "`developer`") {
		t.Fatal("body must name the caller")
	}
}

func TestPeers_RosterChangeRefreshesAndRemoves(t *testing.T) {
	dir, caller := peerPool(t)
	if _, err := Onboard(dir, caller.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	// catalog-expert stops being dispatchable → its definition is removed.
	mp := ManifestPath(dir, "catalog-expert")
	b, _ := os.ReadFile(mp)
	_ = os.WriteFile(mp, []byte(strings.Replace(string(b), "dispatchable: true", "dispatchable: false", 1)), 0o644)
	// architect's title changes → its definition is refreshed.
	mp = ManifestPath(dir, "architect")
	b, _ = os.ReadFile(mp)
	_ = os.WriteFile(mp, []byte(strings.Replace(string(b), "architect title", "Chief Architect", 1)), 0o644)

	plan := PlanPeerAgents(dir, caller.Dir())
	// catalog-expert loses both definitions (no longer dispatchable, not mutating);
	// architect's consult and work definitions both carry the title.
	if strings.Join(plan.Remove, ",") != "catalog-expert,catalog-expert-work" || strings.Join(plan.Update, ",") != "architect,architect-work" {
		t.Fatalf("plan: %+v", plan)
	}
	notes, err := ensurePeerAgents(dir, caller.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("notes: %v", notes)
	}
	if fileExists(PeerAgentFile(caller.Dir(), "catalog-expert")) || fileExists(PeerWorkAgentFile(caller.Dir(), "catalog-expert")) {
		t.Fatal("stale definition not removed")
	}
	got, _ := os.ReadFile(PeerAgentFile(caller.Dir(), "architect"))
	if !strings.Contains(string(got), "Chief Architect") {
		t.Fatal("definition not refreshed")
	}
	if n, _ := ensurePeerAgents(dir, caller.Dir()); len(n) != 0 {
		t.Fatalf("second run must be a no-op, got %v", n)
	}
}

func TestPeers_HandAuthoredLeftAlone(t *testing.T) {
	dir, caller := peerPool(t)
	out := PeerAgentFile(caller.Dir(), "architect")
	_ = os.MkdirAll(filepath.Dir(out), 0o755)
	_ = os.WriteFile(out, []byte("---\nname: mesh-architect\n---\nmine\n"), 0o644)
	if _, err := Onboard(dir, caller.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "---\nname: mesh-architect\n---\nmine\n" {
		t.Fatal("hand-authored peer definition was overwritten")
	}
	plan := PlanPeerAgents(dir, caller.Dir())
	if len(plan.Hand) != 1 || len(plan.Update) != 0 {
		t.Fatalf("plan must report it as hand-authored, not stale: %+v", plan)
	}
}

func TestPeers_PoolAccessWiredInLocalSettings(t *testing.T) {
	dir, caller := peerPool(t)
	if _, err := Onboard(dir, caller.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if !PoolAccessWired(dir, caller.Dir()) {
		t.Fatal("pool root not granted")
	}
	// Idempotent, and the local file is git-ignored.
	note, err := ensurePoolAccess(dir, caller.Dir())
	if err != nil || note != "" {
		t.Fatalf("second run: %q %v", note, err)
	}
	gi, _ := os.ReadFile(filepath.Join(caller.Dir(), ".gitignore"))
	if !strings.Contains(string(gi), ".claude/settings.local.json") {
		t.Fatal("settings.local.json not ignored")
	}
	// Existing local settings are preserved.
	local := filepath.Join(caller.Dir(), ".claude", localSettingsFile)
	var root map[string]any
	b, _ := os.ReadFile(local)
	_ = json.Unmarshal(b, &root)
	root["permissions"].(map[string]any)["allow"] = []any{"Bash(ls:*)"}
	b, _ = json.Marshal(root)
	_ = os.WriteFile(local, b, 0o644)
	if _, err := ensurePoolAccess(dir, caller.Dir()); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(local)
	if !strings.Contains(string(b), "Bash(ls:*)") {
		t.Fatal("existing local settings lost")
	}
}

func TestPeers_UpgradePlanAndDoctorFlagStaleSet(t *testing.T) {
	dir, caller := peerPool(t)
	// The caller was scaffolded before its peers existed, so its set is stale.
	plan := PlanPeerAgents(dir, caller.Dir())
	if strings.Join(plan.Write, ",") != "architect,architect-work,catalog-expert,catalog-expert-work,pm-work" {
		t.Fatalf("want consult+work for dispatchable peers and work for pm, got %+v", plan)
	}
	ap := planAgent(caller, time.Now())
	joined := strings.Join(ap.Actions, "\n")
	if !strings.Contains(joined, "write in-session peer subagents: mesh-architect, mesh-architect-work, mesh-catalog-expert, mesh-catalog-expert-work, mesh-pm-work") {
		t.Fatalf("upgrade plan missing peer actions: %v", ap.Actions)
	}
	found := false
	for _, f := range gateWiringFindings(caller) {
		if strings.Contains(f.Message, "in-session peer subagents out of date") {
			found = true
		}
	}
	if !found {
		t.Fatal("doctor must flag the stale peer set")
	}
	if _, err := UpgradeAgents(dir, "vtest"); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"developer", "architect", "catalog-expert", "pm"} {
		ag, _ := FindAgent(dir, a)
		if p := PlanPeerAgents(dir, ag.Dir()); !p.Empty() {
			t.Fatalf("%s still stale after upgrade: %+v", a, p)
		}
		for _, f := range gateWiringFindings(ag) {
			if strings.Contains(f.Message, "in-session") {
				t.Fatalf("%s: doctor still complains: %s", a, f.Message)
			}
		}
	}
	// A dispatchable peer gets the OTHER dispatchable peer, never itself.
	arch, _ := FindAgent(dir, "architect")
	if fileExists(PeerAgentFile(arch.Dir(), "architect")) || !fileExists(PeerAgentFile(arch.Dir(), "catalog-expert")) {
		t.Fatal("peer set for architect wrong")
	}
}

func TestPeers_WorkFrontmatterContract(t *testing.T) {
	dir, caller := peerPool(t)
	arch, _ := FindAgent(dir, "architect")
	body, err := RenderPeerWorkAgent(caller.Name, arch)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(body, "---\n", 3)
	var fm struct {
		Name        string         `yaml:"name"`
		Description string         `yaml:"description"`
		Tools       string         `yaml:"tools"`
		Hooks       map[string]any `yaml:"hooks"`
	}
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		t.Fatalf("frontmatter is not YAML: %v", err)
	}
	if fm.Name != "mesh-architect-work" {
		t.Fatalf("name: %q", fm.Name)
	}
	for _, want := range []string{"Edit", "Write", "MultiEdit", "Bash"} {
		if !strings.Contains(fm.Tools, want) {
			t.Fatalf("work peer needs %s: %q", want, fm.Tools)
		}
	}
	if strings.Contains(fm.Tools, "Agent") {
		t.Fatalf("work peer must not get Agent: %q", fm.Tools)
	}
	if !strings.Contains(fm.Description, "--in-session --work") {
		t.Fatalf("description must name the work precondition: %q", fm.Description)
	}
	h, _ := json.Marshal(fm.Hooks)
	for _, want := range []string{"meshctl hook pre-tool-use --agent architect --in-session --work", "meshctl hook session-end --agent architect --in-session --work"} {
		if !strings.Contains(string(h), want) {
			t.Fatalf("hooks missing %q: %s", want, h)
		}
	}
	for _, want := range []string{distillerAgentMarker, "receipt", "meshctl kb begin", "worktree", "`developer`"} {
		if !strings.Contains(parts[2], want) {
			t.Fatalf("body missing %q", want)
		}
	}
}

func TestPeers_MutatingPeerGetsWorkDefinitionOnly(t *testing.T) {
	dir, caller := peerPool(t)
	if _, err := Onboard(dir, caller.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if fileExists(PeerAgentFile(caller.Dir(), "pm")) || !fileExists(PeerWorkAgentFile(caller.Dir(), "pm")) {
		t.Fatal("mutating pm must get mesh-pm-work and no consult definition")
	}
	b, _ := os.ReadFile(PeerWorkAgentFile(caller.Dir(), "pm"))
	if !strings.Contains(string(b), "approve") {
		t.Fatal("mutating peer's description must say delegation needs approval")
	}
	got := InstalledPeers(caller.Dir())
	want := []InstalledPeer{{"architect", true, true}, {"catalog-expert", true, true}, {"pm", false, true}}
	if len(got) != len(want) {
		t.Fatalf("installed: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("installed: %+v", got)
		}
	}
	line := PeerLine(caller)
	for _, w := range []string{"`architect` (consult/work)", "`pm` (work)", "--work"} {
		if !strings.Contains(line, w) {
			t.Fatalf("peer line missing %q: %s", w, line)
		}
	}
}

func TestPeers_WorkdirsWiredIntoCallerAdditionalDirs(t *testing.T) {
	dir, caller := peerPool(t)
	repo := t.TempDir()
	mp := ManifestPath(dir, "architect")
	if _, err := addManifestWorkdirs(mp, []string{repo}); err != nil {
		t.Fatal(err)
	}
	// Add-only: a second --workdirs does not overwrite.
	if note, _ := addManifestWorkdirs(mp, []string{"/elsewhere"}); !strings.Contains(note, "left unchanged") {
		t.Fatalf("note: %q", note)
	}
	arch, _ := FindAgent(dir, "architect")
	if len(arch.Workdirs) != 1 || arch.WorkdirPaths()[0] != filepath.Clean(repo) {
		t.Fatalf("workdirs: %v", arch.Workdirs)
	}
	if miss := MissingAdditionalDirs(caller.Dir(), PeerWorkdirs(dir, caller.Dir())...); len(miss) != 1 {
		t.Fatalf("workdir should be missing before onboard: %v", miss)
	}
	if !strings.Contains(strings.Join(planAgent(caller, time.Now()).Actions, "\n"), repo) {
		t.Fatal("upgrade plan must preview the workdir grant")
	}
	if _, err := Onboard(dir, caller.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if miss := MissingAdditionalDirs(caller.Dir(), dir, repo); len(miss) != 0 {
		t.Fatalf("not wired: %v", miss)
	}
	// The agent's own workdirs are not granted to itself through this path.
	if PeerWorkdirs(dir, arch.Dir()) != nil {
		t.Fatal("an agent is not its own peer")
	}
}

// TestPeers_GateMatcherAndPostToolUse: both rendered definitions carry the
// matcher Claude Code actually matches MCP tool names with, and a PostToolUse
// hook on the same matcher invoking `meshctl hook post-tool-use` with the same
// peer flags as the gate.
func TestPeers_GateMatcherAndPostToolUse(t *testing.T) {
	dir, caller := peerPool(t)
	arch, _ := FindAgent(dir, "architect")
	for _, c := range []struct {
		name   string
		render func(string, *Agent) (string, error)
		flags  string
	}{
		{"consult", RenderPeerAgent, "--agent architect --in-session"},
		{"work", RenderPeerWorkAgent, "--agent architect --in-session --work"},
	} {
		body, err := c.render(caller.Name, arch)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(body, "---\n", 3)
		var fm struct {
			Hooks map[string][]struct {
				Matcher string           `yaml:"matcher"`
				Hooks   []map[string]any `yaml:"hooks"`
			} `yaml:"hooks"`
		}
		if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
			t.Fatalf("%s: frontmatter is not YAML: %v", c.name, err)
		}
		for _, ev := range []string{"PreToolUse", "PostToolUse"} {
			es := fm.Hooks[ev]
			if len(es) != 1 || es[0].Matcher != GateHookMatcher {
				t.Fatalf("%s: %s matcher = %+v, want %q", c.name, ev, es, GateHookMatcher)
			}
		}
		if cmd, _ := fm.Hooks["PostToolUse"][0].Hooks[0]["command"].(string); cmd != "meshctl hook post-tool-use "+c.flags {
			t.Fatalf("%s: PostToolUse command = %q", c.name, cmd)
		}
	}
}

// TestPeers_StaleMatcherRefreshesOnUpgrade: a definition rendered with the
// bare `mcp__` matcher is planned as a refresh and rewritten by pool upgrade.
func TestPeers_StaleMatcherRefreshesOnUpgrade(t *testing.T) {
	dir, caller := peerPool(t)
	if _, err := Onboard(dir, caller.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	f := PeerAgentFile(caller.Dir(), "architect")
	b, _ := os.ReadFile(f)
	old := strings.Replace(string(b), GateHookMatcher, "Write|Edit|MultiEdit|NotebookEdit|Bash|mcp__", -1)
	if old == string(b) {
		t.Fatalf("rendered definition does not carry the gate matcher:\n%s", b)
	}
	_ = os.WriteFile(f, []byte(old), 0o644)
	if plan := PlanPeerAgents(dir, caller.Dir()); strings.Join(plan.Update, ",") != "architect" {
		t.Fatalf("stale matcher must plan a refresh, got %+v", plan)
	}
	if _, err := UpgradeAgents(dir, "vtest"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(f)
	if !strings.Contains(string(b), GateHookMatcher) || !strings.Contains(string(b), "meshctl hook post-tool-use") {
		t.Fatalf("upgrade did not re-render the definition:\n%s", b)
	}
}
