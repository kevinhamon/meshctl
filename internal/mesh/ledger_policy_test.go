package mesh

import (
	"encoding/json"
	"strings"
	"testing"
)

// Policy confirmations: a ledger line with by: policy reads as
// confirmed·policy while its hash matches, ranks below human confirmations and
// above unconfirmed knowledge, and a human kb confirm makes it a human one.

// policyKB records rels as confirmed by a grant at their current content.
func policyKB(t *testing.T, a *Agent, grant string, rels ...string) {
	t.Helper()
	l, err := ReadLedger(a.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if l.Entries == nil {
		l.Entries = map[string]LedgerEntry{}
	}
	for _, rel := range rels {
		e, err := CurrentLedgerEntry(a.Dir(), rel)
		if err != nil {
			t.Fatal(err)
		}
		e.Confirmed, e.Session, e.By, e.Grant = "2026-10-18", "headless", LedgerByPolicy, grant
		l.Entries[rel] = e
	}
	if err := WriteLedger(a.Dir(), l); err != nil {
		t.Fatal(err)
	}
}

func TestLedger_PolicyEntryRoundTripsByAndGrant(t *testing.T) {
	l := Ledger{Entries: map[string]LedgerEntry{
		"reference/a.md": {Hash: "sha256:aa", Confirmed: "2026-10-18", By: LedgerByPolicy, Grant: "kb-promote"},
		"reference/b.md": {Hash: "sha256:bb", Confirmed: "2026-10-18"},
	}}
	out := string(RenderLedger(l))
	if !strings.Contains(out, "  by: policy\n  grant: kb-promote\n") || strings.Count(out, "by:") != 1 {
		t.Fatalf("render:\n%s", out)
	}
	back, err := ParseLedger([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if e := back.Entries["reference/a.md"]; e.By != LedgerByPolicy || e.Grant != "kb-promote" {
		t.Fatalf("round trip: %+v", e)
	}
	if e := back.Entries["reference/b.md"]; e.By != "" || e.Grant != "" {
		t.Fatalf("human line gained a confirmer: %+v", e)
	}
}

func TestLedger_MixedHumanPolicyStaleStatuses(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "catalog-expert", true, nil)
	writeKB(t, a, "reference/human.md", "# Human\n")
	writeKB(t, a, "reference/policy.md", "# Policy\n")
	writeKB(t, a, "reference/edited.md", "# Edited\n")
	writeKB(t, a, "reference/none.md", "# None\n")
	writeKB(t, a, StateFile, "## Now\n\nShipping.\n\n## Next\n\nRelease.\n")
	confirmKB(t, a, "reference/human.md")
	policyKB(t, a, "kb-promote", "reference/policy.md", "reference/edited.md", StateFile)
	writeKB(t, a, "reference/edited.md", "# Edited\n\nAfter the grant.\n")
	writeKB(t, a, StateFile, "## Now\n\nShipping.\n\n## Next\n\nChanged.\n")

	l, err := ReadLedger(a.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]Confirmation{
		"reference/human.md":  ConfirmHuman,
		"reference/policy.md": ConfirmPolicy,
		"reference/edited.md": ConfirmNone, // an edit unconfirms a policy line as it does a human one
		"reference/none.md":   ConfirmNone,
		StateFile:             ConfirmNone, // one section edited ⇒ the whole file is not confirmed
	} {
		if got := l.Confirmation(a.Dir(), rel); got != want {
			t.Errorf("%s: %v, want %v", rel, got, want)
		}
	}
	if got := l.SectionConfirmation(a.Dir(), StateFile, "now"); got != ConfirmPolicy {
		t.Errorf("state.md#now: %v", got)
	}
	if got := l.SectionConfirmation(a.Dir(), StateFile, "next"); got != ConfirmNone {
		t.Errorf("state.md#next: %v", got)
	}
	if !l.Confirmed(a.Dir(), "reference/policy.md") {
		t.Error("a policy line is still a confirmation")
	}
	if un := UnconfirmedCurrentEntries(a.Dir()); strings.Contains(strings.Join(un, ","), "reference/policy.md") {
		t.Errorf("policy-confirmed entries are not unconfirmed: %v", un)
	}
	if ConfirmPolicy.String() != "confirmed·policy" || ConfirmHuman.String() != "confirmed" || ConfirmNone.String() != "unconfirmed" {
		t.Error("render strings")
	}
}

func TestRecall_PolicyRanksBetweenHumanAndUnconfirmed(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "catalog-expert", true, nil)
	// One matching token each, no headings, no cross-mentions: only the tier
	// and the confirmer differ.
	writeKB(t, a, "architecture/circulation-alpha.md", "a\n")   // human current
	writeKB(t, a, "decisions/circulation-beta.md", "b\n")       // human log
	writeKB(t, a, "architecture/circulation-gamma.md", "c\n")   // policy current
	writeKB(t, a, "decisions/circulation-delta.md", "d\n")      // policy log
	writeKB(t, a, "architecture/circulation-epsilon.md", "e\n") // unconfirmed
	writeKB(t, a, "candidates/circulation-zeta.md", "z\n")      // inbox
	confirmKB(t, a, "architecture/circulation-alpha.md", "decisions/circulation-beta.md")
	policyKB(t, a, "kb-promote", "architecture/circulation-gamma.md", "decisions/circulation-delta.md")

	hits, err := Recall(a, "circulation", 20)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	for i := 1; i < len(names); i++ {
		hi, lo := kbHit(hits, "circulation-"+names[i-1]), kbHit(hits, "circulation-"+names[i])
		if hi == nil || lo == nil || hi.Score <= lo.Score {
			t.Fatalf("%s must outrank %s: %+v", names[i-1], names[i], hits)
		}
	}
	g := kbHit(hits, "circulation-gamma")
	if !g.Confirmed || g.ConfirmedBy != LedgerByPolicy {
		t.Fatalf("policy hit: %+v", g)
	}
	if h := kbHit(hits, "circulation-alpha"); h.ConfirmedBy != "" {
		t.Fatalf("human hit carries a confirmer: %+v", h)
	}
	md := RecallMarkdown(hits)
	for _, want := range []string{
		"- [kb/current] circulation-alpha",
		"- [kb/current·confirmed·policy] circulation-gamma",
		"- [kb/log·confirmed·policy] circulation-delta",
		"- [kb/current·unconfirmed] circulation-epsilon",
		"- [kb/inbox] circulation-zeta",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	js, _ := json.Marshal(g)
	if !strings.Contains(string(js), `"confirmed":true`) || !strings.Contains(string(js), `"confirmed_by":"policy"`) {
		t.Fatalf("json: %s", js)
	}

	// A policy hit inlines as knowledge, not a hypothesis; an edit unconfirms it.
	hits, _ = RecallWithBodies(a, "circulation gamma", 20, 200)
	if g := kbHit(hits, "circulation-gamma"); g == nil || strings.Contains(g.Body, "_hypothesis_") {
		t.Fatalf("policy body: %+v", g)
	}
	writeKB(t, a, "architecture/circulation-gamma.md", "c, edited\n")
	hits, _ = Recall(a, "circulation", 20)
	if g := kbHit(hits, "circulation-gamma"); g.Confirmed || g.ConfirmedBy != "" {
		t.Fatalf("edited policy entry: %+v", g)
	}
}

func TestPeerHitBefore_HumanPolicyUnconfirmed(t *testing.T) {
	human := RecallHit{Confirmed: true, Score: 1, Where: []string{"a"}}
	policy := RecallHit{Confirmed: true, ConfirmedBy: LedgerByPolicy, Score: 50, Where: []string{"b"}}
	none := RecallHit{Score: 99, Where: []string{"c"}}
	if !peerHitBefore(human, policy) || !peerHitBefore(policy, none) || peerHitBefore(none, human) {
		t.Fatal("cross-peer order is human, policy, unconfirmed regardless of score")
	}
}

func TestKBConfirm_HumanConfirmUpgradesPolicyEntry(t *testing.T) {
	dir, a := confirmFixture(t)
	rep := writeConfirmReport(t, a, confirmReport)
	policyKB(t, a, "kb-promote", "reference/a.md", PeerGlossaryFile)
	if l, _ := ReadLedger(a.Dir()); l.Confirmation(a.Dir(), "reference/a.md") != ConfirmPolicy {
		t.Fatal("fixture: reference/a.md should start policy-confirmed")
	}

	if _, err := confirmPaths(dir, a, rep, "reference/a.md", "glossary.md#circulation-window"); err != nil {
		t.Fatalf("kb confirm on policy-confirmed entries: %v", err)
	}
	l := ledgerOnMain(t, a)
	if e := l.Entries["reference/a.md"]; e.By != "" || e.Grant != "" || e.Session != "sess-1" {
		t.Fatalf("human confirm must clear by/grant: %+v", e)
	}
	if got := l.Confirmation(a.Dir(), "reference/a.md"); got != ConfirmHuman {
		t.Fatalf("reference/a.md: %v", got)
	}
	ge := l.Entries[PeerGlossaryFile]
	if ge.By != "" || ge.Grant != "" {
		t.Fatalf("glossary line: %+v", ge)
	}
	if got := l.SectionConfirmation(a.Dir(), PeerGlossaryFile, "circulation-window"); got != ConfirmHuman {
		t.Fatalf("named section: %v", got)
	}
	// The section the human did not name was confirmed by a grant only: it is
	// not relabelled as a human confirmation.
	if got := l.SectionConfirmation(a.Dir(), PeerGlossaryFile, "fact-owner"); got != ConfirmNone {
		t.Fatalf("unnamed policy section: %v", got)
	}
}

func TestStateSetConfirmed_WritesHumanEntry(t *testing.T) {
	a := stateFixture(t)
	if err := StateSet(a, stateDigest, finishNow); err != nil {
		t.Fatal(err)
	}
	policyKB(t, a, "kb-promote", StateFile)
	sum := writeSummary(t, a, "sess-1", stateSummary)
	if _, err := StateSetConfirmed(a, stateDigest, StateSetOptions{SummaryFile: sum, Session: "sess-1", Now: finishNow}); err != nil {
		t.Fatalf("state set: %v", err)
	}
	l := ledgerOnMain(t, a)
	if e := l.Entries[StateFile]; e.By != "" || e.Grant != "" {
		t.Fatalf("state set by a human writes a human entry: %+v", e)
	}
	if got := l.Confirmation(a.Dir(), StateFile); got != ConfirmHuman {
		t.Fatalf("state.md: %v", got)
	}
}
