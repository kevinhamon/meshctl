package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Handoff (ADR-0017) is a per-agent, git-tracked, authoritative "where we are +
// next step" record — the deliberate-tier complement to claude-mem's fuzzy
// episodic recall. Written at session end (or when attention shifts), read at
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
	content := header + strings.TrimRight(body, "\n") + "\n"
	return os.WriteFile(HandoffPath(agent), []byte(content), 0o644)
}
