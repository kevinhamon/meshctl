package mesh

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// peers.go — in-session dispatch: every dispatchable peer is rendered
// as a subagent definition in each caller whose harness can run one — Claude
// Code's `.claude/agents/mesh-<peer>.md`, opencode V2's
// `.opencode/agents/mesh-<peer>.md` — computed from the pool's
// manifests exactly like the roster. A caller then consults an advisory peer with the Agent tool
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

// peerFormat is how one harness reads in-session peer definitions and how a
// caller is told to delegate to one. Whether the harness can run peers at all
// is its InSessionPeers cap; this is only the format, keyed by harness id.
type peerFormat struct {
	dir    string // workspace-relative definitions dir
	render func(caller string, peer *Agent, work bool) (string, error)
	// callLabel names the delegation tool; callField opens the backticked
	// field that takes the definition's name.
	callLabel, callField string
	// sharedSession: the peer subagent runs inside the caller's harness
	// session, so a claim under that session id is one the peer can release.
	sharedSession bool
	// delegate prints the delegation call `ask --in-session` hands the caller.
	delegate func(w io.Writer, subagent, peer, id string)
}

var peerFormats = map[string]peerFormat{
	HarnessClaude: {
		dir: filepath.Join(".claude", "agents"), render: renderPeer,
		callLabel: "Agent-tool", callField: "`subagent_type", sharedSession: true,
		delegate: func(w io.Writer, subagent, _, _ string) {
			fmt.Fprintln(w, "Now delegate with the Agent tool:")
			fmt.Fprintf(w, "  subagent_type: %s\n", subagent)
		},
	},
	HarnessOpencode: {
		dir: filepath.Join(".opencode", "agents"), render: renderOpencodePeer,
		callLabel: "subagent-tool", callField: "`agent",
		delegate: func(w io.Writer, subagent, peer, id string) {
			fmt.Fprintln(w, "Now delegate with the subagent tool:")
			fmt.Fprintf(w, "  agent: %s\n", subagent)
			fmt.Fprintf(w, "  description: %s %s\n", peer, id)
		},
	},
}

// peerFormatFor is harness's peer format, falling back to DefaultHarness's
// for a harness with none (callers check peersSupportedBy first).
func peerFormatFor(harness string) peerFormat {
	if f, ok := peerFormats[normHarnessID(harness)]; ok {
		return f
	}
	return peerFormats[DefaultHarness]
}

// peersSupportedBy reports whether h can run in-session peers: the cap, and a
// definition format meshctl can render for it.
func peersSupportedBy(h Harness) bool {
	_, ok := peerFormats[h.Name()]
	return ok && h.Caps().InSessionPeers
}

// PeerAgentFile is the path of a peer's subagent definition inside a caller,
// in the caller's preferred harness's format.
func PeerAgentFile(callerDir, peer string) string {
	return PeerAgentFileFor(callerHarness(callerDir), callerDir, peer)
}

// PeerAgentFileFor is PeerAgentFile in harness's format.
func PeerAgentFileFor(harness, callerDir, peer string) string {
	return filepath.Join(peerDefsDirFor(harness, callerDir), PeerAgentPrefix+peer+".md")
}

// peerDefsDirFor is where harness reads subagent definitions in callerDir.
func peerDefsDirFor(harness, callerDir string) string {
	return filepath.Join(callerDir, peerFormatFor(harness).dir)
}

// callerHarness is the preferred harness of the manifest in callerDir, else
// the one the workspace's files show (wiring can run before the manifest
// exists). Wire-time only: what runs a session now is RuntimeHarness.
func callerHarness(callerDir string) string {
	a, err := LoadAgent(filepath.Join(callerDir, ManifestName))
	if err != nil {
		return DetectHarness(callerDir)
	}
	return a.HarnessName()
}

// peerHarnesses are the harnesses whose peer definitions a caller workspace
// holds: every wired harness with a peer format, and the preferred one. A
// harness without the InSessionPeers cap is listed too, so its stale
// managed definitions are removed.
func peerHarnesses(callerDir string) []Harness {
	ids := WiredHarnesses(callerDir)
	pref := callerHarness(callerDir)
	if !hasString(ids, pref) {
		ids = append(ids, pref)
	}
	var out []Harness
	for _, id := range ids {
		if _, ok := peerFormats[normHarnessID(id)]; ok {
			out = append(out, HarnessByName(id))
		}
	}
	return out
}

