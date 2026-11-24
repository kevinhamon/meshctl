package mesh

import (
	"crypto/sha256"
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
	Harness  string   // the preferred harness (agent.yaml `harness:`)
	Actions  []string // changes the upgrade will make
	Warnings []string // things the user must handle (not changed by the upgrade)
	// AddHarnesses are the harnesses the upgrade wires the workspace for
	// that it is not wired for yet; RefreshHarnesses are already-wired ones
	// whose wiring it changes. Both are in the wire set (wire_set.go).
	AddHarnesses     []string
	RefreshHarnesses []string
}

// PoolUpgradePlan is the read-only preview of a pool upgrade.
type PoolUpgradePlan struct {
	PoolRoot      string
	OpencodeMajor int      // installed opencode major (0 ⇒ none on $PATH)
	Pool          []string // pool directory steps: rename, repo, ignore file, first commit
	Doctrine      []string // doctrine files that will be added or replaced
	Conflicts     []string // locally-edited doctrine: left alone, .upstream written for a merge
	DoctrineDiff  string   // unified diff of the Doctrine updates, for review before applying
	Warnings      []string // pool-wide issues outside the pool (e.g. global harness config)
	// Skipped notes the harnesses the pool would wire that are not installed
	// on this host. Informational: not a warning, so it never holds up a
	// headless upgrade.
	Skipped []string
	Agents  []AgentUpgradePlan
}

