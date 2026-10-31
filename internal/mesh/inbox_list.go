package mesh

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// inbox_list.go — the clean intake queue `meshctl inbox list` prints:
// open requests only by default, in the order an agent proposes
// to the human on "check intake" — priority, then age.

// InboxQueue returns the agent's requests in queue order: priority (P1 first;
// a missing or unknown priority counts as P2), then oldest created first,
// then id. Unless all, only pending and in-progress requests are kept.
func InboxQueue(a *Agent, all bool) ([]*Request, error) {
	reqs, err := ListRequests(a.InboxPath())
	if err != nil {
		return nil, err
	}
	var q []*Request
	for _, r := range reqs {
		if s := canonicalOr(r.Status); all || s == StatusPending || s == StatusInProgress {
			q = append(q, r)
		}
	}
	sort.SliceStable(q, func(i, j int) bool {
		if pi, pj := priorityRank(q[i].Priority), priorityRank(q[j].Priority); pi != pj {
			return pi < pj
		}
		if ci, cj := strings.TrimSpace(q[i].Created), strings.TrimSpace(q[j].Created); ci != cj {
			return ci < cj
		}
		return q[i].ID < q[j].ID
	})
	return q, nil
}

// priorityRank orders P1 < P2 < P3; anything else ranks as the P2 default.
func priorityRank(p string) int {
	switch strings.ToUpper(strings.TrimSpace(p)) {
	case "P1":
		return 1
	case "P3":
		return 3
	}
	return 2
}

// InboxQueueMarkdown renders a queue as the numbered list an agent reads to
// the human: `N. <id> · from <who> · <type> · <priority> · <age>d · <ask>`.
// A status other than pending is named after the age.
func InboxQueueMarkdown(q []*Request, now time.Time) string {
	if len(q) == 0 {
		return "(no open requests)\n"
	}
	var b strings.Builder
	for i, r := range q {
		pri := strings.TrimSpace(r.Priority)
		if pri == "" {
			pri = "P2"
		}
		fmt.Fprintf(&b, "%d. %s · from %s · %s · %s · %dd", i+1, r.ID, r.From, r.Type, pri, requestAgeDays(r, now))
		if s := canonicalOr(r.Status); s != StatusPending {
			b.WriteString(" · " + s)
		}
		b.WriteString(" · " + askGist(r.Body) + "\n")
	}
	return b.String()
}