// PeersSupported reports whether a caller's preferred harness can run
// in-session peers. Runtime checks ask the running harness instead
// (peersSupportedBy(RuntimeHarness(…))).
func PeersSupported(callerDir string) bool {
	return peersSupportedBy(HarnessByName(callerHarness(callerDir)))
}

// PeerAgentName is the `subagent_type` a caller passes to the Agent tool.
func PeerAgentName(peer string) string { return PeerAgentPrefix + peer }

// PeerWorkSuffix marks the write-capable definition a hub session delegates
// owned work to: `mesh-<peer>-work`.
const PeerWorkSuffix = "-work"

// PeerWorkAgentFile is the path of a peer's delegated-work definition, in
// the caller's preferred harness's format.
func PeerWorkAgentFile(callerDir, peer string) string {
	return PeerWorkAgentFileFor(callerHarness(callerDir), callerDir, peer)
}

// PeerWorkAgentFileFor is PeerWorkAgentFile in harness's format.
func PeerWorkAgentFileFor(harness, callerDir, peer string) string {
	return filepath.Join(peerDefsDirFor(harness, callerDir), PeerAgentPrefix+peer+PeerWorkSuffix+".md")
}

// PeerWorkAgentName is the work-mode `subagent_type`.
func PeerWorkAgentName(peer string) string { return PeerAgentPrefix + peer + PeerWorkSuffix }

// peerWorkHookFlag is how a rendered definition tells the hook it is the
// work-mode one; also how an installed file's mode is read back without
// guessing from a name (an agent may itself be called "<x>-work").
const peerWorkHookFlag = "--in-session --work"

// rePeerHookFlags reads the peer and mode back out of a rendered definition:
// the hook commands of a Claude Code one, the flags comment of an opencode one.
var rePeerHookFlags = regexp.MustCompile(`--agent (\S+) --in-session( --work)?`)

// RenderPeerAgent renders the read-only consult definition for `peer` as
// installed into `caller`'s workspace.
func RenderPeerAgent(caller string, peer *Agent) (string, error) {
	return renderPeer(caller, peer, false)
}

// RenderPeerWorkAgent renders the write-capable delegated-work definition.
// The peer edits and commits in its own workspace and declared
// workdirs; the in-session work gate confines the writes, not this list.
func RenderPeerWorkAgent(caller string, peer *Agent) (string, error) {
	return renderPeer(caller, peer, true)
}

// peerBody renders the definition's prompt body, shared by every harness.
func peerBody(caller string, peer *Agent, work bool) (string, error) {
	tplPath := "templates/claude/mesh-peer.md"
	if work {
		tplPath = "templates/claude/mesh-peer-work.md"
	}
	tpl, err := templatesFS.ReadFile(tplPath)
	if err != nil {
		return "", err
	}
	workdirs := "none declared"
	if len(peer.Workdirs) > 0 {
		workdirs = "`" + strings.Join(peer.Workdirs, "`, `") + "`"
	}
	return strings.NewReplacer("__PEER__", peer.Name, "__CALLER__", caller,
		"__TITLE__", firstNonEmpty(peer.Title, peer.Name), "__WORKDIRS__", workdirs).Replace(string(tpl)), nil
}

