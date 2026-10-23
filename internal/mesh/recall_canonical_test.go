package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Canonical recall scope and one-hop references (ADR-0065, ADR-0069; spec
// decisions 30 and 33). Fixture: a three-peer pool, grid and market canonical,
// critic perspective.

type canonicalPool struct {
	root                 string
	grid, market, critic *Agent
}

func newCanonicalPool(t *testing.T) canonicalPool {
	t.Helper()
	root := migratedRoot(t)
	grid := newTestAgent(t, root, "grid", true, nil) // dispatchable: the brief test asks it in-session
	market := newTestAgent(t, root, "market", false, nil)
	newTestAgent(t, root, "critic", false, nil)
	critic := setManifestKnowledge(t, root, "critic", KnowledgePerspective)
	return canonicalPool{root: root, grid: grid, market: market, critic: critic}
}

func viaHits(hits []RecallHit, via string) []RecallHit {
	var out []RecallHit
	for _, h := range hits {
		if h.Via == via {
			out = append(out, h)
		}
	}
	return out
}

func TestRecallCanonical_ReadsCanonicalPeersCurrentAndLogOnly(t *testing.T) {
	p := newCanonicalPool(t)
	writeKB(t, p.grid, "architecture/settlement.md", "# Settlement window\n\nGrid view: daily.\n")
	writeKB(t, p.market, "architecture/settlement-rules.md", "# Settlement window rules\n\nMarket: daily netting.\n")
	writeKB(t, p.market, "decisions/0003-settlement-window-daily.md", "---\nstatus: accepted\n---\n# 0003. Settlement window is daily\n")
	writeKB(t, p.market, "candidates/settlement-window-weekly.md", "# Settlement window weekly?\n")
	writeKB(t, p.critic, "architecture/settlement-critique.md", "# Settlement window critique\n\nIt should be hourly.\n")
	// Session records and the git spine of another peer are never read.
	os.MkdirAll(filepath.Join(p.market.Dir(), ".memory", "sessions"), 0o755)
	os.WriteFile(filepath.Join(p.market.Dir(), ".memory", "sessions", "s1.json"),
		[]byte(`{"id":"s1","agent":"market","first_prompt":"settlement window chat","ended":"2026-10-01T00:00:00Z"}`), 0o644)

	hits, err := RecallScoped(p.grid, "settlement window", RecallScopeCanonical, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(viaHits(hits, "")) == 0 {
		t.Fatalf("own hits must still be returned: %+v", hits)
	}
	market := viaHits(hits, "market")
	if len(market) == 0 {
		t.Fatalf("expected hits via market: %+v", hits)
	}
	tiers := map[string]bool{}
	for _, h := range market {
		if h.Kind != "kb" {
			t.Fatalf("peer hits are kb only (no sessions, no commits): %+v", h)
		}
		tiers[h.Tier] = true
		for _, w := range h.Where {
			if !strings.HasPrefix(w, "mesh://market/") || strings.Contains(w, "candidates/") {
				t.Fatalf("peer hit must point at market's current/log tiers by reference: %+v", h)
			}
		}
	}
	if !tiers[TierCurrent] || !tiers[TierLog] || tiers[TierInbox] {
		t.Fatalf("expected current and log tiers only, got %v", tiers)
	}
	if len(viaHits(hits, "critic")) != 0 {
		t.Fatalf("a perspective peer is never read by another peer: %+v", hits)
	}
	md := RecallMarkdown(hits)
	if !strings.Contains(md, "[kb/log·via market") || !strings.Contains(md, "[kb/current·via market") {
		t.Fatalf("rendering must tag via market:\n%s", md)
	}
	if strings.Contains(md, "settlement window chat") {
		t.Fatalf("another peer's session records must not appear:\n%s", md)
	}

	// The default scope stays own-only.
	own, _ := RecallScoped(p.grid, "settlement window", RecallScopeOwn, 20, 0)
	if len(viaHits(own, "market")) != 0 {
		t.Fatalf("own scope must not read peers: %+v", own)
	}
	if _, err := RecallScoped(p.grid, "x", "everything", 20, 0); err == nil {
		t.Fatal("unknown scope must be an error")
	}
}

func TestRecallCanonical_PerspectivePeerStillReadsCanonicalPeers(t *testing.T) {
	p := newCanonicalPool(t)
	writeKB(t, p.market, "architecture/settlement-rules.md", "# Settlement window rules\n\nMarket: daily netting.\n")
	writeKB(t, p.grid, "architecture/settlement.md", "# Settlement window\n\nGrid view: daily.\n")
	hits, err := RecallScoped(p.critic, "settlement window", RecallScopeCanonical, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(viaHits(hits, "market")) == 0 || len(viaHits(hits, "grid")) == 0 {
		t.Fatalf("a perspective peer's canonical scope reads the canonical peers: %+v", hits)
	}
}

func TestRecallCanonical_CapsCrossPeerHitsAndRanksUnconfirmedBelow(t *testing.T) {
	p := newCanonicalPool(t)
	var rels []string
	for i := 0; i < 8; i++ {
		rel := fmt.Sprintf("reference/tariff-%d.md", i)
		writeKB(t, p.market, rel, fmt.Sprintf("# Tariff note %d\n\nTariff detail.\n", i))
		rels = append(rels, rel)
	}
	// The best textual match is unconfirmed; it must still rank below the
	// confirmed ones.
	writeKB(t, p.market, "reference/tariff-zone-peak.md", "# Tariff zone peak\n\nUnconfirmed.\n")
	confirmKB(t, p.market, rels...)

	hits, err := CanonicalPeerHits(p.grid, "tariff zone peak")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != CanonicalPeerHitCap {
		t.Fatalf("expected %d cross-peer hits, got %d: %+v", CanonicalPeerHitCap, len(hits), hits)
	}
	seenUnconfirmed := false
	for _, h := range hits {
		if !h.Confirmed {
			seenUnconfirmed = true
		} else if seenUnconfirmed {
			t.Fatalf("unconfirmed hits must rank below confirmed ones: %+v", hits)
		}
	}
	// The scope applies the same cap.
	all, _ := RecallScoped(p.grid, "tariff zone peak", RecallScopeCanonical, 20, 0)
	market := viaHits(all, "market")
	if len(market) != CanonicalPeerHitCap {
		t.Fatalf("scope must cap cross-peer hits at %d, got %d", CanonicalPeerHitCap, len(market))
	}
	// Unconfirmed peer hits are still returned, rendered as such.
	writeKB(t, p.market, "reference/zone-peak.md", "# Zone peak\n")
	few, _ := CanonicalPeerHits(p.grid, "zone peak")
	if len(few) != 2 {
		t.Fatalf("expected two zone-peak hits: %+v", few)
	}
	if !strings.Contains(RecallMarkdown(few), "·via market·unconfirmed]") {
		t.Fatalf("unconfirmed peer hit renders ·unconfirmed:\n%s", RecallMarkdown(few))
	}
}

func TestRecallCanonical_PeerBodiesOnlyWithBudgetAfterOwn(t *testing.T) {
	p := newCanonicalPool(t)
	writeKB(t, p.grid, "architecture/settlement.md", "# Settlement window\n\nGrid body text.\n")
	writeKB(t, p.market, "architecture/settlement-rules.md", "---\ntags: [x]\n---\n# Settlement window rules\n\nMarket body text.\n")
	confirmKB(t, p.grid, "architecture/settlement.md")
	confirmKB(t, p.market, "architecture/settlement-rules.md")

	hits, _ := RecallScoped(p.grid, "settlement window", RecallScopeCanonical, 20, 0)
	for _, h := range hits {
		if h.Body != "" {
			t.Fatalf("no body without an explicit budget: %+v", h)
		}
	}
	hits, _ = RecallScoped(p.grid, "settlement window", RecallScopeCanonical, 20, 2000)
	ownAt, peerAt := -1, -1
	for i, h := range hits {
		if h.Body == "" {
			continue
		}
		if h.Via == "" && ownAt < 0 {
			ownAt = i
		}
		if h.Via == "market" && peerAt < 0 {
			peerAt = i
			if !strings.Contains(h.Body, "Market body text.") || strings.Contains(h.Body, "tags:") {
				t.Fatalf("peer body is the entry text without frontmatter:\n%s", h.Body)
			}
		}
	}
	if ownAt < 0 || peerAt < 0 || ownAt > peerAt {
		t.Fatalf("own bodies first, then peer bodies: own=%d peer=%d %+v", ownAt, peerAt, hits)
	}
	// A budget that only fits the own body leaves the peer as a pointer.
	writeKB(t, p.grid, "architecture/settlement.md", "# Settlement window\n\n"+strings.Repeat("grid ", 400)+"\n")
	confirmKB(t, p.grid, "architecture/settlement.md")
	hits, _ = RecallScoped(p.grid, "settlement window", RecallScopeCanonical, 20, 500)
	for _, h := range viaHits(hits, "market") {
		if h.Body != "" {
			t.Fatalf("own bodies spend the budget first: %+v", h)
		}
	}
}

func TestRecallCanonical_OneHopReferenceFollowedNeverTransitively(t *testing.T) {
	p := newCanonicalPool(t)
	writePoolGlossary(t, p.root, "# Pool glossary\n\n**Netting**: TRANSITIVE-POOL-TEXT.\n")
	writeKB(t, p.market, "architecture/window.md",
		"# Window\n\n## Daily window\n\nMARKET-OWNER-TEXT: netting is daily, see [netting](mesh://pool/glossary.md#netting).\n\n## Other\n\nNOT-THIS-SECTION.\n")
	writeKB(t, p.critic, "architecture/view.md", "# View\n\nCRITIC-TEXT.\n")
	writeKB(t, p.grid, "architecture/settlement.md",
		"# Settlement window\n\nSee [daily](mesh://market/architecture/window.md#daily-window) and [view](mesh://critic/architecture/view.md).\n")
	confirmKB(t, p.grid, "architecture/settlement.md")

	hits, err := RecallScoped(p.grid, "settlement window", RecallScopeCanonical, 20, 2000)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, h := range hits {
		if h.Via == "" && h.Kind == "kb" && strings.Contains(h.Body, "Settlement window") {
			body = h.Body
		}
	}
	if !strings.Contains(body, "MARKET-OWNER-TEXT") || !strings.Contains(body, "via market") {
		t.Fatalf("one hop must inline market's entry marked via market:\n%s", body)
	}
	if strings.Contains(body, "NOT-THIS-SECTION") {
		t.Fatalf("only the referenced section is inlined:\n%s", body)
	}
	if strings.Contains(body, "TRANSITIVE-POOL-TEXT") {
		t.Fatalf("a reference inside the followed text is not followed:\n%s", body)
	}
	if strings.Contains(body, "CRITIC-TEXT") {
		t.Fatalf("a perspective target is never followed:\n%s", body)
	}
}

// TestRecallOwn_OneHopReferenceFollowed: the default (own) scope still
// searches only the peer's own KB, but an inlined body's `mesh://` reference
// is followed one hop like in the canonical scope (ADR-0065; story 42).
func TestRecallOwn_OneHopReferenceFollowed(t *testing.T) {
	p := newCanonicalPool(t)
	writePoolGlossary(t, p.root, "# Pool glossary\n\n**Netting**: TRANSITIVE-POOL-TEXT.\n")
	writeKB(t, p.market, "architecture/window.md",
		"# Window\n\n## Daily window\n\nMARKET-OWNER-TEXT: netting is daily, see [netting](mesh://pool/glossary.md#netting).\n")
	writeKB(t, p.market, "architecture/settlement-other.md", "# Settlement window elsewhere\n\nMARKET-SEARCH-HIT.\n")
	writeKB(t, p.critic, "architecture/view.md", "# View\n\nCRITIC-TEXT.\n")
	writeKB(t, p.grid, "architecture/settlement.md",
		"# Settlement window\n\nSee [daily](mesh://market/architecture/window.md#daily-window) and [view](mesh://critic/architecture/view.md).\n")
	confirmKB(t, p.grid, "architecture/settlement.md")

	for _, scope := range []string{"", RecallScopeOwn} {
		hits, err := RecallScoped(p.grid, "settlement window", scope, 20, 2000)
		if err != nil {
			t.Fatal(err)
		}
		if v := viaHits(hits, "market"); len(v) > 0 {
			t.Fatalf("scope %q: search must stay own-scoped: %+v", scope, v)
		}
		md := RecallMarkdown(hits)
		if strings.Contains(md, "MARKET-SEARCH-HIT") {
			t.Fatalf("scope %q: another peer's KB was searched:\n%s", scope, md)
		}
		if !strings.Contains(md, "MARKET-OWNER-TEXT") || !strings.Contains(md, "via market") {
			t.Fatalf("scope %q: one hop must inline market's entry marked via market:\n%s", scope, md)
		}
		if strings.Contains(md, "TRANSITIVE-POOL-TEXT") || strings.Contains(md, "CRITIC-TEXT") {
			t.Fatalf("scope %q: transitive or perspective target followed:\n%s", scope, md)
		}
	}
	// Without a budget nothing is inlined, references included.
	hits, _ := RecallScoped(p.grid, "settlement window", RecallScopeOwn, 20, 0)
	if strings.Contains(RecallMarkdown(hits), "MARKET-OWNER-TEXT") {
		t.Fatal("no budget, no expansion")
	}
}

func TestBrief_NeverUsesCanonicalScope(t *testing.T) {
	p := newCanonicalPool(t)
	writeKB(t, p.market, "architecture/settlement-rules.md", "# Settlement window rules\n\nMarket body.\n")
	writeKB(t, p.grid, "architecture/settlement.md", "# Settlement window\n\nGrid body.\n")
	req, _, err := AskInSession(p.root, p.grid, InSessionOptions{Type: "guidance", Prompt: "What is the settlement window?", From: p.market.Name, Stdout: discard{}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Brief(p.root, p.grid, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Grid body.") {
		t.Fatalf("brief still inlines own recall:\n%s", out)
	}
	if strings.Contains(out, "via market") || strings.Contains(out, "Market body.") {
		t.Fatalf("brief must never include via <peer> hits:\n%s", out)
	}
}

func TestDoctor_MeshRefFindings(t *testing.T) {
	p := newCanonicalPool(t)
	writePoolGlossary(t, p.root, "# Pool glossary\n\n**Netting**: daily.\n")
	writeKB(t, p.critic, "architecture/view.md", "# View\n\nCritic view.\n")
	writeKB(t, p.market, "architecture/window.md", "# Window\n\n## Daily window\n\nDaily.\n")
	writeKB(t, p.grid, "architecture/settlement.md", strings.Join([]string{
		"# Settlement",
		"[ok](mesh://market/architecture/window.md#daily-window)",
		"[ok pool](mesh://pool/glossary.md#netting)",
		"[persp](mesh://critic/architecture/view.md)",
		"[gone](mesh://market/architecture/nope.md#x)",
		"[no term](mesh://pool/glossary.md#no-such-term)",
		"[no peer](mesh://ghost/a.md)",
		"Example only: `mesh://<peer>/<path>#<slug>` in code is not a link.",
	}, "\n")+"\n")
	// A perspective peer may reference a canonical entry, and a perspective
	// peer's own dangling link is still dangling.
	writeKB(t, p.critic, "architecture/refs.md", "# Refs\n\n[w](mesh://market/architecture/window.md#daily-window)\n[x](mesh://market/missing.md)\n")
	before, _ := os.ReadFile(filepath.Join(p.grid.Dir(), KnowledgeDir, "architecture/settlement.md"))

	findings, _ := Doctor(p.root)
	count := func(substr string) int {
		n := 0
		for _, f := range findings {
			if strings.Contains(f.Message, substr) {
				n++
			}
		}
		return n
	}
	if count("mesh://critic/architecture/view.md") != 1 || !hasFinding(findings, "perspective") {
		t.Fatalf("canonical→perspective reference must be flagged once: %+v", findings)
	}
	for _, ref := range []string{"mesh://market/architecture/nope.md#x", "mesh://pool/glossary.md#no-such-term", "mesh://ghost/a.md", "mesh://market/missing.md"} {
		if count(ref) != 1 {
			t.Fatalf("dangling %s must be flagged once: %+v", ref, findings)
		}
	}
	for _, ref := range []string{"mesh://market/architecture/window.md#daily-window", "mesh://pool/glossary.md#netting", "<peer>"} {
		if count(ref) != 0 {
			t.Fatalf("%s must not be flagged: %+v", ref, findings)
		}
	}
	after, _ := os.ReadFile(filepath.Join(p.grid.Dir(), KnowledgeDir, "architecture/settlement.md"))
	if string(before) != string(after) {
		t.Fatal("doctor never auto-fixes references")
	}
}
