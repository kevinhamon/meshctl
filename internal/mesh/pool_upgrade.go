package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// pool_upgrade.go — bring a whole pool up to this meshctl binary and the harness
// versions installed on this machine: the pool doctrine plus every agent's
// wiring (hooks/plugin, mesh-owned skills, injected CLAUDE.md stanzas).
// PlanPoolUpgrade is the read-only preview (`pool upgrade --check`); the steward
// shows it to the user before anything is written. UpgradeAgents applies the
// agent half by re-running onboard, the same idempotent, add-only path a human
// would run per agent.

// AgentUpgradePlan is what an upgrade would change in one agent workspace.
type AgentUpgradePlan struct {
	Name     string
	Dir      string
	Harness  string
	Actions  []string // changes the upgrade will make
	Warnings []string // things the user must handle (not changed by the upgrade)
}

// PoolUpgradePlan is the read-only preview of a pool upgrade.
type PoolUpgradePlan struct {
	PoolRoot      string
	OpencodeMajor int      // installed opencode major (0 ⇒ none on $PATH)
	Doctrine      []string // doctrine files that will be added or replaced
	Conflicts     []string // locally-edited doctrine: left alone, .upstream written for a merge
	Warnings      []string // pool-wide issues outside the pool (e.g. global harness config)
	Agents        []AgentUpgradePlan
}

// Pending reports whether the upgrade would change anything.
func (p PoolUpgradePlan) Pending() bool {
	if len(p.Doctrine) > 0 || len(p.Conflicts) > 0 {
		return true
	}
	for _, a := range p.Agents {
		if len(a.Actions) > 0 {
			return true
		}
	}
	return false
}

// PlanPoolUpgrade previews a pool upgrade without writing anything.
func PlanPoolUpgrade(poolRoot string, now time.Time) (PoolUpgradePlan, error) {
	plan := PoolUpgradePlan{PoolRoot: poolRoot}
	var err error
	if plan.Doctrine, plan.Conflicts, err = planDoctrine(poolRoot); err != nil {
		return plan, err
	}
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return plan, err
	}
	usesOpencode := false
	for _, a := range agents {
		usesOpencode = usesOpencode || a.HarnessName() == HarnessOpencode
		plan.Agents = append(plan.Agents, planAgent(a, now))
	}
	if usesOpencode {
		plan.OpencodeMajor = InstalledOpencodeMajor()
		if plan.OpencodeMajor >= 2 {
			plan.Warnings = append(plan.Warnings, opencodeGlobalConfigWarnings()...)
		}
	}
	return plan, nil
}

// planDoctrine classifies each embedded doctrine file the way UpgradeDoctrine
// would act on it (without force), read-only.
func planDoctrine(poolRoot string) (update, conflicts []string, err error) {
	dest := filepath.Join(poolRoot, DoctrineDir)
	stamps := readDoctrineStamps(dest)
	entries, err := templatesFS.ReadDir("templates/doctrine")
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		want, err := templatesFS.ReadFile("templates/doctrine/" + e.Name())
		if err != nil {
			return nil, nil, err
		}
		rel := filepath.Join(DoctrineDir, e.Name())
		have, err := os.ReadFile(filepath.Join(dest, e.Name()))
		switch {
		case err != nil:
			update = append(update, rel+" (new)")
		case sha(have) == sha(want):
			// current
		case stamps[e.Name()] == sha(have):
			update = append(update, rel)
		case stamps[e.Name()] == "":
			// Written before meshctl stamped doctrine: an old pristine copy and a
			// local edit look the same, so it is treated as edited (never clobbered).
			conflicts = append(conflicts, rel+" (unstamped — may be an older pristine copy or a local edit; diff to tell)")
		default:
			conflicts = append(conflicts, rel)
		}
	}
	return update, conflicts, nil
}

