package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// doctor_ledger.go — doctor's view of the confirmation ledger. Warnings only,
// never an auto-fix: a ledger line with no matching file or section, an
// accepted ADR that is unconfirmed (ADR status is decision state, not
// confirmation), and the count of unconfirmed current-tier entries.

// UnconfirmedCurrentEntries lists a peer's current-tier entries that do not
// match its ledger, knowledge-relative and sorted: whole files, or
// `path#anchor` per section of a sectioned file. Directory indexes
// (README.md, template.md), the handoff and inactive decisions are not
// entries. No ledger ⇒ every entry.
func UnconfirmedCurrentEntries(agentDir string) []string {
	return unconfirmedCurrent(newLedgerCheck(agentDir))
}

func unconfirmedCurrent(c *ledgerCheck) []string {
	kb := filepath.Join(c.dir, KnowledgeDir)
	inactive := inactiveKBFiles(kb)
	var out []string
	_ = filepath.WalkDir(kb, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		r, _ := filepath.Rel(kb, p)
		rel := filepath.ToSlash(r)
		switch base := filepath.Base(rel); {
		case inactive[rel], rel == HandoffFile, strings.EqualFold(base, "README.md"), base == "template.md":
			return nil
		}
		if kbTier(rel) != TierCurrent {
			return nil
		}
		if LedgerSectioned(rel) {
			for _, s := range c.file(rel).sections {
				if !c.sectionConfirmed(rel, s.Anchor) {
					out = append(out, rel+"#"+s.Anchor)
				}
			}
			return nil
		}
		if !c.confirmed(rel) {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// ledgerFindings is doctor's ledger check for one peer with a knowledge/ tree.
func ledgerFindings(a *Agent) []DoctorFinding {
	var out []DoctorFinding
	warn := func(msg string) { out = append(out, DoctorFinding{Agent: a.Name, Level: "warn", Message: msg}) }
	ledgerRel := KnowledgeDir + "/" + LedgerFile

	l, err := ReadLedger(a.Dir())
	if err != nil {
		warn(fmt.Sprintf("%s is unparseable (%v) — everything reads unconfirmed until it is fixed", ledgerRel, err))
	}
	for _, s := range l.Stale(a.Dir()) {
		warn(fmt.Sprintf("%s lists %s, but no such file or section exists — drop the stale line", ledgerRel, s))
	}

	c := newLedgerCheck(a.Dir())
	for _, d := range LoadADRs(filepath.Join(a.Dir(), KnowledgeDir)) {
		if d.Status == ADRAccepted && !c.confirmed("decisions/"+d.File) {
			warn(fmt.Sprintf("decisions/%s is accepted but unconfirmed (not in %s, or edited since) — ADR status is not confirmation", d.File, ledgerRel))
		}
	}

	if un := unconfirmedCurrent(c); len(un) > 0 {
		noun := "entries"
		if len(un) == 1 {
			noun = "entry"
		}
		msg := fmt.Sprintf("%d unconfirmed current-tier %s: %s — recall renders them as hypotheses until confirmed", len(un), noun, groupList(un, 4))
		if !l.Present && err == nil {
			msg += " (no " + ledgerRel + ")"
		}
		warn(msg)
	}
	return out
}
