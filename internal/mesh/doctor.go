package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	reSupersededBy = regexp.MustCompile(`(?i)superseded by\s+(?:ADR[-\s]?)?#?0*(\d{1,4})`)
	reLeadingNum   = regexp.MustCompile(`^0*(\d{1,4})`)
	reADRRef       = regexp.MustCompile(`(?i)\bADR[-\s]0*(\d{1,4})\b`)
	reReadmeRow    = regexp.MustCompile(`\(0*(\d{1,4})-[^)]*\.md\)`) // README's own bare links, e.g. (0007-arck.md)
	reStatusWord   = regexp.MustCompile(`(?i)\b(accepted|proposed|rejected|deprecated|superseded)\b`)
	rePendingADR   = regexp.MustCompile(`(?i)pending[^.|\n]{0,24}?\badr\b|\badr\b[^.|\n]{0,12}?pending`)
	reEffective    = regexp.MustCompile(`(?im)^\s*effective\s*:`)
)

// referenceMissingEffective warns on reference notes lacking an `effective:` date
// (ADR-0019): external source knowledge without a version/effective date silently
// goes stale. Only checks knowledge/reference/*.md when that (optional) area exists.
func referenceMissingEffective(kbRoot string) []string {
	dir := filepath.Join(kbRoot, "reference")
	if !dirExists(dir) {
		return nil
	}
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if !reEffective.Match(b) {
			out = append(out, "knowledge/reference/"+e.Name()+" has no `effective:` date — reference notes must be dated (ADR-0019); it may be stale")
		}
	}
	return out
}

// fileStatus reads an ADR's status: frontmatter `status:` when present, else
// the `- **Status:** <word>` bullet (bold and trailing notes tolerated).
func fileStatus(path string) string {
	d, err := ParseADR(path)
	if err != nil {
		return ""
	}
	return d.Status
}

// kbConsistencyFindings reports mechanizable ADR/direction drift — all warn-level
// (advisory, never fails the fleet). Checks: decisions/README index ↔ file parity +
// status match, broken ADR cross-references, and stale "pending … ADR" markers in
// direction.md. Only meaningful where an agent keeps numbered ADRs.
func kbConsistencyFindings(agent, kbRoot string) []DoctorFinding {
	decisionsDir := filepath.Join(kbRoot, "decisions")
	if !dirExists(decisionsDir) {
		return nil
	}
	entries, _ := os.ReadDir(decisionsDir)
	byNum := map[string]string{}       // num -> filename
	statusByNum := map[string]string{} // num -> in-file status
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		m := reLeadingNum.FindStringSubmatch(e.Name()) // skips README.md / template.md
		if m == nil {
			continue
		}
		byNum[m[1]] = e.Name()
		statusByNum[m[1]] = fileStatus(filepath.Join(decisionsDir, e.Name()))
	}
	if len(byNum) == 0 {
		return nil
	}

	var out []DoctorFinding
	warn := func(msg string) { out = append(out, DoctorFinding{Agent: agent, Level: "warn", Message: msg}) }

	// README index parity + status match.
	readmeNums := map[string]bool{}
	if b, err := os.ReadFile(filepath.Join(decisionsDir, "README.md")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			m := reReadmeRow.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			num := m[1]
			readmeNums[num] = true
			if _, ok := byNum[num]; !ok {
				warn("decisions/README.md row references ADR " + num + " but no such file exists")
				continue
			}
			if sm := reStatusWord.FindAllString(strings.ToLower(line), -1); len(sm) > 0 {
				rowStatus := strings.ToLower(sm[len(sm)-1]) // status column sits near the row end
				if fs := statusByNum[num]; fs != "" && rowStatus != fs {
					warn(fmt.Sprintf("decisions/README.md lists ADR %s as %q but the file says %q", num, rowStatus, fs))
				}
			}
		}
	}
	for num, fn := range byNum {
		if !readmeNums[num] {
			warn("decisions/" + fn + " has no row in decisions/README.md")
		}
	}

	// Broken ADR cross-refs (decision bodies + direction.md); pending-marker in direction.md only.
	scan := func(label, path string, pendingCheck bool) {
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, m := range reADRRef.FindAllStringSubmatch(string(b), -1) {
			if num := m[1]; !seen[num] {
				seen[num] = true
				if _, ok := byNum[num]; !ok {
					warn(label + " references ADR-" + num + " but no such decision file exists")
				}
			}
		}
		if pendingCheck && rePendingADR.MatchString(string(b)) {
			warn(label + " contains a 'pending … ADR' marker — re-check it's still unresolved (an accepted ADR may have superseded it)")
		}
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || reLeadingNum.FindStringSubmatch(e.Name()) == nil {
			continue
		}
		scan("decisions/"+e.Name(), filepath.Join(decisionsDir, e.Name()), false)
	}
	scan("direction.md", filepath.Join(kbRoot, "direction.md"), true)
	return out
}

