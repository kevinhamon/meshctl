package mesh

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// recall_canonical.go — the canonical recall scope and one-hop references.
// A peer checks what the pool already
// holds before it proposes: its own recall, plus every canonical peer's
// current and log tiers, read-only, each hit tagged `via <peer>`. Candidates,
// session records and the git spine of other peers are never read, and a
// perspective peer is never read by anyone else. The session-start brief and
// the default scope search own-only; in every scope an inlined body's
// `mesh://` reference is followed one hop (oneHopExpander).

// Recall scopes.
const (
	RecallScopeOwn       = "own"       // the peer's own KB, sessions, git spine, pool glossary
	RecallScopeCanonical = "canonical" // own + canonical peers' current and log tiers
)

// CanonicalPeerHitCap is the most cross-peer hits one canonical-scope call
// returns.
const CanonicalPeerHitCap = 5

// ValidRecallScope reports whether s names a recall scope ("" = own).
func ValidRecallScope(s string) bool {
	return s == "" || s == RecallScopeOwn || s == RecallScopeCanonical
}

// RecallScoped is recall in a named scope. The own scope is exactly
// RecallWithBodies (own search; bodies still follow references one hop). The
// canonical scope returns the own-scope hits (ranked and limited as before)
// followed by at most CanonicalPeerHitCap cross-peer hits (CanonicalPeerHits
// order). Bodies are inlined only with budgetTokens > 0: own bodies spend the
// budget first, peer bodies take the remainder, and an inlined body's `mesh://`
// references are followed one hop to canonical targets, read-only, marked via
// the owner, never transitively.
func RecallScoped(agent *Agent, query, scope string, limit, budgetTokens int) ([]RecallHit, error) {
	switch scope {
	case "", RecallScopeOwn:
		return RecallWithBodies(agent, query, limit, budgetTokens)
	case RecallScopeCanonical:
	default:
		return nil, fmt.Errorf("unknown recall scope %q: use %s or %s", scope, RecallScopeOwn, RecallScopeCanonical)
	}
	hits, err := Recall(agent, query, limit)
	if err != nil {
		return nil, err
	}
	peers, err := CanonicalPeerHits(agent, query)
	if err != nil {
		return nil, err
	}
	hits = append(hits, peers...)
	if budgetTokens > 0 {
		inlineBodies(agent, hits, budgetTokens, oneHopExpander(agent))
	}
	return hits, nil
}

