package mesh

import (
	"fmt"
	"os"
	"path/filepath"
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

// Agent is the parsed agent.yaml — an agent's authoritative self-description
// (ADR-0010). It is the single source of truth for identity + discovery.
type Agent struct {
	Name    string   `yaml:"name"`
	Title   string   `yaml:"title"`
	Role    string   `yaml:"role"`
	Inbox   string   `yaml:"inbox"`
	Owns    []string `yaml:"owns"`
	Domains []string `yaml:"domains"`
	Accepts []Accept `yaml:"accepts"`
	// Dispatchable is a pointer so we can tell "unset" from "false" and apply
	// the mutates-implies-not-dispatchable invariant deterministically.
	Dispatchable *bool    `yaml:"dispatchable"`
	Mutates      []string `yaml:"mutates"`
	Badge        Badge    `yaml:"badge"`
	Model        string   `yaml:"model,omitempty"`
	Repo         string   `yaml:"repo,omitempty"`
	// Harness is the coding-agent harness this workspace runs under (claude |
	// opencode). Determines how hooks/skills are wired (ADR-0032). Empty ⇒ claude.
	Harness string `yaml:"harness,omitempty"`
	// Tools are agent-specific tool references (ADR-0042), merged over the pool's.
	Tools []ToolRef `yaml:"tools,omitempty"`

	// dir is the on-disk workspace directory this manifest was loaded from.
	dir string `yaml:"-"`
}

// HarnessName returns the agent's harness, defaulting to "claude".
func (a *Agent) HarnessName() string {
	if a.Harness == "" {
		return HarnessClaude
	}
	return a.Harness
}

// Dir returns the workspace directory the manifest was loaded from.
func (a *Agent) Dir() string { return a.dir }

// IsDispatchable reports whether the CLI may run this agent headlessly. The
// invariant (len(mutates)>0 ⇒ not dispatchable) is enforced here so a
// misconfigured manifest can never be dispatched even if the field says true.
func (a *Agent) IsDispatchable() bool {
	if len(a.Mutates) > 0 {
		return false
	}
	return a.Dispatchable != nil && *a.Dispatchable
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
	rel, err := filepath.Rel(filepath.Join(agentsDir, WorktreesDir), cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "" // cwd is not inside .worktrees
	}
	base := strings.Split(rel, string(filepath.Separator))[0] // <agent>-<session>
	agents, _, err := ScanAgents(agentsDir)
	if err != nil {
		return ""
	}
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

// FindAgent loads a single agent by name; error if its manifest is missing.
func FindAgent(agentsDir, name string) (*Agent, error) {
	mp := ManifestPath(agentsDir, name)
	if !fileExists(mp) {
		return nil, fmt.Errorf("agent %q has no manifest (%s not found)", name, mp)
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
	if a.dir != "" && a.Name != "" && filepath.Base(a.dir) != a.Name {
		problems = append(problems, fmt.Sprintf("name %q does not match directory %q", a.Name, filepath.Base(a.dir)))
	}
	if a.Title == "" {
		problems = append(problems, "title is empty")
	}
	if len(a.Accepts) == 0 {
		problems = append(problems, "accepts is empty (inbox handles no request types)")
	}
	// The core safety invariant (ADR-0010).
	if len(a.Mutates) > 0 && a.Dispatchable != nil && *a.Dispatchable {
		problems = append(problems, fmt.Sprintf("dispatchable:true conflicts with mutates:%v — a state-mutating agent must not be headlessly dispatchable", a.Mutates))
	}
	if a.Badge.Label == "" {
		problems = append(problems, "badge.label is empty")
	}
	return problems
}
