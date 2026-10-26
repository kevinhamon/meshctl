package mesh

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// ocPeerPool: an opencode V2 caller `hub` with a dispatchable `architect`, a
// state-mutating `pm` and a send-only `scout` in the pool (ADR-0076).
func ocPeerPool(t *testing.T) (dir string, hub, arch, pm *Agent) {
	t.Helper()
	t.Setenv(opencodeMajorEnv, "2")
	dir = meshRoot(t)
	arch = newTestAgent(t, dir, "architect", true, nil)
	pm = newTestAgent(t, dir, "pm", false, []string{"tracker"})
	newTestAgent(t, dir, "scout", false, nil)
	spec := AgentSpec{Name: "hub", Title: "hub title", Role: "role", Accepts: []Accept{{Type: "guidance"}},
		Harness: HarnessOpencode, Badge: Badge{Label: "hub"}, Bare: true}
	if _, err := New(dir, spec, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	hub, err := FindAgent(dir, "hub")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_DISPATCH_HOURLY_MAX", "12")
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("OPENCODE_SESSION_ID", "ses_parent")
	return dir, hub, arch, pm
}

func TestOpencodePeers_RenderedIntoOpencodeAgents(t *testing.T) {
	dir, hub, _, _ := ocPeerPool(t)
	defs := filepath.Join(hub.Dir(), ".opencode", "agents")
	for _, f := range []string{"mesh-architect.md", "mesh-architect-work.md", "mesh-pm-work.md"} {
		if !fileExists(filepath.Join(defs, f)) {
			t.Errorf("missing %s", f)
		}
	}
	for _, f := range []string{"mesh-pm.md", "mesh-scout.md", "mesh-hub.md"} {
		if fileExists(filepath.Join(defs, f)) {
			t.Errorf("%s must not be rendered", f)
		}
	}
	if fileExists(filepath.Join(hub.Dir(), ".claude", "agents", "mesh-architect.md")) {
		t.Error("an opencode caller must not get Claude Code definitions")
	}
	if !PlanPeerAgents(dir, hub.Dir()).Empty() {
		t.Fatal("plan must be empty right after wiring")
	}
	got := InstalledPeers(hub.Dir())
	if len(got) != 2 || got[0].Name != "architect" || !got[0].Consult || !got[0].Work || got[1].Name != "pm" || got[1].Consult {
		t.Fatalf("installed peers: %+v", got)
	}
	if line := PeerLine(hub); !strings.Contains(line, "subagent-tool call") || !strings.Contains(line, "`agent: mesh-<peer>`") {
		t.Fatalf("peer line must name the subagent tool: %s", line)
	}
}

func TestOpencodePeers_DefinitionContract(t *testing.T) {
	_, hub, _, _ := ocPeerPool(t)
	read := func(name string) (opencodePeerFrontmatter, string) {
		b, err := os.ReadFile(filepath.Join(hub.Dir(), ".opencode", "agents", name))
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(string(b), "---\n", 3)
		var fm opencodePeerFrontmatter
		if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
			t.Fatal(err)
		}
		return fm, parts[2]
	}
	fm, body := read("mesh-architect.md")
	if fm.Mode != "subagent" || !strings.Contains(fm.Description, "--in-session") {
		t.Fatalf("frontmatter: %+v", fm)
	}
	want := []opencodePermRule{{"subagent", "*", "deny"}, {"edit", "*", "deny"}}
	if len(fm.Permissions) != 2 || fm.Permissions[0] != want[0] || fm.Permissions[1] != want[1] {
		t.Fatalf("consult permissions: %+v", fm.Permissions)
	}
	for _, w := range []string{distillerAgentMarker, "--agent architect --in-session -->", "You are **architect**", "subagent calls"} {
		if !strings.Contains(body, w) {
			t.Errorf("consult body missing %q", w)
		}
	}
	if strings.Contains(body, "Agent calls") {
		t.Error("opencode body must not name Claude Code's Agent tool")
	}
	fm, body = read("mesh-pm-work.md")
	if len(fm.Permissions) != 1 || fm.Permissions[0] != want[0] {
		t.Fatalf("work permissions: %+v", fm.Permissions)
	}
	if !strings.Contains(body, "--agent pm --in-session --work -->") {
		t.Errorf("work body must carry the work flags: %s", body)
	}
}