func planAgent(a *Agent, now time.Time) AgentUpgradePlan {
	p := AgentUpgradePlan{Name: a.Name, Dir: a.Dir(), Harness: a.HarnessName()}
	dir := a.Dir()

	// Harness wiring.
	if a.HarnessName() == HarnessOpencode {
		major := opencodeExpectedMajor(dir)
		want, wantRel, wantJS, staleRel, staleJS := 1, opencodePluginV1Rel, opencodePluginJS, opencodePluginV2Rel, opencodePluginV2JS
		if major >= 2 {
			want, wantRel, wantJS, staleRel, staleJS = 2, opencodePluginV2Rel, opencodePluginV2JS, opencodePluginV1Rel, opencodePluginJS
		}
		switch opencodePluginState(dir, wantRel, wantJS) {
		case pluginAbsent:
			p.Actions = append(p.Actions, fmt.Sprintf("write opencode V%d plugin %s", want, wantRel))
		case pluginStale:
			p.Actions = append(p.Actions, "update "+wantRel)
		case pluginForeign:
			p.Warnings = append(p.Warnings, wantRel+" is not meshctl's — left alone; mesh hooks/gate unverified")
		}
		if managed(opencodePluginState(dir, staleRel, staleJS)) {
			p.Actions = append(p.Actions, "remove "+staleRel+" (other opencode major)")
		}
		if major < 2 {
			p.Warnings = append(p.Warnings, "opencode V1: no pre-tool hook, so the mesh gate stays unavailable until opencode V2")
		}
	} else {
		if v := WiringVersion(dir); v < MeshWiringVersion {
			p.Actions = append(p.Actions, fmt.Sprintf("refresh .claude/settings.json mesh hooks (v%d → v%d)", v, MeshWiringVersion))
		}
		// In-session peer subagents are derived from the roster; a roster
		// change or a template change makes them stale.
		poolRoot := filepath.Dir(dir)
		p.Actions = append(p.Actions, PlanPeerAgents(poolRoot, dir).Actions()...)
		if IsPool(poolRoot) {
			if miss := MissingAdditionalDirs(dir, append([]string{poolRoot}, PeerWorkdirs(poolRoot, dir)...)...); len(miss) > 0 {
				p.Actions = append(p.Actions, "add "+strings.Join(miss, ", ")+" to .claude/settings.local.json additionalDirectories (in-session peers)")
			}
		}
	}

	// Mesh-owned skills (regenerated from the embedded templates).
	skillRoot := ".claude"
	if a.HarnessName() == HarnessOpencode {
		skillRoot = ".opencode"
	}
	skills := []string{"mesh-cognition"}
	if hasSkill(dir, "mesh-steward") {
		skills = append(skills, "mesh-steward")
	}
	for _, s := range skills {
		want, err := templatesFS.ReadFile("templates/skills/" + s + "/SKILL.md")
		if err != nil {
			continue
		}
		have, err := os.ReadFile(filepath.Join(dir, skillRoot, "skills", s, "SKILL.md"))
		if err != nil || string(have) != string(want) {
			p.Actions = append(p.Actions, "refresh skill "+s)
		}
	}

	// Workspace scaffolding onboard adds when missing (add-only).
	if b, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); !strings.Contains(string(b), gitignoreMarker) {
		p.Actions = append(p.Actions, "add meshctl-managed block to .gitignore (.memory/, intake traffic)")
	}
	if !dirExists(filepath.Join(dir, IntakeDir)) {
		p.Actions = append(p.Actions, "create "+IntakeDir+"/")
	}
	for _, area := range []string{"playbooks", "experience", "candidates", "reference"} {
		if !fileExists(filepath.Join(dir, KnowledgeDir, area, "README.md")) {
			p.Actions = append(p.Actions, "add "+KnowledgeDir+"/"+area+"/README.md")
		}
	}

	// CLAUDE.md stanzas onboard injects (add-only).
	if b, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); err == nil {
		if !hasCommsSection(string(b)) {
			p.Actions = append(p.Actions, "add Agent Communication section to CLAUDE.md")
		}
		if !hasCognitionSection(string(b)) {
			p.Actions = append(p.Actions, "add Cognition stanza to CLAUDE.md")
		}
	}

	// Things the upgrade does not fix but the user must know before running it.
	if n := len(liveSessions(dir, now)); n > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d live session(s) — close them before upgrading and restart after", n))
	}
	if dirty := gitDirty(dir); dirty != "" {
		p.Warnings = append(p.Warnings, "uncommitted changes ("+dirty+") — commit first so the upgrade is easy to review/revert")
	}
	return p
}

