package mesh

import (
	"os"
	"strings"
	"testing"
)

// Recall marks what the human has not confirmed:
// a current or log hit whose hash mismatches the ledger renders
// `[kb/<tier>·unconfirmed]`, ranks below confirmed log and above inbox, and
// inlines its body prefixed `_hypothesis_`. No ledger ⇒ everything unconfirmed.

func kbHit(hits []RecallHit, title string) *RecallHit {
	for i := range hits {
		if hits[i].Kind == "kb" && hits[i].Via == "" && hits[i].Title == title {
			return &hits[i]
		}
	}
	return nil
}

func TestRecall_LedgerMatchRendersConfirmedEditRendersHypothesis(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "catalog-expert", true, nil)
	body := "# Circulation window\n\nDaily since 0002.\n"
	writeKB(t, a, "architecture/circulation.md", body)
	// A hand-written ledger, as a human or the write side would leave it.
	ledger := "architecture/circulation.md:\n  hash: " + HashBytes([]byte(body)) + "\n  confirmed: 2026-10-16\n  session: f93b9f92\n"
	if err := os.WriteFile(LedgerPath(a.Dir()), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}

	hits, err := RecallWithBodies(a, "circulation window", 20, 200)
	if err != nil {
		t.Fatal(err)
	}
	h := kbHit(hits, "circulation-window")
	if h == nil || !h.Confirmed || strings.Contains(h.Body, "_hypothesis_") {
		t.Fatalf("matching hash ⇒ confirmed: %+v", hits)
	}
	md := RecallMarkdown(hits)
	if !strings.Contains(md, "- [kb/current] circulation-window") || strings.Contains(md, "unconfirmed") {
		t.Fatalf("confirmed markdown:\n%s", md)
	}

	// Edit after confirmation: unconfirmed by construction.
	writeKB(t, a, "architecture/circulation.md", "# Circulation window\n\nWeekly, actually.\n")
	hits, _ = RecallWithBodies(a, "circulation window", 20, 200)
	h = kbHit(hits, "circulation-window")
	if h == nil || h.Confirmed || !strings.HasPrefix(h.Body, "_hypothesis_\n") || !strings.Contains(h.Body, "Weekly, actually.") {
		t.Fatalf("edited ⇒ unconfirmed hypothesis body: %+v", h)
	}
	md = RecallMarkdown(hits)
	if !strings.Contains(md, "- [kb/current·unconfirmed] circulation-window") || !strings.Contains(md, "  _hypothesis_\n  # Circulation window") {
		t.Fatalf("unconfirmed markdown:\n%s", md)
	}
}

func TestRecall_NoLedgerReadsEverythingUnconfirmed(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "catalog-expert", true, nil)
	writeKB(t, a, "architecture/circulation.md", "# Circulation window\n")
	writeKB(t, a, "decisions/0002-circulation-daily.md", "---\nstatus: accepted\n---\n# 0002. Circulation is daily\n")
	writeKB(t, a, StateFile, "## Circulation\n\nDaily.\n")
	if err := os.Remove(LedgerPath(a.Dir())); err != nil {
		t.Fatal(err)
	}
	hits, err := Recall(a, "circulation", 20)
	if err != nil {
		t.Fatal(err)
	}
	tiers := map[string]bool{}
	for _, h := range hits {
		if h.Kind != "kb" {
			continue
		}
		if h.Confirmed {
			t.Fatalf("no ledger ⇒ unconfirmed: %+v", h)
		}
		tiers[h.Tier] = true
	}
	if !tiers[TierCurrent] || !tiers[TierLog] {
		t.Fatalf("current and log hits expected: %+v", hits)
	}
	md := RecallMarkdown(hits)
	if !strings.Contains(md, "[kb/current·unconfirmed]") || !strings.Contains(md, "[kb/log·unconfirmed]") {
		t.Fatalf("markdown:\n%s", md)
	}
}

