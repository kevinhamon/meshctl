package mesh

import (
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Unified recall (ADR-0033): one contract over three sources —
//   - KB topics (durable, authoritative; live-computed, no persisted index),
//   - session-records (owned episodic capture),
//   - the git time-spine (always-on floor: recent commits, even before any
//     session was captured).
// Returns compact pointers (title + where + date), never file bodies — the
// caller reads the KB/handoff/session that a hit points to. Backend is
// keyword/BM25-ish today; embeddings slot behind this same function later.

// RecallHit is one compact pointer from any recall source.
type RecallHit struct {
	Kind   string   `json:"kind"`             // "kb" | "session" | "commit"
	Title  string   `json:"title"`            // topic, opening ask, or commit subject
	Where  []string `json:"where,omitempty"`  // KB paths, session id + handoff ref, or commit sha
	Date   string   `json:"date,omitempty"`   // freshness / ended / commit time
	Detail string   `json:"detail,omitempty"` // refs, files, extra context
	Score  int      `json:"score"`
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
	if limit < 1 {
		limit = 10
	}
	tokens := queryTokens(query)
	var hits []RecallHit

	// KB topics (authoritative; live).
	if idx, err := computeTopics(agent); err == nil {
		for _, t := range idx.Topics {
			s := len(tokens) == 0 // empty query → include, score 0
			score := 0
			if !s {
				hay := t.Topic + " " + strings.Join(t.Tags, " ")
				score = tokenScore(hay, tokens)
			}
			if s || score > 0 {
				hits = append(hits, RecallHit{
					Kind: "kb", Title: t.Topic, Where: t.Sources,
					Date: t.Freshness, Score: score + t.Refs, // KB refs as a mild prior
				})
			}
		}
	}

	// Session-records (episodic capture).
	for _, r := range ListSessionRecords(agent) {
		hay := strings.Join([]string{
			r.FirstPrompt, r.Summary, strings.Join(r.Tools, " "),
			strings.Join(r.FilesTouched, " "), strings.Join(r.Commits, " "),
			strings.Join(r.Refs, " "),
		}, " ")
		score := 0
		if len(tokens) > 0 {
			score = tokenScore(hay, tokens)
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
			Date: r.Ended, Detail: detail, Score: score,
		})
	}

	// Git time-spine floor: recent commits whose subject matches (always on, even
	// with zero captured sessions). Deduped against session-captured commits.
	hits = append(hits, gitSpineHits(agent, tokens)...)

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
			score = tokenScore(subj, tokens)
			if score == 0 {
				continue
			}
		}
		hits = append(hits, RecallHit{
			Kind: "commit", Title: truncate(subj, 120),
			Where: []string{sha}, Date: date, Score: score,
		})
	}
	return hits
}
