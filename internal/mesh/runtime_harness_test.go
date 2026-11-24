package mesh

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Runtime harness resolution: the harness is chosen when the agent
// runs; `harness:` in agent.yaml is only the preference.

func TestRuntimeHarnessID_Order(t *testing.T) {
	oc := &Agent{Harness: HarnessOpencode}
	cases := []struct {
		name                string
		a                   *Agent
		flag, running, want string
	}{
		{"the flag wins", oc, HarnessClaude, HarnessOpencode, HarnessClaude},
		{"then the env marker", oc, "", HarnessClaude, HarnessClaude},
		{"then the preference", oc, "", "", HarnessOpencode},
		{"no agent: the default", nil, "", "", DefaultHarness},
		{"empty preference: the default", &Agent{}, "", "", DefaultHarness},
		{"an unknown flag falls through", oc, "nope", "", HarnessOpencode},
		{"an unknown preference falls to the default", &Agent{Harness: "nope"}, "", "", DefaultHarness},
		{"ids are normalised", nil, " OpenCode ", "", HarnessOpencode},
	}
	for _, c := range cases {
		if got := runtimeHarnessID(c.a, c.flag, c.running); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRuntimeHarness_ReadsTheEnvMarker(t *testing.T) {
	t.Setenv(opencodeMajorEnv, "2")
	oc := &Agent{Harness: HarnessOpencode}
	if h := RuntimeHarness(oc, ""); h.Name() != HarnessOpencode {
		t.Fatalf("no marker: the preference, got %s", h.Name())
	}
	t.Setenv("CLAUDECODE", "1")
	if h := RuntimeHarness(oc, ""); h.Name() != HarnessClaude {
		t.Fatalf("Claude Code's marker: got %s", h.Name())
	}
	if h := RuntimeHarness(oc, HarnessOpencode); h.Name() != HarnessOpencode {
		t.Fatalf("the bridge's flag beats the marker: got %s", h.Name())
	}
}

func TestHarnessForPayload_RunningHarnessBeatsPreference(t *testing.T) {
	t.Setenv(opencodeMajorEnv, "2")
	dir := meshRoot(t)
	a := setHarness(t, dir, newTestAgent(t, dir, "architect", true, nil), HarnessOpencode)
	p := HookPayload{Cwd: a.Dir()}
	if h := HarnessForPayload(dir, p, ""); h.Name() != HarnessOpencode {
		t.Fatalf("nothing running: the agent's preference, got %s", h.Name())
	}
	t.Setenv("CLAUDECODE", "1")
	if h := HarnessForPayload(dir, p, ""); h.Name() != HarnessClaude {
		t.Fatalf("Claude Code running the hook: got %s", h.Name())
	}
	if h := HarnessForPayload(dir, p, HarnessOpencode); h.Name() != HarnessOpencode {
		t.Fatalf("--harness wins: got %s", h.Name())
	}
}

func TestAgentModelFor(t *testing.T) {
	a := &Agent{Model: "sonnet", Models: map[string]string{HarnessOpencode: "prov/model"}}
	if got := a.ModelFor(HarnessOpencode); got != "prov/model" {
		t.Errorf("per-harness model: %q", got)
	}
	if got := a.ModelFor(HarnessClaude); got != "sonnet" {
		t.Errorf("no entry: model:, got %q", got)
	}
	if got := (&Agent{}).ModelFor(HarnessClaude); got != "" {
		t.Errorf("neither set: %q", got)
	}
	if got := (*Agent)(nil).ModelFor(HarnessClaude); got != "" {
		t.Errorf("nil agent: %q", got)
	}
	p := filepath.Join(t.TempDir(), ManifestName)
	if err := os.WriteFile(p, []byte("name: x\nmodel: sonnet\nmodels:\n  opencode: prov/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := LoadAgent(p)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ModelFor(HarnessOpencode) != "prov/m" || parsed.ModelFor(HarnessClaude) != "sonnet" {
		t.Fatalf("models: from agent.yaml: %+v", parsed)
	}
}

func TestChooseHeadlessHarness_Order(t *testing.T) {
	inst := func(ids ...string) func(string) bool {
		return func(id string) bool { return hasString(ids, id) }
	}
	both := []string{HarnessClaude, HarnessOpencode}
	cases := []struct {
		name               string
		preferred, running string
		wired              []string
		installed          func(string) bool
		want               string
	}{
		{"caller's harness, wired and installed", HarnessOpencode, HarnessClaude, both, inst(HarnessClaude, HarnessOpencode), HarnessClaude},
		{"caller's harness not wired in the target", HarnessOpencode, HarnessClaude, []string{HarnessOpencode}, inst(HarnessClaude, HarnessOpencode), HarnessOpencode},
		{"caller's harness not installed", HarnessOpencode, HarnessClaude, both, inst(HarnessOpencode), HarnessOpencode},
		{"no caller harness (a terminal)", HarnessOpencode, "", both, inst(HarnessClaude, HarnessOpencode), HarnessOpencode},
		{"preference not installed: first installed wired", HarnessOpencode, "", both, inst(HarnessClaude), HarnessClaude},
		{"nothing installed: the preference", HarnessOpencode, HarnessClaude, both, inst(), HarnessOpencode},
		{"empty preference: the default", "", "", nil, inst(DefaultHarness), DefaultHarness},
	}
	for _, c := range cases {
		if got := chooseHeadlessHarness(c.preferred, c.running, c.wired, c.installed); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWithoutHarnessMarkers(t *testing.T) {
	got := withoutHarnessMarkers([]string{"PATH=/bin", "CLAUDECODE=1", "OPENCODE=1", "OPENCODE_CONFIG_CONTENT={}", "CLAUDECODE_X=1"})
	want := []string{"PATH=/bin", "OPENCODE_CONFIG_CONTENT={}", "CLAUDECODE_X=1"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// fakeHarnessBins puts no-op `claude` and `opencode` executables first on
// PATH, so both harnesses count as installed without running either.
func fakeHarnessBins(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	for _, n := range []string{"claude", "opencode"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(opencodeMajorEnv, "2")
}

// setManifestLines replaces (or appends) top-level keys in an agent.yaml.
func setManifestLines(t *testing.T, dir string, a *Agent, kv map[string]string) *Agent {
	t.Helper()
	p := ManifestPath(dir, a.Name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range kv {
		re := regexp.MustCompile(`(?m)^` + k + `:.*\n(?:[ \t]+.*\n)*`)
		b = re.ReplaceAll(b, nil)
		b = append(b, []byte(k+":"+v+"\n")...)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err = FindAgent(dir, a.Name)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAsk_ChildHarnessAndModelFollowTheCaller(t *testing.T) {
	fakeHarnessBins(t)
	dir := meshRoot(t)
	target := newTestAgent(t, dir, "architect", true, nil)
	target = setHarness(t, dir, target, HarnessOpencode)
	if _, err := (opencodeHarness{major: 2}).WireAgent(target.Dir()); err != nil {
		t.Fatal(err)
	}
	target = setManifestLines(t, dir, target, map[string]string{
		"model":  " sonnet",
		"models": "\n  opencode: prov/model",
	})
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	t.Setenv("AGENT_DISPATCH_MODEL", "")
	t.Setenv("AGENT_DISPATCH_HOURLY_MAX", "12")

	var model string
	var env []string
	runner := func(_ context.Context, _, _, _, m string, e []string, _, _ io.Writer) int {
		model, env = m, e
		return 0
	}
	ask := func() {
		t.Helper()
		if _, rc, err := Ask(dir, target, AskOptions{Type: "guidance", Prompt: "x", From: "tester", Runner: runner, Now: time.Now(), Stdout: io.Discard, Stderr: io.Discard}); err != nil || rc != 0 {
			t.Fatalf("rc=%d err=%v", rc, err)
		}
	}

	ask() // a terminal: the target's preference, with its opencode model
	if model != "prov/model" {
		t.Fatalf("opencode child: model %q", model)
	}
	t.Setenv("CLAUDECODE", "1")
	ask() // a Claude Code caller, target wired for it: Claude Code, its model
	if model != "sonnet" {
		t.Fatalf("Claude Code child: model %q", model)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDECODE=") {
			t.Fatal("the caller's running-harness marker must not reach the child")
		}
	}
	logb, _ := os.ReadFile(DispatchLogPath(dir))
	if !strings.Contains(string(logb), "harness=opencode") || !strings.Contains(string(logb), "harness=claude") {
		t.Fatalf("the log names each child's harness:\n%s", logb)
	}
	t.Setenv("AGENT_DISPATCH_MODEL", "haiku")
	ask()
	if model != "haiku" {
		t.Fatalf("AGENT_DISPATCH_MODEL overrides: %q", model)
	}
}

// bothWiredCaller is a Claude Code-preferred caller wired for opencode V2
// too, with the `architect` peer dispatchable.
func bothWiredCaller(t *testing.T) (dir string, caller, arch *Agent) {
	t.Helper()
	dir, caller, arch = inSessionPool(t)
	t.Setenv(opencodeMajorEnv, "2")
	if _, err := (opencodeHarness{major: 2}).WireAgent(caller.Dir()); err != nil {
		t.Fatal(err)
	}
	return dir, caller, arch
}

func TestPeers_EveryWiredHarnessGetsItsFormat(t *testing.T) {
	dir, caller, _ := bothWiredCaller(t)
	for _, f := range []string{".claude/agents/mesh-architect.md", ".opencode/agents/mesh-architect.md"} {
		if !fileExists(filepath.Join(caller.Dir(), f)) {
			t.Errorf("missing %s", f)
		}
	}
	if plan := PlanPeerAgents(dir, caller.Dir()); !plan.Empty() {
		t.Fatalf("plan must be empty after wiring: %+v", plan)
	}
	if l := PeerLineFor(caller, claudeHarness{}); !strings.Contains(l, "Agent-tool call") {
		t.Errorf("Claude Code session: %s", l)
	}
	if l := PeerLineFor(caller, opencodeHarness{major: 2}); !strings.Contains(l, "subagent-tool call") {
		t.Errorf("opencode V2 session: %s", l)
	}
	if l := PeerLineFor(caller, opencodeHarness{major: 1}); l != "" {
		t.Errorf("opencode V1 runs no peers: %s", l)
	}
	// A stale definition in either format is planned for refresh.
	if err := os.WriteFile(filepath.Join(caller.Dir(), ".opencode", "agents", "mesh-architect.md"), []byte(distillerAgentMarker+" stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if plan := PlanPeerAgents(dir, caller.Dir()); len(plan.Update) != 1 || plan.Update[0] != "architect" {
		t.Fatalf("stale opencode definition: %+v", plan)
	}
	if _, err := ensurePeerAgents(dir, caller.Dir()); err != nil {
		t.Fatal(err)
	}
	if !PlanPeerAgents(dir, caller.Dir()).Empty() {
		t.Fatal("ensurePeerAgents must refresh every format")
	}
}

func TestAskInSession_FollowsTheRunningHarness(t *testing.T) {
	dir, caller, arch := bothWiredCaller(t)
	ask := func() (string, error) {
		var out bytes.Buffer
		_, _, err := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "x", From: caller.Name, Now: time.Now(), Stdout: &out})
		return out.String(), err
	}
	s, err := ask()
	if err != nil || !strings.Contains(s, "subagent_type: mesh-architect") {
		t.Fatalf("no marker: the preference (Claude Code): %v\n%s", err, s)
	}
	t.Setenv("OPENCODE", "1")
	s, err = ask()
	if err != nil || !strings.Contains(s, "agent: mesh-architect") || strings.Contains(s, "subagent_type") {
		t.Fatalf("opencode V2 running the caller: %v\n%s", err, s)
	}
	t.Setenv(opencodeMajorEnv, "1")
	if _, err := ask(); err == nil || !strings.Contains(err.Error(), "opencode V1 (1.x)") {
		t.Fatalf("opencode V1 runs no peers: %v", err)
	}
}

func TestAskInSession_RunningHarnessNotWiredHasNoDefinition(t *testing.T) {
	dir, caller, arch := inSessionPool(t) // Claude Code only
	t.Setenv(opencodeMajorEnv, "2")
	t.Setenv("OPENCODE", "1")
	_, _, err := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "x", From: caller.Name, Now: time.Now(), Stdout: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "no in-session definition") || !strings.Contains(err.Error(), ".opencode/agents") {
		t.Fatalf("opencode running where it is not wired: %v", err)
	}
}

func TestDistill_FollowsTheRunningHarness(t *testing.T) {
	t.Setenv(opencodeMajorEnv, "2")
	a := &Agent{Name: "x", Harness: HarnessOpencode}
	if SpawnDistillUnder(claudeHarness{}, "", a, "s") {
		t.Fatal("Claude Code distils in-session: nothing to spawn")
	}
	if !harnessAutoDistillsUnder(opencodeHarness{major: 2}) || harnessAutoDistillsUnder(claudeHarness{}) {
		t.Fatal("auto-distil follows the caps")
	}
	if argv := peerDistillArgvFor(claudeHarness{}, "p"); len(argv) < 3 || argv[0] != "claude" || argv[1] != "-p" || !hasString(argv, "--allowedTools") {
		t.Fatalf("Claude Code peer distill keeps its scoped argv: %v", argv)
	}
	oc := opencodeHarness{major: 2}
	if got, want := strings.Join(peerDistillArgvFor(oc, "p"), " "), strings.Join(oc.DistillCmd("p"), " "); got != want {
		t.Fatalf("a harness with no scoped form uses its DistillCmd: %q vs %q", got, want)
	}

	// The boundary nudge asks for an in-session distiller: only under a
	// harness that has one, whatever the agent prefers.
	fires := func(flag string) bool {
		p := HookPayload{Cwd: t.TempDir(), SessionID: "s", Prompt: "x", Harness: flag}
		for i := 0; i <= distillThreshold; i++ {
			if distillNudge(p, a.Name, a) != "" {
				return true
			}
		}
		return false
	}
	if fires(HarnessOpencode) {
		t.Error("opencode distils at session end: no nudge")
	}
	if !fires(HarnessClaude) {
		t.Error("Claude Code running an opencode-preferred agent is nudged")
	}
}

func TestRenderPeer_PerHarnessModel(t *testing.T) {
	peer := &Agent{Name: "architect", Model: "sonnet", Models: map[string]string{HarnessOpencode: "prov/model"},
		Accepts: []Accept{{Type: "guidance"}}}
	c, err := renderPeer("hub", peer, false)
	if err != nil || !strings.Contains(c, "model: sonnet") {
		t.Fatalf("Claude Code peer takes model: — %v\n%s", err, c)
	}
	o, err := renderOpencodePeer("hub", peer, false)
	if err != nil || !strings.Contains(o, "model: prov/model") {
		t.Fatalf("opencode peer takes models.opencode — %v\n%s", err, o)
	}
	peer.Models = nil
	if o, _ := renderOpencodePeer("hub", peer, false); strings.Contains(o, "model:") {
		t.Fatalf("a bare alias is not an opencode model:\n%s", o)
	}
}
