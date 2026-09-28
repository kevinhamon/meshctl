package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	reSupersededBy = regexp.MustCompile(`(?i)superseded by\s+#?0*(\d{1,4})`)
	reLeadingNum   = regexp.MustCompile(`^0*(\d{1,4})`)
	reADRRef       = regexp.MustCompile(`(?i)\bADR[-\s]0*(\d{1,4})\b`)
	reReadmeRow    = regexp.MustCompile(`\(0*(\d{1,4})-[^)]*\.md\)`) // README's own bare links, e.g. (0007-arck.md)
	reStatusLine   = regexp.MustCompile(`(?im)^\s*-?\s*\*\*status:\*\*\s*([a-z]+)`)
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

// fileStatus reads an ADR's `- **Status:** <word>` (first status word, lowercased).
func fileStatus(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if m := reStatusLine.FindStringSubmatch(string(b)); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
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
	byNum := map[string]string{}      // num -> filename
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
		b, err := os.ReadFile(filepath.Join(decisionsDir, e.Name()))
		if err != nil {
			continue
		}
		m := reSupersededBy.FindStringSubmatch(string(b))
		if m == nil {
			continue
		}
		target := m[1]
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

	poolCfg := LoadPoolConfig(agentsDir) // mesh-wide tool refs (ADR-0042)

	// Pool-level: doctrine that instructs agents to run invalid commands.
	findings = append(findings, doctrineFlagFindings(agentsDir)...)

	// Pool-level: legacy (pre-ADR-0038) layout → nudge `pool migrate`.
	if IsPool(agentsDir) && IsLegacyLayout(agentsDir) {
		findings = append(findings, DoctorFinding{Agent: "pool", Level: "warn", Message: "legacy layout detected (top-level agent-mesh/ or .worktrees/.dispatch.*) — run `meshctl pool migrate`"})
	}

	// Badge label + color collisions.
	byLabel := map[string][]string{}
	byColor := map[string][]string{}

	for _, a := range agents {
		for _, p := range a.Validate() {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "error", Message: p})
		}
		byLabel[strings.ToUpper(a.Badge.Label)] = append(byLabel[strings.ToUpper(a.Badge.Label)], a.Name)
		ckey := fmt.Sprintf("%.3f,%.3f,%.3f", a.Badge.R, a.Badge.G, a.Badge.B)
		byColor[ckey] = append(byColor[ckey], a.Name)

		// CLAUDE.md intake section.
		claude := filepath.Join(a.Dir(), "CLAUDE.md")
		if b, err := os.ReadFile(claude); err == nil {
			if !hasCommsSection(string(b)) {
				findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "CLAUDE.md missing the Agent Communication (intake) section — run `meshctl agent onboard " + a.Name + "`"})
			}
		} else {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "warn", Message: "no CLAUDE.md"})
		}

		// Inbox exists?
		if !dirExists(a.InboxPath()) {
			findings = append(findings, DoctorFinding{Agent: a.Name, Level: "error", Message: "inbox " + a.InboxPath() + " does not exist"})
		}

		// Intake statuses outside the canonical vocabulary (warn).
		for _, msg := range InboxStatusFindings(a) {
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
	}

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
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Level != findings[j].Level {
			return findings[i].Level == "error"
		}
		return findings[i].Agent < findings[j].Agent
	})
	return findings, ok
}