// groupList joins up to n items and counts the rest, so a long list is one line.
func groupList(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(", … (+%d more)", len(items)-n)
}

// dirEmptyish is true when a dir has no real entries (README.md/.dotfiles don't count).
func dirEmptyish(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") || strings.EqualFold(n, "README.md") {
			continue
		}
		return false
	}
	return true
}

// supersededDefects finds decisions marked "superseded by NNNN" whose target is
// missing or doesn't back-reference them — the one mechanizable KB defect (ADR-0014).
func supersededDefects(decisionsDir string) []string {
	if !dirExists(decisionsDir) {
		return nil
	}
	entries, _ := os.ReadDir(decisionsDir)
	byNum := map[string]string{} // normalized number -> filename
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if m := reLeadingNum.FindStringSubmatch(e.Name()); m != nil {
			byNum[m[1]] = e.Name()
		}
	}
	var defects []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		d, err := ParseADR(filepath.Join(decisionsDir, e.Name()))
		if err != nil {
			continue
		}
		var target string
		if len(d.SupersededBy) > 0 {
			target = strings.TrimLeft(d.SupersededBy[0], "0")
		} else if b, err := os.ReadFile(filepath.Join(decisionsDir, e.Name())); err == nil {
			if m := reSupersededBy.FindStringSubmatch(string(b)); m != nil {
				target = m[1]
			}
		}
		if target == "" {
			continue
		}
		tf, ok := byNum[target]
		if !ok {
			defects = append(defects, fmt.Sprintf("%s is marked superseded by %s, but no such decision file exists", e.Name(), target))
			continue
		}
		self := ""
		if mm := reLeadingNum.FindStringSubmatch(e.Name()); mm != nil {
			self = mm[1]
		}
		tb, _ := os.ReadFile(filepath.Join(decisionsDir, tf))
		if self != "" && !strings.Contains(string(tb), self) {
			defects = append(defects, fmt.Sprintf("%s superseded by %s, but %s never references it back", e.Name(), tf, tf))
		}
	}
	return defects
}

// DoctorFinding is one validation problem (or warning).
type DoctorFinding struct {
	Agent   string
	Level   string // "error" | "warn"
	Message string
	// Fixable marks a hygiene item `doctor --fix` repairs.
	Fixable bool
	// Action names the console button the finding carries: commit or push
	// for an agent repo (ADR-0079 amendment); "" for none.
	Action string
}

// reservedOwnerFindings errors on a workspace at the pool root whose name is
// the reserved owner `pool` (any case) and which carries an agent manifest: a
// peer with that name would make `mesh://pool/...` ambiguous (ADR-0066, spec
// decision 14). ScanAgents may or may not load it (the pool directory itself
// has no manifest; a stray one would), so this reads the directory, not the
// roster.
// Workspaces in validated (loaded agents' directories) are skipped: their
// manifest validation already reports the reserved name.
func reservedOwnerFindings(agentsDir string, validated map[string]bool) []DoctorFinding {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil
	}
	var out []DoctorFinding
	for _, e := range entries {
		if !e.IsDir() || !strings.EqualFold(e.Name(), ReservedOwner) || validated[e.Name()] {
			continue
		}
		if !fileExists(filepath.Join(agentsDir, e.Name(), ManifestName)) {
			continue
		}
		out = append(out, DoctorFinding{Agent: ReservedOwner, Level: "error",
			Message: "peer " + e.Name() + "/ carries the reserved owner name — `" + ReservedOwner + "` is the pool itself (mesh://" + ReservedOwner + "/...); rename the peer and remove " + e.Name() + "/" + ManifestName})
	}
	return out
}

