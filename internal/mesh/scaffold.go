package mesh

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// AgentSpec is the input to New/Onboard for writing an agent.yaml.
type AgentSpec struct {
	Name         string
	Title        string
	Role         string
	Owns         []string
	Domains      []string
	Accepts      []Accept
	Dispatchable bool
	Mutates      []string
	Badge        Badge
	Model        string
	Repo         string
	Harness      string // claude | opencode (ADR-0032); empty ⇒ claude
	Bare         bool   // if false, New appends the "-agent" repo suffix (ADR-0038)
}

// ScaffoldOptions controls optional extras when creating/onboarding an agent.
type ScaffoldOptions struct {
	// ITerm writes a macOS iTerm2 dynamic profile (.iterm2/) for per-agent window
	// identity. Off by default — portable identity comes from `meshctl agent identity`
	// (see identity.go), which works in any terminal on any OS.
	ITerm bool
}

// scaffoldKnowledgeAreas creates the ADR-0014 KB areas. Add-only: it never
// touches an area that already has a README, and a second run is a no-op.
func scaffoldKnowledgeAreas(dest string) ([]string, error) {
	var added []string
	for _, area := range []string{"playbooks", "experience", "candidates", "reference"} {
		readme := filepath.Join(dest, KnowledgeDir, area, "README.md")
		if fileExists(readme) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(readme), 0o755); err != nil {
			return added, err
		}
		if err := copyTemplate("templates/knowledge/"+area+"/README.md", readme); err != nil {
			return added, err
		}
		added = append(added, area)
	}
	return added, nil
}

// scaffoldSkills installs the shared mesh skills, rendered into the given
// harness's native location (ADR-0032). Add-only: never clobbers an existing
// skill; a second run is a no-op. mesh-cognition is every agent's primary
// cognition instructions (ADR-0014–0018).
func scaffoldSkills(dest string, h Harness) ([]string, error) {
	var added []string
	skills := []string{"mesh-cognition"}
	// Update the steward skill too if this agent already has it (it's a steward),
	// so onboard propagates mesh-steward updates without adding it to other agents.
	if hasSkill(dest, "mesh-steward") {
		skills = append(skills, "mesh-steward")
	}
	for _, skill := range skills {
		b, err := templatesFS.ReadFile("templates/skills/" + skill + "/SKILL.md")
		if err != nil {
			return added, err
		}
		ok, err := h.RenderSkill(skill, string(b), dest)
		if err != nil {
			return added, err
		}
		if ok {
			added = append(added, skill)
		}
	}
	return added, nil
}

