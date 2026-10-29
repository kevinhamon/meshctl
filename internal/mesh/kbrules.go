package mesh

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// kbrules.go — the KB-rule gate. A standing rule lives in the agent's own KB as
// an ordinary note that carries two frontmatter keys:
//
//	---
//	rule: "Run migrations with --dry-run first; never apply one straight to prod."
//	triggers:
//	  - 'migrate'
//	---
//
// Before a gated tool call runs, the gate matches the call's tool name and what
// it acts on (the Bash command without heredoc bodies, the edited paths, an MCP
// tool's arguments) against every rule's triggers, case-insensitively. On a
// match it DENIES the call once, with the rule text and the note path as the
// reason, and records an ack for (session, note). The re-issued call — now made
// with the rule in context — is allowed, as is every later match of that note
// in the same session.
//
// Why the gate does the matching itself: prescribing "recall the KB before you
// act" in prose is optional and is observably skipped under task pressure. A
// check the model must satisfy (e.g. "a recall ran") is gamed by an irrelevant
// recall. Matching the action itself takes the model out of the loop.
//
// Cost: nothing is injected unless a rule fires. Fail-open throughout — an
// unreadable KB, a bad regex, or an unresolvable agent never blocks a call.

// KBRule is one standing rule loaded from a KB note.
type KBRule struct {
	Path     string // KB-relative path of the note
	Rule     string
	Triggers []*regexp.Regexp
}

const ruleAckDir = "rule-acks"

// LoadKBRules walks <agent>/knowledge/** and returns every note that declares a
// non-empty rule with at least one valid trigger. Invalid trigger patterns are
// skipped (fail-open), never fatal.
func LoadKBRules(agentDir string) []KBRule {
	root := filepath.Join(agentDir, KnowledgeDir)
	var rules []KBRule
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "candidates" { // undistilled drafts are not rules yet
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		fm, _ := splitFrontmatter(string(b))
		if fm == "" {
			return nil
		}
		var meta struct {
			Rule     string   `yaml:"rule"`
			Triggers []string `yaml:"triggers"`
		}
		if decodeYAML(fm, &meta) != nil || strings.TrimSpace(meta.Rule) == "" {
			return nil
		}
		var res []*regexp.Regexp
		for _, t := range meta.Triggers {
			if strings.TrimSpace(t) == "" {
				continue
			}
			if re, err := regexp.Compile("(?i)" + t); err == nil {
				res = append(res, re)
			}
		}
		if len(res) == 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rules = append(rules, KBRule{Path: filepath.ToSlash(filepath.Join(KnowledgeDir, rel)), Rule: strings.TrimSpace(meta.Rule), Triggers: res})
		return nil
	})
	sort.Slice(rules, func(i, j int) bool { return rules[i].Path < rules[j].Path })
	return rules
}

// toolInputText flattens every string value in a tool_input blob into one
// newline-joined string — the Bash command, an edit's path and new text, an MCP
// tool's arguments — so triggers match the action regardless of input shape.
func toolInputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	var parts []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			parts = append(parts, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		}
	}
	walk(v)
	return strings.Join(parts, "\n")
}

// kbRuleActionText is what a rule's triggers are matched against: the tool name
// plus what the call acts on. A shell call contributes its own arguments only
// (ShellOwnArguments: no heredoc bodies, no description field); a file edit or
// patch contributes its target paths, never the text it writes — a report or
// note that discusses a rule's topic is not the action the rule guards
// (issue #2). Every other tool (MCP above all) contributes its flattened input.
func kbRuleActionText(p HookPayload) string {
	switch {
	case shellTools[p.ToolName]:
		return p.ToolName + "\n" + ShellOwnArguments(shellCommandInput(p))
	case fileEditTools[p.ToolName], patchTools[p.ToolName]:
		return p.ToolName + "\n" + strings.Join(gateEditPaths(p), "\n")
	}
	return p.ToolName + "\n" + toolInputText(p.ToolInput)
}