// CanonicalPeers lists the pool's canonical peers other than self, by name.
// Perspective peers are never in it (never read, neither index nor
// bodies).
func CanonicalPeers(root string, cfg PoolConfig, self string) ([]*Agent, error) {
	agents, _, err := ScanAgents(root)
	if err != nil {
		return nil, err
	}
	var out []*Agent
	for _, a := range agents {
		if a.Name != self && IsCanonical(cfg, a) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CanonicalPeerHits searches every canonical peer's current and log tiers
// (not the caller's own KB) for query. Each hit is a kb hit with Via set to
// the peer and Where holding `mesh://<peer>/<kb-path>` references, one per
// source file. Hits are deduplicated per source set, ordered confirmed before
// unconfirmed (by the owning peer's ledger), then by score and
// recency, and capped at CanonicalPeerHitCap. Read-only.
func CanonicalPeerHits(agent *Agent, query string) ([]RecallHit, error) {
	root := filepath.Dir(agent.Dir())
	peers, err := CanonicalPeers(root, LoadPoolConfig(root), agent.Name)
	if err != nil {
		return nil, err
	}
	tokens := queryTokens(query)
	best := map[string]RecallHit{}
	for _, p := range peers {
		idx, err := computeTopics(p)
		if err != nil {
			continue // no knowledge/ yet
		}
		ledger := newLedgerCheck(p.Dir())
		for _, t := range idx.Topics {
			var srcs []string
			for _, s := range t.Sources {
				if kbTier(s) != TierInbox {
					srcs = append(srcs, s)
				}
			}
			if len(srcs) == 0 {
				continue // candidates are proposals, never read across peers
			}
			score := 0
			if len(tokens) > 0 {
				score = tokenScore(t.Topic+" "+strings.Join(t.Tags, " "), tokens)
				if score == 0 {
					continue
				}
			}
			tier := bestTier(srcs)
			confirmed := ledger.topicConfirmed(t.Topic, srcs)
			if score > 0 {
				score = score*3 + rankBonus(tier, confirmed) + refsPrior(t.Refs)
			}
			where := make([]string, len(srcs))
			for i, s := range srcs {
				where[i] = MeshRef{Owner: p.Name, Path: kbRelPath(s)}.String()
			}
			h := RecallHit{
				Kind: "kb", Title: t.Topic, Where: where, Date: t.Freshness,
				Score: score, Tier: tier, Via: p.Name, Confirmed: confirmed,
			}
			key := strings.Join(where, " ")
			if prev, ok := best[key]; !ok || peerHitBefore(h, prev) {
				best[key] = h
			}
		}
	}
	hits := make([]RecallHit, 0, len(best))
	for _, h := range best {
		hits = append(hits, h)
	}
	sort.Slice(hits, func(i, j int) bool { return peerHitBefore(hits[i], hits[j]) })
	if len(hits) > CanonicalPeerHitCap {
		hits = hits[:CanonicalPeerHitCap]
	}
	return hits, nil
}

// peerHitBefore orders cross-peer hits: confirmed first, then score, then
// recency, then a stable textual tiebreak.
func peerHitBefore(a, b RecallHit) bool {
	if a.Confirmed != b.Confirmed {
		return a.Confirmed
	}
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.Date != b.Date {
		return a.Date > b.Date
	}
	return strings.Join(a.Where, " ")+a.Title < strings.Join(b.Where, " ")+b.Title
}

// oneHopExpander appends, after an inlined body, the entry each of its
// `mesh://` references names, when the target is canonical knowledge: the
// pool's own (always canonical) or a canonical peer's current or log tier.
// References to the reader itself, to perspective peers, to candidates, or
// that do not resolve are not followed. The appended text is never scanned
// for references (one hop, never transitively). Each expansion is added only
// if it fits in the remaining budget.
func oneHopExpander(agent *Agent) func(h *RecallHit, body string, remaining int) string {
	root := filepath.Dir(agent.Dir())
	cfg := LoadPoolConfig(root)
	return func(h *RecallHit, body string, remaining int) string {
		var b strings.Builder
		b.WriteString(body)
		for _, ref := range MeshRefsIn(body) {
			text, owner, ok := followCanonicalRef(root, cfg, agent.Name, ref)
			if !ok || ref == h.Where[0] {
				continue
			}
			add := fmt.Sprintf("\n\n↪ via %s — %s\n%s", owner, ref, text)
			if len(add) > remaining {
				continue
			}
			b.WriteString(add)
			remaining -= len(add)
		}
		return b.String()
	}
}

// followCanonicalRef resolves ref for one-hop expansion: ok only for a
// canonical, non-inbox target owned by someone other than self.
func followCanonicalRef(root string, cfg PoolConfig, self, ref string) (text, owner string, ok bool) {
	r, err := ParseMeshRef(ref)
	if err != nil || r.Owner == self {
		return "", "", false
	}
	if r.Owner != ReservedOwner {
		a, err := FindAgent(root, r.Owner)
		if err != nil || !IsCanonical(cfg, a) || kbTier(r.Path) == TierInbox {
			return "", "", false
		}
	}
	text, found, err := ResolveMeshRef(root, ref)
	if err != nil || !found {
		return "", "", false
	}
	_, text = splitFrontmatter(text)
	text = strings.TrimSpace(text)
	return text, r.Owner, text != ""
}