// Pending reports whether the upgrade would change anything.
func (p PoolUpgradePlan) Pending() bool {
	if len(p.Pool) > 0 || len(p.Doctrine) > 0 || len(p.Conflicts) > 0 {
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
	var poolWarnings []string
	plan.Pool, poolWarnings = PlanPoolDir(poolRoot)
	plan.Warnings = append(plan.Warnings, poolWarnings...)
	var err error
	if plan.Doctrine, plan.Conflicts, err = planDoctrine(poolRoot); err != nil {
		return plan, err
	}
	if len(plan.Doctrine) > 0 {
		if plan.DoctrineDiff, err = doctrineDiff(poolRoot); err != nil {
			return plan, err
		}
	}
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return plan, err
	}
	installed := installedHarnessesFn()
	usesOpencode := false
	skipped := map[string]bool{}
	for _, a := range agents {
		ap := planAgentOn(a, now, installed)
		for _, id := range append(WiredHarnesses(a.Dir()), a.HarnessName()) {
			usesOpencode = usesOpencode || id == HarnessOpencode
		}
		for _, id := range ap.AddHarnesses {
			usesOpencode = usesOpencode || id == HarnessOpencode
		}
		if !installedHas(installed, a.HarnessName()) {
			skipped[a.HarnessName()] = true
		}
		plan.Agents = append(plan.Agents, ap)
	}
	// A harness the pool would wire but that is not installed is skipped;
	// say so once. A pool that never wants it lists the others in
	// `harnesses:` and the note goes away.
	allowAll, allowed := poolAllowsAll(poolRoot), map[string]bool{}
	for _, id := range PoolHarnessAllowlist(poolRoot) {
		allowed[id] = true
	}
	for _, s := range RegisteredHarnesses() {
		if !installedHas(installed, s.ID) && (allowAll || allowed[s.ID] || skipped[s.ID]) {
			plan.Skipped = append(plan.Skipped, s.Label+" is not installed on this host — skipped: its wiring is neither added nor refreshed until it is installed")
		}
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
	dest := DoctrinePath(poolRoot)
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
		rel := poolRel(poolRoot, DoctrineDirName, e.Name())
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

// doctrineDiff renders, as one unified diff, what the upgrade would change in
// each doctrine file it may replace (missing, or unchanged since meshctl wrote
// it), so the human reviews the doctrine before `pool upgrade` commits it.
// Locally-edited files are conflicts, not updates, and are
// left out. Nothing in the pool is written: both sides are copied to a
// temporary directory and compared there.
func doctrineDiff(poolRoot string) (string, error) {
	dest := DoctrinePath(poolRoot)
	stamps := readDoctrineStamps(dest)
	entries, err := templatesFS.ReadDir("templates/doctrine")
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "meshctl-doctrine-diff-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		want, err := templatesFS.ReadFile("templates/doctrine/" + e.Name())
		if err != nil {
			return "", err
		}
		have, err := os.ReadFile(filepath.Join(dest, e.Name()))
		switch {
		case err != nil && !os.IsNotExist(err):
			return "", err
		case err == nil && (sha(have) == sha(want) || stamps[e.Name()] != sha(have)):
			continue // current, or a local edit (a conflict, never replaced)
		}
		rel := filepath.ToSlash(poolRel(poolRoot, DoctrineDirName, e.Name()))
		for side, body := range map[string][]byte{"a": have, "b": want} {
			p := filepath.Join(tmp, side, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(p, body, 0o644); err != nil {
				return "", err
			}
		}
		// --no-index exits 1 when the files differ; the output is the diff.
		cmd := exec.Command("git", "diff", "--no-index", "--no-color", "--no-ext-diff", "--no-prefix", "--",
			filepath.Join("a", filepath.FromSlash(rel)), filepath.Join("b", filepath.FromSlash(rel)))
		cmd.Dir = tmp
		out, err := cmd.Output()
		if err != nil && len(out) == 0 {
			return "", fmt.Errorf("diff %s: %v", rel, err)
		}
		b.Write(out)
	}
	return b.String(), nil
}

// harnessUpgradePlanners preview, read-only, what one harness's WireAgent
// would change in a workspace, for the variant it would wire. A harness
// without an entry is planned as a whole ("wire for X") when unwired.
var harnessUpgradePlanners = map[string]func(dir string, variant int) (actions, warnings []string){
	HarnessClaude:   planClaudeWiring,
	HarnessOpencode: planOpencodeWiring,
}

func planClaudeWiring(dir string, _ int) (actions, warnings []string) {
	if v := WiringVersion(dir); v < MeshWiringVersion {
		actions = append(actions, fmt.Sprintf("refresh .claude/settings.json mesh hooks (v%d → v%d)", v, MeshWiringVersion))
	}
	if !hasStatusLine(dir) {
		actions = append(actions, "add the mesh status line to .claude/settings.json")
	}
	if poolRoot := filepath.Dir(dir); IsPool(poolRoot) {
		if miss := MissingAdditionalDirs(dir, append([]string{poolRoot}, PeerWorkdirs(poolRoot, dir)...)...); len(miss) > 0 {
			actions = append(actions, "add "+strings.Join(miss, ", ")+" to .claude/settings.local.json additionalDirectories (in-session peers)")
		}
	}
	return actions, warnings
}

func planOpencodeWiring(dir string, major int) (actions, warnings []string) {
	if major <= 0 {
		major = opencodeExpectedMajor(dir)
	}
	want, wantRel, wantJS, staleRel, staleJS := 1, opencodePluginV1Rel, opencodePluginJS, opencodePluginV2Rel, opencodePluginV2JS
	if major >= 2 {
		want, wantRel, wantJS, staleRel, staleJS = 2, opencodePluginV2Rel, opencodePluginV2JS, opencodePluginV1Rel, opencodePluginJS
	}
	switch opencodePluginState(dir, wantRel, wantJS) {
	case pluginAbsent:
		actions = append(actions, fmt.Sprintf("write opencode V%d plugin %s", want, wantRel))
	case pluginStale:
		actions = append(actions, "update "+wantRel)
	case pluginForeign:
		warnings = append(warnings, wantRel+" is not meshctl's — left alone; mesh hooks/gate unverified")
	}
	if managed(opencodePluginState(dir, staleRel, staleJS)) {
		actions = append(actions, "remove "+staleRel+" (other opencode major)")
	}
	if major < 2 {
		warnings = append(warnings, "opencode V1: no pre-tool hook, so the mesh gate stays unavailable until opencode V2")
		return actions, warnings
	}
	switch opencodePluginState(dir, opencodeTUIRel, opencodeTUIPlugin) {
	case pluginAbsent:
		actions = append(actions, "add the mesh status panel "+opencodeTUIRel)
	case pluginStale:
		actions = append(actions, "update "+opencodeTUIRel)
	}
	if !opencodeAskWired(dir, false) {
		actions = append(actions, "add opencode ask rules for promotions to opencode.json")
	}
	if miss := missingSubagentAsk(dir, false); len(miss) > 0 {
		actions = append(actions, "add opencode subagent ask rules to opencode.json: "+strings.Join(miss, ", "))
	}
	return actions, warnings
}

func planAgent(a *Agent, now time.Time) AgentUpgradePlan {
	return planAgentOn(a, now, installedHarnessesFn())
}

// planAgentOn plans one agent against the host's installed harnesses: the
// wiring of every harness in its wire set (added when the workspace is not
// wired for it yet, refreshed when it is), then the harness-neutral
// scaffolding. Mirrors what Onboard applies.
func planAgentOn(a *Agent, now time.Time, installed []InstalledHarness) AgentUpgradePlan {
	p := AgentUpgradePlan{Name: a.Name, Dir: a.Dir(), Harness: a.HarnessName()}
	dir := a.Dir()
	poolRoot := filepath.Dir(dir)

	// Mesh-owned skills (regenerated from the embedded templates).
	skills := []string{"mesh-cognition"}
	if isSteward(dir) {
		skills = append(skills, "mesh-steward")
	}

	// Harness wiring, per harness in the wire set.
	targets, _ := wireTargetsFrom(poolRoot, a.HarnessName(), 0, installed)
	wired := map[string]bool{}
	for _, id := range WiredHarnesses(dir) {
		wired[id] = true
	}
	linking := map[string]bool{} // skills some harness links (which renders the pool copy)
	for _, t := range targets {
		var acts, warns []string
		if plan := harnessUpgradePlanners[t.ID]; plan != nil {
			acts, warns = plan(dir, t.Harness.Variant())
		}
		for _, s := range skills {
			if !skillLinked(poolRoot, dir, s, t.Harness) {
				linking[s] = true
				acts = append(acts, "link skill "+s+" to the pool's copy ("+PoolDirName(poolRoot)+"/"+PoolSkillsDirName+"/"+s+")")
			}
		}
		tag := ""
		if len(targets) > 1 {
			tag = "[" + harnessIDLabel(t.ID) + "] "
		}
		if !wired[t.ID] {
			p.AddHarnesses = append(p.AddHarnesses, t.ID)
			p.Actions = append(p.Actions, "wire for "+harnessIDLabel(t.ID)+" (installed on this host; not wired yet)")
		} else if len(acts) > 0 {
			p.RefreshHarnesses = append(p.RefreshHarnesses, t.ID)
		}
		for _, x := range acts {
			p.Actions = append(p.Actions, tag+x)
		}
		for _, x := range warns {
			p.Warnings = append(p.Warnings, tag+x)
		}
	}
	// In-session peer subagents are derived from the roster; a roster
	// change or a template change makes them stale.
	p.Actions = append(p.Actions, PlanPeerAgents(poolRoot, dir).Actions()...)

	// The pool's rendered copy of each mesh skill, shared by every harness.
	for _, s := range skills {
		want, err := renderedSkill(poolRoot, s)
		if err != nil || linking[s] {
			continue
		}
		if have, err := os.ReadFile(filepath.Join(PoolSkillPath(poolRoot, s), "SKILL.md")); err != nil || string(have) != want {
			p.Actions = append(p.Actions, "refresh skill "+s)
		}
	}

	// Workspace scaffolding onboard adds when missing (add-only).
	if b, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); !strings.Contains(string(b), gitignoreMarker) {
		p.Actions = append(p.Actions, "add meshctl-managed block to .gitignore (.memory/, intake traffic, "+ClaudeLocalFile+", mesh skill links)")
	} else if miss := missingManagedIgnores(string(b)); len(miss) > 0 {
		p.Actions = append(p.Actions, "add to the .gitignore managed block: "+strings.Join(miss, ", "))
	}
	if !dirExists(filepath.Join(dir, IntakeDir)) {
		p.Actions = append(p.Actions, "create "+IntakeDir+"/")
	} else if !fileExists(filepath.Join(dir, IntakeDir, "README.md")) {
		p.Actions = append(p.Actions, "recreate "+IntakeDir+"/README.md")
	}
	for _, area := range []string{"playbooks", "experience", "candidates", "reference"} {
		if !fileExists(filepath.Join(dir, KnowledgeDir, area, "README.md")) {
			p.Actions = append(p.Actions, "add "+KnowledgeDir+"/"+area+"/README.md")
		}
	}

	if rm := planITermRetire(dir); len(rm) > 0 {
		p.Actions = append(p.Actions, "remove the retired iTerm2 profile files: "+strings.Join(rm, ", "))
	}

	// Standing orders: moved out of CLAUDE.md, (re)written in CLAUDE.local.md.
	so := planStandingOrders(dir, filepath.Dir(dir))
	if so.movedOut {
		p.Actions = append(p.Actions, "move the mesh standing orders out of CLAUDE.md into "+ClaudeLocalFile)
	}
	if so.wroteLocal && so.freshLocal {
		p.Actions = append(p.Actions, "write the mesh standing orders into "+ClaudeLocalFile)
	} else if so.wroteLocal {
		p.Actions = append(p.Actions, "refresh the mesh standing orders in "+ClaudeLocalFile)
	}
	p.Warnings = append(p.Warnings, so.kept...)

	// Things the upgrade does not fix but the user must know before running it.
	if n := len(liveSessions(dir, now)); n > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d live session(s) — close them before upgrading and restart after", n))
	}
	if dirty := gitDirty(dir); dirty != "" {
		p.Warnings = append(p.Warnings, "uncommitted changes ("+dirty+") — the upgrade commits only the files it writes; one of these it also changes stays uncommitted, so commit first")
	}
	return p
}

