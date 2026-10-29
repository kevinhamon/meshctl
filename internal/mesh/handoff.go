package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Handoff (ADR-0017) is a per-agent, git-tracked, authoritative "where we are +
// next step" record — the deliberate-tier complement to the fuzzy episodic
// recall of session records. Written at session end (or when attention shifts), read at
// session start, so work resumes precisely. Distinct from strategic State
// (direction.md/risks.md) and from cross-agent intake requests.
//
// One current file per agent: knowledge/handoff.md (overwritten; git log is the
// history). It lives in knowledge/, so `memory recall` routes to it too.
const HandoffFile = "handoff.md"

// HandoffPath is knowledge/handoff.md for an agent.
func HandoffPath(agent *Agent) string {
	return filepath.Join(agent.Dir(), KnowledgeDir, HandoffFile)
}

// HandoffShow returns the current handoff, or ("", nil) if none exists yet
// (fail-open — the SessionStart surface must never error on a fresh agent).
func HandoffShow(agent *Agent) (string, error) {
	b, err := os.ReadFile(HandoffPath(agent))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// HandoffSet writes body to knowledge/handoff.md with a generated timestamp
// header. The body is authored by the agent (it knows the WIP); this just stamps
// + persists it. Overwrites the current handoff (git preserves prior versions).
func HandoffSet(agent *Agent, body string, now time.Time) error {
	kb := filepath.Join(agent.Dir(), KnowledgeDir)
	if err := os.MkdirAll(kb, 0o755); err != nil {
		return err
	}
	header := fmt.Sprintf("<!-- meshctl handoff — %s updated %s. Authoritative resume point; read at session start, rewrite at session end. -->\n\n",
		agent.Name, now.UTC().Format(time.RFC3339))
	content := header + strings.TrimRight(stripHandoffHeader(body), "\n") + "\n"
	return os.WriteFile(HandoffPath(agent), []byte(content), 0o644)
}

// HandoffCommitSubject is the subject of the commit `handoff set` makes.
const HandoffCommitSubject = "meshctl: session handoff"

// HandoffCommit commits knowledge/handoff.md alone in the agent's repo, as
// `state set` commits its digest (issue #6), so a written handoff is never left
// for the next session to find uncommitted. Returns the short sha, or "" when
// the repo is not git or the handoff has no change to commit.
func HandoffCommit(agent *Agent) (string, error) {
	if !isGitRepo(agent.Dir()) {
		return "", nil
	}
	rel := KnowledgeDir + "/" + HandoffFile
	out, err := git(agent.Dir(), "status", "--porcelain", "--", rel)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", nil
	}
	if _, err := git(agent.Dir(), "add", "--", rel); err != nil {
		return "", err
	}
	if _, err := git(agent.Dir(), "commit", "-q", "-m", HandoffCommitSubject, "--", rel); err != nil {
		return "", err
	}
	sha, err := git(agent.Dir(), "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(sha), err
}

// HandoffTemplate is the one shape a handoff takes. It is fixed — headings,
// order, budget — so writing one is form-filling, not a design exercise: a
// model given only "write a handoff" deliberates over format, length and tone
// before it writes a word.
const HandoffTemplate = `## Current State
<2-6 lines: what is true right now — done, in progress, where things live. Facts, not a session diary.>

## Next Step
<The single next action, concrete enough to start cold: what, where, which command.>

## Open Threads
- <blocker / pending decision / follow-up — who owns it> (or: none)
- Deferred: <ids> (only when the human deferred pending work; drop this line otherwise)

## Pointers
- <paths, commits, intake ids, URLs the next session needs> (or: none)
`

// HandoffMaxLines is the budget a handoff should fit in; longer ones are
// journals, which the next session skims instead of reads.
const HandoffMaxLines = 40

var handoffSections = []string{"## Current State", "## Next Step", "## Open Threads", "## Pointers"}

// HandoffLint returns advisory problems with a handoff body (never blocking):
// missing template sections, unfilled placeholders, over budget.
func HandoffLint(body string) []string {
	var out []string
	for _, s := range handoffSections {
		if !strings.Contains(body, s) {
			out = append(out, "missing section "+s)
		}
	}
	if strings.Contains(body, "<2-6 lines") || strings.Contains(body, "<The single next action") {
		out = append(out, "template placeholder left in — replace every <…> line")
	}
	if n := len(strings.Split(strings.TrimSpace(stripHandoffHeader(body)), "\n")); n > HandoffMaxLines {
		out = append(out, fmt.Sprintf("%d lines (budget %d) — cut to what the next session needs", n, HandoffMaxLines))
	}
	return out
}

// stripHandoffHeader drops a previous stamp line (and leading blank lines), so
// a body built by editing `handoff show` output does not accumulate headers.
func stripHandoffHeader(body string) string {
	lines := strings.Split(body, "\n")
	for len(lines) > 0 && (strings.HasPrefix(strings.TrimSpace(lines[0]), "<!-- meshctl handoff") || strings.TrimSpace(lines[0]) == "") {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}
