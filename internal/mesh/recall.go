package mesh

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Unified recall: one contract over three sources —
//   - KB topics (durable, authoritative; live-computed, no persisted index),
//   - session-records (owned episodic capture),
//   - the git time-spine (always-on floor: recent commits, even before any
//     session was captured).
// Returns compact pointers (title + where + date) by default; RecallWithBodies
// also inlines the top current-tier KB hits within a token budget, so what the
// KB knows stays in view instead of behind a pointer the model may not follow.
// Backend is keyword/BM25-ish today; embeddings slot behind this same function
// later.
//
// KB tiers (S1): a KB has a current tier — architecture/, playbooks/,
// reference/, the state docs (direction.md, risks.md, state.md, glossary.md,
// system-map.md) and rule notes — maintained in place and stating what is true
// now, and a log tier — decisions/ and experience/ — append-only history.
// Recall ranks current above log for the same match, ranks candidates/ (the
// KB inbox) below both, and drops decisions whose status is superseded,
// deprecated or rejected: history must not outrank its replacement.

// RecallHit is one compact pointer from any recall source.
type RecallHit struct {
	Kind   string   `json:"kind"`             // "kb" | "session" | "commit"
	Title  string   `json:"title"`            // topic, opening ask, or commit subject
	Where  []string `json:"where,omitempty"`  // KB paths, session id + handoff ref, or commit sha
	Date   string   `json:"date,omitempty"`   // freshness / ended / commit time
	Detail string   `json:"detail,omitempty"` // refs, files, extra context
	Score  int      `json:"score"`
	Tier   string   `json:"tier,omitempty"` // kb hits: "current" | "log" | "inbox"
	Body   string   `json:"body,omitempty"` // RecallWithBodies: the source text, within budget
	// Via names the source peer of a hit that is not the calling peer's own:
	// ReservedOwner for the pool glossary, a peer name for canonical
	// scope. "" is the peer's own knowledge. Where[0] of a via hit
	// is the `mesh://` reference, not a workspace-relative path.
	Via string `json:"via,omitempty"`
	// Confirmed is false for a hit nobody has agreed to: an
	// own entry whose hash does not match the peer's ledger (no ledger ⇒
	// unconfirmed), any inbox hit, or a pool file with uncommitted changes.
	Confirmed bool `json:"confirmed"`
	// ConfirmedBy is LedgerByPolicy for a confirmed hit a grant, not a human,
	// confirmed; "" for a human confirmation or an unconfirmed
	// hit. Recall renders it `confirmed·policy` and ranks it below human
	// confirmations, above unconfirmed ones.
	ConfirmedBy string `json:"confirmed_by,omitempty"`
}

// confirmation is the hit's standing: human, policy or none.
func (h RecallHit) confirmation() Confirmation {
	switch {
	case !h.Confirmed:
		return ConfirmNone
	case h.ConfirmedBy != "":
		return ConfirmPolicy
	}
	return ConfirmHuman
}

// KB tiers.
const (
	TierCurrent = "current" // maintained in place; states what is true now
	TierLog     = "log"     // append-only history (decisions/, experience/)
	TierInbox   = "inbox"   // candidates/ — proposals, not truth
)

// kbTier classifies a KB-relative path (knowledge/decisions/x.md → log).
func kbTier(rel string) string {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), KnowledgeDir+"/")
	switch {
	case strings.HasPrefix(rel, "candidates/"):
		return TierInbox
	case strings.HasPrefix(rel, "decisions/"), strings.HasPrefix(rel, "experience/"):
		return TierLog
	}
	return TierCurrent
}

// bestTier is the highest tier among a topic's sources.
func bestTier(sources []string) string {
	best := ""
	for _, s := range sources {
		t := kbTier(s)
		switch {
		case t == TierCurrent:
			return TierCurrent
		case t == TierLog, best == "":
			best = t
		}
	}
	if best == "" {
		return TierCurrent
	}
	return best
}

// rankScale spreads the recall priors so a policy confirmation fits between
// human-confirmed and unconfirmed knowledge. Every recall score is a multiple
// of it except the policy offsets, so the order among human-confirmed,
// unconfirmed and non-KB hits is what it was before policy confirmations.
const rankScale = 3

