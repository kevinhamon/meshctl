package mesh

import (
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

// gate.go — the mesh's only fail-CLOSED hook. The lifecycle hooks (hook.go) are
// advisory: they inject context and exit 0, so a model is free to ignore them.
// This one can REFUSE a tool call. It exists because doctrine delivered as
// injected prose is optional, and the only way to make "route memory through the
// mesh" non-optional is to deny the competing paths so the mesh is the one road
// left (ADR-0044).
//
// Scope of "fail-closed": it denies the patterns it recognises, reliably —
// including by tool NAME alone, so a malformed tool_input cannot smuggle a write
// past it. Anything it does not recognise (an unrelated tool, an edit whose path
// it cannot read) is ALLOWED: a gate that blocked on every parse miss would
// break every turn, which is the opposite of enforcement. The gate removes known
// alternatives; it does not police all writes.
//
// It cannot force a positive act (a model that simply never calls meshctl writes
// nothing for the gate to see). Compelling positive behaviour needs a harness
// the mesh fully controls; this closes the negative space, which is what kills
// the observed drift to Claude's built-in memory.

// GateDecision is the outcome of evaluating one tool call.
type GateDecision struct {
	Deny   bool
	Reason string
}

const claudeMemPrefix = "mcp__plugin_claude-mem__"

// claudeMemMutators are the claude-mem MCP tools that WRITE its store. Reads
// (search/query/get/list/*context*/timeline/outline) stay allowed — the gate
// removes the competing write path, not lookups.
var claudeMemMutators = map[string]bool{
	"memory_add":               true,
	"observation_add":          true,
	"observation_record_event": true,
	"build_corpus":             true,
	"rebuild_corpus":           true,
	"prime_corpus":             true,
	"reprime_corpus":           true,
}

// fileEditTools name the tools whose input carries a target path the gate reads:
// Claude Code's, and opencode's (lowercase; V2 write/edit take a `path`).
var fileEditTools = map[string]bool{
	"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true,
	"write": true, "edit": true, "multiedit": true,
}

// patchTools carry their target paths inside patch text (opencode apply_patch).
var patchTools = map[string]bool{"apply_patch": true, "patch": true}

// isClaudeMemMutator reports whether a tool name is a claude-mem write tool.
// Claude Code names it mcp__plugin_claude-mem__<tool>; opencode names MCP tools
// <server>_<tool> with a user-chosen server name, so match any name carrying
// "claude-mem"/"claude_mem" that ends in a mutator.
func isClaudeMemMutator(name string) bool {
	if strings.HasPrefix(name, claudeMemPrefix) {
		return claudeMemMutators[strings.TrimPrefix(name, claudeMemPrefix)]
	}
	if !strings.Contains(name, "claude-mem") && !strings.Contains(name, "claude_mem") {
		return false
	}
	for m := range claudeMemMutators {
		if strings.HasSuffix(name, "_"+m) {
			return true
		}
	}
	return false
}

// GatePreToolUse evaluates a PreToolUse payload against mesh doctrine. agentsDir
// is accepted for future workspace-scoped rules; today's rules are path-shaped
// and machine-global, so the gate holds even when pool resolution fails.
func GatePreToolUse(agentsDir string, p HookPayload) GateDecision {
	name := p.ToolName

	// 1. claude-mem write tools — competing memory store. Denied by name, so this
	//    holds even if tool_input is missing or malformed. Reads fall through to
	//    the KB-rule check.
	if isClaudeMemMutator(name) {
		return GateDecision{true, "mesh: memory is owned by the mesh — do not write the claude-mem store. " +
			"Persist through `meshctl memory` / `meshctl kb` (knowledge), or `meshctl send` / `meshctl ask` (messages)."}
	}

	// 2/3. File writes: inspect the target path(s).
	for _, path := range gateEditPaths(p) {
		if reason := deniedWritePath(path); reason != "" {
			return GateDecision{true, reason}
		}
	}

	// 4. Standing rules from the agent's own KB (kbrules.go): deny once per
	//    session per matching rule, with the rule as the reason.
	return gateKBRules(agentsDir, p)
}

// gateEditPaths returns the absolute target paths of a file-writing tool call
// (nil for any other tool). opencode passes paths relative to the workspace, so
// a relative path is resolved against the payload cwd — "intake/x.md" must match
// the same rules as "/abs/agent/intake/x.md".
func gateEditPaths(p HookPayload) []string {
	var paths []string
	switch {
	case fileEditTools[p.ToolName]:
		if path := gateFilePath(p.ToolInput); path != "" {
			paths = []string{path}
		}
	case patchTools[p.ToolName]:
		paths = patchFilePaths(p.ToolInput)
	}
	for i, path := range paths {
		if !filepath.IsAbs(path) && p.Cwd != "" {
			paths[i] = filepath.Join(p.Cwd, path)
		}
	}
	return paths
}

// gateFilePath pulls the target path out of a tool_input blob: file_path
// (Claude Write/Edit/MultiEdit), notebook_path (NotebookEdit), filePath (opencode
// V1) or path (opencode V2), tolerating any or none.
func gateFilePath(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		CamelPath    string `json:"filePath"`
		Path         string `json:"path"`
	}
	_ = json.Unmarshal(raw, &in)
	for _, v := range []string{in.FilePath, in.NotebookPath, in.CamelPath, in.Path} {
		if v != "" {
			return v
		}
	}
	return ""
}

