package mesh

import (
	"fmt"
	"strings"
)

// The intuition tier (ADR-0016, ADR-0033): compact, deduped pointers (title +
// date) to past work, from the owned engine — episodic session records plus the
// git time-spine. Fail-open: any error yields no hits (intuition is advisory;
// it must never break a caller).

// IntuitionHit is one compact pointer into episodic memory.
type IntuitionHit struct {
	Title string `json:"title"`
	Date  string `json:"date,omitempty"`
}

// intuitionMinQuery is the shortest prompt the per-prompt hook recalls for:
// shorter prompts carry too little to match on.
const intuitionMinQuery = 20

// RecallIntuition returns compact past-work pointers for the injection surface:
// episodic session records + the git time-spine, via RecallActivity, so
// intuition works on any harness with no external daemon.
func RecallIntuition(agent *Agent, query string, limit int) []IntuitionHit {
	if limit < 1 {
		limit = 3
	}
	if agent == nil {
		return nil
	}
	// Intuition = past *activity*; KB topics are `memory recall`'s job, so the
	// KB tier is excluded before ranking, not filtered after the limit.
	hits, err := RecallActivity(agent, query, limit)
	if err != nil {
		return nil
	}
	var out []IntuitionHit
	for _, h := range hits {
		out = append(out, IntuitionHit{Title: h.Title, Date: h.Date})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// IntuitionMarkdown renders hits as the compact injected block (empty if none).
func IntuitionMarkdown(hits []IntuitionHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Intuition (past work — pointers; read the KB for detail)\n")
	for _, h := range hits {
		if h.Date != "" {
			fmt.Fprintf(&b, "- %s (%s)\n", h.Title, h.Date)
		} else {
			fmt.Fprintf(&b, "- %s\n", h.Title)
		}
	}
	return b.String()
}