// matchWeight is the score of one matched query token.
const matchWeight = 3 * rankScale

// tierBonus is the ranking prior per tier for human-confirmed knowledge: a
// current-tier match outranks the same match in the log, which outranks it in
// the inbox. Policy-confirmed and unconfirmed knowledge take the slots between
// log and inbox (rankBonus).
func tierBonus(tier string) int {
	switch tier {
	case TierCurrent:
		return 3 * rankScale
	case TierLog:
		return 2 * rankScale
	}
	return 0
}

// rankBonus is tierBonus for a hit by its confirmation: below human-confirmed
// log come a policy-confirmed current hit, then a policy-confirmed log hit,
// then an unconfirmed current or log hit (one rank), then the inbox.
func rankBonus(tier string, c Confirmation) int {
	if tier != TierCurrent && tier != TierLog {
		return tierBonus(tier)
	}
	switch c {
	case ConfirmNone:
		return 1 * rankScale
	case ConfirmPolicy:
		if tier == TierCurrent {
			return 1*rankScale + 2
		}
		return 1*rankScale + 1
	}
	return tierBonus(tier)
}

// kbScore is a kb hit's score: matched tokens, the tier and confirmation
// prior, and the capped cross-reference boost. Zero matches score 0 (the
// empty-query listing).
func kbScore(matches int, tier string, c Confirmation, refs int) int {
	if matches == 0 {
		return 0
	}
	return matches*matchWeight + rankBonus(tier, c) + refsPrior(refs)*rankScale
}

// refsPrior caps the cross-reference boost: a heavily cited decision is
// familiar, not three times as relevant.
func refsPrior(refs int) int {
	if refs > 3 {
		return 3
	}
	return refs
}

// queryTokens lowercases + kebab-splits a query into terms of length ≥3.
func queryTokens(query string) []string {
	var toks []string
	for _, t := range strings.Split(normTopic(query), "-") {
		if len(t) >= 3 {
			toks = append(toks, t)
		}
	}
	return toks
}

// tokenScore counts how many distinct tokens appear in the haystack.
func tokenScore(haystack string, tokens []string) int {
	hay := strings.ToLower(haystack)
	n := 0
	for _, t := range tokens {
		if strings.Contains(hay, t) {
			n++
		}
	}
	return n
}

// Recall merges KB + session + git-spine hits for a query, ranked by score then
// recency, capped at limit. An empty query returns the most recent activity
// (sessions + commits) plus freshest KB topics — the "what have we been doing"
// view. Never reads source bodies.
func Recall(agent *Agent, query string, limit int) ([]RecallHit, error) {
	return recall(agent, query, limit, true, true)
}

// RecallActivity is Recall without the KB tier: session-records and the git
// spine only. Intuition uses it — filtering KB hits out AFTER a shared limit
// let a dense KB (whose hits carry a refs boost) crowd every activity hit out
// of the window, so intuition returned nothing exactly where it mattered.
func RecallActivity(agent *Agent, query string, limit int) ([]RecallHit, error) {
	return recall(agent, query, limit, false, true)
}

// RecallKnowledge is Recall without the activity tier and without the KB
// inbox: the peer's own knowledge entries plus the pool glossary. The
// per-prompt hook uses it so a matching entry reaches the agent even when it
// never runs `memory recall` itself.
func RecallKnowledge(agent *Agent, query string, limit int) ([]RecallHit, error) {
	hits, err := recall(agent, query, math.MaxInt, true, false)
	if err != nil {
		return nil, err
	}
	var out []RecallHit
	seen := map[string]bool{} // one pointer per file: a file's sections are separate topics
	for _, h := range hits {
		if h.Tier == TierInbox {
			continue
		}
		if len(h.Where) > 0 {
			if seen[h.Where[0]] {
				continue
			}
			seen[h.Where[0]] = true
		}
		if out = append(out, h); len(out) == limit {
			break
		}
	}
	return out, nil
}

