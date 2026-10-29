package mesh

import (
	"fmt"
	"strings"
	"time"
)

// kb consolidate — the KB's debt list.
//
// Distillation adds to the KB; nothing in the loop took anything out or
// folded it into the current tier, so a KB grew as a log: decisions nobody
// landed in architecture docs, candidates nobody judged, a digest nobody
// wrote. Consolidate enumerates that debt in one place, in the order to work
// it off, so a session (or the distiller) can do it mechanically.

// ConsolidateReport is the debt an agent's KB carries.
type ConsolidateReport struct {
	Agent            string   `json:"agent"`
	NoFrontmatter    []string `json:"adr_no_frontmatter,omitempty"` // decisions without machine-readable status
	Unlanded         []string `json:"adr_unlanded,omitempty"`       // accepted ADRs whose `updates:` docs are older
	NotInDigest      []string `json:"adr_not_in_digest,omitempty"`  // accepted ADRs newer than state.md
	StateMissing     bool     `json:"state_missing"`
	StateProblems    []string `json:"state_problems,omitempty"`
	HandoffProblems  []string `json:"handoff_problems,omitempty"`
	AgedCandidates   []string `json:"aged_candidates,omitempty"`
	Uncommitted      []string `json:"uncommitted_candidates,omitempty"` // never dated by git, so never aged
	SupersededBroken []string `json:"superseded_broken,omitempty"`
}

// Clean reports whether there is nothing to consolidate.
func (r ConsolidateReport) Clean() bool {
	return len(r.NoFrontmatter) == 0 && len(r.Unlanded) == 0 && len(r.NotInDigest) == 0 &&
		!r.StateMissing && len(r.StateProblems) == 0 && len(r.HandoffProblems) == 0 &&
		len(r.AgedCandidates) == 0 && len(r.Uncommitted) == 0 && len(r.SupersededBroken) == 0
}

// KBConsolidate computes the debt list for an agent.
func KBConsolidate(agent *Agent, now time.Time) ConsolidateReport {
	r := ConsolidateReport{Agent: agent.Name}
	dir := agent.Dir()
	kb := dir + "/" + KnowledgeDir

	for _, d := range adrNeedsFrontmatter(kb) {
		r.NoFrontmatter = append(r.NoFrontmatter, "decisions/"+d.File)
	}
	for _, debt := range adrLandingDebt(dir) {
		r.Unlanded = append(r.Unlanded, fmt.Sprintf("ADR-%s %s → %s", debt.ADR.Num, debt.ADR.Title, strings.Join(debt.Targets, ", ")))
	}
	r.SupersededBroken = supersededDefects(kb + "/decisions")

	state, _ := StateShow(agent)
	if strings.TrimSpace(state) == "" {
		r.StateMissing = true
	} else {
		r.StateProblems = StateLint(state)
	}
	for _, d := range stateDigestDebt(dir) {
		r.NotInDigest = append(r.NotInDigest, fmt.Sprintf("ADR-%s %s (%s)", d.Num, d.Title, firstNonEmpty(d.Date, "undated")))
	}

	if h, _ := HandoffShow(agent); strings.TrimSpace(h) != "" {
		r.HandoffProblems = HandoffLint(h)
	}
	r.AgedCandidates = agedCandidates(dir, now)
	r.Uncommitted = uncommittedCandidates(dir)
	return r
}

// Markdown renders the report as the worklist a session follows.
func (r ConsolidateReport) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# KB consolidation — %s\n\n", r.Agent)
	if r.Clean() {
		b.WriteString("nothing to consolidate ✓ (decisions landed, digest current, no candidate uncommitted or older than 14 days)\n")
		return b.String()
	}
	section := func(title, hint string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s (%d)\n", title, len(items))
		for _, it := range items {
			b.WriteString("- " + it + "\n")
		}
		if hint != "" {
			b.WriteString("  → " + hint + "\n")
		}
		b.WriteString("\n")
	}
	// One fix covers every file; list the count, not the files.
	if n := len(r.NoFrontmatter); n > 0 {
		fmt.Fprintf(&b, "## Decisions without machine-readable status (%d)\n- %s\n  → `meshctl doctor --fix` adds the frontmatter from each Status bullet\n\n", n, groupList(r.NoFrontmatter, 3))
	}
	section("Superseded links broken", "fix the `superseded_by:` / back-reference so history stays traceable", r.SupersededBroken)
	section("Accepted decisions not landed in the docs they update", "rewrite each target doc to reflect the decision (then it is newer than the ADR)", r.Unlanded)
	if r.StateMissing {
		b.WriteString("## No state digest\n- knowledge/state.md does not exist\n  → write it: `meshctl state template`, fill it in, `meshctl state set --summary-file <summary> <<'EOF' … EOF` (a promotion: write the summary and read the round first)\n\n")
	}
	section("State digest problems", "rewrite with `meshctl state set --summary-file <summary>` (it refuses a digest that fails lint)", r.StateProblems)
	section("Accepted decisions the state digest predates", "fold each into state.md (Now / Recently decided), then `meshctl state set --summary-file <summary>`", r.NotInDigest)
	section("Candidates older than 14 days", "judge each: promote into playbooks/experience/a decision, or delete it", r.AgedCandidates)
	section("Uncommitted candidates", "judge each now, or commit it so it is dated and ages like the rest", r.Uncommitted)
	section("Handoff problems", "rewrite with `meshctl handoff set` — template sections, ≤40 lines", r.HandoffProblems)
	b.WriteString("Work it off in a KB worktree: `meshctl kb begin` → edit → commit → summary + confirmation round → `meshctl kb finish --summary-file <summary>`. Digest and handoff go through `meshctl state set --summary-file <summary>` / `meshctl handoff set`.\n")
	return b.String()
}
