package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// peers.go — in-session dispatch: every dispatchable peer is rendered
// as a Claude Code subagent definition in each Claude-harness agent's
// `.claude/agents/mesh-<peer>.md`, computed from the pool's manifests exactly
// like the roster. A caller then consults an advisory peer with the Agent tool
// instead of spawning a second `claude` process in a second terminal: same
// intake file, same audit trail, same cap — one process.
//
// The definitions are derived files. They are written by `agent new|onboard`
// and `pool upgrade`, carry the meshctl-managed marker so hand-authored
// subagents are never touched, and are removed when a peer stops being
// dispatchable or leaves the pool.

// PeerAgentPrefix is the subagent-name prefix for in-session peers.
const PeerAgentPrefix = "mesh-"

// peerGateMatcher mirrors the settings.json gate matcher: file edits, shell and
// every MCP tool pass through the peer's own gate (its KB rules + the in-session
// deny set).
const peerGateMatcher = "Write|Edit|MultiEdit|NotebookEdit|Bash|mcp__"

// peerFrontmatter is the subagent frontmatter, marshalled with yaml.v3 so a role
// text containing ':' or '#' can never break the header.
type peerFrontmatter struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Tools       string         `yaml:"tools"`
	Model       string         `yaml:"model"`
	OmitClaude  bool           `yaml:"omitClaudeMd"`
	Hooks       map[string]any `yaml:"hooks"`
}

// PeerAgentFile is the path of a peer's subagent definition inside a caller.
func PeerAgentFile(callerDir, peer string) string {
	return filepath.Join(callerDir, ".claude", "agents", PeerAgentPrefix+peer+".md")
}

// PeerAgentName is the `subagent_type` a caller passes to the Agent tool.
func PeerAgentName(peer string) string { return PeerAgentPrefix + peer }

// RenderPeerAgent renders the subagent definition for `peer` as installed into
// `caller`'s workspace.
func RenderPeerAgent(caller string, peer *Agent) (string, error) {
	tpl, err := templatesFS.ReadFile("templates/claude/mesh-peer.md")
	if err != nil {
		return "", err
	}
	body := strings.NewReplacer("__PEER__", peer.Name, "__CALLER__", caller,
		"__TITLE__", firstNonEmpty(peer.Title, peer.Name)).Replace(string(tpl))

	model := strings.TrimSpace(peer.Model)
	if model == "" || model == "default" {
		model = "inherit"
	}
	hook := func(sub string) map[string]any {
		return map[string]any{"type": "command", "timeout": 10,
			"command": fmt.Sprintf("meshctl hook %s --agent %s --in-session", sub, peer.Name)}
	}
	fm := peerFrontmatter{
		Name:        PeerAgentName(peer.Name),
		Description: peerDescription(peer),
		// No Agent tool: a consulted peer cannot spawn subagents (depth guard).
		// No Write/Edit: it answers through `meshctl inbox respond`. Bash is
		// scoped by the in-session gate, not by this list (the `tools` field
		// takes tool names only).
		Tools:      "Read, Grep, Glob, Bash",
		Model:      model,
		OmitClaude: true,
		Hooks: map[string]any{
			"PreToolUse": []any{map[string]any{"matcher": peerGateMatcher, "hooks": []any{hook("pre-tool-use")}}},
			"Stop":       []any{map[string]any{"hooks": []any{hook("session-end")}}},
		},
	}
	head, err := yaml.Marshal(fm)
	if err != nil {
		return "", err
	}
	return "---\n" + string(head) + "---\n\n" + body, nil
}

// peerDescription is what makes the caller's model delegate correctly: title,
// role, the request types this inbox accepts, and the one precondition.
func peerDescription(a *Agent) string {
	var types []string
	for _, x := range a.Accepts {
		if x.Desc != "" {
			types = append(types, x.Type+" ("+x.Desc+")")
		} else {
			types = append(types, x.Type)
		}
	}
	role := strings.Join(strings.Fields(a.Role), " ")
	if len(role) > 300 {
		role = role[:297] + "..."
	}
	s := firstNonEmpty(a.Title, a.Name)
	if role != "" {
		s += " — " + strings.TrimRight(role, ".")
	}
	s += ". Mesh peer `" + a.Name + "`, consulted in-session for: " + strings.Join(types, "; ") +
		". Use ONLY after `meshctl msg ask " + a.Name + " <type> \"...\" --in-session` printed a request id, and pass that id in the prompt."
	return s
}