func TestRecall_UnconfirmedRanksBelowConfirmedLogAboveInbox(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "catalog-expert", true, nil)
	// One matching token each, no headings, no cross-mentions: only the tier
	// and the ledger differ.
	writeKB(t, a, "architecture/circulation-alpha.md", "a\n")
	writeKB(t, a, "decisions/circulation-beta.md", "b\n")
	writeKB(t, a, "architecture/circulation-gamma.md", "c\n")
	writeKB(t, a, "experience/circulation-delta.md", "d\n")
	writeKB(t, a, "candidates/circulation-epsilon.md", "e\n")
	confirmKB(t, a, "architecture/circulation-alpha.md", "decisions/circulation-beta.md")

	hits, err := Recall(a, "circulation", 20)
	if err != nil {
		t.Fatal(err)
	}
	score := map[string]int{}
	for _, name := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
		h := kbHit(hits, "circulation-"+name)
		if h == nil {
			t.Fatalf("missing circulation-%s: %+v", name, hits)
		}
		score[name] = h.Score
	}
	if !(score["alpha"] > score["beta"] && score["beta"] > score["gamma"] && score["gamma"] > score["epsilon"]) {
		t.Fatalf("confirmed current > confirmed log > unconfirmed > inbox: %+v", score)
	}
	if score["gamma"] != score["delta"] {
		t.Fatalf("unconfirmed current and log share one rank: %+v", score)
	}
	order := []string{}
	for _, h := range hits {
		if h.Kind == "kb" {
			order = append(order, h.Title)
		}
	}
	if order[0] != "circulation-alpha" || order[1] != "circulation-beta" || order[len(order)-1] != "circulation-epsilon" {
		t.Fatalf("rank order: %v", order)
	}
	if md := RecallMarkdown(hits); strings.Contains(md, "[kb/inbox·unconfirmed]") || !strings.Contains(md, "[kb/inbox] circulation-epsilon") {
		t.Fatalf("inbox hits are proposals by tier, not marked unconfirmed:\n%s", md)
	}
}

func TestRecall_StateDigestConfirmedPerSection(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "catalog-expert", true, nil)
	if err := StateSet(a, "## Circulation cadence\n\nDaily.\n\n## Circulation owners\n\nOps.\n", ledgerNow); err != nil {
		t.Fatal(err)
	}
	confirmKB(t, a, StateFile)
	if err := StateSet(a, "## Circulation cadence\n\nWeekly.\n\n## Circulation owners\n\nOps.\n", ledgerNow); err != nil {
		t.Fatal(err)
	}
	hits, _ := Recall(a, "circulation", 20)
	cad, own := kbHit(hits, "circulation-cadence"), kbHit(hits, "circulation-owners")
	if cad == nil || own == nil || cad.Confirmed || !own.Confirmed {
		t.Fatalf("one edited section unconfirms only itself: cadence=%+v owners=%+v", cad, own)
	}
}

func TestRecall_PoolGlossaryConfirmedRegardlessOfLedger(t *testing.T) {
	root := migratedRoot(t)
	commitPoolGlossary(t, root, circulationGlossary)
	a := newTestAgent(t, root, "catalog-expert", true, nil)
	writeKB(t, a, "architecture/circulation.md", "# Circulation window\n")
	// No ledger, then a ledger that names glossary.md with a wrong hash: the
	// pool term stays confirmed by construction either way.
	_ = os.Remove(LedgerPath(a.Dir()))
	for _, ledger := range []string{"", "glossary.md:\n  hash: sha256:00\n"} {
		if ledger != "" {
			if err := os.WriteFile(LedgerPath(a.Dir()), []byte(ledger), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		hits, err := RecallWithBodies(a, "circulation window", 20, 200)
		if err != nil {
			t.Fatal(err)
		}
		pool := poolHits(hits)
		if len(pool) != 1 || !pool[0].Confirmed || strings.Contains(pool[0].Body, "_hypothesis_") {
			t.Fatalf("pool glossary is confirmed by construction: %+v", pool)
		}
		if md := RecallMarkdown(hits); !strings.Contains(md, "[kb/current·via pool] circulation-window") {
			t.Fatalf("markdown:\n%s", md)
		}
	}
}