// ensureMemoryIgnored appends the meshctl-managed .gitignore block if absent:
// the disposable memory index (ADR-0015) AND the ephemeral intake queue + its
// archive (ADR-0043 — intake is transient discussion; durable residue lives in the
// KB, not in git). intake/README.md stays tracked. Idempotent via a marker line.
func ensureMemoryIgnored(dest string) error {
	const marker = "# meshctl-managed ignores"
	gi := filepath.Join(dest, ".gitignore")
	b, _ := os.ReadFile(gi)
	if strings.Contains(string(b), marker) {
		return nil
	}
	block := "\n" + marker + " — do not edit\n" +
		".memory/\n" + // generated, disposable (meshctl memory rebuild)
		IntakeArchiveDir + "/\n" + // archived closed requests (ADR-0043)
		"intake/*.md\n" + // ephemeral inbox traffic
		"!intake/README.md\n" + // …but keep the inbox doc
		"intake/*.claim\n" + // runtime claim locks (ADR-0011)
		"# end meshctl-managed ignores\n"
	f, err := os.OpenFile(gi, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(block)
	return err
}

// New scaffolds a brand-new agent workspace as a sibling of the mesh, including
// its agent.yaml manifest. dest must not exist.
func New(agentsDir string, spec AgentSpec, opts ScaffoldOptions) (string, error) {
	// Poly-repo repo-naming (ADR-0038): an agent repo is flagged `<name>-agent`
	// unless --bare. dir == manifest name (discovery is unchanged).
	if !spec.Bare && spec.Name != "" && !strings.HasSuffix(spec.Name, "-agent") {
		spec.Name += "-agent"
	}
	dest := AgentDir(agentsDir, spec.Name)
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s already exists — aborting", dest)
	}
	if err := os.MkdirAll(filepath.Join(dest, IntakeDir), 0o755); err != nil {
		return "", err
	}

	// Verbatim template files.
	if err := copyTemplate("templates/Taskfile.yaml", filepath.Join(dest, "Taskfile.yaml")); err != nil {
		return "", err
	}

	// Optional macOS iTerm2 profile for per-agent window identity. Portable identity
	// (any terminal, any OS) comes from `meshctl agent identity` instead.
	if opts.ITerm {
		if err := writeITermProfile(dest, spec); err != nil {
			return "", err
		}
	}

	// Rendered template files.
	repl := strings.NewReplacer("__NAME__", spec.Name, "__DIR__", spec.Name, "__BADGE__", spec.Badge.Label)
	if err := renderTemplate("templates/CLAUDE.md", filepath.Join(dest, "CLAUDE.md"), repl); err != nil {
		return "", err
	}
	if err := renderTemplate("templates/intake/README.md", filepath.Join(dest, IntakeDir, "README.md"), repl); err != nil {
		return "", err
	}

	// The manifest — the agent's identity/discovery source.
	if err := writeManifest(dest, spec); err != nil {
		return "", err
	}

	// ADR-0014 KB areas + ADR-0015 memory gitignore.
	if _, err := scaffoldKnowledgeAreas(dest); err != nil {
		return "", err
	}
	if err := ensureMemoryIgnored(dest); err != nil {
		return "", err
	}
	// Shared mesh skills + harness integration (ADR-0032), rendered per harness.
	h := HarnessByName(spec.Harness)
	if _, err := scaffoldSkills(dest, h); err != nil {
		return "", err
	}
	if _, err := h.WireAgent(dest); err != nil {
		return "", err
	}
	// Repo hygiene (ADR-0039): make the workspace a local git repo so knowledge/
	// is version-controlled from creation. Remote push is the human's job (nagged
	// by the session-start hook until done).
	initAgentRepo(dest)

	return dest, nil
}

// OnboardResult reports what onboard added (for the summary print).
type OnboardResult struct {
	Dir           string
	AddedManifest bool
	AddedIntake   bool
	AddedCommsSec bool
	InferredTitle string
	InferredRole  bool
	Notes         []string
}

