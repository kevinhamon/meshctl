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

// PeerWorkSuffix marks the write-capable definition a hub session delegates
// owned work to (ADR-0063): `mesh-<peer>-work`.
const PeerWorkSuffix = "-work"

// PeerWorkAgentFile is the path of a peer's delegated-work definition.
func PeerWorkAgentFile(callerDir, peer string) string {
	return filepath.Join(callerDir, ".claude", "agents", PeerAgentPrefix+peer+PeerWorkSuffix+".md")
}

// PeerWorkAgentName is the work-mode `subagent_type`.
func PeerWorkAgentName(peer string) string { return PeerAgentPrefix + peer + PeerWorkSuffix }

// peerWorkHookFlag is how a rendered definition tells the hook it is the
// work-mode one; also how an installed file's mode is read back without
// guessing from a name (an agent may itself be called "<x>-work").
const peerWorkHookFlag = "--in-session --work"

// RenderPeerAgent renders the read-only consult definition for `peer` as
// installed into `caller`'s workspace.
func RenderPeerAgent(caller string, peer *Agent) (string, error) {
	return renderPeer(caller, peer, false)
}

// RenderPeerWorkAgent renders the write-capable delegated-work definition
// (ADR-0063). The peer edits and commits in its own workspace and declared
// workdirs; the in-session work gate confines the writes, not this list.
func RenderPeerWorkAgent(caller string, peer *Agent) (string, error) {
	return renderPeer(caller, peer, true)
}

