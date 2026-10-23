package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// onboard_stanza_test.go — re-onboard refreshes the managed Cognition stanza
// in an already-onboarded peer's CLAUDE.md in place (story 109: one
// re-onboard picks everything up), and leaves human text around it alone.

const oldCognitionStanza = `## Cognition — maintain your own knowledge base (standing order)

You own a git-tracked ` + "`knowledge/`" + ` KB.

- **Distill as you learn.** OLD-STANZA-LINE run kb finish without a summary.

Authoritative spec: ` + "`.agentmesh/doctrine/agent-comms.md`" + ` §Knowledge & Memory
(ADR-0014–0018).
`

func TestOnboard_RefreshesStaleCognitionStanzaInPlace(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	claude := filepath.Join(a.Dir(), "CLAUDE.md")
	b, err := os.ReadFile(claude)
	if err != nil {
		t.Fatal(err)
	}
	cur := string(b)
	start := strings.Index(cur, "## Cognition — ")
	if start < 0 {
		t.Fatalf("fresh CLAUDE.md lacks the stanza:\n%s", cur)
	}
	end := strings.Index(cur[start:], "(ADR-0014–0018).\n") + start + len("(ADR-0014–0018).\n")
	stale := "# HUMAN-HEADER\n\n" + cur[:start] + oldCognitionStanza + cur[end:] + "\n## Human notes\n\nHUMAN-TRAILER keep me.\n"
	if err := os.WriteFile(claude, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(claude)
	got := string(b)
	if strings.Contains(got, "OLD-STANZA-LINE") {
		t.Fatalf("stale stanza not replaced:\n%s", got)
	}
	if !strings.Contains(got, "Align before you act") {
		t.Fatalf("current stanza text missing:\n%s", got)
	}
	if n := strings.Count(got, "## Cognition — "); n != 1 {
		t.Fatalf("want exactly one stanza, got %d:\n%s", n, got)
	}
	for _, keep := range []string{"# HUMAN-HEADER", "## Human notes\n\nHUMAN-TRAILER keep me."} {
		if !strings.Contains(got, keep) {
			t.Fatalf("human text %q lost:\n%s", keep, got)
		}
	}
	if strings.Contains(got, "__POOL_DIR__") {
		t.Fatalf("pool token left unrendered:\n%s", got)
	}

	// A second onboard is a no-op.
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(claude); string(b) != got {
		t.Fatalf("re-onboard of a current stanza changed CLAUDE.md:\n%s", b)
	}
}

func TestPoolUpgradePlan_NamesStaleCognitionStanza(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	claude := filepath.Join(a.Dir(), "CLAUDE.md")
	b, _ := os.ReadFile(claude)
	cur := string(b)
	start := strings.Index(cur, "## Cognition — ")
	end := strings.Index(cur[start:], "(ADR-0014–0018).\n") + start + len("(ADR-0014–0018).\n")
	if err := os.WriteFile(claude, []byte(cur[:start]+oldCognitionStanza+cur[end:]), 0o644); err != nil {
		t.Fatal(err)
	}
	p := planAgent(a, finishNow)
	if !strings.Contains(strings.Join(p.Actions, "\n"), "refresh Cognition stanza") {
		t.Fatalf("plan does not name the stale stanza: %v", p.Actions)
	}
}

// oldInboundBullet is the Agent Communication Inbound bullet every onboard
// wrote before ADR-0073: it claimed the oldest request on "check intake".
const oldInboundBullet = "- **Inbound:** at session start / on \"check intake\", run `meshctl inbox next --claim` (atomic claim) or scan `intake/*.md` for `status: pending`; process by `type`, then write the outcome back with `meshctl inbox respond <id> --status answered|filed|needs-info|needs-human --response \"…\"` (intake files are meshctl-owned — never hand-edit them). **Not yours?** `meshctl inbox decline <id> --reason \"…\" --redirect <owner>` — don't half-answer out-of-domain work.\n"

func TestOnboard_RefreshesStaleInboundOrderInPlace(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	claude := filepath.Join(a.Dir(), "CLAUDE.md")
	stale := "# HUMAN-HEADER\n\n## Agent Communication (intake)\n\nHUMAN-INTRO keep me.\n\n" + oldInboundBullet + "- **Read-back:** HUMAN-BULLET keep me.\n"
	if err := os.WriteFile(claude, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := planAgent(a, finishNow); !strings.Contains(strings.Join(p.Actions, "\n"), "refresh Agent Communication Inbound order") {
		t.Fatalf("plan does not name the stale Inbound order: %v", p.Actions)
	}

	if _, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(claude)
	got := string(b)
	if strings.Contains(got, "inbox next --claim") {
		t.Fatalf("stale Inbound bullet not replaced:\n%s", got)
	}
	for _, want := range []string{"\"check intake\" shows the queue and stops", "`meshctl inbox list`", "`meshctl inbox claim <id>`", "📝 **C<n> — <short title>**"} {
		if !strings.Contains(got, want) {
			t.Fatalf("current Inbound text missing %q:\n%s", want, got)
		}
	}
	for _, keep := range []string{"# HUMAN-HEADER", "HUMAN-INTRO keep me.", "HUMAN-BULLET keep me."} {
		if !strings.Contains(got, keep) {
			t.Fatalf("human text %q lost:\n%s", keep, got)
		}
	}
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(claude); string(b) != got {
		t.Fatalf("re-onboard of a current Inbound order changed CLAUDE.md:\n%s", b)
	}
	if p := planAgent(a, finishNow); strings.Contains(strings.Join(p.Actions, "\n"), "Inbound order") {
		t.Fatalf("a current CLAUDE.md still plans an Inbound refresh: %v", p.Actions)
	}
}