// Onboard upgrades a pre-existing agent directory into a full mesh member.
// Idempotent + non-destructive: only adds what is missing, never clobbers an
// existing agent.yaml. spec supplies fields that cannot be inferred; inference
// from CLAUDE.md fills title/role when spec leaves them blank.
func Onboard(agentsDir, nameOrPath string, spec AgentSpec, opts ScaffoldOptions) (*OnboardResult, error) {
	dir := nameOrPath
	if !strings.ContainsRune(nameOrPath, os.PathSeparator) {
		dir = AgentDir(agentsDir, nameOrPath)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if !dirExists(abs) {
		return nil, fmt.Errorf("%s does not exist — use `meshctl agent new` for a fresh agent", abs)
	}
	res := &OnboardResult{Dir: abs}
	name := filepath.Base(abs)
	if spec.Name == "" {
		spec.Name = name
	}

	claudePath := filepath.Join(abs, "CLAUDE.md")
	claudeBody, _ := os.ReadFile(claudePath)

	// Resolve the harness (ADR-0032): explicit flag, else an existing manifest's
	// value, else detect from the workspace, else claude. Persisted on the manifest.
	mp := filepath.Join(abs, ManifestName)
	if spec.Harness == "" {
		if a, err := LoadAgent(mp); err == nil && a.Harness != "" {
			spec.Harness = a.Harness
		} else {
			spec.Harness = DetectHarness(abs)
		}
	}
	h := HarnessByName(spec.Harness)

	// 1. Manifest — infer where possible, never overwrite.
	if fileExists(mp) {
		res.Notes = append(res.Notes, "agent.yaml present — left unchanged")
	} else {
		if spec.Title == "" {
			spec.Title = inferTitle(string(claudeBody), name)
			res.InferredTitle = spec.Title
		}
		if spec.Role == "" {
			if r := inferRole(string(claudeBody)); r != "" {
				spec.Role = r
				res.InferredRole = true
			}
		}
		if len(spec.Accepts) == 0 {
			spec.Accepts = []Accept{{Type: "fyi", Desc: "awareness only, no action"}}
			res.Notes = append(res.Notes, "no accepts supplied — defaulted to [fyi]; edit agent.yaml to add real request types")
		}
		if spec.Badge.Label == "" {
			spec.Badge = Badge{Label: strings.ToUpper(name), R: 0.5, G: 0.5, B: 0.5}
		}
		if err := writeManifest(abs, spec); err != nil {
			return nil, err
		}
		res.AddedManifest = true
	}

	// 2. Intake dir.
	intake := filepath.Join(abs, IntakeDir)
	if !dirExists(intake) {
		if err := os.MkdirAll(intake, 0o755); err != nil {
			return nil, err
		}
		repl := strings.NewReplacer("__NAME__", name, "__DIR__", name)
		if err := renderTemplate("templates/intake/README.md", filepath.Join(intake, "README.md"), repl); err != nil {
			return nil, err
		}
		res.AddedIntake = true
	}

	// 3. CLAUDE.md intake section.
	if len(claudeBody) > 0 && !hasCommsSection(string(claudeBody)) {
		if err := appendCommsSection(claudePath, name); err != nil {
			return nil, err
		}
		res.AddedCommsSec = true
	} else if len(claudeBody) == 0 {
		res.Notes = append(res.Notes, "no CLAUDE.md — skipped intake-section injection")
	}

	// 3b. CLAUDE.md cognition standing-order stanza (ADR-0026) — add-only.
	if len(claudeBody) > 0 && !hasCognitionSection(string(claudeBody)) {
		if err := appendCognitionSection(claudePath); err != nil {
			return nil, err
		}
		res.Notes = append(res.Notes, "added Cognition standing-order stanza to CLAUDE.md")
	}

	// 4. KB areas (ADR-0014) + 5. memory gitignore (ADR-0015) — add-only.
	if added, err := scaffoldKnowledgeAreas(abs); err != nil {
		return nil, err
	} else if len(added) > 0 {
		res.Notes = append(res.Notes, "scaffolded knowledge areas: "+strings.Join(added, ", "))
	}
	if err := ensureMemoryIgnored(abs); err != nil {
		return nil, err
	}
	// 6. Shared mesh skills — rendered per harness (ADR-0032), add-only.
	if added, err := scaffoldSkills(abs, h); err != nil {
		return nil, err
	} else if len(added) > 0 {
		res.Notes = append(res.Notes, "installed skills: "+strings.Join(added, ", "))
	}
	// 7. Harness integration (hooks + preapproval, or opencode equivalent) — add-only.
	if added, err := h.WireAgent(abs); err != nil {
		return nil, err
	} else {
		res.Notes = append(res.Notes, added...)
	}

	// Optional macOS iTerm2 profile (add-only; never clobber an existing one).
	if opts.ITerm {
		if _, err := os.Stat(filepath.Join(abs, ".iterm2")); os.IsNotExist(err) {
			if a, _ := FindAgent(agentsDir, spec.Name); a != nil {
				if err := writeITermProfile(abs, specFromAgent(a)); err != nil {
					res.Notes = append(res.Notes, "iTerm2 profile not written: "+err.Error())
				} else {
					res.Notes = append(res.Notes, "wrote optional iTerm2 profile (.iterm2/)")
				}
			}
		}
	}
	return res, nil
}

// hasSkill reports whether an agent already has a skill under either harness's
// skills dir (used to update, not add, a skill on onboard).
func hasSkill(dest, name string) bool {
	return fileExists(filepath.Join(dest, ".claude", "skills", name, "SKILL.md")) ||
		fileExists(filepath.Join(dest, ".opencode", "skills", name, "SKILL.md"))
}

// StewardName is the mesh onboarding-hub agent scaffolded by `pool init` /
// `agent bootstrap` (ADR-0035).
const StewardName = "steward-agent"

// ScaffoldSteward stands up the mesh steward — the interview-and-build onboarding
// hub (ADR-0035): a normal agent workspace whose CLAUDE.md is the steward role and
// which carries the mesh-steward skill (the interview → roster → scaffold playbook)
// on top of mesh-cognition. Idempotent: if the steward already exists, it is left
// untouched. harness is the steward's own harness (claude|opencode).
func ScaffoldSteward(agentsDir, harness string, now time.Time) (string, []string, error) {
	dest := AgentDir(agentsDir, StewardName)
	if dirExists(dest) {
		return dest, []string{"steward already present — left unchanged"}, nil
	}
	spec := AgentSpec{
		Name:    StewardName,
		Title:   "Mesh Steward",
		Role:    "Onboarding concierge and front door for this mesh: interview the user for the mesh's purpose, design the agent roster, scaffold the agents with meshctl, and serve as the community's entry point and hub. Builds and coordinates the team; does not do their domain work.",
		Owns:    []string{"mesh-onboarding", "roster"},
		Domains: []string{"mesh-bootstrap"},
		Accepts: []Accept{
			{Type: "guidance", Desc: "help shape the mesh / its roster"},
			{Type: "fyi", Desc: "awareness only"},
		},
		Dispatchable: false, // human-interactive; scaffolds local files
		Badge:        Badge{Label: "STEWARD", R: 0.55, G: 0.25, B: 0.7},
		Harness:      harness,
		Bare:         true, // StewardName already carries the -agent suffix
	}
	dir, err := New(agentsDir, spec, ScaffoldOptions{})
	if err != nil {
		return dest, nil, err
	}
	notes := []string{"scaffolded steward (" + HarnessByName(harness).Name() + ")"}

	// Steward CLAUDE.md = the steward role + the standard comms + cognition stanzas.
	claudePath := filepath.Join(dir, "CLAUDE.md")
	body, err := templatesFS.ReadFile("templates/steward/CLAUDE.md")
	if err != nil {
		return dir, notes, err
	}
	if err := os.WriteFile(claudePath, body, 0o644); err != nil {
		return dir, notes, err
	}
	if err := appendCommsSection(claudePath, StewardName); err != nil {
		return dir, notes, err
	}
	if err := appendCognitionSection(claudePath); err != nil {
		return dir, notes, err
	}

	// The mesh-steward skill (the interview/roster/scaffold playbook), per harness.
	sb, err := templatesFS.ReadFile("templates/skills/mesh-steward/SKILL.md")
	if err != nil {
		return dir, notes, err
	}
	if _, err := HarnessByName(harness).RenderSkill("mesh-steward", string(sb), dir); err != nil {
		return dir, notes, err
	}
	notes = append(notes, "installed mesh-steward skill")
	return dir, notes, nil
}

// --- template rendering ---

func copyTemplate(embedded, dest string) error {
	b, err := templatesFS.ReadFile(embedded)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, b, 0o644)
}

