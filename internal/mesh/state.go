package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// State digest — knowledge/state.md.
//
// A KB has two tiers. The log tier (decisions/, experience/) is append-only
// and grows without bound; the current tier (architecture/, playbooks/,
// reference/, direction.md, risks.md) is maintained in place and states what
// is true now. state.md is the current tier's front page: a digest at most
// StateMaxLines long that a session reads before anything else, injected at
// session start beside the handoff. The handoff says where the last session
// stopped; the state digest says what the world looks like. `kb consolidate`
// lists the decisions the digest has not absorbed yet; the agent rewrites it
// with `meshctl state set --summary-file <summary>` (a promotion).
const StateFile = "state.md"

// StatePath is knowledge/state.md for an agent.
func StatePath(agent *Agent) string {
	return filepath.Join(agent.Dir(), KnowledgeDir, StateFile)
}

// StateShow returns the current digest, or ("", nil) when none exists yet.
func StateShow(agent *Agent) (string, error) {
	b, err := os.ReadFile(StatePath(agent))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// StateTemplate is the one shape the digest takes — form-filling, like the
// handoff. Facts a new session must not contradict, not a narrative.
const StateTemplate = `## Now
<3-8 lines: the standing truths of this agent's domain today — what runs where, what the rules are. A new session must not contradict these.>

## Active
- <initiative / migration in flight — where it stands> (or: none)

## Recently decided
- ADR-NNNN <one line: what changed because of it> (or: none)

## Open questions
- <unresolved conflict / pending decision — who owns it> (or: none)
`

// StateMaxLines is the digest budget; longer and it is a second direction.md.
const StateMaxLines = 40

var stateSections = []string{"## Now", "## Active", "## Recently decided", "## Open questions"}

// StateLint returns the problems with a digest body: missing template
// sections, unfilled placeholders, over budget. `state set` refuses on any.
func StateLint(body string) []string {
	var out []string
	for _, s := range stateSections {
		if !strings.Contains(body, s) {
			out = append(out, "missing section "+s)
		}
	}
	if strings.Contains(body, "<3-8 lines") || strings.Contains(body, "ADR-NNNN") {
		out = append(out, "template placeholder left in — replace every <…> line")
	}
	if n := bodyLines(stripStateHeader(body)); n > StateMaxLines {
		out = append(out, fmt.Sprintf("%d lines (budget %d) — a digest, not a second direction.md", n, StateMaxLines))
	}
	return out
}

// StateSet writes the digest with a generated stamp header. Overwrites (git
// keeps history). Lint is the caller's gate.
func StateSet(agent *Agent, body string, now time.Time) error {
	kb := filepath.Join(agent.Dir(), KnowledgeDir)
	if err := os.MkdirAll(kb, 0o755); err != nil {
		return err
	}
	header := fmt.Sprintf("<!-- meshctl state — %s updated %s. Current-state digest; read at session start, rewritten by `meshctl state set --summary-file <summary>` after consolidation. -->\n\n",
		agent.Name, now.UTC().Format(time.RFC3339))
	content := header + strings.TrimRight(stripStateHeader(body), "\n") + "\n"
	return os.WriteFile(StatePath(agent), []byte(content), 0o644)
}

func stripStateHeader(body string) string {
	lines := strings.Split(body, "\n")
	for len(lines) > 0 && (strings.HasPrefix(strings.TrimSpace(lines[0]), "<!-- meshctl state") || strings.TrimSpace(lines[0]) == "") {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

// bodyLines counts the lines of a trimmed body.
func bodyLines(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// StateBlock renders the digest for injection (session start, brief). Empty
// when there is no digest.
func StateBlock(agent *Agent) string {
	s, _ := StateShow(agent)
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return "# Current state — digest (knowledge/state.md; the KB files hold the detail)\n\n" + s
}