// InstalledPeers lists the peers with a managed in-session definition in a
// caller workspace, sorted.
func InstalledPeers(callerDir string) []string {
	entries, _ := os.ReadDir(filepath.Join(callerDir, ".claude", "agents"))
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, PeerAgentPrefix) || !strings.HasSuffix(n, ".md") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(callerDir, ".claude", "agents", n)); err == nil && strings.Contains(string(b), distillerAgentMarker) {
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(n, PeerAgentPrefix), ".md"))
		}
	}
	sort.Strings(out)
	return out
}

// PeerLine is the session-start sentence that tells a Claude Code agent which
// peers it can consult in-session and how. Injected so existing agents learn
// the verb without a CLAUDE.md edit. "" when the agent has no peer definitions.
func PeerLine(a *Agent) string {
	if a.HarnessName() != HarnessClaude {
		return ""
	}
	peers := InstalledPeers(a.Dir())
	if len(peers) == 0 {
		return ""
	}
	return "# In-session peers\nAdvisory peers you can consult without leaving this session: `" +
		strings.Join(peers, "`, `") + "`. Run `meshctl ask <peer> <type> \"…\" --in-session`, then make the " +
		"Agent-tool call it prints (`subagent_type: " + PeerAgentPrefix + "<peer>`); read the answer with `meshctl sent`. " +
		"Plain `meshctl ask` (headless, second process) still works; state-mutating peers stay `send`-only."
}

// PeerPlan is what ensurePeerAgents would do to one caller workspace.
type PeerPlan struct {
	Write, Update, Remove []string // peer names
	Hand                  []string // hand-authored mesh-<peer>.md left alone
}

// Empty reports no pending change.
func (p PeerPlan) Empty() bool {
	return len(p.Write)+len(p.Update)+len(p.Remove) == 0
}

// Actions renders the plan as upgrade-preview lines.
func (p PeerPlan) Actions() []string {
	var out []string
	if len(p.Write) > 0 {
		out = append(out, "write in-session peer subagents: "+strings.Join(prefixed(p.Write), ", "))
	}
	if len(p.Update) > 0 {
		out = append(out, "refresh in-session peer subagents: "+strings.Join(prefixed(p.Update), ", "))
	}
	if len(p.Remove) > 0 {
		out = append(out, "remove in-session peer subagents (no longer dispatchable): "+strings.Join(prefixed(p.Remove), ", "))
	}
	return out
}

func prefixed(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = PeerAgentPrefix + n
	}
	return out
}

// PlanPeerAgents compares the dispatchable peers of the pool with the managed
// definitions in callerDir. The caller itself is never its own peer. Fail-soft:
// an unreadable pool yields an empty plan.
func PlanPeerAgents(poolRoot, callerDir string) PeerPlan {
	var plan PeerPlan
	caller := filepath.Base(callerDir)
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return plan
	}
	want := map[string]bool{}
	for _, a := range agents {
		if a.Name == caller || !a.IsDispatchable() {
			continue
		}
		want[a.Name] = true
		out := PeerAgentFile(callerDir, a.Name)
		cur, err := os.ReadFile(out)
		if err != nil {
			plan.Write = append(plan.Write, a.Name)
			continue
		}
		if !strings.Contains(string(cur), distillerAgentMarker) {
			plan.Hand = append(plan.Hand, a.Name)
			continue
		}
		body, err := RenderPeerAgent(caller, a)
		if err != nil || string(cur) != body {
			plan.Update = append(plan.Update, a.Name)
		}
	}
	// Managed definitions for peers that are gone or no longer dispatchable.
	entries, _ := os.ReadDir(filepath.Join(callerDir, ".claude", "agents"))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, PeerAgentPrefix) || !strings.HasSuffix(n, ".md") {
			continue
		}
		peer := strings.TrimSuffix(strings.TrimPrefix(n, PeerAgentPrefix), ".md")
		if want[peer] {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(callerDir, ".claude", "agents", n)); err == nil && strings.Contains(string(b), distillerAgentMarker) {
			plan.Remove = append(plan.Remove, peer)
		}
	}
	sort.Strings(plan.Write)
	sort.Strings(plan.Update)
	sort.Strings(plan.Remove)
	return plan
}