func renderPeer(caller string, peer *Agent, work bool) (string, error) {
	name, flags := PeerAgentName(peer.Name), "--in-session"
	// No Agent tool in either mode: a peer cannot spawn subagents (depth
	// guard). Bash is scoped by the in-session gate, not by this list (the
	// `tools` field takes tool names only).
	// Consult: no Write/Edit — it answers through `meshctl inbox respond`.
	tools := "Read, Grep, Glob, Bash"
	if work {
		name, flags = PeerWorkAgentName(peer.Name), peerWorkHookFlag
		tools = "Read, Grep, Glob, Bash, Edit, Write, MultiEdit"
	}
	body, err := peerBody(caller, peer, work)
	if err != nil {
		return "", err
	}

	model := peer.ModelFor(HarnessClaude)
	if model == "" || model == "default" {
		model = "inherit"
	}
	hook := func(sub string) map[string]any {
		return map[string]any{"type": "command", "timeout": 10,
			"command": fmt.Sprintf("meshctl hook %s --harness %s --agent %s %s", sub, HarnessClaude, peer.Name, flags)}
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

// opencodePeerFrontmatter is an opencode V2 agent definition
// (.opencode/agents/<name>.md); the file name is the agent id.
type opencodePeerFrontmatter struct {
	Description string             `yaml:"description"`
	Mode        string             `yaml:"mode"`
	Model       string             `yaml:"model,omitempty"`
	Permissions []opencodePermRule `yaml:"permissions"`
}

type opencodePermRule struct {
	Action   string `yaml:"action"`
	Resource string `yaml:"resource"`
	Effect   string `yaml:"effect"`
}

// renderOpencodePeer renders the opencode V2 form. V2 has no
// per-agent hooks: the caller's mesh plugin sees the child session's agent id
// on every tool call and the hook maps it back to this file's flags comment
// (ResolvePeerSubagent), so the peer gate and the capture fire as on Claude
// Code. The permissions are a second line behind the gate: no nested
// subagents, and no edit tools in a consult. The model is the peer's opencode
// model (ModelFor) only in provider/model form: without one a subagent
// inherits the caller's, and a bare alias would not resolve here.
func renderOpencodePeer(caller string, peer *Agent, work bool) (string, error) {
	flags := "--in-session"
	perms := []opencodePermRule{{"subagent", "*", "deny"}}
	if work {
		flags = peerWorkHookFlag
	} else {
		perms = append(perms, opencodePermRule{"edit", "*", "deny"})
	}
	body, err := peerBody(caller, peer, work)
	if err != nil {
		return "", err
	}
	body = strings.ReplaceAll(body, "Agent calls", "subagent calls")
	model := peer.ModelFor(HarnessOpencode)
	if !strings.Contains(model, "/") {
		model = ""
	}
	head, err := yaml.Marshal(opencodePeerFrontmatter{Description: peerDescription(peer, work), Mode: "subagent", Model: model, Permissions: perms})
	if err != nil {
		return "", err
	}
	return "---\n" + string(head) + "---\n\n<!-- meshctl peer hook flags: --agent " + peer.Name + " " + flags + " -->\n" + body, nil
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
// The mode comes from the rendered hook flag, not the file name. harness
// picks the format's definitions dir.
func managedPeerDefs(harness, callerDir string) map[string]bool {
	dir := peerDefsDirFor(harness, callerDir)
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
// caller workspace, in its preferred harness's format, sorted, with the modes
// each one is installed for.
func InstalledPeers(callerDir string) []InstalledPeer {
	return InstalledPeersFor(callerHarness(callerDir), callerDir)
}

// InstalledPeersFor is InstalledPeers in harness's format.
func InstalledPeersFor(harness, callerDir string) []InstalledPeer {
	by := map[string]*InstalledPeer{}
	for stem, work := range managedPeerDefs(harness, callerDir) {
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

// PeerLine is the session-start sentence that tells an agent which peers it
// can consult or delegate to in-session and how, for the harness running its
// session now (RuntimeHarness with no flag). Injected so existing agents learn
// the verb without a CLAUDE.md edit. "" when that harness cannot run peers or
// the agent has no peer definitions for it.
func PeerLine(a *Agent) string { return PeerLineFor(a, RuntimeHarness(a, "")) }

// PeerLineFor is PeerLine for the session running under h.
func PeerLineFor(a *Agent, h Harness) string {
	if !peersSupportedBy(h) {
		return ""
	}
	f := peerFormatFor(h.Name())
	peers := InstalledPeersFor(h.Name(), a.Dir())
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
		"its KB learns from it): add `--work`. Then make the " + f.callLabel + " call it prints (" + f.callField + ": " + PeerAgentPrefix +
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
// ones; a state-mutating peer gets the work definition only. None when h
// cannot run in-session peers (no InSessionPeers cap).
func wantedPeerDefs(agents []*Agent, h Harness, callerDir string) map[string]peerDef {
	want := map[string]peerDef{}
	if !peersSupportedBy(h) {
		return want
	}
	caller := filepath.Base(callerDir)
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

// render renders the definition in harness's format.
func (d peerDef) render(harness, callerDir string) (string, error) {
	return peerFormatFor(harness).render(filepath.Base(callerDir), d.peer, d.work)
}

func peerDefFile(harness, callerDir, stem string) string {
	return filepath.Join(peerDefsDirFor(harness, callerDir), PeerAgentPrefix+stem+".md")
}

// PlanPeerAgents compares the peers of the pool with the managed definitions
// in callerDir, for every harness whose definitions the workspace holds
// (peerHarnesses); a stem pending in any format is listed once. Entries are
// definition stems (`architect`, `architect-work`). The caller itself is
// never its own peer. Fail-soft: an unreadable pool yields an empty plan.
func PlanPeerAgents(poolRoot, callerDir string) PeerPlan {
	var plan PeerPlan
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return plan
	}
	for _, h := range peerHarnesses(callerDir) {
		p := planPeerAgentsFor(agents, h, callerDir)
		plan.Write = appendNew(plan.Write, p.Write...)
		plan.Update = appendNew(plan.Update, p.Update...)
		plan.Remove = appendNew(plan.Remove, p.Remove...)
		plan.Hand = appendNew(plan.Hand, p.Hand...)
	}
	sort.Strings(plan.Write)
	sort.Strings(plan.Update)
	sort.Strings(plan.Remove)
	sort.Strings(plan.Hand)
	return plan
}

// appendNew appends the xs not already in out.
func appendNew(out []string, xs ...string) []string {
	for _, x := range xs {
		if !hasString(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// planPeerAgentsFor is PlanPeerAgents for one harness's format.
func planPeerAgentsFor(agents []*Agent, h Harness, callerDir string) PeerPlan {
	var plan PeerPlan
	id := h.Name()
	want := wantedPeerDefs(agents, h, callerDir)
	for stem, d := range want {
		cur, err := os.ReadFile(peerDefFile(id, callerDir, stem))
		if err != nil {
			plan.Write = append(plan.Write, stem)
			continue
		}
		if !strings.Contains(string(cur), distillerAgentMarker) {
			plan.Hand = append(plan.Hand, stem)
			continue
		}
		body, err := d.render(id, callerDir)
		if err != nil || string(cur) != body {
			plan.Update = append(plan.Update, stem)
		}
	}
	// Managed definitions for peers that are gone or no longer eligible.
	for stem := range managedPeerDefs(id, callerDir) {
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

// ensurePeerAgents applies the peer plan to a caller workspace, in the format
// of every harness whose definitions it holds (peerHarnesses), so whichever
// harness's wiring calls it, each wired harness gets its own definitions.
// Returns onboard-style notes (empty ⇒ nothing changed).
func ensurePeerAgents(poolRoot, callerDir string) ([]string, error) {
	if PlanPeerAgents(poolRoot, callerDir).Empty() {
		return nil, nil
	}
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, h := range peerHarnesses(callerDir) {
		n, err := ensurePeerAgentsFor(agents, h, callerDir)
		notes = append(notes, n...)
		if err != nil {
			return notes, err
		}
	}
	return notes, nil
}

// ensurePeerAgentsFor applies one harness's peer plan.
func ensurePeerAgentsFor(agents []*Agent, h Harness, callerDir string) ([]string, error) {
	id := h.Name()
	plan := planPeerAgentsFor(agents, h, callerDir)
	if plan.Empty() {
		return nil, nil
	}
	want := wantedPeerDefs(agents, h, callerDir)
	write := func(stems []string) error {
		for _, stem := range stems {
			body, err := want[stem].render(id, callerDir)
			if err != nil {
				return err
			}
			out := peerDefFile(id, callerDir, stem)
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
		if err := os.Remove(peerDefFile(id, callerDir, stem)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	var notes []string
	if len(plan.Write) > 0 {
		rel, _ := filepath.Rel(callerDir, peerDefsDirFor(id, callerDir))
		notes = append(notes, fmt.Sprintf("wrote %d in-session peer subagent(s) (%s/%s<peer>[%s].md): %s",
			len(plan.Write), filepath.ToSlash(rel), PeerAgentPrefix, PeerWorkSuffix, strings.Join(plan.Write, ", ")))
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
// the repos a delegated peer may write from the caller's session.
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

// RefreshPeerRoster brings every other agent's in-session peer subagents in
// line with the roster after an agent joins or changes (`agent new|onboard`),
// so no workspace is left stale until the next `pool upgrade`. Each agent repo
// gets one commit holding exactly the files written; nothing is pushed.
// Returns one line per agent touched.
func RefreshPeerRoster(poolRoot, except string) ([]string, error) {
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range agents {
		if a.Dir() == except || PlanPeerAgents(poolRoot, a.Dir()).Empty() {
			continue
		}
		before := dirtySnapshot(a.Dir())
		notes, err := ensurePeerAgents(poolRoot, a.Dir())
		if err != nil {
			return out, fmt.Errorf("%s: %w", a.Name, err)
		}
		line := a.Name + ": " + strings.Join(notes, "; ")
		committed, warnings := commitChangedPaths(a.Dir(), before, "meshctl: refresh in-session peers")
		if committed != "" {
			line += " (" + committed + ")"
		}
		for _, w := range warnings {
			line += " — " + w
		}
		out = append(out, line)
	}
	return out, nil
}