func TestOpencodePeers_V1CallerGetsNone(t *testing.T) {
	dir, hub, _, _ := ocPeerPool(t)
	t.Setenv(opencodeMajorEnv, "1")
	plan := PlanPeerAgents(dir, hub.Dir())
	if len(plan.Remove) != 3 || len(plan.Write)+len(plan.Update) != 0 {
		t.Fatalf("V1 caller: managed definitions must be removed: %+v", plan)
	}
	if PeerLine(hub) != "" {
		t.Error("V1 caller must not be told about in-session peers")
	}
}

func TestOpencodePeers_SubagentAskRulesForMutatingPeers(t *testing.T) {
	_, hub, _, _ := ocPeerPool(t)
	if miss := missingSubagentAsk(hub.Dir(), false); len(miss) != 0 {
		t.Fatalf("wiring must add the ask rules: %v", miss)
	}
	ps := readOcPerms(hub.Dir(), false)
	if ps.effect("subagent", "mesh-pm-work") != "ask" || ps.effect("subagent", "mesh-architect-work") != "allow" {
		t.Fatalf("rules: %+v", ps.rules)
	}
	// Idempotent.
	if n, err := ensureOpencodeSubagentAsk(hub.Dir()); err != nil || len(n) != 0 {
		t.Fatalf("second run must change nothing: %v %v", n, err)
	}
}