func renderTemplate(embedded, dest string, repl *strings.Replacer) error {
	b, err := templatesFS.ReadFile(embedded)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, []byte(repl.Replace(string(b))), 0o644)
}

var (
	reRed   = regexp.MustCompile(`(?m)^  red: .*$`)
	reGreen = regexp.MustCompile(`(?m)^  green: .*$`)
	reBlue  = regexp.MustCompile(`(?m)^  blue: .*$`)
)

// writeITermProfile writes the optional macOS iTerm2 dynamic profile for an agent:
// the .iterm2/ dir, the profile template, and the per-agent values (badge + color).
func writeITermProfile(dest string, spec AgentSpec) error {
	if err := os.MkdirAll(filepath.Join(dest, ".iterm2"), 0o755); err != nil {
		return err
	}
	if err := copyTemplate("templates/.iterm2/profile.json.tmpl", filepath.Join(dest, ".iterm2", "profile.json.tmpl")); err != nil {
		return err
	}
	return renderITermValues(dest, spec)
}

func renderITermValues(dest string, spec AgentSpec) error {
	b, err := templatesFS.ReadFile("templates/.iterm2.values.yaml")
	if err != nil {
		return err
	}
	s := string(b)
	s = strings.NewReplacer("__NAME__", spec.Name, "__BADGE__", spec.Badge.Label).Replace(s)
	s = reRed.ReplaceAllString(s, fmt.Sprintf("  red: %g", spec.Badge.R))
	s = reGreen.ReplaceAllString(s, fmt.Sprintf("  green: %g", spec.Badge.G))
	s = reBlue.ReplaceAllString(s, fmt.Sprintf("  blue: %g", spec.Badge.B))
	return os.WriteFile(filepath.Join(dest, ".iterm2.values.yaml"), []byte(s), 0o644)
}