func recall(agent *Agent, query string, limit int, includeKB, includeActivity bool) ([]RecallHit, error) {
	if limit < 1 {
		limit = 10
	}
	tokens := queryTokens(query)
	var hits []RecallHit

	// KB topics (authoritative; live). Inactive decisions are already gone
	// (computeTopics drops them); tiers rank what is left.
	if idx, err := topicIndex(agent); err == nil && includeKB {
		ledger := newLedgerCheck(agent.Dir()) // no ledger ⇒ unconfirmed
		for _, t := range idx.Topics {
			s := len(tokens) == 0 // empty query → include, score 0
			score := 0
			if !s {
				hay := t.Topic + " " + strings.Join(t.Tags, " ")
				score = tokenScore(hay, tokens)
			}
			if s || score > 0 {
				tier := bestTier(t.Sources)
				conf := ledger.topicConfirmation(t.Topic, t.Sources)
				hits = append(hits, RecallHit{
					Kind: "kb", Title: t.Topic, Where: t.Sources,
					Date: t.Freshness, Score: kbScore(score, tier, conf, t.Refs), Tier: tier,
					Confirmed: conf != ConfirmNone, ConfirmedBy: conf.By(),
				})
			}
		}
	}
	// The pool glossary: a current-tier source for every peer, via pool.
	// Independent of the peer's own knowledge/ being present.
	if includeKB {
		hits = append(hits, poolGlossaryHits(filepath.Dir(agent.Dir()), tokens)...)
	}

	// Session-records (episodic capture).
	for _, r := range activityRecords(agent, includeActivity) {
		hay := strings.Join([]string{
			r.FirstPrompt, r.Summary, strings.Join(r.Tools, " "),
			strings.Join(r.FilesTouched, " "), strings.Join(r.Commits, " "),
			strings.Join(r.Refs, " "),
		}, " ")
		score := 0
		if len(tokens) > 0 {
			score = tokenScore(hay, tokens) * matchWeight
			if score == 0 {
				continue
			}
		}
		title := r.Summary
		if title == "" {
			title = r.FirstPrompt
		}
		where := []string{"session:" + r.ID}
		if r.Handoff != "" {
			where = append(where, r.Handoff)
		}
		detail := ""
		if len(r.Refs) > 0 {
			detail = "refs: " + strings.Join(r.Refs, ", ")
		}
		hits = append(hits, RecallHit{
			Kind: "session", Title: truncate(title, 120), Where: where,
			Date: r.Ended, Detail: detail, Score: score, Confirmed: true,
		})
	}

	// Git time-spine floor: recent commits whose subject matches (always on, even
	// with zero captured sessions). Deduped against session-captured commits.
	if includeActivity {
		hits = append(hits, gitSpineHits(agent, tokens)...)
	}

	// Rank: score desc, then date desc (recency).
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Date > hits[j].Date
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// RecallWithBodies is Recall plus the text of the top current-tier KB hits,
// inlined in rank order until budgetTokens (≈4 chars per token) is spent. A
// hit whose source does not fit is left as a pointer; nothing is truncated
// mid-file except the last one that fits partially. Log-tier and inbox hits
// stay pointers: history is for following up, not for reading by default.
// Search is the peer's own; an inlined body's `mesh://` references are
// followed one hop to canonical targets within the same budget.
func RecallWithBodies(agent *Agent, query string, limit, budgetTokens int) ([]RecallHit, error) {
	hits, err := Recall(agent, query, limit)
	if err != nil || budgetTokens <= 0 {
		return hits, err
	}
	inlineBodies(agent, hits, budgetTokens, oneHopExpander(agent))
	return hits, nil
}

// inlineBodies fills Body for current-tier kb hits in rank order until
// budgetTokens (≈4 chars per token) is spent. expand, when set, may append
// text to a body before it is budgeted (one-hop references).
func inlineBodies(agent *Agent, hits []RecallHit, budgetTokens int, expand func(h *RecallHit, body string, remaining int) string) {
	root := filepath.Dir(agent.Dir())
	remaining := budgetTokens * 4
	for i := range hits {
		h := &hits[i]
		if h.Kind != "kb" || h.Tier != TierCurrent || len(h.Where) == 0 || remaining <= 0 {
			continue
		}
		var body string
		if h.Via != "" {
			// A via hit points at a knowledge reference; the resolver reads the
			// entry (a glossary term, a heading section, a peer's file), never
			// anything outside the owner's knowledge.
			text, ok, err := ResolveMeshRef(root, h.Where[0])
			if err != nil || !ok {
				continue
			}
			_, body = splitFrontmatter(text)
		} else {
			b, err := os.ReadFile(filepath.Join(agent.Dir(), h.Where[0]))
			if err != nil {
				continue
			}
			_, body = splitFrontmatter(string(b))
		}
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		if !h.Confirmed {
			body = hypothesisPrefix + body // a proposal, not truth
		}
		if expand != nil && len(body) < remaining {
			body = expand(h, body, remaining-len(body))
		}
		if len(body) > remaining {
			if remaining < 400 {
				break // too little left to be worth a fragment
			}
			body = body[:remaining] + "\n…(truncated — read " + h.Where[0] + ")"
		}
		h.Body = body
		remaining -= len(body)
	}
}

// hypothesisPrefix opens the inlined body of an unconfirmed hit.
const hypothesisPrefix = "_hypothesis_\n"

// RecallMarkdown renders hits as a list; a hit with a body gets it inlined
// under the pointer.
func RecallMarkdown(hits []RecallHit) string {
	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "- [%s", h.Kind)
		if h.Tier != "" {
			fmt.Fprintf(&b, "/%s", h.Tier)
		}
		if h.Via != "" {
			fmt.Fprintf(&b, "·via %s", h.Via)
		}
		if h.Kind == "kb" && h.Tier != TierInbox {
			// The inbox is unconfirmed by tier; a human confirmation is the
			// unmarked default.
			if c := h.confirmation(); c != ConfirmHuman {
				b.WriteString("·" + c.String())
			}
		}
		fmt.Fprintf(&b, "] %s", h.Title)
		if len(h.Where) > 0 {
			fmt.Fprintf(&b, " — %s", strings.Join(h.Where, "  "))
		}
		if h.Date != "" {
			fmt.Fprintf(&b, " (%s)", h.Date)
		}
		b.WriteString("\n")
		if h.Detail != "" {
			fmt.Fprintf(&b, "  %s\n", h.Detail)
		}
		if h.Body != "" {
			b.WriteString("\n  ```\n")
			for _, ln := range strings.Split(h.Body, "\n") {
				b.WriteString("  " + ln + "\n")
			}
			b.WriteString("  ```\n")
		}
	}
	return b.String()
}