// Doctor validates every manifest and reports drift. ok is false if any error
// (not warning) was found — callers exit non-zero on !ok.
func Doctor(agentsDir string) (findings []DoctorFinding, ok bool) {
	agents, loadErrs, err := ScanAgents(agentsDir)
	if err != nil {
		return []DoctorFinding{{Agent: "-", Level: "error", Message: "cannot scan agents dir: " + err.Error()}}, false
	}

	for name, e := range loadErrs {
		findings = append(findings, DoctorFinding{Agent: name, Level: "error", Message: "unparseable agent.yaml: " + e.Error()})
	}

	poolCfg := LoadPoolConfig(agentsDir) // identity + human-owned settings (ADR-0066), mesh-wide tool refs (ADR-0042)

	// Pool-level: config file health and the tools-in-identity-file transition.
	findings = append(findings, poolConfigFindings(agentsDir, poolCfg)...)
	// Pool-level: outbound gate config vs the peers' `mutates` tags (ADR-0068).
	findings = append(findings, outboundFindings(agentsDir, poolCfg, agents)...)

	// Pool-level: doctrine that instructs agents to run invalid commands.
	findings = append(findings, doctrineFlagFindings(agentsDir)...)

	// Pool-level: legacy (pre-ADR-0038) layout → nudge `pool migrate`.
	if IsPool(agentsDir) && IsLegacyLayout(agentsDir) {
		findings = append(findings, DoctorFinding{Agent: "pool", Level: "warn", Message: "legacy layout detected (top-level agent-mesh/ or .worktrees/.dispatch.*) — run `meshctl pool migrate`"})
	}
	if IsPool(agentsDir) && PoolDirIsLegacy(agentsDir) {
		findings = append(findings, DoctorFinding{Agent: "pool", Level: "warn", Message: "pool directory is the legacy hidden " + LegacyPoolDir + "/ — run `meshctl pool upgrade` to move it to " + CurrentPoolDir + "/ (a git repo, ADR-0066)"})
	}
	// Pool-level: pool repo nag (ADR-0066) — not a repo, no remote, dirty, or
	// ahead of its remote. Warn, never error: durability is the human's call.
	if nag := PoolRepoNag(agentsDir); nag != "" {
		findings = append(findings, DoctorFinding{Agent: "pool", Level: "warn", Message: nag})
	}

	// Pool-level: a peer carrying the reserved owner name (ADR-0066).
	validated := map[string]bool{}
	for _, a := range agents {
		if a.Dir() != "" && strings.EqualFold(a.Name, ReservedOwner) {
			validated[filepath.Base(a.Dir())] = true
		}
	}
	findings = append(findings, reservedOwnerFindings(agentsDir, validated)...)

	// Pool glossary terms, read once for the per-peer glossary checks.
	poolTerms, poolGlossary := poolGlossaryTermSlugs(agentsDir)

	// Badge label + color collisions.
	byLabel := map[string][]string{}
	byColor := map[string][]string{}

	for _, a := range agents {
		byLabel[strings.ToUpper(a.Badge.Label)] = append(byLabel[strings.ToUpper(a.Badge.Label)], a.Name)
		ckey := fmt.Sprintf("%.3f,%.3f,%.3f", a.Badge.R, a.Badge.G, a.Badge.B)
		byColor[ckey] = append(byColor[ckey], a.Name)
	}
	// Per-peer checks run concurrently, results kept in roster order: each
	// reads only its own peer's files and runs git in its own repo, and git
	// process start-up is most of doctor's time.
	perAgent := make([][]DoctorFinding, len(agents))
	var wg sync.WaitGroup
	sem := make(chan struct{}, doctorParallel)
	for i, a := range agents {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, a *Agent) {
			defer func() { <-sem; wg.Done() }()
			perAgent[i] = doctorAgentFindings(a, poolCfg, poolTerms, poolGlossary)
		}(i, a)
	}
	wg.Wait()
	for _, fs := range perAgent {
		findings = append(findings, fs...)
	}

	// `owns` uniqueness across canonical peers (ADR-0065, ADR-0069).
	findings = append(findings, canonicalOwnsFindings(poolCfg, agents)...)
	// Knowledge references: dangling links, canonical→perspective (ADR-0065).
	findings = append(findings, meshRefFindings(agentsDir, poolCfg, agents)...)

	for label, names := range byLabel {
		if len(names) > 1 {
			sort.Strings(names)
			findings = append(findings, DoctorFinding{Agent: strings.Join(names, ","), Level: "error", Message: "badge label collision: " + label + " used by " + strings.Join(names, ", ")})
		}
	}
	for color, names := range byColor {
		if len(names) > 1 {
			sort.Strings(names)
			findings = append(findings, DoctorFinding{Agent: strings.Join(names, ","), Level: "warn", Message: "badge color collision (" + color + "): " + strings.Join(names, ", ")})
		}
	}

	// Orphaned inboxes: an intake/ dir with no manifest in the same workspace.
	if entries, err := os.ReadDir(agentsDir); err == nil {
		have := map[string]bool{}
		for _, a := range agents {
			have[a.Name] = true
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == MeshRepoName {
				continue
			}
			if have[e.Name()] {
				continue
			}
			if dirExists(filepath.Join(agentsDir, e.Name(), IntakeDir)) {
				findings = append(findings, DoctorFinding{Agent: e.Name(), Level: "warn", Message: "has intake/ but no agent.yaml — run `meshctl agent onboard " + e.Name() + "`"})
			}
		}
	}

	ok = true
	for _, f := range findings {
		if f.Level == "error" {
			ok = false
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Level != findings[j].Level {
			return findings[i].Level == "error"
		}
		return findings[i].Agent < findings[j].Agent
	})
	return findings, ok
}