// --- manifest writer ---

func writeManifest(dir string, spec AgentSpec) error {
	// Enforce the invariant at write time too.
	dispatchable := spec.Dispatchable && len(spec.Mutates) == 0

	var b strings.Builder
	fmt.Fprintf(&b, "# %s/agent.yaml — the agent's authoritative self-description (ADR-0010).\n", spec.Name)
	fmt.Fprintf(&b, "# Discovery is computed from these manifests; there is no committed roster.\n")
	fmt.Fprintf(&b, "name: %s\n", spec.Name)
	fmt.Fprintf(&b, "title: %s\n", yamlScalar(spec.Title))
	fmt.Fprintf(&b, "role: >\n  %s\n", indentFold(spec.Role))
	fmt.Fprintf(&b, "inbox: %s/\n", IntakeDir)
	fmt.Fprintf(&b, "owns: [%s]\n", strings.Join(spec.Owns, ", "))
	fmt.Fprintf(&b, "domains: [%s]\n", strings.Join(spec.Domains, ", "))
	fmt.Fprintf(&b, "accepts:\n")
	for _, a := range spec.Accepts {
		fmt.Fprintf(&b, "  - { type: %-16s desc: %s }\n", a.Type+",", a.Desc)
	}
	fmt.Fprintf(&b, "dispatchable: %t\n", dispatchable)
	fmt.Fprintf(&b, "mutates: [%s]\n", strings.Join(spec.Mutates, ", "))
	fmt.Fprintf(&b, "badge: { label: %s, r: %g, g: %g, b: %g }\n", spec.Badge.Label, spec.Badge.R, spec.Badge.G, spec.Badge.B)
	model := spec.Model
	if model == "" {
		model = "default"
	}
	fmt.Fprintf(&b, "model: %s\n", model)
	harness := spec.Harness
	if harness == "" {
		harness = HarnessClaude
	}
	fmt.Fprintf(&b, "harness: %s\n", harness)
	if spec.Repo != "" {
		fmt.Fprintf(&b, "repo: %s\n", spec.Repo)
	}
	return os.WriteFile(filepath.Join(dir, ManifestName), []byte(b.String()), 0o644)
}

func specFromAgent(a *Agent) AgentSpec {
	return AgentSpec{
		Name: a.Name, Title: a.Title, Role: a.Role, Owns: a.Owns, Domains: a.Domains,
		Accepts: a.Accepts, Dispatchable: a.IsDispatchable(), Mutates: a.Mutates,
		Badge: a.Badge, Model: a.Model, Repo: a.Repo, Harness: a.Harness,
	}
}

func yamlScalar(s string) string {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, ":#") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func indentFold(s string) string {
	s = strings.TrimSpace(s)
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	if s == "" {
		return "(describe this agent)"
	}
	return s
}

// --- CLAUDE.md inference + injection ---

var reH1 = regexp.MustCompile(`(?m)^#\s+(.+)$`)

func inferTitle(claude, fallback string) string {
	if m := reH1.FindStringSubmatch(claude); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.Title(strings.ReplaceAll(fallback, "-", " ")) //nolint:staticcheck
}

func inferRole(claude string) string {
	// Grab the text under a "## Role" heading up to the next heading.
	lines := strings.Split(claude, "\n")
	var buf []string
	in := false
	for _, ln := range lines {
		if strings.HasPrefix(ln, "## ") {
			if in {
				break
			}
			if strings.EqualFold(strings.TrimSpace(ln), "## Role") {
				in = true
			}
			continue
		}
		if in {
			buf = append(buf, ln)
		}
	}
	return strings.TrimSpace(strings.Join(buf, " "))
}

func hasCommsSection(claude string) bool {
	return strings.Contains(claude, "Agent Communication (intake)") ||
		strings.Contains(claude, "agent-comms.md")
}