// gitDirty returns a short summary of uncommitted changes, or "" if clean or
// not a repo.
func gitDirty(dir string) string {
	if !isGitRepo(dir) {
		return ""
	}
	lines := gitStatusLines(dir)
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("%d path(s)", len(lines))
}

// UpgradeAgents re-runs onboard for every agent in the pool, which re-wires the
// harness for the installed versions, refreshes mesh-owned skills, adds any
// missing CLAUDE.md stanzas and refreshes the managed Cognition stanza and
// Inbound order, then commits what it wrote in each agent repo (never pushes).
// Never touches agent.yaml or the role body. Returns per-agent notes in
// roster order. version names the meshctl in the commit subject.
func UpgradeAgents(poolRoot, version string) ([]AgentUpgradePlan, error) {
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return nil, err
	}
	var out []AgentUpgradePlan
	for _, a := range agents {
		before := dirtySnapshot(a.Dir())
		res, err := Onboard(poolRoot, a.Dir(), AgentSpec{})
		if err != nil {
			return out, fmt.Errorf("%s: %w", a.Name, err)
		}
		skipped := map[string]bool{}
		for _, n := range res.Skipped {
			skipped[n] = true // reported once for the pool by the plan
		}
		var did []string
		for _, n := range res.Notes {
			if n != "agent.yaml present — left unchanged" && !skipped[n] {
				did = append(did, n)
			}
		}
		if res.AddedIntake {
			did = append(did, "created intake/")
		}
		committed, warnings := commitChangedPaths(a.Dir(), before, UpgradeWiringSubject(version))
		if committed != "" {
			did = append(did, committed)
		}
		out = append(out, AgentUpgradePlan{Name: a.Name, Dir: a.Dir(), Harness: a.HarnessName(), Actions: did, Warnings: warnings})
	}
	return out, nil
}