// ensurePeerAgents applies PlanPeerAgents to a Claude-harness caller workspace.
// Returns onboard-style notes (empty ⇒ nothing changed).
func ensurePeerAgents(poolRoot, callerDir string) ([]string, error) {
	plan := PlanPeerAgents(poolRoot, callerDir)
	if plan.Empty() {
		return nil, nil
	}
	caller := filepath.Base(callerDir)
	write := func(names []string) error {
		for _, n := range names {
			a, err := FindAgent(poolRoot, n)
			if err != nil {
				return err
			}
			body, err := RenderPeerAgent(caller, a)
			if err != nil {
				return err
			}
			out := PeerAgentFile(callerDir, n)
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	if err := write(plan.Write); err != nil {
		return nil, err
	}
	if err := write(plan.Update); err != nil {
		return nil, err
	}
	for _, n := range plan.Remove {
		if err := os.Remove(PeerAgentFile(callerDir, n)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	var notes []string
	if len(plan.Write) > 0 {
		notes = append(notes, fmt.Sprintf("wrote %d in-session peer subagent(s) (.claude/agents/%s<peer>.md): %s",
			len(plan.Write), PeerAgentPrefix, strings.Join(plan.Write, ", ")))
	}
	if len(plan.Update) > 0 {
		notes = append(notes, fmt.Sprintf("refreshed %d in-session peer subagent(s): %s", len(plan.Update), strings.Join(plan.Update, ", ")))
	}
	if len(plan.Remove) > 0 {
		notes = append(notes, "removed in-session peer subagent(s) no longer dispatchable: "+strings.Join(plan.Remove, ", "))
	}
	return notes, nil
}

// --- pool-root access for the caller's session ---

// localSettingsFile is Claude Code's machine-local, uncommitted settings file.
// The pool root goes here as an additional working directory so an in-session
// peer can read its own workspace (`../<peer>/knowledge`) without a permission
// prompt — and so the committed settings.json stays free of absolute paths.
const localSettingsFile = "settings.local.json"

// PoolAccessWired reports whether the caller's settings.local.json already
// lists the pool root under permissions.additionalDirectories.
func PoolAccessWired(poolRoot, callerDir string) bool {
	root := readJSONObject(filepath.Join(callerDir, ".claude", localSettingsFile))
	perms, _ := root["permissions"].(map[string]any)
	dirs, _ := perms["additionalDirectories"].([]any)
	for _, d := range dirs {
		if s, ok := d.(string); ok && samePath(s, poolRoot) {
			return true
		}
	}
	return false
}

// ensurePoolAccess adds the pool root to settings.local.json
// permissions.additionalDirectories (add-only, idempotent) and makes sure the
// file is git-ignored. Returns a note, or "" when nothing changed.
func ensurePoolAccess(poolRoot, callerDir string) (string, error) {
	if PoolAccessWired(poolRoot, callerDir) {
		return "", nil
	}
	path := filepath.Join(callerDir, ".claude", localSettingsFile)
	root := readJSONObject(path)
	if root == nil {
		if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			return ".claude/" + localSettingsFile + " is not valid JSON — left unchanged (add the pool root to permissions.additionalDirectories by hand)", nil
		}
		root = map[string]any{}
	}
	perms, _ := root["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
		root["permissions"] = perms
	}
	dirs, _ := perms["additionalDirectories"].([]any)
	abs, err := filepath.Abs(poolRoot)
	if err != nil {
		abs = poolRoot
	}
	perms["additionalDirectories"] = append(dirs, abs)
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	_ = ensureGitignoreLine(callerDir, ".claude/"+localSettingsFile)
	return "added the pool root to .claude/" + localSettingsFile + " additionalDirectories (in-session peers read their own workspaces; takes effect on the next session)", nil
}

// readJSONObject parses a JSON object file; nil when absent or invalid.
func readJSONObject(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var root map[string]any
	if json.Unmarshal(b, &root) != nil {
		return nil
	}
	return root
}

func samePath(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return filepath.Clean(aa) == filepath.Clean(bb)
}

// ensureGitignoreLine appends one ignore line when the file does not already
// carry it. Add-only.
func ensureGitignoreLine(dir, line string) error {
	gi := filepath.Join(dir, ".gitignore")
	b, _ := os.ReadFile(gi)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	f, err := os.OpenFile(gi, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	prefix := ""
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		prefix = "\n"
	}
	_, err = f.WriteString(prefix + line + "\n")
	return err
}
