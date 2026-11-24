package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Accept is one request type an agent's inbox handles.
type Accept struct {
	Type string `yaml:"type"`
	Desc string `yaml:"desc,omitempty"`
}

// Badge is the agent's terminal identity: a label + color (0..1 components).
type Badge struct {
	Label string  `yaml:"label"`
	R     float64 `yaml:"r"`
	G     float64 `yaml:"g"`
	B     float64 `yaml:"b"`
}

// Agent is the parsed agent.yaml — an agent's authoritative self-description.
// It is the single source of truth for identity + discovery.
type Agent struct {
	Name    string   `yaml:"name"`
	Title   string   `yaml:"title"`
	Role    string   `yaml:"role"`
	Inbox   string   `yaml:"inbox"`
	Owns    []string `yaml:"owns"`
	Domains []string `yaml:"domains"`
	Accepts []Accept `yaml:"accepts"`
	// Dispatchable is a pointer so we can tell "unset" from "false". With
	// mutates it takes effect only under outbound grants in the pool config:
	// read it through IsDispatchable / DispatchRefusal.
	Dispatchable *bool    `yaml:"dispatchable"`
	Mutates      []string `yaml:"mutates"`
	Badge        Badge    `yaml:"badge"`
	Model        string   `yaml:"model,omitempty"`
	// Models overrides Model per harness id (`models: { opencode: provider/x }`):
	// a model name one harness resolves may mean nothing to another. Read the
	// effective model through ModelFor.
	Models map[string]string `yaml:"models,omitempty"`
	Repo   string            `yaml:"repo,omitempty"`
	// Workdirs are local repo paths (~ allowed) this agent may write when it
	// does delegated work in a hub session, besides its workspace.
	Workdirs []string `yaml:"workdirs,omitempty"`
	// Harness is the agent's preferred harness (a registered harness id): the
	// one a headless run uses when the caller's own harness is not wired here,
	// and the fallback when nothing says which harness runs the session. Which
	// harness runs a session is decided at runtime (RuntimeHarness).
	// Empty ⇒ DefaultHarness.
	Harness string `yaml:"harness,omitempty"`
	// Tools are agent-specific tool references, merged over the pool's.
	Tools []ToolRef `yaml:"tools,omitempty"`
	// Knowledge is this peer's knowledge kind: canonical |
	// perspective. Empty ⇒ the pool config's default; read it through
	// EffectiveKnowledge, never directly.
	Knowledge string `yaml:"knowledge,omitempty"`
	// Docs are workspace-relative folders holding documents written for the
	// person; empty ⇒ [docs]. Read them through DocFolders.
	Docs []string `yaml:"docs,omitempty"`

	// dir is the on-disk workspace directory this manifest was loaded from.
	dir string `yaml:"-"`
}

// HarnessName returns the agent's preferred harness, defaulting to
// DefaultHarness. It is a preference, not what runs the session now: ask
// RuntimeHarness for that.
func (a *Agent) HarnessName() string {
	if a.Harness == "" {
		return DefaultHarness
	}
	return a.Harness
}

// ModelFor is the model the agent runs under harness: its `models:` entry for
// that harness, else `model:`. "" when neither is set.
func (a *Agent) ModelFor(harness string) string {
	if a == nil {
		return ""
	}
	if m := strings.TrimSpace(a.Models[normHarnessID(harness)]); m != "" {
		return m
	}
	return strings.TrimSpace(a.Model)
}

// RuntimeHarness is the harness running a's session now, in order: the
// explicit flag (bridges always pass --harness), the running-harness env
// marker, a's preferred harness, DefaultHarness. An id that is not registered
// falls through to the next source. a may be nil (no agent resolved).
func RuntimeHarness(a *Agent, flag string) Harness {
	return HarnessByName(runtimeHarnessID(a, flag, RunningHarness()))
}

// runtimeHarnessID is RuntimeHarness's choice of id, with the env marker
// passed in so the order is testable.
func runtimeHarnessID(a *Agent, flag, running string) string {
	cands := []string{flag, running}
	if a != nil {
		cands = append(cands, a.HarnessName())
	}
	for _, id := range cands {
		if s, ok := LookupHarness(id); ok {
			return s.ID
		}
	}
	return DefaultHarness
}

// harnessLabel is h's display label: its variant's label when it has one.
func harnessLabel(h Harness) string {
	s, ok := LookupHarness(h.Name())
	if !ok {
		return h.Name()
	}
	for _, v := range s.Variants {
		if v.Num == h.Variant() {
			return v.Label
		}
	}
	return s.Label
}

