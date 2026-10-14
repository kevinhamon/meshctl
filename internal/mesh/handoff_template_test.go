package mesh

import (
	"strings"
	"testing"
	"time"
)

func TestHandoffLint(t *testing.T) {
	filled := "## Current State\nDone.\n\n## Next Step\nShip it.\n\n## Open Threads\n- none\n\n## Pointers\n- none\n"
	if ws := HandoffLint(filled); len(ws) != 0 {
		t.Fatalf("a filled template must lint clean: %v", ws)
	}
	if ws := HandoffLint(HandoffTemplate); !strings.Contains(strings.Join(ws, "\n"), "placeholder") {
		t.Fatalf("an unfilled template must be flagged: %v", ws)
	}
	if ws := HandoffLint("## Current State\nx\n"); len(ws) < 3 {
		t.Fatalf("missing sections must be flagged: %v", ws)
	}
	long := filled + strings.Repeat("- more\n", HandoffMaxLines)
	if ws := HandoffLint(long); !strings.Contains(strings.Join(ws, "\n"), "budget") {
		t.Fatalf("over-budget handoff must be flagged: %v", ws)
	}
}

// Rewriting from `handoff show` output must not stack timestamp headers.
func TestHandoffSet_StripsPreviousHeader(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	if err := HandoffSet(a, "## Current State\nv1\n", time.Now()); err != nil {
		t.Fatal(err)
	}
	prev, _ := HandoffShow(a)
	if err := HandoffSet(a, strings.Replace(prev, "v1", "v2", 1), time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ := HandoffShow(a)
	if n := strings.Count(got, "<!-- meshctl handoff"); n != 1 || !strings.Contains(got, "v2") {
		t.Fatalf("want exactly one header and the new body, got %d headers:\n%s", n, got)
	}
}