const commsSection = `

## Agent Communication (intake)

Bidirectional file-based handoff — canonical protocol: ` + "`.agentmesh/doctrine/agent-comms.md`" + `. Inbox: ` + "`~/agents/%s/intake/`" + `. The roster is computed on demand — run ` + "`meshctl agent list`" + ` (do not maintain a peer list here).

- **Inbound:** at session start / on "check intake", run ` + "`meshctl inbox next --claim`" + ` (atomic claim) or scan ` + "`intake/*.md`" + ` for ` + "`status: pending`" + `; process by ` + "`type`" + `, then write the outcome back with ` + "`meshctl inbox respond <id> --status answered|filed|needs-info|needs-human --response \"…\"`" + ` (intake files are meshctl-owned — never hand-edit them). **Not yours?** ` + "`meshctl inbox decline <id> --reason \"…\" --redirect <owner>`" + ` — don't half-answer out-of-domain work.
- **Read-back:** ` + "`meshctl sent`" + ` lists requests you sent that came back (answered / declined / needs-info); read each, act, then ` + "`meshctl sent ack <id>`" + `. The session start surfaces them automatically.
- **Outbound:** ` + "`meshctl send <target> <type> \"…\"`" + ` writes a request into a peer's inbox; ` + "`meshctl ask <target> <type> \"…\"`" + ` also dispatches an advisory peer headlessly and blocks for the answer. State-mutating peers (pm→tracker, developer→code) are ` + "`send`" + `-only and human-triggered.
- **Blocked on a decision mid-task?** ` + "`meshctl ask <advisor> <type> \"…\"`" + ` for a synchronous answer; if it returns ` + "`needs-human`" + `, stop and surface to the user.
`

func appendCommsSection(claudePath, name string) error {
	f, err := os.OpenFile(claudePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, commsSection, name)
	return err
}

// hasCognitionSection detects the ADR-0026 cognition standing-order stanza.
func hasCognitionSection(claude string) bool {
	return strings.Contains(claude, "## Cognition") &&
		strings.Contains(claude, "mesh-cognition")
}

const cognitionSection = `

## Cognition — maintain your own knowledge base (standing order)

You own a git-tracked ` + "`knowledge/`" + ` KB and **learn over time** — this is not optional.
Run the **` + "`mesh-cognition`" + ` skill** for the full procedure; its short form:

- **Recall at start.** ` + "`meshctl memory recall \"<topic>\"`" + `, then read the KB files it
  points to. Your ` + "`knowledge/handoff.md`" + ` is auto-injected.
- **Distill as you learn.** When a session yields a recurring pattern, a reusable
  procedure, a failure mode, or a consequential decision: ` + "`meshctl kb begin`" + ` →
  write ` + "`knowledge/candidates/<slug>.md`" + ` → consolidate into ` + "`playbooks/`" + ` /
  ` + "`experience/`" + ` / a decision (ADR) → ` + "`meshctl kb finish`" + `.
- **Record decisions & risks.** Consequential domain choices become ADRs in
  ` + "`knowledge/decisions/`" + `; new risks get logged in ` + "`knowledge/risks.md`" + `.
- **Handoff at end.** Rewrite ` + "`knowledge/handoff.md`" + ` via ` + "`meshctl handoff set`" + `.
- **Repo hygiene (your knowledge is only as safe as your remote).** Your workspace
  must be its own git repo **with a remote** — ` + "`knowledge/`" + ` is durable only
  once committed **and pushed**. If your dir is not a git repo, ` + "`git init`" + ` +
  commit it now. Then create a remote and push. Session start will keep reminding you
  until a remote exists — clear it by pushing.

Authoritative spec: ` + "`.agentmesh/doctrine/agent-comms.md`" + ` §Knowledge & Memory
(ADR-0014–0018).
`

func appendCognitionSection(claudePath string) error {
	f, err := os.OpenFile(claudePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(cognitionSection)
	return err
}

// emitDoctrine writes the embedded doctrine docs (agent-comms.md, distiller.md)
// into <poolRoot>/agent-mesh/, the path agents' CLAUDE.md reference. Binary is
// the source (ADR-0031); this replaces the ADR-0029 git clone. overwrite=false
// skips files already present (fresh init), true re-emits (upgrade).
func emitDoctrine(poolRoot string, overwrite bool) ([]string, error) {
	dest := filepath.Join(poolRoot, DoctrineDir)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	var did []string
	entries, err := templatesFS.ReadDir("templates/doctrine")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out := filepath.Join(dest, e.Name())
		if fileExists(out) && !overwrite {
			continue
		}
		if err := copyTemplate("templates/doctrine/"+e.Name(), out); err != nil {
			return did, err
		}
		did = append(did, "doctrine: "+filepath.Join(DoctrineDir, e.Name()))
	}
	return did, nil
}