// UpgradeWiringSubject is the subject of the commit `pool upgrade` makes in
// an agent repo for the wiring it wrote.
func UpgradeWiringSubject(version string) string { return "meshctl: upgrade wiring to " + version }

// dirtySnapshot maps each uncommitted path of a repo (untracked files listed
// one by one) to a hash of its content, "" when it is gone from disk. nil
// when dir is not a repo.
func dirtySnapshot(dir string) map[string]string {
	if !isGitRepo(dir) {
		return nil
	}
	out, err := git(dir, "-c", "core.quotePath=false", "status", "--porcelain", "-uall")
	if err != nil {
		return nil
	}
	snap := map[string]string{}
	for _, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if len(ln) < 4 {
			continue
		}
		p := ln[3:]
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		p = strings.Trim(p, `"`)
		snap[p] = ""
		if b, err := os.ReadFile(filepath.Join(dir, p)); err == nil {
			snap[p] = fmt.Sprintf("%x", sha256.Sum256(b))
		}
	}
	return snap
}

// commitChangedPaths commits, in an agent repo, exactly the paths meshctl
// changed (issue #10): those clean before it and dirty after. A path that was
// already uncommitted and that the upgrade changed again mixes the person's
// edit with meshctl's, so it is left uncommitted and named in a warning.
// Nothing is pushed. Returns the action line ("" when nothing was committed)
// and any warnings.
func commitChangedPaths(dir string, before map[string]string, subject string) (string, []string) {
	if before == nil {
		return "", nil // not a repo
	}
	after := dirtySnapshot(dir)
	var written, mixed []string
	for p, h := range after {
		prev, was := before[p]
		switch {
		case !was:
			written = append(written, p)
		case prev != h:
			mixed = append(mixed, p)
		}
	}
	sort.Strings(written)
	sort.Strings(mixed)
	var warnings []string
	if len(mixed) > 0 {
		warnings = append(warnings, "already uncommitted and changed again by the upgrade, left for you to commit: "+strings.Join(mixed, ", "))
	}
	if len(written) == 0 {
		return "", warnings
	}
	// Anything the person had staged before meshctl stages its own paths.
	// Unknown (nil) on an unborn branch, which takes the pathspec commit.
	var preStaged []string
	if out, err := git(dir, "-c", "core.quotePath=false", "diff", "--cached", "--name-only"); err == nil {
		mine := map[string]bool{}
		for _, p := range written {
			mine[p] = true
		}
		preStaged = []string{}
		for _, p := range strings.Split(strings.TrimSpace(out), "\n") {
			if p != "" && !mine[p] {
				preStaged = append(preStaged, p)
			}
		}
	}
	// A tracked file the upgrade replaced with a symlinked parent (issue #23:
	// .opencode/skills/mesh-cognition/SKILL.md, now under a link into the
	// pool) cannot be named to `git add` — git rejects the whole pathspec as
	// "beyond a symbolic link". Stage its removal from the index instead.
	var add, rm, behindLink []string
	isBehindLink := map[string]bool{}
	for _, p := range written {
		switch {
		case beyondSymlink(dir, p):
			rm = append(rm, p)
			behindLink = append(behindLink, p)
			isBehindLink[p] = true
		case !pathExists(filepath.Join(dir, p)):
			rm = append(rm, p)
		default:
			add = append(add, p)
		}
	}
	if len(add) > 0 {
		if _, err := git(dir, append([]string{"add", "-A", "--"}, add...)...); err != nil {
			return "", append(warnings, "could not stage the upgrade's files: "+err.Error())
		}
	}
	if len(rm) > 0 {
		if _, err := git(dir, append([]string{"rm", "-q", "-r", "--cached", "--ignore-unmatch", "--"}, rm...)...); err != nil {
			return "", append(warnings, "could not stage the upgrade's removals: "+err.Error())
		}
	}
	if preStaged != nil && len(preStaged) == 0 {
		// The index holds only meshctl's paths: commit it as staged. A
		// pathspec commit would re-read each named path from disk, and for a
		// path behind a symlink git follows the link and commits the target's
		// content in place of the removal.
		if _, err := git(dir, "commit", "-q", "-m", subject); err != nil {
			return "", append(warnings, "could not commit the upgrade's files (left staged): "+err.Error())
		}
		return fmt.Sprintf("committed %d path(s): %s", len(written), subject), warnings
	}
	// The person has their own staged work: commit only meshctl's paths and
	// leave theirs staged. Paths behind a symlink cannot go through a
	// pathspec commit (see above), so they stay staged for the person.
	only := add
	for _, p := range rm {
		if !isBehindLink[p] {
			only = append(only, p)
		}
	}
	if len(behindLink) > 0 {
		warnings = append(warnings, "staged but not committed, because other changes were already staged (commit them together): removal of "+strings.Join(behindLink, ", "))
	}
	if len(only) == 0 {
		return "", warnings
	}
	sort.Strings(only)
	if _, err := git(dir, append([]string{"commit", "-q", "-m", subject, "--"}, only...)...); err != nil {
		return "", append(warnings, "could not commit the upgrade's files (left staged): "+err.Error())
	}
	return fmt.Sprintf("committed %d path(s): %s", len(only), subject), warnings
}

// beyondSymlink reports whether the repo-relative path p sits under a
// directory that is now a symbolic link, so git refuses it in a pathspec.
func beyondSymlink(dir, p string) bool {
	parts := strings.Split(filepath.ToSlash(p), "/")
	cur := dir
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return false
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// pathExists reports whether p exists, without following a final symlink.
func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
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
