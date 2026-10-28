package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const migrateRuleNote = `---
rule: "Run migrations with --dry-run first — never apply one straight to prod."
triggers:
  - 'migrate\b.*\bprod\b'
  - 'schema\.sql'
  - 'drop-table'
  - '('
---

# Migrations get a dry run
`

func writeKBNote(t *testing.T, a *Agent, rel, body string) {
	t.Helper()
	p := filepath.Join(a.Dir(), KnowledgeDir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func bash(a *Agent, session, cmd string) HookPayload {
	return HookPayload{Cwd: a.Dir(), SessionID: session, ToolName: "Bash",
		ToolInput: json.RawMessage(`{"command":` + strconv.Quote(cmd) + `}`)}
}

func TestKBRules_LoadSkipsNonRulesAndBadPatterns(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	writeKBNote(t, a, "experience/migrations.md", migrateRuleNote)
	writeKBNote(t, a, "experience/plain.md", "# no frontmatter\n")
	writeKBNote(t, a, "experience/no-triggers.md", "---\nrule: \"x\"\n---\nbody\n")
	writeKBNote(t, a, "candidates/draft.md", migrateRuleNote) // drafts are not rules

	rules := LoadKBRules(a.Dir())
	if len(rules) != 1 || rules[0].Path != "knowledge/experience/migrations.md" {
		t.Fatalf("want only the token rule, got %+v", rules)
	}
	if len(rules[0].Triggers) != 3 { // the invalid '(' is skipped, not fatal
		t.Fatalf("want 3 valid triggers, got %d", len(rules[0].Triggers))
	}
}

func TestKBRules_GateDeniesOnceThenAllows(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	writeKBNote(t, a, "experience/migrations.md", migrateRuleNote)

	// First matching action in a session: denied, with the rule as the reason.
	d := GatePreToolUse(dir, bash(a, "s1", "goose -dir db migrate prod up"))
	if !d.Deny || !strings.Contains(d.Reason, "never apply one straight to prod") || !strings.Contains(d.Reason, "knowledge/experience/migrations.md") {
		t.Fatalf("want deny carrying the rule, got %+v", d)
	}
	// Re-issue (rule now in context): allowed, and stays allowed this session.
	if d := GatePreToolUse(dir, bash(a, "s1", "goose -dir db migrate prod up")); d.Deny {
		t.Fatalf("retry must be allowed, got %+v", d)
	}
	if d := GatePreToolUse(dir, bash(a, "s1", "cat db/schema.sql")); d.Deny {
		t.Fatalf("acked rule must stay allowed in-session, got %+v", d)
	}
	// A new session sees the rule again.
	if d := GatePreToolUse(dir, bash(a, "s2", "migrate --env prod")); !d.Deny {
		t.Fatalf("new session must be denied once, got %+v", d)
	}
	// Unrelated action: no rule, no cost.
	if d := GatePreToolUse(dir, bash(a, "s3", "go test ./...")); d.Deny {
		t.Fatalf("unrelated command must pass, got %+v", d)
	}
}

func TestKBRules_MatchesMCPArgsAndSkipsKBEdits(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	writeKBNote(t, a, "experience/migrations.md", migrateRuleNote)

	mcp := HookPayload{Cwd: a.Dir(), SessionID: "m1", ToolName: "mcp__tracker__addComment",
		ToolInput: json.RawMessage(`{"issue":"PROJ-1","body":{"text":"remove the drop-table rule"}}`)}
	if d := GatePreToolUse(dir, mcp); !d.Deny {
		t.Fatalf("nested MCP args must match, got %+v", d)
	}
	kbEdit := HookPayload{Cwd: a.Dir(), SessionID: "m2", ToolName: "Edit",
		ToolInput: json.RawMessage(`{"file_path":` + strconv.Quote(filepath.Join(a.Dir(), KnowledgeDir, "candidates/migrations.md")) + `,"new_string":"schema.sql"}`)}
	if d := GatePreToolUse(dir, kbEdit); d.Deny {
		t.Fatalf("editing the KB itself must not be gated, got %+v", d)
	}
}

func TestKBRules_MatchesToolName(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	writeKBNote(t, a, "experience/tracker.md", "---\nrule: \"Every tracker issue links its decision record.\"\ntriggers:\n  - 'createTrackerIssue'\n---\nbody\n")
	p := HookPayload{Cwd: a.Dir(), SessionID: "n1", ToolName: "mcp__tracker_server__createTrackerIssue",
		ToolInput: json.RawMessage(`{"projectKey":"PROJ","summary":"x"}`)}
	if d := GatePreToolUse(dir, p); !d.Deny {
		t.Fatalf("rule must match on tool name, got %+v", d)
	}
}

func TestKBRules_NoAgentFailsOpen(t *testing.T) {
	p := HookPayload{Cwd: "/nonexistent/nowhere", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"migrate --env prod"}`)}
	if d := GatePreToolUse("", p); d.Deny {
		t.Fatalf("unresolvable agent must fail open, got %+v", d)
	}
}

func TestSettings_UpgradesMeshMatcher(t *testing.T) {
	dest := t.TempDir()
	old := `{"hooks":{"PreToolUse":[
	  {"matcher":"Write|Edit|MultiEdit|NotebookEdit|mcp__plugin_claude-mem__","hooks":[{"type":"command","command":"meshctl hook pre-tool-use","timeout":10}]},
	  {"matcher":"Bash","hooks":[{"type":"command","command":"bash ~/x/pr-review-gate.sh"}]}]}}`
	if err := os.MkdirAll(filepath.Join(dest, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".claude", "settings.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAgentSettings(dest); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dest, ".claude", "settings.json"))
	s := string(b)
	if !strings.Contains(s, `"`+GateHookMatcher+`"`) {
		t.Fatalf("mesh matcher not upgraded:\n%s", s)
	}
	if !strings.Contains(s, "pr-review-gate.sh") || strings.Count(s, "meshctl hook pre-tool-use") != 1 {
		t.Fatalf("non-mesh entry must be kept and mesh entry not duplicated:\n%s", s)
	}
	if WiringVersion(dest) != MeshWiringVersion {
		t.Fatalf("wiring stamp not bumped")
	}
}
