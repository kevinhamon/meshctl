package mesh

import (
	"strings"
	"testing"
	"time"
)

// The per-prompt hook lists the peer's own matching knowledge as pointers:
// one per file (a file's sections are separate topics), never the KB inbox,
// never session or commit activity, at most promptKnowledgeLimit.
func TestRecallKnowledge_OnePointerPerFileNoInbox(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "coursework", true, nil)
	writeKB(t, a, "experience/peer-reply-shape.md", "# Peer reply shape\n\n## Peer reply voice\n\nShort.\n\n## Peer reply openers\n\nNo hedging.\n")
	writeKB(t, a, "playbooks/peer-reply.md", "# Peer reply playbook\n\nSteps.\n")
	writeKB(t, a, "candidates/peer-reply-idea.md", "# Peer reply idea\n\nMaybe.\n")

	hits, err := RecallKnowledge(a, "write my peer reply", 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, h := range hits {
		if h.Kind != "kb" || len(h.Where) == 0 {
			t.Fatalf("only knowledge pointers, got %+v", h)
		}
		if strings.Contains(h.Where[0], "candidates/") {
			t.Fatalf("the KB inbox is not listed: %+v", hits)
		}
		seen[h.Where[0]]++
	}
	if seen["knowledge/experience/peer-reply-shape.md"] != 1 || seen["knowledge/playbooks/peer-reply.md"] != 1 {
		t.Fatalf("want one pointer per file for both entries, got %v", seen)
	}
	if one, _ := RecallKnowledge(a, "write my peer reply", 1); len(one) != 1 {
		t.Fatalf("limit not applied: %+v", one)
	}
}

func TestHookUserPrompt_ListsMatchingKnowledge(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "coursework", true, nil)
	writeKB(t, a, "experience/peer-reply-shape.md", "# Peer reply shape\n\nShort.\n")

	out := HookUserPrompt(dir, HookPayload{Cwd: a.Dir(), Prompt: "please draft the peer reply for unit four"}, time.Now())
	if !strings.Contains(out, "## Your knowledge that matches this prompt") || !strings.Contains(out, "`knowledge/experience/peer-reply-shape.md`") {
		t.Fatalf("matching knowledge not listed:\n%s", out)
	}
	if out := HookUserPrompt(dir, HookPayload{Cwd: a.Dir(), Prompt: "peer reply"}, time.Now()); strings.Contains(out, "Your knowledge") {
		t.Fatalf("a prompt under the intuition minimum lists nothing:\n%s", out)
	}
}
