package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Canonical recall scope and one-hop references (ADR-0065, ADR-0069; spec
// decisions 30 and 33). Fixture: a three-peer pool, catalog and lending canonical,
// critic perspective.

type canonicalPool struct {
	root                     string
	catalog, lending, critic *Agent
}

func newCanonicalPool(t *testing.T) canonicalPool {
	t.Helper()
	root := migratedRoot(t)
	catalog := newTestAgent(t, root, "catalog", true, nil) // dispatchable: the brief test asks it in-session
	lending := newTestAgent(t, root, "lending", false, nil)
	newTestAgent(t, root, "critic", false, nil)
	critic := setManifestKnowledge(t, root, "critic", KnowledgePerspective)
	return canonicalPool{root: root, catalog: catalog, lending: lending, critic: critic}
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
	writeKB(t, p.catalog, "architecture/circulation.md", "# Circulation window\n\nCatalog view: daily.\n")
	writeKB(t, p.lending, "architecture/circulation-rules.md", "# Circulation window rules\n\nLending: daily renewal.\n")
	writeKB(t, p.lending, "decisions/0003-circulation-window-daily.md", "---\nstatus: accepted\n---\n# 0003. Circulation window is daily\n")
	writeKB(t, p.lending, "candidates/circulation-window-weekly.md", "# Circulation window weekly?\n")
	writeKB(t, p.critic, "architecture/circulation-critique.md", "# Circulation window critique\n\nIt should be hourly.\n")
	// Session records and the git spine of another peer are never read.
	os.MkdirAll(filepath.Join(p.lending.Dir(), ".memory", "sessions"), 0o755)
	os.WriteFile(filepath.Join(p.lending.Dir(), ".memory", "sessions", "s1.json"),
		[]byte(`{"id":"s1","agent":"lending","first_prompt":"circulation window chat","ended":"2026-10-01T00:00:00Z"}`), 0o644)

	hits, err := RecallScoped(p.catalog, "circulation window", RecallScopeCanonical, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(viaHits(hits, "")) == 0 {
		t.Fatalf("own hits must still be returned: %+v", hits)
	}
	lending := viaHits(hits, "lending")
	if len(lending) == 0 {
		t.Fatalf("expected hits via lending: %+v", hits)
	}
	tiers := map[string]bool{}
	for _, h := range lending {
		if h.Kind != "kb" {
			t.Fatalf("peer hits are kb only (no sessions, no commits): %+v", h)
		}
		tiers[h.Tier] = true
		for _, w := range h.Where {
			if !strings.HasPrefix(w, "mesh://lending/") || strings.Contains(w, "candidates/") {
				t.Fatalf("peer hit must point at lending's current/log tiers by reference: %+v", h)
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
	if !strings.Contains(md, "[kb/log·via lending") || !strings.Contains(md, "[kb/current·via lending") {
		t.Fatalf("rendering must tag via lending:\n%s", md)
	}
	if strings.Contains(md, "circulation window chat") {
		t.Fatalf("another peer's session records must not appear:\n%s", md)
	}

	// The default scope stays own-only.
	own, _ := RecallScoped(p.catalog, "circulation window", RecallScopeOwn, 20, 0)
	if len(viaHits(own, "lending")) != 0 {
		t.Fatalf("own scope must not read peers: %+v", own)
	}
	if _, err := RecallScoped(p.catalog, "x", "everything", 20, 0); err == nil {
		t.Fatal("unknown scope must be an error")
	}
}

func TestRecallCanonical_PerspectivePeerStillReadsCanonicalPeers(t *testing.T) {
	p := newCanonicalPool(t)
	writeKB(t, p.lending, "architecture/circulation-rules.md", "# Circulation window rules\n\nLending: daily renewal.\n")
	writeKB(t, p.catalog, "architecture/circulation.md", "# Circulation window\n\nCatalog view: daily.\n")
	hits, err := RecallScoped(p.critic, "circulation window", RecallScopeCanonical, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(viaHits(hits, "lending")) == 0 || len(viaHits(hits, "catalog")) == 0 {
		t.Fatalf("a perspective peer's canonical scope reads the canonical peers: %+v", hits)
	}
}

func TestRecallCanonical_CapsCrossPeerHitsAndRanksUnconfirmedBelow(t *testing.T) {
	p := newCanonicalPool(t)
	var rels []string
	for i := 0; i < 8; i++ {
		rel := fmt.Sprintf("reference/levy-%d.md", i)
		writeKB(t, p.lending, rel, fmt.Sprintf("# Levy note %d\n\nLevy detail.\n", i))
		rels = append(rels, rel)
	}
	// The best textual match is unconfirmed; it must still rank below the
	// confirmed ones.
	writeKB(t, p.lending, "reference/levy-zone-peak.md", "# Levy zone peak\n\nUnconfirmed.\n")
	confirmKB(t, p.lending, rels...)

	hits, err := CanonicalPeerHits(p.catalog, "levy zone peak")
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
	all, _ := RecallScoped(p.catalog, "levy zone peak", RecallScopeCanonical, 20, 0)
	lending := viaHits(all, "lending")
	if len(lending) != CanonicalPeerHitCap {
		t.Fatalf("scope must cap cross-peer hits at %d, got %d", CanonicalPeerHitCap, len(lending))
	}
	// Unconfirmed peer hits are still returned, rendered as such.
	writeKB(t, p.lending, "reference/zone-peak.md", "# Zone peak\n")
	few, _ := CanonicalPeerHits(p.catalog, "zone peak")
	if len(few) != 2 {
		t.Fatalf("expected two zone-peak hits: %+v", few)
	}
	if !strings.Contains(RecallMarkdown(few), "·via lending·unconfirmed]") {
		t.Fatalf("unconfirmed peer hit renders ·unconfirmed:\n%s", RecallMarkdown(few))
	}
}

func TestRecallCanonical_PeerBodiesOnlyWithBudgetAfterOwn(t *testing.T) {
	p := newCanonicalPool(t)
	writeKB(t, p.catalog, "architecture/circulation.md", "# Circulation window\n\nCatalog body text.\n")
	writeKB(t, p.lending, "architecture/circulation-rules.md", "---\ntags: [x]\n---\n# Circulation window rules\n\nLending body text.\n")
	confirmKB(t, p.catalog, "architecture/circulation.md")
	confirmKB(t, p.lending, "architecture/circulation-rules.md")

	hits, _ := RecallScoped(p.catalog, "circulation window", RecallScopeCanonical, 20, 0)
	for _, h := range hits {
		if h.Body != "" {
			t.Fatalf("no body without an explicit budget: %+v", h)
		}
	}
	hits, _ = RecallScoped(p.catalog, "circulation window", RecallScopeCanonical, 20, 2000)
	ownAt, peerAt := -1, -1
	for i, h := range hits {
		if h.Body == "" {
			continue
		}
		if h.Via == "" && ownAt < 0 {
			ownAt = i
		}
		if h.Via == "lending" && peerAt < 0 {
			peerAt = i
			if !strings.Contains(h.Body, "Lending body text.") || strings.Contains(h.Body, "tags:") {
				t.Fatalf("peer body is the entry text without frontmatter:\n%s", h.Body)
			}
		}
	}
	if ownAt < 0 || peerAt < 0 || ownAt > peerAt {
		t.Fatalf("own bodies first, then peer bodies: own=%d peer=%d %+v", ownAt, peerAt, hits)
	}
	// A budget that only fits the own body leaves the peer as a pointer.
	writeKB(t, p.catalog, "architecture/circulation.md", "# Circulation window\n\n"+strings.Repeat("catalog ", 400)+"\n")
	confirmKB(t, p.catalog, "architecture/circulation.md")
	hits, _ = RecallScoped(p.catalog, "circulation window", RecallScopeCanonical, 20, 500)
	for _, h := range viaHits(hits, "lending") {
		if h.Body != "" {
			t.Fatalf("own bodies spend the budget first: %+v", h)
		}
	}
}

func TestRecallCanonical_OneHopReferenceFollowedNeverTransitively(t *testing.T) {
	p := newCanonicalPool(t)
	writePoolGlossary(t, p.root, "# Pool glossary\n\n**Renewal**: TRANSITIVE-POOL-TEXT.\n")
	writeKB(t, p.lending, "architecture/window.md",
		"# Window\n\n## Daily window\n\nLENDING-OWNER-TEXT: renewal is daily, see [renewal](mesh://pool/glossary.md#renewal).\n\n## Other\n\nNOT-THIS-SECTION.\n")
	writeKB(t, p.critic, "architecture/view.md", "# View\n\nCRITIC-TEXT.\n")
	writeKB(t, p.catalog, "architecture/circulation.md",
		"# Circulation window\n\nSee [daily](mesh://lending/architecture/window.md#daily-window) and [view](mesh://critic/architecture/view.md).\n")
	confirmKB(t, p.catalog, "architecture/circulation.md")

	hits, err := RecallScoped(p.catalog, "circulation window", RecallScopeCanonical, 20, 2000)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, h := range hits {
		if h.Via == "" && h.Kind == "kb" && strings.Contains(h.Body, "Circulation window") {
			body = h.Body
		}
	}
	if !strings.Contains(body, "LENDING-OWNER-TEXT") || !strings.Contains(body, "via lending") {
		t.Fatalf("one hop must inline lending's entry marked via lending:\n%s", body)
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
	writePoolGlossary(t, p.root, "# Pool glossary\n\n**Renewal**: TRANSITIVE-POOL-TEXT.\n")
	writeKB(t, p.lending, "architecture/window.md",
		"# Window\n\n## Daily window\n\nLENDING-OWNER-TEXT: renewal is daily, see [renewal](mesh://pool/glossary.md#renewal).\n")
	writeKB(t, p.lending, "architecture/circulation-other.md", "# Circulation window elsewhere\n\nLENDING-SEARCH-HIT.\n")
	writeKB(t, p.critic, "architecture/view.md", "# View\n\nCRITIC-TEXT.\n")
	writeKB(t, p.catalog, "architecture/circulation.md",
		"# Circulation window\n\nSee [daily](mesh://lending/architecture/window.md#daily-window) and [view](mesh://critic/architecture/view.md).\n")
	confirmKB(t, p.catalog, "architecture/circulation.md")

	for _, scope := range []string{"", RecallScopeOwn} {
		hits, err := RecallScoped(p.catalog, "circulation window", scope, 20, 2000)
		if err != nil {
			t.Fatal(err)
		}
		if v := viaHits(hits, "lending"); len(v) > 0 {
			t.Fatalf("scope %q: search must stay own-scoped: %+v", scope, v)
		}
		md := RecallMarkdown(hits)
		if strings.Contains(md, "LENDING-SEARCH-HIT") {
			t.Fatalf("scope %q: another peer's KB was searched:\n%s", scope, md)
		}
		if !strings.Contains(md, "LENDING-OWNER-TEXT") || !strings.Contains(md, "via lending") {
			t.Fatalf("scope %q: one hop must inline lending's entry marked via lending:\n%s", scope, md)
		}
		if strings.Contains(md, "TRANSITIVE-POOL-TEXT") || strings.Contains(md, "CRITIC-TEXT") {
			t.Fatalf("scope %q: transitive or perspective target followed:\n%s", scope, md)
		}
	}
	// Without a budget nothing is inlined, references included.
	hits, _ := RecallScoped(p.catalog, "circulation window", RecallScopeOwn, 20, 0)
	if strings.Contains(RecallMarkdown(hits), "LENDING-OWNER-TEXT") {
		t.Fatal("no budget, no expansion")
	}
}

func TestBrief_NeverUsesCanonicalScope(t *testing.T) {
	p := newCanonicalPool(t)
	writeKB(t, p.lending, "architecture/circulation-rules.md", "# Circulation window rules\n\nLending body.\n")
	writeKB(t, p.catalog, "architecture/circulation.md", "# Circulation window\n\nCatalog body.\n")
	req, _, err := AskInSession(p.root, p.catalog, InSessionOptions{Type: "guidance", Prompt: "What is the circulation window?", From: p.lending.Name, Stdout: discard{}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Brief(p.root, p.catalog, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Catalog body.") {
		t.Fatalf("brief still inlines own recall:\n%s", out)
	}
	if strings.Contains(out, "via lending") || strings.Contains(out, "Lending body.") {
		t.Fatalf("brief must never include via <peer> hits:\n%s", out)
	}
}

func TestDoctor_MeshRefFindings(t *testing.T) {
	p := newCanonicalPool(t)
	writePoolGlossary(t, p.root, "# Pool glossary\n\n**Renewal**: daily.\n")
	writeKB(t, p.critic, "architecture/view.md", "# View\n\nCritic view.\n")
	writeKB(t, p.lending, "architecture/window.md", "# Window\n\n## Daily window\n\nDaily.\n")
	writeKB(t, p.catalog, "architecture/circulation.md", strings.Join([]string{
		"# Circulation",
		"[ok](mesh://lending/architecture/window.md#daily-window)",
		"[ok pool](mesh://pool/glossary.md#renewal)",
		"[persp](mesh://critic/architecture/view.md)",
		"[gone](mesh://lending/architecture/nope.md#x)",
		"[no term](mesh://pool/glossary.md#no-such-term)",
		"[no peer](mesh://ghost/a.md)",
		"Example only: `mesh://<peer>/<path>#<slug>` in code is not a link.",
	}, "\n")+"\n")
	// A perspective peer may reference a canonical entry, and a perspective
	// peer's own dangling link is still dangling.
	writeKB(t, p.critic, "architecture/refs.md", "# Refs\n\n[w](mesh://lending/architecture/window.md#daily-window)\n[x](mesh://lending/missing.md)\n")
	before, _ := os.ReadFile(filepath.Join(p.catalog.Dir(), KnowledgeDir, "architecture/circulation.md"))

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
	for _, ref := range []string{"mesh://lending/architecture/nope.md#x", "mesh://pool/glossary.md#no-such-term", "mesh://ghost/a.md", "mesh://lending/missing.md"} {
		if count(ref) != 1 {
			t.Fatalf("dangling %s must be flagged once: %+v", ref, findings)
		}
	}
	for _, ref := range []string{"mesh://lending/architecture/window.md#daily-window", "mesh://pool/glossary.md#renewal", "<peer>"} {
		if count(ref) != 0 {
			t.Fatalf("%s must not be flagged: %+v", ref, findings)
		}
	}
	after, _ := os.ReadFile(filepath.Join(p.catalog.Dir(), KnowledgeDir, "architecture/circulation.md"))
	if string(before) != string(after) {
		t.Fatal("doctor never auto-fixes references")
	}
}
