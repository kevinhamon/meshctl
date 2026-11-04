package mesh

import (
	"os"
	"strings"
	"testing"
	"time"
)

// session_start_budget_test.go — Claude Code caps a SessionStart hook's
// additionalContext at 10,000 characters; over it, the agent sees only a
// 2,000-char preview. The block meshctl injects stays under
// SessionStartBudget by trimming the state digest first, with a pointer to the
// full file.

func writeStateAndHandoff(t *testing.T, a *Agent, state, handoff string) {
	t.Helper()
	if err := os.WriteFile(StatePath(a), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(HandoffPath(a), []byte(handoff), 0o644); err != nil {
		t.Fatal(err)
	}
}

func bigText(prefix string, lines int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString(prefix + " line with enough words to take up some real room in the digest body\n")
	}
	return b.String()
}

func TestSessionStart_OverBudgetTrimsDigestKeepsHandoff(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	writeStateAndHandoff(t, a, "STATE-FIRST\n"+bigText("state", 200), "HANDOFF-KEEP resume here\n")

	ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s1"}, 1, time.Now())
	if len(ctx) > SessionStartBudget {
		t.Fatalf("session start is %d chars, over the %d budget", len(ctx), SessionStartBudget)
	}
	for _, want := range []string{"STATE-FIRST", "HANDOFF-KEEP resume here", "read `knowledge/" + StateFile + "`"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("trimmed session start lacks %q:\n%s", want, ctx)
		}
	}
}

func TestSessionStart_UnderBudgetUntouched(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	writeStateAndHandoff(t, a, "STATE-SMALL\n", "HANDOFF-SMALL\n")
	ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s1"}, 1, time.Now())
	if strings.Contains(ctx, "trimmed to fit") || !strings.Contains(ctx, "STATE-SMALL") {
		t.Fatalf("small session start was altered:\n%s", ctx)
	}
}

func TestSessionStart_OversizeHandoffTrimmedToo(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	writeStateAndHandoff(t, a, bigText("state", 200), "HANDOFF-FIRST\n"+bigText("handoff", 200))
	ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s1"}, 1, time.Now())
	if len(ctx) > SessionStartBudget {
		t.Fatalf("session start is %d chars, over the %d budget", len(ctx), SessionStartBudget)
	}
	if !strings.Contains(ctx, "HANDOFF-FIRST") || !strings.Contains(ctx, "read `knowledge/"+HandoffFile+"`") {
		t.Fatalf("handoff not trimmed with a pointer:\n%s", ctx)
	}
}

func TestDoctor_WarnsWhenSessionStartIsTrimmed(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	writeStateAndHandoff(t, a, bigText("state", 200), "h\n")
	got := findingsText(doctorAgentFindings(a, PoolConfig{}, nil, false))
	if !strings.Contains(got, "session start is trimmed") {
		t.Fatalf("doctor does not warn about the trimmed session start:\n%s", got)
	}
	if fileExists(sessionPath(a.Dir(), "doctor")) {
		t.Fatal("doctor registered a session")
	}
}
