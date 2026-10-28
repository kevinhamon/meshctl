package mesh

import (
	"os"
	"strings"
	"testing"
)

// Recall indexes the pool glossary as a current-tier source for every peer,
// marked `via pool`, ranked and budgeted like the peer's own current tier, and
// confirmed unless the pool repo is dirty (ADR-0066, spec decision 15).

const circulationGlossary = `# Pool glossary

## Lending

**Circulation window**: The period over which loans are renewed before billing.
_Avoid_: "billing cycle" — that is the billing cadence, not the renewal period.

**Dispatch**: A headless run of a peer against one request.
`

func commitPoolGlossary(t *testing.T, root, body string) {
	t.Helper()
	writePoolGlossary(t, root, body)
	gitOut(t, PoolDir(root), "add", "-A")
	gitOut(t, PoolDir(root), "commit", "-q", "-m", "glossary: circulation window")
}

func poolHits(hits []RecallHit) []RecallHit {
	var out []RecallHit
	for _, h := range hits {
		if h.Via == ReservedOwner {
			out = append(out, h)
		}
	}
	return out
}

func TestRecall_PoolGlossaryIsCurrentTierViaPool(t *testing.T) {
	root := migratedRoot(t)
	commitPoolGlossary(t, root, circulationGlossary)
	a := newTestAgent(t, root, "catalog-expert", true, nil)
	writeKB(t, a, "architecture/circulation.md", "# Circulation window\n\nDaily since 0002.\n")
	writeKB(t, a, "decisions/0002-circulation-window-daily.md", "---\nstatus: accepted\n---\n# 0002. Circulation window is daily\n")
	confirmKB(t, a, "architecture/circulation.md", "decisions/0002-circulation-window-daily.md") // own entries confirmed (ADR-0067)

	hits, err := Recall(a, "circulation window", 20)
	if err != nil {
		t.Fatal(err)
	}
	pool := poolHits(hits)
	if len(pool) != 1 {
		t.Fatalf("expected exactly the matching glossary term via pool, got %+v (all: %+v)", pool, hits)
	}
	p := pool[0]
	if p.Kind != "kb" || p.Tier != TierCurrent || !p.Confirmed {
		t.Fatalf("pool hit must be a confirmed current-tier kb hit: %+v", p)
	}
	if len(p.Where) != 1 || p.Where[0] != "mesh://pool/glossary.md#circulation-window" {
		t.Fatalf("pool hit must point at the knowledge reference: %+v", p.Where)
	}
	if p.Date == "" {
		t.Fatalf("pool hit must carry the commit date so it sorts among own hits: %+v", p)
	}

	// Ranked like the peer's own current tier: same score as the best own
	// current-tier match on the same topic, above the own log-tier match.
	var own, log *RecallHit
	for i := range hits {
		h := &hits[i]
		if h.Via != "" || h.Kind != "kb" {
			continue
		}
		switch {
		case h.Tier == TierCurrent && (own == nil || h.Score > own.Score):
			own = h
		case h.Tier == TierLog && (log == nil || h.Score > log.Score):
			log = h
		}
	}
	if own == nil || log == nil {
		t.Fatalf("own current and log hits must still be recalled: %+v", hits)
	}
	if !own.Confirmed || own.Via != "" {
		t.Fatalf("own hits are confirmed and carry no via: %+v", own)
	}
	if p.Score != own.Score || p.Score <= log.Score {
		t.Fatalf("pool hit scored %d; own current %d, own log %d", p.Score, own.Score, log.Score)
	}

	// Rendering and bodies: `via pool` label, body inlined within the budget
	// like an own current-tier hit, and budget 0 leaves pointers.
	withBodies, err := RecallWithBodies(a, "circulation window", 20, 200)
	if err != nil {
		t.Fatal(err)
	}
	pb := poolHits(withBodies)
	if len(pb) != 1 || !strings.Contains(pb[0].Body, "renewed before billing") || strings.Contains(pb[0].Body, "Dispatch") {
		t.Fatalf("pool body must be the term's entry: %+v", pb)
	}
	md := RecallMarkdown(withBodies)
	if !strings.Contains(md, "[kb/current·via pool] circulation-window") || strings.Contains(md, "unconfirmed") {
		t.Fatalf("markdown:\n%s", md)
	}
	if !strings.Contains(md, "- [kb/current] ") {
		t.Fatalf("own current hit renders as before:\n%s", md)
	}
	plain, _ := RecallWithBodies(a, "circulation window", 20, 0)
	for _, h := range poolHits(plain) {
		if h.Body != "" {
			t.Fatal("budget 0 must not inline pool bodies")
		}
	}

	// A query the glossary does not answer yields no pool hit; activity-only
	// recall never includes the glossary.
	none, _ := Recall(a, "bill cadence", 20)
	if len(poolHits(none)) != 0 {
		t.Fatalf("terms match on their name, not their definition: %+v", none)
	}
	act, _ := RecallActivity(a, "circulation window", 20)
	if len(poolHits(act)) != 0 {
		t.Fatalf("activity recall must not index the glossary: %+v", act)
	}
}

func TestRecall_PoolGlossaryDirtyIsUnconfirmed(t *testing.T) {
	root := migratedRoot(t)
	commitPoolGlossary(t, root, circulationGlossary)
	a := newTestAgent(t, root, "catalog-expert", true, nil)
	writeKB(t, a, "architecture/circulation.md", "# Circulation window\n")
	confirmKB(t, a, "architecture/circulation.md") // own entry confirmed (ADR-0067)

	// An uncommitted edit to the glossary: the hit stays, unconfirmed.
	if err := os.WriteFile(PoolGlossaryPath(root), []byte(circulationGlossary+"\n**Renewal**: Extending a loan.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hits, err := RecallWithBodies(a, "circulation window", 20, 200)
	if err != nil {
		t.Fatal(err)
	}
	pool := poolHits(hits)
	if len(pool) != 1 || pool[0].Confirmed || pool[0].Tier != TierCurrent {
		t.Fatalf("dirty glossary must recall as an unconfirmed current hit: %+v", pool)
	}
	if md := RecallMarkdown(hits); !strings.Contains(md, "[kb/current·via pool·unconfirmed] circulation-window") {
		t.Fatalf("markdown:\n%s", md)
	}
	// Own entries are untouched by the pool's state.
	for _, h := range hits {
		if h.Via == "" && h.Kind == "kb" && !h.Confirmed {
			t.Fatalf("own hit must stay confirmed: %+v", h)
		}
	}
}

func TestRecall_LegacyPoolGlossaryIsConfirmedViaPool(t *testing.T) {
	root := meshRoot(t) // hidden legacy pool dir, no repo
	writePoolGlossary(t, root, circulationGlossary)
	a := newTestAgent(t, root, "catalog-expert", true, nil)
	writeKB(t, a, "architecture/circulation.md", "# Circulation window\n")

	hits, err := Recall(a, "circulation window", 20)
	if err != nil {
		t.Fatal(err)
	}
	pool := poolHits(hits)
	if len(pool) != 1 || !pool[0].Confirmed || pool[0].Date == "" {
		t.Fatalf("legacy pool glossary must recall confirmed via pool: %+v", pool)
	}
	if md := RecallMarkdown(hits); !strings.Contains(md, "[kb/current·via pool] circulation-window") {
		t.Fatalf("markdown:\n%s", md)
	}
}