// InitPool bootstraps a NEW, isolated mesh pool at poolRoot FROM THE BINARY
// (ADR-0031): emits the doctrine docs from embed, creates the runtime dirs, and
// writes the .agentmesh/pool.yaml marker. No git clone, no network. Aborts if
// poolRoot is already a pool. name defaults to the pool dir's basename.
func InitPool(poolRoot, name string, now time.Time) ([]string, error) {
	abs, err := filepath.Abs(poolRoot)
	if err != nil {
		return nil, err
	}
	if IsPool(abs) {
		return nil, fmt.Errorf("%s is already a pool (%s/ present) — aborting to avoid clobber", abs, PoolMarkerDir)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	var did []string

	dd, err := emitDoctrine(abs, false)
	if err != nil {
		return did, err
	}
	did = append(did, dd...)

	// Write the marker (with the requested identity) BEFORE InitMesh, so InitMesh's
	// add-only marker step sees an existing pool and doesn't overwrite the name.
	if err := WritePoolMarker(abs, name, doctrineVersion(), now, false); err != nil {
		return did, err
	}
	did = append(did, "pool marker: "+PoolMarkerPath(abs))

	more, err := InitMesh(abs)
	if err != nil {
		return did, err
	}
	did = append(did, more...)
	return did, nil
}

// IsLegacyLayout reports whether a pool still uses the pre-ADR-0038 layout: a
// STALE top-level `agent-mesh/` doctrine emit, or `.worktrees`/`.dispatch.*` at the
// root. A git-repo `agent-mesh/` is NOT legacy — it's the intentional build-source
// repo that `pool migrate` keeps (e.g. a `~/agents` pool), so it must not trip this.
func IsLegacyLayout(poolRoot string) bool {
	legacy := filepath.Join(poolRoot, LegacyDoctrineDir)
	if fileExists(filepath.Join(legacy, CommsFile)) && !dirExists(filepath.Join(legacy, ".git")) {
		return true
	}
	for _, p := range []string{".worktrees", ".dispatch.log", ".dispatch.count"} {
		if _, err := os.Stat(filepath.Join(poolRoot, p)); err == nil {
			return true
		}
	}
	return false
}

// MigratePool reconciles a legacy (pre-ADR-0038) pool to the consolidated
// `.agentmesh/` layout: emits doctrine under `.agentmesh/doctrine`, moves
// `.worktrees` → `.agentmesh/worktrees` and `.dispatch.*` → `.agentmesh/`, and
// removes the legacy top-level `agent-mesh/` doctrine dir — but ONLY if it is not a
// git repo (protects a pool where `agent-mesh/` is the actual meshctl clone, e.g.
// a `~/agents` pool). Idempotent.
func MigratePool(poolRoot string) ([]string, error) {
	abs, err := filepath.Abs(poolRoot)
	if err != nil {
		return nil, err
	}
	if !IsPool(abs) {
		return nil, fmt.Errorf("%s is not a pool (no %s/ marker)", abs, PoolMarkerDir)
	}
	var did []string

	// New doctrine location.
	if dd, err := emitDoctrine(abs, false); err != nil {
		return did, err
	} else {
		did = append(did, dd...)
	}

	// Move runtime into .agentmesh/.
	moves := []struct{ from, to string }{
		{".worktrees", WorktreesDir},
		{".dispatch.log", DispatchLog},
		{".dispatch.count", DispatchCount},
		{".dispatch.lock", DispatchLock},
	}
	for _, m := range moves {
		src := filepath.Join(abs, m.from)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(abs, m.to)
		if _, err := os.Stat(dst); err == nil {
			continue // already migrated; leave both, non-destructive
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return did, err
		}
		if err := os.Rename(src, dst); err != nil {
			return did, err
		}
		did = append(did, "moved "+m.from+" → "+m.to)
	}

	// Retire the legacy top-level doctrine dir — but never delete a git repo.
	legacy := filepath.Join(abs, LegacyDoctrineDir)
	if dirExists(legacy) {
		if dirExists(filepath.Join(legacy, ".git")) {
			did = append(did, "kept "+LegacyDoctrineDir+"/ (it is a git repo — not a doctrine emit); doctrine now lives in "+DoctrineDir)
		} else if fileExists(filepath.Join(legacy, CommsFile)) {
			if err := os.RemoveAll(legacy); err != nil {
				return did, err
			}
			did = append(did, "removed legacy "+LegacyDoctrineDir+"/ (doctrine now in "+DoctrineDir+")")
		}
	}
	if len(did) == 0 {
		did = append(did, "already on the current layout")
	}
	return did, nil
}

// TeardownPool removes a pool's mesh infrastructure (the `.agentmesh/` dir + the
// legacy `agent-mesh/` doctrine dir if present and not a git repo) and deregisters
// it. Agent repos are LEFT in place (they hold committed knowledge). With purge,
// agent workspaces (`*` dirs holding an agent.yaml) are also removed — destructive.
func TeardownPool(poolRoot string, purge bool) ([]string, error) {
	abs, err := filepath.Abs(poolRoot)
	if err != nil {
		return nil, err
	}
	if !IsPool(abs) {
		return nil, fmt.Errorf("%s is not a pool (no %s/ marker)", abs, PoolMarkerDir)
	}
	var did []string
	if purge {
		agents, _, _ := ScanAgents(abs)
		for _, a := range agents {
			if err := os.RemoveAll(a.Dir()); err != nil {
				return did, err
			}
			did = append(did, "purged agent "+a.Name)
		}
	}
	// Legacy doctrine dir (only if not a git repo).
	legacy := filepath.Join(abs, LegacyDoctrineDir)
	if dirExists(legacy) && !dirExists(filepath.Join(legacy, ".git")) && fileExists(filepath.Join(legacy, CommsFile)) {
		_ = os.RemoveAll(legacy)
		did = append(did, "removed "+LegacyDoctrineDir+"/")
	}
	if err := os.RemoveAll(filepath.Join(abs, PoolMarkerDir)); err != nil {
		return did, err
	}
	did = append(did, "removed "+PoolMarkerDir+"/")
	if ok, _ := ForgetPool(abs); ok {
		did = append(did, "deregistered from the pool registry")
	}
	return did, nil
}

// UpgradeDoctrine re-emits the embedded doctrine into the current pool (ADR-0031
// `init --upgrade`), refreshing agent-mesh/*.md from a newer binary. Overwrites
// mesh-owned doctrine only; never touches agent content.
func UpgradeDoctrine(poolRoot string) ([]string, error) {
	return emitDoctrine(poolRoot, true)
}

// InitMesh ensures runtime artifact paths, the pool marker, and (if absent) the
// doctrine docs exist in an existing pool. Idempotent — safe to run repeatedly,
// and it never clobbers an existing doctrine file or marker.
func InitMesh(agentsDir string) ([]string, error) {
	var did []string
	// Runtime artifacts live outside any repo, directly in the container.
	for _, d := range []string{WorktreesDir} {
		p := filepath.Join(agentsDir, d)
		if !dirExists(p) {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return did, err
			}
			did = append(did, "created "+p)
		}
	}
	// Ensure the pool marker exists (add-only; never overwrites identity). This is
	// how an existing (pre-ADR-0031) pool like ~/agents gains the marker without
	// touching its agent-mesh/ dir.
	if !IsPool(agentsDir) {
		if err := WritePoolMarker(agentsDir, "", doctrineVersion(), time.Now(), false); err != nil {
			return did, err
		}
		did = append(did, "pool marker: "+PoolMarkerPath(agentsDir))
	}
	// Verify the embedded templates are intact.
	n := 0
	_ = fs.WalkDir(templatesFS, "templates", func(_ string, d fs.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			n++
		}
		return nil
	})
	did = append(did, fmt.Sprintf("templates embedded: %d files", n))
	return did, nil
}

// doctrineVersion is a coarse version stamp for the embedded doctrine, recorded
// in the pool marker so `init --upgrade` intent is auditable. Derived from the
// embedded agent-comms.md size (a cheap change-detector without build-time vars).
func doctrineVersion() string {
	if b, err := templatesFS.ReadFile("templates/doctrine/" + CommsFile); err == nil {
		return fmt.Sprintf("comms-%db", len(b))
	}
	return "unknown"
}