// harnessLabelsWith lists the registered harness variants whose caps satisfy
// ok, by label ("Claude Code", "opencode V2 (2.x)"), for messages that tell
// the human which harness can do what a refused call needed.
func harnessLabelsWith(ok func(HarnessCaps) bool) string {
	var out []string
	for _, c := range HarnessColumns() {
		s, found := LookupHarness(c.ID)
		if !found {
			continue
		}
		if ok(s.New(c.Variant).Caps()) {
			out = append(out, c.Label)
		}
	}
	if len(out) == 0 {
		return "none registered"
	}
	return strings.Join(out, ", ")
}

// Dir returns the workspace directory the manifest was loaded from.
func (a *Agent) Dir() string { return a.dir }

// DeclaresDispatchable reports the manifest's `dispatchable` flag as written.
func (a *Agent) DeclaresDispatchable() bool {
	return a.Dispatchable != nil && *a.Dispatchable
}

// IsDispatchable reports whether this agent may be run headlessly on the
// manifest alone, needing no grant: declared dispatchable and mutating
// nothing. A peer with mutates is dispatched headless only when the pool
// config grants every surface; that check needs the pool, so
// the dispatch path uses DispatchRefusal instead.
func (a *Agent) IsDispatchable() bool {
	if len(a.Mutates) > 0 {
		return false
	}
	return a.DeclaresDispatchable()
}

// IsWorkDelegable reports whether a hub session may delegate write-capable
// work to this agent in-session: any dispatchable agent, and any
// state-mutating one (those only with the human's per-delegation approval).
// An agent that is neither stays send-only.
func (a *Agent) IsWorkDelegable() bool {
	return len(a.Mutates) > 0 || a.IsDispatchable()
}

// WorkdirPaths returns Workdirs with ~ expanded and made absolute; entries
// that cannot be resolved are dropped.
func (a *Agent) WorkdirPaths() []string {
	var out []string
	for _, d := range a.Workdirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if d == "~" || strings.HasPrefix(d, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				continue
			}
			d = filepath.Join(home, strings.TrimPrefix(d, "~"))
		}
		if abs, err := filepath.Abs(d); err == nil {
			out = append(out, filepath.Clean(abs))
		}
	}
	return out
}

// Accepts reports whether this inbox handles the given request type.
func (a *Agent) AcceptsType(t string) bool {
	for _, x := range a.Accepts {
		if x.Type == t {
			return true
		}
	}
	return false
}

// InboxPath returns the absolute inbox directory.
func (a *Agent) InboxPath() string {
	inbox := a.Inbox
	if inbox == "" {
		inbox = IntakeDir
	}
	return filepath.Join(a.dir, inbox)
}

// LoadAgent reads and parses one agent.yaml at the given manifest path.
func LoadAgent(manifestPath string) (*Agent, error) {
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var a Agent
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(false)
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("%s: %w", manifestPath, err)
	}
	a.dir = filepath.Dir(manifestPath)
	return &a, nil
}

// ScanAgents finds every agent.yaml under agentsDir/*/ and loads it. Parse
// failures are returned as loadErrs keyed by directory name so doctor can report
// them without aborting the whole scan.
func ScanAgents(agentsDir string) (agents []*Agent, loadErrs map[string]error, err error) {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil, nil, err
	}
	loadErrs = map[string]error{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		mp := filepath.Join(agentsDir, e.Name(), ManifestName)
		if !fileExists(mp) {
			continue
		}
		a, lerr := LoadAgent(mp)
		if lerr != nil {
			loadErrs[e.Name()] = lerr
			continue
		}
		agents = append(agents, a)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	return agents, loadErrs, nil
}

// AgentFromWorktreePath recovers the agent name when cwd is a KB worktree
// (<agentsDir>/.worktrees/<agent>-<session>) — whose basename is NOT an agent, so
// a bare cwd-basename lookup (CallerName) fails with "has no manifest". Returns ""
// when cwd is not under .worktrees or no known agent prefixes the worktree name.
// Matches against the real agent list with longest-prefix wins, so agent names
// containing "-" resolve correctly against the "<agent>-<session>" worktree name.
func AgentFromWorktreePath(agentsDir, cwd string) string {
	rel, err := filepath.Rel(WorktreesPath(agentsDir), cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "" // cwd is not inside .worktrees
	}
	base := strings.Split(rel, string(filepath.Separator))[0] // <agent>-<session>
	agents, _, err := ScanAgents(agentsDir)
	if err != nil {
		return ""
	}
	return agentForWorktreeBase(agents, base)
}

