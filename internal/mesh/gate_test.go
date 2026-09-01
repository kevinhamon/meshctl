package mesh

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// edit builds a PreToolUse payload for a file-editing tool.
func edit(tool, filePath string) HookPayload {
	p := HookPayload{ToolName: tool}
	if filePath != "" {
		p.ToolInput = json.RawMessage(`{"file_path":` + strconv.Quote(filePath) + `}`)
	}
	return p
}

func TestGate_DenyAndAllow(t *testing.T) {
	cases := []struct {
		name string
		p    HookPayload
		deny bool
	}{
		// --- competing memory stores: DENY ---
		{"claude auto-memory write", edit("Write", "/Users/k/.claude/projects/p/memory/note.md"), true},
		{"claude auto-memory edit", edit("Edit", "/home/u/.claude/projects/x/memory/MEMORY.md"), true},
		{"claude-mem on-disk store", edit("Write", "/Users/k/.claude-mem/store.db"), true},
		{"claude-mem write tool", HookPayload{ToolName: "mcp__plugin_claude-mem__memory_add"}, true},
		{"claude-mem observation tool", HookPayload{ToolName: "mcp__plugin_claude-mem__observation_add"}, true},
		{"claude-mem build_corpus", HookPayload{ToolName: "mcp__plugin_claude-mem__build_corpus"}, true},
		// --- hand-written intake: DENY ---
		{"hand-written intake", edit("Write", "/Users/k/agents/architect/"+IntakeDir+"/msg.md"), true},
		{"intake via MultiEdit", edit("MultiEdit", "/Users/k/agents/pm/"+IntakeDir+"/queued.json"), true},

		// --- the mesh's OWN stores: ALLOW ---
		{"mesh knowledge tree", edit("Write", "/Users/k/agents/architect/knowledge/playbooks/x.md"), false},
		{"mesh .memory dir", edit("Write", "/Users/k/agents/architect/.memory/index.json"), false},
		// --- claude-mem READS: ALLOW ---
		{"claude-mem search", HookPayload{ToolName: "mcp__plugin_claude-mem__search"}, false},
		{"claude-mem smart_search", HookPayload{ToolName: "mcp__plugin_claude-mem__smart_search"}, false},
		{"claude-mem timeline", HookPayload{ToolName: "mcp__plugin_claude-mem__timeline"}, false},
		// --- unrelated work: ALLOW ---
		{"ordinary source write", edit("Write", "/Users/k/project/src/main.go"), false},
		{"read of a memory path", edit("Read", "/Users/k/.claude/projects/p/memory/note.md"), false},
		{"bash", HookPayload{ToolName: "Bash"}, false},
		{"edit with no path", HookPayload{ToolName: "Edit"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := GatePreToolUse("", c.p).Deny
			if got != c.deny {
				t.Fatalf("Deny=%v, want %v (tool=%q)", got, c.deny, c.p.ToolName)
			}
		})
	}
}

func TestGate_EmitEnvelope(t *testing.T) {
	// Deny ⇒ Claude Code's PreToolUse deny envelope with a reason.
	var buf bytes.Buffer
	if err := EmitGateDecision(&buf, GatePreToolUse("", edit("Write", "/x/.claude-mem/y"))); err != nil {
		t.Fatal(err)
	}
	var out struct {
		HSO struct {
			Event  string `json:"hookEventName"`
			Dec    string `json:"permissionDecision"`
			Reason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("deny output is not valid JSON: %v (%s)", err, buf.String())
	}
	if out.HSO.Event != "PreToolUse" || out.HSO.Dec != "deny" || out.HSO.Reason == "" {
		t.Fatalf("bad deny envelope: %+v", out.HSO)
	}

	// Allow ⇒ nothing written (default flow proceeds).
	buf.Reset()
	if err := EmitGateDecision(&buf, GatePreToolUse("", edit("Write", "/x/src/main.go"))); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("allow must emit nothing, got %q", buf.String())
	}
}

// TestGate_DoctorRequiresGate is the enforcement proof: an agent missing the
// PreToolUse gate is a doctor ERROR (doctrine unenforced), and onboarding
// (EnsureAgentSettings) clears it. This is the before/after the human runs.
func TestGate_DoctorRequiresGate(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)

	// Onboard wires it: gate present, stamp current, no gate finding.
	if _, err := EnsureAgentSettings(a.Dir()); err != nil {
		t.Fatal(err)
	}
	if hasFinding(gateWiringFindings(a), "enforcement gate not wired") {
		t.Fatal("freshly-wired agent should have the gate")
	}
	if v := WiringVersion(a.Dir()); v != MeshWiringVersion {
		t.Fatalf("stamp = v%d, want v%d", v, MeshWiringVersion)
	}
	sp := filepath.Join(a.Dir(), ".claude", "settings.json")
	root := map[string]any{}
	b, _ := os.ReadFile(sp)
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	if !hookCommandPresent(root["hooks"].(map[string]any)["PreToolUse"].([]any), "meshctl hook pre-tool-use") {
		t.Fatal("PreToolUse gate not written to settings.json")
	}

	// Downgrade to the pre-gate state the 13 live agents are in: strip PreToolUse
	// and the stamp.
	delete(root["hooks"].(map[string]any), "PreToolUse")
	nb, _ := json.MarshalIndent(root, "", "  ")
	if err := os.WriteFile(sp, nb, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(a.Dir(), ".claude", meshWiringStampFile))

	// Now doctor must FAIL on this agent for the gate specifically.
	if !hasFinding(gateWiringFindings(a), "enforcement gate not wired") {
		t.Fatal("unwired agent must report the gate as missing")
	}
	findings, ok := Doctor(dir)
	if ok {
		t.Fatal("Doctor must not be ok while an agent's enforcement gate is missing")
	}
	if !hasFinding(findings, "enforcement gate not wired") {
		t.Fatal("Doctor output must name the missing gate")
	}

	// Re-onboard: the same distribution pipe re-installs the gate + stamp.
	if _, err := EnsureAgentSettings(a.Dir()); err != nil {
		t.Fatal(err)
	}
	if hasFinding(gateWiringFindings(a), "enforcement gate not wired") {
		t.Fatal("re-onboard must restore the gate")
	}
	if v := WiringVersion(a.Dir()); v != MeshWiringVersion {
		t.Fatalf("re-onboard stamp = v%d, want v%d", v, MeshWiringVersion)
	}
}