var rePatchFile = regexp.MustCompile(`(?m)^\*\*\* (?:(?:Add|Update|Delete) File|Move to): *(.+?)\s*$`)

// patchFilePaths extracts every file an apply_patch-style patch touches, from
// whichever string field carries the patch text.
func patchFilePaths(raw json.RawMessage) []string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	var paths []string
	for _, v := range in {
		if s, ok := v.(string); ok {
			for _, m := range rePatchFile.FindAllStringSubmatch(s, -1) {
				paths = append(paths, m[1])
			}
		}
	}
	return paths
}

// deniedWritePath returns a non-empty reason when the path is a competing memory
// store (Claude Code auto-memory or claude-mem) or a hand-written intake file.
// The mesh's OWN stores — <agent>/knowledge/ and <agent>/.memory/ — are not
// matched: ".memory" never contains the "/memory/" segment, and neither carries
// a ".claude"/".claude-mem"/"intake" segment.
func deniedWritePath(path string) string {
	s := filepath.ToSlash(path)

	// Claude Code's built-in auto-memory: ~/.claude/**/memory/**
	if strings.Contains(s, "/.claude/") && (strings.Contains(s, "/memory/") || strings.HasSuffix(s, "/memory")) {
		return "mesh: this writes Claude Code's built-in memory store, which the mesh replaces. " +
			"Persist through the mesh (`meshctl memory` / `meshctl kb`), not ~/.claude/**/memory/**."
	}
	// claude-mem on-disk store.
	if strings.Contains(s, "/.claude-mem/") || strings.HasSuffix(s, "/.claude-mem") {
		return "mesh: this writes the claude-mem store, which the mesh replaces. Use `meshctl memory` / `meshctl kb`."
	}
	// Hand-written intake (ADR-0043): intake/ is an ephemeral queue owned by
	// meshctl, never authored by hand.
	if strings.Contains(s, "/"+IntakeDir+"/") || strings.HasSuffix(s, "/"+IntakeDir) {
		return "mesh: intake/ is an ephemeral queue owned by meshctl — do not hand-write it. " +
			"Send work with `meshctl send`/`meshctl ask`; write an outcome back with `meshctl inbox respond`, " +
			"or `meshctl inbox decline --reason … [--redirect <agent>]` if it is not yours."
	}
	return ""
}

// EmitGateDecision writes Claude Code's PreToolUse permission-decision envelope.
// Allow ⇒ nothing written (the default flow proceeds); deny ⇒ the deny envelope
// carrying its reason. opencode's form is the raw reason (Harness.EmitGate):
// the V2 plugin throws it from tool.execute.before. opencode V1 has no
// equivalent hook and is not gated.
func EmitGateDecision(w io.Writer, d GateDecision) error {
	if !d.Deny {
		return nil
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": d.Reason,
		},
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