func TestResolvePeerSubagent(t *testing.T) {
	dir, hub, _, _ := ocPeerPool(t)
	p := func(agentType string) HookPayload {
		return HookPayload{Cwd: hub.Dir(), SessionID: "ses_parent", AgentID: "ses_child", AgentType: agentType, ToolName: "read", Harness: HarnessOpencode}
	}
	got, err := ResolvePeerSubagent(dir, p("mesh-architect"))
	if err != nil || got.PeerAgent != "architect" || !got.InSession || got.Work {
		t.Fatalf("consult: %+v %v", got, err)
	}
	got, err = ResolvePeerSubagent(dir, p("mesh-pm-work"))
	if err != nil || got.PeerAgent != "pm" || !got.InSession || !got.Work {
		t.Fatalf("work: %+v %v", got, err)
	}
	// Fail closed: a mesh- agent with no managed definition.
	if _, err := ResolvePeerSubagent(dir, p("mesh-scout")); err == nil {
		t.Error("an unrendered mesh- agent must not resolve")
	}
	hand := filepath.Join(hub.Dir(), ".opencode", "agents", "mesh-hand.md")
	if err := os.WriteFile(hand, []byte("---\nmode: subagent\n---\n--agent architect --in-session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePeerSubagent(dir, p("mesh-hand")); err == nil {
		t.Error("a hand-written mesh- definition must not resolve")
	}
	// Pass-through: non-peer subagents, Claude Code payloads, flagged payloads.
	for _, q := range []HookPayload{p("general"), p(""), {Cwd: hub.Dir(), AgentType: "mesh-architect", Harness: HarnessClaude}} {
		got, err := ResolvePeerSubagent(dir, q)
		if err != nil || got.InSession || got.PeerAgent != "" {
			t.Errorf("must pass through: %+v → %+v %v", q, got, err)
		}
	}
}

func TestOpencodePeers_GateAppliesPeerContract(t *testing.T) {
	dir, hub, arch, _ := ocPeerPool(t)
	edit := func(agentType, path string) GateDecision {
		in, _ := json.Marshal(map[string]string{"path": path, "content": "x"})
		p, err := ResolvePeerSubagent(dir, HookPayload{Cwd: hub.Dir(), SessionID: "ses_parent", AgentID: "ses_child",
			AgentType: agentType, ToolName: "write", ToolInput: in, Harness: HarnessOpencode})
		if err != nil {
			t.Fatal(err)
		}
		return GatePreToolUse(dir, p)
	}
	if d := edit("mesh-architect", filepath.Join(arch.Dir(), "notes.md")); !d.Deny {
		t.Errorf("a consulted peer must not write: %+v", d)
	}
	if d := edit("mesh-architect-work", filepath.Join(arch.Dir(), "notes.md")); d.Deny {
		t.Errorf("a working peer may write its own workspace: %+v", d)
	}
	if d := edit("mesh-architect-work", filepath.Join(hub.Dir(), "notes.md")); !d.Deny {
		t.Errorf("a working peer must not write the caller's workspace: %+v", d)
	}
	in, _ := json.Marshal(map[string]string{"agent": "general", "description": "x", "prompt": "x"})
	p, _ := ResolvePeerSubagent(dir, HookPayload{Cwd: hub.Dir(), AgentType: "mesh-architect-work", ToolName: "subagent", ToolInput: in, Harness: HarnessOpencode})
	if d := GatePreToolUse(dir, p); !d.Deny {
		t.Errorf("a peer must not launch subagents: %+v", d)
	}
}

func TestOpencodePeers_DelegationToMutatingPeerUsesOpencodeAsk(t *testing.T) {
	dir, hub, _, _ := ocPeerPool(t)
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	call := func(agent string) GateDecision {
		in, _ := json.Marshal(map[string]string{"agent": agent, "description": "x", "prompt": "x"})
		return GatePreToolUse(dir, HookPayload{Cwd: hub.Dir(), SessionID: "ses_parent", ToolName: "subagent", ToolInput: in, Harness: HarnessOpencode})
	}
	if d := call("mesh-pm-work"); d.Deny || d.Ask {
		t.Fatalf("with opencode's ask rule wired the gate lets opencode ask: %+v", d)
	}
	for _, a := range []string{"mesh-architect-work", "mesh-architect", "general"} {
		if d := call(a); d.Deny || d.Ask {
			t.Errorf("%s must pass: %+v", a, d)
		}
	}
	// An env override that allows the subagent outright: refused, naming onboard.
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permissions":[{"action":"subagent","resource":"*","effect":"allow"}]}`)
	if d := call("mesh-pm-work"); !d.Deny || !strings.Contains(d.Reason, "meshctl agent onboard") {
		t.Fatalf("without opencode's ask the delegation is refused: %+v", d)
	}
}

func TestAskInSession_OpencodeCaller(t *testing.T) {
	dir, hub, arch, _ := ocPeerPool(t)
	var out bytes.Buffer
	now := time.Now()
	req, rc, err := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "Split it?", From: hub.Name, Now: now, Stdout: &out})
	if err != nil || rc != 0 {
		t.Fatalf("rc=%d err=%v", rc, err)
	}
	s := out.String()
	for _, w := range []string{"subagent tool", "agent: mesh-architect", "description: architect " + req.ID, "meshctl agent brief --agent architect --request " + req.ID} {
		if !strings.Contains(s, w) {
			t.Errorf("output missing %q:\n%s", w, s)
		}
	}
	if strings.Contains(s, "subagent_type") {
		t.Errorf("opencode output must not name Claude Code's field:\n%s", s)
	}
	// The child session has its own id: no claim, so its respond is not refused.
	if fileExists(claimPath(req.Path())) {
		t.Fatal("an opencode caller must not claim")
	}
	r, _ := ReadRequest(req.Path())
	if r.Status != StatusInProgress {
		t.Fatalf("status: %s", r.Status)
	}
	if _, err := RespondRequest(arch, req.ID, RespondOptions{Status: "answered", Response: "yes", Session: "ses_child", Now: now}); err != nil {
		t.Fatalf("the child session must be able to respond: %v", err)
	}

	// V1 caller: refused, pointing at headless ask.
	t.Setenv(opencodeMajorEnv, "1")
	if _, _, err := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "x", From: hub.Name, Stdout: discard{}}); err == nil || !strings.Contains(err.Error(), "opencode V1") {
		t.Fatalf("V1 caller must be refused: %v", err)
	}
}

func TestHookSessionEnd_OpencodePeerChildCaptured(t *testing.T) {
	dir, hub, arch, _ := ocPeerPool(t)
	p, err := ResolvePeerSubagent(dir, HookPayload{Cwd: hub.Dir(), SessionID: "ses_parent", AgentID: "ses_child", AgentType: "mesh-architect", Harness: HarnessOpencode})
	if err != nil {
		t.Fatal(err)
	}
	HookSessionEnd(dir, p, os.Getpid(), time.Now())
	recs, _ := filepath.Glob(filepath.Join(arch.Dir(), MemoryDir, "sessions", "*ses_parent-peer-ses_child*"))
	if len(recs) == 0 {
		entries, _ := filepath.Glob(filepath.Join(arch.Dir(), MemoryDir, "*", "*"))
		t.Fatalf("no peer session record in %s: %v", arch.Name, entries)
	}
	logb, _ := os.ReadFile(DispatchLogPath(dir))
	if !strings.Contains(string(logb), "DONE mode=in-session agent_id=ses_child") || !strings.Contains(string(logb), "hub") {
		t.Fatalf("dispatch log: %s", logb)
	}
}
