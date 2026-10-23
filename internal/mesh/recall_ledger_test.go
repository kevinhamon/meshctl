package mesh

import (
	"os"
	"strings"
	"testing"
)

// Recall marks what the human has not confirmed (ADR-0067, spec decision 28):
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
	a := newTestAgent(t, dir, "grid-expert", true, nil)
	body := "# Settlement window\n\nDaily since 0002.\n"
	writeKB(t, a, "architecture/settlement.md", body)
	// A hand-written ledger, as a human or the write side would leave it.
	ledger := "architecture/settlement.md:\n  hash: " + HashBytes([]byte(body)) + "\n  confirmed: 2026-10-16\n  session: f93b9f92\n"
	if err := os.WriteFile(LedgerPath(a.Dir()), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}

	hits, err := RecallWithBodies(a, "settlement window", 20, 200)
	if err != nil {
		t.Fatal(err)
	}
	h := kbHit(hits, "settlement-window")
	if h == nil || !h.Confirmed || strings.Contains(h.Body, "_hypothesis_") {
		t.Fatalf("matching hash ⇒ confirmed: %+v", hits)
	}
	md := RecallMarkdown(hits)
	if !strings.Contains(md, "- [kb/current] settlement-window") || strings.Contains(md, "unconfirmed") {
		t.Fatalf("confirmed markdown:\n%s", md)
	}

	// Edit after confirmation: unconfirmed by construction.
	writeKB(t, a, "architecture/settlement.md", "# Settlement window\n\nWeekly, actually.\n")
	hits, _ = RecallWithBodies(a, "settlement window", 20, 200)
	h = kbHit(hits, "settlement-window")
	if h == nil || h.Confirmed || !strings.HasPrefix(h.Body, "_hypothesis_\n") || !strings.Contains(h.Body, "Weekly, actually.") {
		t.Fatalf("edited ⇒ unconfirmed hypothesis body: %+v", h)
	}
	md = RecallMarkdown(hits)
	if !strings.Contains(md, "- [kb/current·unconfirmed] settlement-window") || !strings.Contains(md, "  _hypothesis_\n  # Settlement window") {
		t.Fatalf("unconfirmed markdown:\n%s", md)
	}
}

func TestRecall_NoLedgerReadsEverythingUnconfirmed(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "grid-expert", true, nil)
	writeKB(t, a, "architecture/settlement.md", "# Settlement window\n")
	writeKB(t, a, "decisions/0002-settlement-daily.md", "---\nstatus: accepted\n---\n# 0002. Settlement is daily\n")
	writeKB(t, a, StateFile, "## Settlement\n\nDaily.\n")
	if err := os.Remove(LedgerPath(a.Dir())); err != nil {
		t.Fatal(err)
	}
	hits, err := Recall(a, "settlement", 20)
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
	a := newTestAgent(t, dir, "grid-expert", true, nil)
	// One matching token each, no headings, no cross-mentions: only the tier
	// and the ledger differ.
	writeKB(t, a, "architecture/settlement-alpha.md", "a\n")
	writeKB(t, a, "decisions/settlement-beta.md", "b\n")
	writeKB(t, a, "architecture/settlement-gamma.md", "c\n")
	writeKB(t, a, "experience/settlement-delta.md", "d\n")
	writeKB(t, a, "candidates/settlement-epsilon.md", "e\n")
	confirmKB(t, a, "architecture/settlement-alpha.md", "decisions/settlement-beta.md")

	hits, err := Recall(a, "settlement", 20)
	if err != nil {
		t.Fatal(err)
	}
	score := map[string]int{}
	for _, name := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
		h := kbHit(hits, "settlement-"+name)
		if h == nil {
			t.Fatalf("missing settlement-%s: %+v", name, hits)
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
	if order[0] != "settlement-alpha" || order[1] != "settlement-beta" || order[len(order)-1] != "settlement-epsilon" {
		t.Fatalf("rank order: %v", order)
	}
	if md := RecallMarkdown(hits); strings.Contains(md, "[kb/inbox·unconfirmed]") || !strings.Contains(md, "[kb/inbox] settlement-epsilon") {
		t.Fatalf("inbox hits are proposals by tier, not marked unconfirmed:\n%s", md)
	}
}

func TestRecall_StateDigestConfirmedPerSection(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "grid-expert", true, nil)
	if err := StateSet(a, "## Settlement cadence\n\nDaily.\n\n## Settlement owners\n\nOps.\n", ledgerNow); err != nil {
		t.Fatal(err)
	}
	confirmKB(t, a, StateFile)
	if err := StateSet(a, "## Settlement cadence\n\nWeekly.\n\n## Settlement owners\n\nOps.\n", ledgerNow); err != nil {
		t.Fatal(err)
	}
	hits, _ := Recall(a, "settlement", 20)
	cad, own := kbHit(hits, "settlement-cadence"), kbHit(hits, "settlement-owners")
	if cad == nil || own == nil || cad.Confirmed || !own.Confirmed {
		t.Fatalf("one edited section unconfirms only itself: cadence=%+v owners=%+v", cad, own)
	}
}

func TestRecall_PoolGlossaryConfirmedRegardlessOfLedger(t *testing.T) {
	root := migratedRoot(t)
	commitPoolGlossary(t, root, settlementGlossary)
	a := newTestAgent(t, root, "grid-expert", true, nil)
	writeKB(t, a, "architecture/settlement.md", "# Settlement window\n")
	// No ledger, then a ledger that names glossary.md with a wrong hash: the
	// pool term stays confirmed by construction either way.
	_ = os.Remove(LedgerPath(a.Dir()))
	for _, ledger := range []string{"", "glossary.md:\n  hash: sha256:00\n"} {
		if ledger != "" {
			if err := os.WriteFile(LedgerPath(a.Dir()), []byte(ledger), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		hits, err := RecallWithBodies(a, "settlement window", 20, 200)
		if err != nil {
			t.Fatal(err)
		}
		pool := poolHits(hits)
		if len(pool) != 1 || !pool[0].Confirmed || strings.Contains(pool[0].Body, "_hypothesis_") {
			t.Fatalf("pool glossary is confirmed by construction: %+v", pool)
		}
		if md := RecallMarkdown(hits); !strings.Contains(md, "[kb/current·via pool] settlement-window") {
			t.Fatalf("markdown:\n%s", md)
		}
	}
}