// doctorParallel bounds how many peers Doctor checks at once.
const doctorParallel = 8

// doctorAgentFindings runs every per-peer check for one agent. It must stay
// safe to run for several peers at once: it reads only this peer's files and
// shared read-only config.
func doctorAgentFindings(a *Agent, poolCfg PoolConfig, poolTerms map[string]bool, poolGlossary bool) (findings []DoctorFinding) {
	for _, p := range a.Validate() {
		findings = append(findings, DoctorFinding{Agent: a.Name, Level: "error", Message: p})
	}

	// CLAUDE.md intake section.
	claude := filepath.Join(a.Dir(), "CLAUDE.md")
	if b, err := os.ReadFile(claude); err == nil {
		if !hasCommsSection(string(b)) {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "CLAUDE.md missing the Agent Communication (intake) section — run `meshctl agent onboard " + a.Name + "`"})
		}
	} else {
		findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "no CLAUDE.md"})
	}

	// Peer glossaries retire (ADR-0066): leftover file, `owns` coupling.
	findings = append(findings, glossaryFindings(a, poolTerms, poolGlossary)...)
	findings = append(findings, docFolderFindings(a)...)

	// Inbox exists?
	if !dirExists(a.InboxPath()) {
		findings = append(findings, DoctorFinding{Agent: a.Name, Level: "error", Message: "inbox " + a.InboxPath() + " does not exist"})
	}

	// Intake statuses outside the canonical vocabulary (warn).
	for _, msg := range InboxStatusFindings(a) {
		findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: msg})
	}
	// Intake triage age (ADR-0072): reports, never closes.
	if msg := TriageDoctorFinding(a, time.Now()); msg != "" {
		findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: msg})
	}

	// Is the role file the harness will actually read? (Not gated on a
	// knowledge/ tree: an agent with no KB still needs its doctrine loaded.)
	findings = append(findings, opencodeInstructionsFindings(a)...)

	// Fail-closed enforcement gate present + current? (Not gated on a
	// knowledge/ tree: EVERY claude agent must enforce doctrine, KB or not.)
	findings = append(findings, gateWiringFindings(a)...)

	// ADR-0014 knowledge-base areas (tiered: warn on missing/empty, never error;
	// a fresh agent legitimately has empty Experience). Only checked if the
	// agent keeps a knowledge/ tree at all.
	kbRoot := filepath.Join(a.Dir(), KnowledgeDir)
	if dirExists(kbRoot) {
		for _, area := range []string{"playbooks", "experience", "candidates"} {
			ap := filepath.Join(kbRoot, area)
			switch {
			case !dirExists(ap):
				findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "knowledge/" + area + "/ missing — run `meshctl agent onboard " + a.Name + "`"})
			case dirEmptyish(ap):
				findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "knowledge/" + area + "/ has no entries yet"})
			}
		}
		// The one structurally-enforceable KB defect (ADR-0014): a decision marked
		// "superseded by NNNN" where NNNN is missing or doesn't back-reference it.
		for _, msg := range supersededDefects(filepath.Join(kbRoot, "decisions")) {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "error", Message: msg})
		}
		// KB consistency drift (README parity, broken ADR refs, stale "pending ADR"
		// markers) — all warn-level, advisory.
		findings = append(findings, kbConsistencyFindings(a.Name, kbRoot)...)
		// ADR-0019: reference notes must carry an `effective:` date (warn).
		for _, msg := range referenceMissingEffective(kbRoot) {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: msg})
		}
		// S1 — the KB's two tiers stay consistent: an accepted decision must
		// land in the state docs it names (error: the doc is stale by the
		// decision's own account); the state digest must not predate accepted
		// decisions (warn); candidates must not age unjudged (warn).
		for _, debt := range adrLandingDebt(a.Dir()) {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "error", Message: fmt.Sprintf(
				"decisions/%s (accepted) updates %s, but that doc is older than the decision — fold the decision in (`meshctl kb consolidate`)",
				debt.ADR.File, strings.Join(debt.Targets, ", "))})
		}
		if dirExists(filepath.Join(kbRoot, "decisions")) || dirExists(filepath.Join(kbRoot, "architecture")) {
			if st, _ := os.ReadFile(filepath.Join(kbRoot, StateFile)); strings.TrimSpace(string(st)) == "" {
				findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "no knowledge/" + StateFile + " digest — sessions start without a current-state view; write one (`meshctl state template`, then `meshctl state set --summary-file <summary>`)"})
			} else {
				if w := StateLint(string(st)); len(w) > 0 {
					findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "knowledge/" + StateFile + ": " + strings.Join(w, "; ") + " — rewrite with `meshctl state set --summary-file <summary>`"})
				}
				if debt := stateDigestDebt(a.Dir()); len(debt) > 0 {
					nums := make([]string, 0, len(debt))
					for _, d := range debt {
						nums = append(nums, "ADR-"+d.Num)
					}
					findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: fmt.Sprintf(
						"knowledge/%s predates %d accepted decision(s): %s — run `meshctl kb consolidate`", StateFile, len(debt), groupList(nums, 6))})
				}
			}
		}
		// ADR-0067: confirmation ledger drift and unconfirmed knowledge.
		findings = append(findings, ledgerFindings(a)...)
		// ADR-0070: the latest reconcile report's age and open conflicts.
		findings = append(findings, reconcileFindings(a, time.Now())...)
		if aged := agedCandidates(a.Dir(), time.Now()); len(aged) > 0 {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: fmt.Sprintf(
				"%d candidate(s) older than 14 days, unconsolidated: %s — judge them (`meshctl kb consolidate`)", len(aged), groupList(aged, 4))})
		}
		if h, _ := HandoffShow(a); strings.TrimSpace(h) != "" {
			if w := HandoffLint(h); len(w) > 0 {
				findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "knowledge/" + HandoffFile + ": " + strings.Join(w, "; ") + " — rewrite with `meshctl handoff set`"})
			}
		}
		// (ADR-0033) The persisted memory index is off the routing path — recall
		// reads live — so its staleness is no longer a doctor warning.
		// ADR-0026 cognition wiring: the mesh-cognition skill + the always-present
		// "## Cognition" standing-order stanza in CLAUDE.md. An agent with a knowledge/
		// tree must have both, else the learn loop has no trigger (warn only).
		// Skill location is harness-specific (ADR-0032): .claude/skills vs .opencode/skills.
		skillDir := ".claude"
		if a.HarnessName() == HarnessOpencode {
			skillDir = ".opencode"
		}
		if !fileExists(filepath.Join(a.Dir(), skillDir, "skills", "mesh-cognition", "SKILL.md")) {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "mesh-cognition skill not installed (" + skillDir + "/skills/mesh-cognition/) — run `meshctl agent onboard " + a.Name + "`"})
		}
		if b, err := os.ReadFile(claude); err == nil && !hasCognitionSection(string(b)) {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "CLAUDE.md missing the Cognition standing-order stanza (ADR-0026) — run `meshctl agent onboard " + a.Name + "`"})
		}
		// Harness wiring present? (ADR-0032)
		if a.HarnessName() == HarnessOpencode && !fileExists(filepath.Join(a.Dir(), "opencode.json")) {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "harness=opencode but no opencode.json — run `meshctl agent onboard " + a.Name + "`"})
		}
		// Will cognition actually fire, and will it have anything to mine?
		// Both fail silently otherwise — see doctor_wiring.go.
		findings = append(findings, cognitionTriggerFindings(a)...)
		findings = append(findings, sessionSignalFindings(a)...)
		// Repo hygiene (ADR-0039): knowledge is only safe once pushed to a remote.
		if h := AgentRepoHygiene(a.Dir()); !h.IsRepo {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "workspace is not a git repo — `git init` + commit (knowledge/ unsafe until versioned + pushed)"})
		} else if !h.HasRemote {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "no git remote — create one and push (knowledge/ lives only on this machine)"})
		} else if msg, action := AgentRepoFinding(a.Dir()); msg != "" {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: msg, Action: action})
		}
		// Tool reference (ADR-0042): referenced tools should be installed; an
		// `avoid` tool being present is a nudge, not an error.
		for _, tr := range EffectiveTools(poolCfg, a) {
			if tr.Avoid != "" {
				if ToolOnPath(tr.Cmd) {
					findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "`" + tr.Cmd + "` is on PATH but this mesh says avoid it — " + tr.Avoid})
				}
				continue
			}
			if !ToolOnPath(tr.Cmd) {
				findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "referenced tool `" + tr.Cmd + "` not found on $PATH (install it or fix the tool reference)"})
			}
		}
	}
	return findings
}