func renderPeer(caller string, peer *Agent, work bool) (string, error) {
	name, tplPath, flags := PeerAgentName(peer.Name), "templates/claude/mesh-peer.md", "--in-session"
	// No Agent tool in either mode: a peer cannot spawn subagents (depth
	// guard). Bash is scoped by the in-session gate, not by this list (the
	// `tools` field takes tool names only).
	// Consult: no Write/Edit — it answers through `meshctl inbox respond`.
	tools := "Read, Grep, Glob, Bash"
	if work {
		name, tplPath, flags = PeerWorkAgentName(peer.Name), "templates/claude/mesh-peer-work.md", peerWorkHookFlag
		tools = "Read, Grep, Glob, Bash, Edit, Write, MultiEdit"
	}
	tpl, err := templatesFS.ReadFile(tplPath)
	if err != nil {
		return "", err
	}
	workdirs := "none declared"
	if len(peer.Workdirs) > 0 {
		workdirs = "`" + strings.Join(peer.Workdirs, "`, `") + "`"
	}
	body := strings.NewReplacer("__PEER__", peer.Name, "__CALLER__", caller,
		"__TITLE__", firstNonEmpty(peer.Title, peer.Name), "__WORKDIRS__", workdirs).Replace(string(tpl))

	model := strings.TrimSpace(peer.Model)
	if model == "" || model == "default" {
		model = "inherit"
	}
	hook := func(sub string) map[string]any {
		return map[string]any{"type": "command", "timeout": 10,
			"command": fmt.Sprintf("meshctl hook %s --agent %s %s", sub, peer.Name, flags)}
	}
	fm := peerFrontmatter{
		Name:        name,
		Description: peerDescription(peer, work),
		Tools:       tools,
		Model:       model,
		OmitClaude:  true,
		// The same gate matcher as settings.json (GateHookMatcher): file edits,
		// shell, every MCP tool and delegations pass through the peer's own
		// gate (its KB rules + the in-session deny set), and PostToolUse on the
		// same matcher is the peer-flagged seam for pool commits and outbound
		// acks.
		Hooks: map[string]any{
			"PreToolUse":  []any{map[string]any{"matcher": GateHookMatcher, "hooks": []any{hook("pre-tool-use")}}},
			"PostToolUse": []any{map[string]any{"matcher": GateHookMatcher, "hooks": []any{hook("post-tool-use")}}},
			"Stop":        []any{map[string]any{"hooks": []any{hook("session-end")}}},
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
func peerDescription(a *Agent, work bool) string {
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
	if work {
		s += ". Mesh peer `" + a.Name + "`, doing delegated work in-session (edits and commits in its own repos) for: " +
			strings.Join(types, "; ") + ". Use ONLY after `meshctl ask " + a.Name +
			" <type> \"...\" --in-session --work` printed a request id, and pass that id in the prompt."
		if len(a.Mutates) > 0 {
			s += " State-mutating (" + strings.Join(a.Mutates, ", ") + "): each delegation asks the human to approve."
		}
		return s
	}
	s += ". Mesh peer `" + a.Name + "`, consulted in-session for: " + strings.Join(types, "; ") +
		". Use ONLY after `meshctl ask " + a.Name + " <type> \"...\" --in-session` printed a request id, and pass that id in the prompt."
	return s
}

// InstalledPeer is one peer with managed in-session definitions in a caller.
type InstalledPeer struct {
	Name          string
	Consult, Work bool
}

// managedPeerDefs reads the meshctl-managed mesh-*.md files in a caller
// workspace: definition stem (file name minus prefix and .md) → work mode.
// The mode comes from the rendered hook flag, not the file name.
func managedPeerDefs(callerDir string) map[string]bool {
	dir := filepath.Join(callerDir, ".claude", "agents")
	entries, _ := os.ReadDir(dir)
	out := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, PeerAgentPrefix) || !strings.HasSuffix(n, ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil || !strings.Contains(string(b), distillerAgentMarker) {
			continue
		}
		out[strings.TrimSuffix(strings.TrimPrefix(n, PeerAgentPrefix), ".md")] = strings.Contains(string(b), peerWorkHookFlag)
	}
	return out
}

// InstalledPeers lists the peers with a managed in-session definition in a
// caller workspace, sorted, with the modes each one is installed for.
func InstalledPeers(callerDir string) []InstalledPeer {
	by := map[string]*InstalledPeer{}
	for stem, work := range managedPeerDefs(callerDir) {
		name := stem
		if work {
			name = strings.TrimSuffix(stem, PeerWorkSuffix)
		}
		p := by[name]
		if p == nil {
			p = &InstalledPeer{Name: name}
			by[name] = p
		}
		if work {
			p.Work = true
		} else {
			p.Consult = true
		}
	}
	out := make([]InstalledPeer, 0, len(by))
	for _, p := range by {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PeerLine is the session-start sentence that tells a Claude Code agent which
// peers it can consult or delegate to in-session and how. Injected so existing
// agents learn the verb without a CLAUDE.md edit. "" when the agent has no
// peer definitions.
func PeerLine(a *Agent) string {
	if a.HarnessName() != HarnessClaude {
		return ""
	}
	peers := InstalledPeers(a.Dir())
	if len(peers) == 0 {
		return ""
	}
	names := make([]string, len(peers))
	for i, p := range peers {
		var modes []string
		if p.Consult {
			modes = append(modes, "consult")
		}
		if p.Work {
			modes = append(modes, "work")
		}
		names[i] = "`" + p.Name + "` (" + strings.Join(modes, "/") + ")"
	}
	return "# In-session peers\nPeers you can use without leaving this session: " + strings.Join(names, ", ") + ". " +
		"Consult: `meshctl ask <peer> <type> \"…\" --in-session`. Work the peer owns (edits and commits in its own repos, " +
		"its KB learns from it): add `--work`. Then make the Agent-tool call it prints (`subagent_type: " + PeerAgentPrefix +
		"<peer>` or `" + PeerAgentPrefix + "<peer>" + PeerWorkSuffix + "`); read the outcome with `meshctl sent`. " +
		"Plain `meshctl ask` (headless, second process) still works; delegating to a state-mutating peer asks the human first."
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
		out = append(out, "remove in-session peer subagents (no longer eligible): "+strings.Join(prefixed(p.Remove), ", "))
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

// wantedPeerDefs is the set of definitions a caller should hold: stem →
// (peer, work mode). Consult for dispatchable peers, work for work-delegable
// ones; a state-mutating peer gets the work definition only.
func wantedPeerDefs(agents []*Agent, caller string) map[string]peerDef {
	want := map[string]peerDef{}
	for _, a := range agents {
		if a.Name == caller {
			continue
		}
		if a.IsDispatchable() {
			want[a.Name] = peerDef{a, false}
		}
		if a.IsWorkDelegable() {
			want[a.Name+PeerWorkSuffix] = peerDef{a, true}
		}
	}
	return want
}

type peerDef struct {
	peer *Agent
	work bool
}

func (d peerDef) render(caller string) (string, error) {
	return renderPeer(caller, d.peer, d.work)
}

func peerDefFile(callerDir, stem string) string {
	return filepath.Join(callerDir, ".claude", "agents", PeerAgentPrefix+stem+".md")
}

// PlanPeerAgents compares the peers of the pool with the managed definitions
// in callerDir. Entries are definition stems (`architect`, `architect-work`).
// The caller itself is never its own peer. Fail-soft: an unreadable pool
// yields an empty plan.
func PlanPeerAgents(poolRoot, callerDir string) PeerPlan {
	var plan PeerPlan
	caller := filepath.Base(callerDir)
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return plan
	}
	want := wantedPeerDefs(agents, caller)
	for stem, d := range want {
		cur, err := os.ReadFile(peerDefFile(callerDir, stem))
		if err != nil {
			plan.Write = append(plan.Write, stem)
			continue
		}
		if !strings.Contains(string(cur), distillerAgentMarker) {
			plan.Hand = append(plan.Hand, stem)
			continue
		}
		body, err := d.render(caller)
		if err != nil || string(cur) != body {
			plan.Update = append(plan.Update, stem)
		}
	}
	// Managed definitions for peers that are gone or no longer eligible.
	for stem := range managedPeerDefs(callerDir) {
		if _, ok := want[stem]; !ok {
			plan.Remove = append(plan.Remove, stem)
		}
	}
	sort.Strings(plan.Write)
	sort.Strings(plan.Update)
	sort.Strings(plan.Remove)
	sort.Strings(plan.Hand)
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
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return nil, err
	}
	want := wantedPeerDefs(agents, caller)
	write := func(stems []string) error {
		for _, stem := range stems {
			body, err := want[stem].render(caller)
			if err != nil {
				return err
			}
			out := peerDefFile(callerDir, stem)
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
	for _, stem := range plan.Remove {
		if err := os.Remove(peerDefFile(callerDir, stem)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	var notes []string
	if len(plan.Write) > 0 {
		notes = append(notes, fmt.Sprintf("wrote %d in-session peer subagent(s) (.claude/agents/%s<peer>[%s].md): %s",
			len(plan.Write), PeerAgentPrefix, PeerWorkSuffix, strings.Join(plan.Write, ", ")))
	}
	if len(plan.Update) > 0 {
		notes = append(notes, fmt.Sprintf("refreshed %d in-session peer subagent(s): %s", len(plan.Update), strings.Join(plan.Update, ", ")))
	}
	if len(plan.Remove) > 0 {
		notes = append(notes, "removed in-session peer subagent(s) no longer eligible: "+strings.Join(plan.Remove, ", "))
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
	return len(MissingAdditionalDirs(callerDir, poolRoot)) == 0
}

// PeerWorkdirs is every declared workdir of the caller's work-delegable peers:
// the repos a delegated peer may write from the caller's session (ADR-0063).
func PeerWorkdirs(poolRoot, callerDir string) []string {
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return nil
	}
	caller := filepath.Base(callerDir)
	var out []string
	for _, a := range agents {
		if a.Name != caller && a.IsWorkDelegable() {
			out = append(out, a.WorkdirPaths()...)
		}
	}
	return out
}

// MissingAdditionalDirs returns the dirs not yet in the caller's
// settings.local.json permissions.additionalDirectories.
func MissingAdditionalDirs(callerDir string, dirs ...string) []string {
	root := readJSONObject(filepath.Join(callerDir, ".claude", localSettingsFile))
	perms, _ := root["permissions"].(map[string]any)
	have, _ := perms["additionalDirectories"].([]any)
	var missing []string
	for _, d := range dirs {
		found := false
		for _, h := range have {
			if s, ok := h.(string); ok && samePath(s, d) {
				found = true
				break
			}
		}
		for _, m := range missing {
			found = found || samePath(m, d)
		}
		if !found {
			missing = append(missing, d)
		}
	}
	return missing
}

// ensureAdditionalDirs adds dirs to settings.local.json
// permissions.additionalDirectories (add-only, idempotent) and makes sure the
// file is git-ignored. The pool root lets an in-session peer read its own
// workspace; a peer's workdirs let it do delegated work in them. Returns a
// note, or "" when nothing changed.
func ensureAdditionalDirs(callerDir string, dirs ...string) (string, error) {
	missing := MissingAdditionalDirs(callerDir, dirs...)
	if len(missing) == 0 {
		return "", nil
	}
	path := filepath.Join(callerDir, ".claude", localSettingsFile)
	root := readJSONObject(path)
	if root == nil {
		if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			return ".claude/" + localSettingsFile + " is not valid JSON — left unchanged (add " +
				strings.Join(missing, ", ") + " to permissions.additionalDirectories by hand)", nil
		}
		root = map[string]any{}
	}
	perms, _ := root["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
		root["permissions"] = perms
	}
	have, _ := perms["additionalDirectories"].([]any)
	for _, d := range missing {
		abs, err := filepath.Abs(d)
		if err != nil {
			abs = d
		}
		have = append(have, abs)
	}
	perms["additionalDirectories"] = have
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
	return "added " + strings.Join(missing, ", ") + " to .claude/" + localSettingsFile +
		" additionalDirectories (in-session peers reach their workspaces and workdirs; takes effect on the next session)", nil
}

// ensurePoolAccess wires the pool root plus every work-delegable peer's
// workdirs into the caller's additional directories.
func ensurePoolAccess(poolRoot, callerDir string) (string, error) {
	return ensureAdditionalDirs(callerDir, append([]string{poolRoot}, PeerWorkdirs(poolRoot, callerDir)...)...)
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