// kbEditRoots are the directories whose edits the KB-rule gate never sees: the
// agent's own knowledge/ and the knowledge/ of its KB session worktrees
// (`kb begin`, <pool>/worktrees/<agent>-<session>/), where candidates and
// promoted notes are written during a KB session.
func kbEditRoots(agentsDir string, a *Agent) []string {
	roots := []string{filepath.Join(a.Dir(), KnowledgeDir)}
	if agentsDir == "" {
		return roots
	}
	entries, err := os.ReadDir(WorktreesPath(agentsDir))
	if err != nil {
		return roots
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), a.Name+"-") {
			roots = append(roots, filepath.Join(WorktreesPath(agentsDir), e.Name(), KnowledgeDir))
		}
	}
	return roots
}

// MatchKBRules returns the rules whose triggers match text, in path order.
func MatchKBRules(rules []KBRule, text string) []KBRule {
	if text == "" {
		return nil
	}
	var hit []KBRule
	for _, r := range rules {
		for _, re := range r.Triggers {
			if re.MatchString(text) {
				hit = append(hit, r)
				break
			}
		}
	}
	return hit
}

func ruleAckPath(agentDir, sessionID string) string {
	if sessionID == "" {
		sessionID = "no-session"
	}
	return filepath.Join(agentDir, ".memory", ruleAckDir, sanitizeID(sessionID)+".json")
}

func sanitizeID(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '.' || r == ' ' {
			return '_'
		}
		return r
	}, s)
}

func loadRuleAcks(path string) map[string]bool {
	acks := map[string]bool{}
	if b, err := os.ReadFile(path); err == nil {
		var list []string
		if json.Unmarshal(b, &list) == nil {
			for _, p := range list {
				acks[p] = true
			}
		}
	}
	return acks
}

func saveRuleAcks(path string, acks map[string]bool) {
	list := make([]string, 0, len(acks))
	for p := range acks {
		list = append(list, p)
	}
	sort.Strings(list)
	b, err := json.Marshal(list)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}

// gateKBRules is the KB-rule step of GatePreToolUse. It returns a deny decision
// for the first not-yet-acked matching rules of this session, acking them as it
// denies; otherwise allow.
func gateKBRules(agentsDir string, p HookPayload) GateDecision {
	a := payloadAgent(agentsDir, p)
	if a == nil {
		return GateDecision{}
	}
	// Edits to the KB itself (writing or fixing a rule note), on the main
	// checkout or in a KB session worktree, are not gated — otherwise
	// authoring a rule would trip the rule.
	if paths := gateEditPaths(p); len(paths) > 0 {
		roots := kbEditRoots(agentsDir, a)
		inKB := true
		for _, path := range paths {
			in := false
			for _, root := range roots {
				in = in || underDir(path, root)
			}
			inKB = inKB && in
		}
		if inKB {
			return GateDecision{}
		}
	}
	// The tool name is part of the action text, so a rule can target a tool
	// (e.g. 'createTrackerIssue') as well as its arguments.
	hits := MatchKBRules(LoadKBRules(a.Dir()), kbRuleActionText(p))
	if len(hits) == 0 {
		return GateDecision{}
	}
	ackPath := ruleAckPath(a.Dir(), p.SessionID)
	acks := loadRuleAcks(ackPath)
	var fresh []KBRule
	for _, r := range hits {
		if !acks[r.Path] {
			fresh = append(fresh, r)
		}
	}
	if len(fresh) == 0 {
		return GateDecision{}
	}
	var b strings.Builder
	b.WriteString("mesh KB rule — this action matches a standing rule in your own KB. Read it, then re-issue the action ONLY if it is consistent with the rule (the retry is allowed):")
	notes := make([]string, 0, len(fresh))
	for _, r := range fresh {
		b.WriteString("\n- ")
		b.WriteString(r.Rule)
		b.WriteString(" (")
		b.WriteString(r.Path)
		b.WriteString(")")
		acks[r.Path] = true
		notes = append(notes, r.Path)
	}
	saveRuleAcks(ackPath, acks)
	return GateDecision{Deny: true, Check: GateCheckKBRule, Reason: b.String(), Rules: notes}
}