// gitSpineHits reads recent commits from the agent's repo and returns those whose
// subject matches the query tokens (or the most recent, for an empty query). This
// is the harness-neutral floor: "what did we do" derived from git alone.
func gitSpineHits(agent *Agent, tokens []string) []RecallHit {
	dir := agent.Dir()
	if !dirExists(filepath.Join(dir, ".git")) {
		return nil
	}
	out, err := exec.Command("git", "-C", dir, "log", "-n", "50",
		"--format=%h\x1f%cI\x1f%s").Output()
	if err != nil {
		return nil
	}
	var hits []RecallHit
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(ln, "\x1f")
		if len(parts) != 3 {
			continue
		}
		sha, date, subj := parts[0], parts[1], parts[2]
		score := 0
		if len(tokens) > 0 {
			score = tokenScore(subj, tokens) * matchWeight
			if score == 0 {
				continue
			}
		}
		hits = append(hits, RecallHit{
			Kind: "commit", Title: truncate(subj, 120),
			Where: []string{sha}, Date: date, Score: score, Confirmed: true,
		})
	}
	return hits
}

// activityRecords is the agent's session records, or none when the activity
// tier is not wanted.
func activityRecords(agent *Agent, include bool) []*SessionRecord {
	if !include {
		return nil
	}
	return ListSessionRecords(agent)
}

// promptKnowledgeLimit is how many knowledge entries the per-prompt hook lists.
const promptKnowledgeLimit = 3

// PromptKnowledgeMarkdown renders knowledge hits for the per-prompt hook:
// pointers (title, path, standing), never bodies, so the cost per prompt
// stays a few lines. "" when there is nothing to list.
func PromptKnowledgeMarkdown(hits []RecallHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Your knowledge that matches this prompt (read the ones that apply before acting)\n")
	for _, h := range hits {
		where := ""
		if len(h.Where) > 0 {
			where = " — `" + h.Where[0] + "`"
		}
		fmt.Fprintf(&b, "- %s%s (%s)\n", h.Title, where, h.confirmation())
	}
	return strings.TrimRight(b.String(), "\n")
}