// agentForWorktreeBase names the peer a worktree directory `<agent>-<session>`
// belongs to, the longest matching name winning (a peer "pm" must not claim
// "pm-tools-abc"). Callers that walk many worktrees scan the roster once and
// use this directly.
func agentForWorktreeBase(agents []*Agent, base string) string {
	best := ""
	for _, a := range agents {
		if base == a.Name || strings.HasPrefix(base, a.Name+"-") {
			if len(a.Name) > len(best) {
				best = a.Name
			}
		}
	}
	return best
}

// AgentFromWorktreeCwd is AgentFromWorktreePath over the process cwd.
func AgentFromWorktreeCwd(agentsDir string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return AgentFromWorktreePath(agentsDir, cwd)
}

// reValidName is the shape of every identifier that becomes a path segment or
// a frontmatter scalar: agent names, request `from`, related refs. One segment,
// no separators, no whitespace or control characters — so a value can never
// escape its directory (`../x`) or forge a frontmatter line (`x\nstatus: done`).
var reValidName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName rejects identifiers that are not a single safe path segment.
func ValidName(kind, s string) error {
	if !reValidName.MatchString(s) || strings.Contains(s, "..") {
		return fmt.Errorf("invalid %s %q: use letters, digits, '.', '_' or '-' only (max 64 chars, no '..', no '/')", kind, s)
	}
	return nil
}

// ValidAgentName is ValidName for a peer name, which additionally may not be
// the reserved owner `pool`: `mesh://pool/...` must always mean the
// pool itself, and on a case-folding filesystem `Pool/` would collide with the
// pool directory, so the comparison ignores case.
func ValidAgentName(name string) error {
	if err := ValidName("agent name", name); err != nil {
		return err
	}
	if strings.EqualFold(name, ReservedOwner) {
		return fmt.Errorf("agent name %q is reserved: %q is the pool itself (mesh://%s/...) — pick another name", name, ReservedOwner, ReservedOwner)
	}
	return nil
}

// FindAgent loads a single agent by name; error if its manifest is missing.
func FindAgent(agentsDir, name string) (*Agent, error) {
	if err := ValidName("agent name", name); err != nil {
		return nil, err
	}
	mp := ManifestPath(agentsDir, name)
	if !fileExists(mp) {
		if alt := name + agentSuffix; fileExists(ManifestPath(agentsDir, alt)) {
			return nil, fmt.Errorf("agent %q has no manifest (%s not found) — did you mean %q? `meshctl agent list` shows the roster", name, mp, alt)
		}
		return nil, fmt.Errorf("agent %q has no manifest (%s not found) — `meshctl agent list` shows the roster", name, mp)
	}
	return LoadAgent(mp)
}

// Validate returns the list of problems with this manifest (empty == valid).
// Callers may pass a set of all badge labels seen so far to detect collisions.
func (a *Agent) Validate() []string {
	var problems []string
	if a.Name == "" {
		problems = append(problems, "name is empty")
	}
	if a.Name != "" && strings.EqualFold(a.Name, ReservedOwner) {
		problems = append(problems, fmt.Sprintf("name %q is reserved: %q is the pool itself (mesh://%s/...) — rename the peer", a.Name, ReservedOwner, ReservedOwner))
	}
	if a.dir != "" && a.Name != "" && filepath.Base(a.dir) != a.Name {
		problems = append(problems, fmt.Sprintf("name %q does not match directory %q", a.Name, filepath.Base(a.dir)))
	}
	if a.Title == "" {
		problems = append(problems, "title is empty")
	}
	if len(a.Accepts) == 0 {
		problems = append(problems, "accepts is empty (inbox handles no request types)")
	}
	// dispatchable:true with mutates is not a manifest error:
	// such a peer is dispatched headless only under outbound grants in the
	// pool config, and doctor warns for each ungranted surface.
	if a.Badge.Label == "" {
		problems = append(problems, "badge.label is empty")
	}
	if !ValidKnowledgeKind(a.Knowledge) {
		problems = append(problems, knowledgeKindProblem(a.Knowledge))
	}
	problems = append(problems, a.docFolderProblems()...)
	return problems
}