// gitDirty returns a short summary of uncommitted changes, or "" if clean or
// not a repo.
func gitDirty(dir string) string {
	if !isGitRepo(dir) {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	return fmt.Sprintf("%d path(s)", len(lines))
}

// UpgradeAgents re-runs onboard for every agent in the pool, which re-wires the
// harness for the installed versions, refreshes mesh-owned skills and adds any
// missing CLAUDE.md stanzas. Never touches agent.yaml or the role body. Returns
// per-agent notes in roster order.
func UpgradeAgents(poolRoot string) ([]AgentUpgradePlan, error) {
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return nil, err
	}
	var out []AgentUpgradePlan
	for _, a := range agents {
		res, err := Onboard(poolRoot, a.Dir(), AgentSpec{}, ScaffoldOptions{})
		if err != nil {
			return out, fmt.Errorf("%s: %w", a.Name, err)
		}
		var did []string
		for _, n := range res.Notes {
			if n != "agent.yaml present — left unchanged" {
				did = append(did, n)
			}
		}
		if res.AddedIntake {
			did = append(did, "created intake/")
		}
		if res.AddedCommsSec {
			did = append(did, "added Agent Communication section to CLAUDE.md")
		}
		out = append(out, AgentUpgradePlan{Name: a.Name, Dir: a.Dir(), Harness: a.HarnessName(), Actions: did})
	}
	return out, nil
}

// opencodeV1ConfigKeys are V1 opencode.json keys that V2 renames or reshapes
// (https://opencode.ai/v2/docs/migrate-v1/). Under V2 they are ignored, so the
// setting silently stops applying.
var opencodeV1ConfigKeys = map[string]string{
	"plugin":     "plugins (V1 plugins do not run on V2 — each needs a V2 port)",
	"provider":   "providers (npm → package, api → settings.baseURL)",
	"permission": "permissions (ordered array; bash → shell, task → subagent)",
	"tools":      "permissions (tool toggles become permission rules)",
	"agent":      "agents (prompt → system, disable → disabled)",
	"mode":       "agents",
	"command":    "commands",
	"autoshare":  "share",
	"snapshot":   "snapshots",
	"attachment": "media",
	"reference":  "references",
	"logLevel":   "the OPENCODE_LOG_LEVEL env var",
}

// opencodeGlobalConfigWarnings flags V1-only keys in the user's global opencode
// config. Outside the pool, so the upgrade reports and never edits it.
func opencodeGlobalConfigWarnings() []string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		base = filepath.Join(home, ".config")
	}
	var out []string
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		path := filepath.Join(base, "opencode", name)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg map[string]json.RawMessage
		if json.Unmarshal(stripJSONC(b), &cfg) != nil {
			out = append(out, path+" could not be parsed — check it against the V2 migration guide by hand")
			continue
		}
		var hits []string
		for k, to := range opencodeV1ConfigKeys {
			if _, ok := cfg[k]; ok {
				hits = append(hits, k+" → "+to)
			}
		}
		if raw, ok := cfg["mcp"]; ok {
			var mcp map[string]json.RawMessage
			if json.Unmarshal(raw, &mcp) == nil {
				if _, v2 := mcp["servers"]; !v2 && len(mcp) > 0 {
					hits = append(hits, "mcp → mcp.servers (enabled → disabled, inverted)")
				}
			}
		}
		if len(hits) == 0 {
			continue
		}
		sort.Strings(hits)
		out = append(out, path+" uses V1 keys that opencode V2 ignores: "+strings.Join(hits, "; ")+
			" — migrate by hand (https://opencode.ai/v2/docs/migrate-v1/); meshctl does not edit it")
	}
	return out
}

// stripJSONC removes // and /* */ comments (outside strings) and trailing
// commas, turning JSONC into JSON.
func stripJSONC(b []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		case c == '}' || c == ']':
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\n' || out[j] == '\t' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}
