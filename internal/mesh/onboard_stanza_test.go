package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// onboard_stanza_test.go — the mesh standing orders live in a managed block in
// the gitignored CLAUDE.local.md (standing_orders.go, issues #12 and #19).
// Onboard moves the copies an earlier meshctl wrote into CLAUDE.md out, keeps
// the human's text, and rewrites the block whole; a second run changes nothing.

const oldCognitionStanza = `## Cognition — maintain your own knowledge base (standing order)

You own a git-tracked ` + "`knowledge/`" + ` KB. Run the ` + "`mesh-cognition`" + ` skill.

- **Distill as you learn.** OLD-STANZA-LINE run kb finish without a summary.

Authoritative spec: ` + "`.agentmesh/doctrine/agent-comms.md`" + ` §Knowledge & Memory
(ADR-0014–0018).
`

// oldInboundBullet is the Agent Communication Inbound bullet every onboard
// wrote before the clean queue: it claimed the oldest request on "check intake".
const oldInboundBullet = "- **Inbound:** at session start / on \"check intake\", run `meshctl inbox next --claim` (atomic claim) or scan `intake/*.md` for `status: pending`; process by `type`, then write the outcome back with `meshctl inbox respond <id> --status answered|filed|needs-info|needs-human --response \"…\"` (intake files are meshctl-owned — never hand-edit them). **Not yours?** `meshctl inbox decline <id> --reason \"…\" --redirect <owner>` — don't half-answer out-of-domain work.\n"

// legacyCognitionStanza is the stanza the retired templates/ tree wrote: no
// ADR closing line, and no docs/ bullet (issue #11).
const legacyCognitionStanza = `## Cognition — maintain your own knowledge base (standing order)

Run the **` + "`mesh-cognition`" + ` skill** for the full procedure; its short form:

- **Handoff at end.** Rewrite ` + "`knowledge/handoff.md`" + ` via ` + "`meshctl handoff set`" + `.
- **Repo hygiene.** Your workspace must be its own git repo **with a remote**. Session start nags until a remote exists.

Authoritative spec: your pool's ` + "`../pool/doctrine/agent-comms.md`" + ` §Knowledge & Memory.
`

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func actions(p AgentUpgradePlan) string { return strings.Join(p.Actions, "\n") }

// A fresh workspace: role text in CLAUDE.md, the current block in
// CLAUDE.local.md, gitignored, nothing to plan.
func TestScaffold_StandingOrdersInClaudeLocal(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	claude := readFile(t, filepath.Join(a.Dir(), "CLAUDE.md"))
	if strings.Contains(claude, commsHeading) || strings.Contains(claude, cognitionStanzaHeading) {
		t.Fatalf("fresh CLAUDE.md carries standing orders:\n%s", claude)
	}
	local := readFile(t, filepath.Join(a.Dir(), ClaudeLocalFile))
	if local != standingOrdersBlock(dir, true) {
		t.Fatalf("fresh %s is not the current block:\n%s", ClaudeLocalFile, local)
	}
	for _, want := range []string{"Hub session — work a peer owns", "Align before you act", inboundStanza} {
		if !strings.Contains(local, want) {
			t.Fatalf("block lacks %q", want)
		}
	}
	if gi := readFile(t, filepath.Join(a.Dir(), ".gitignore")); !strings.Contains(gi, "\n"+ClaudeLocalFile+"\n") {
		t.Fatalf(".gitignore does not ignore %s:\n%s", ClaudeLocalFile, gi)
	}
	if acts := actions(planAgent(a, finishNow)); strings.Contains(acts, "standing orders") {
		t.Fatalf("a fresh workspace plans a standing-orders change: %s", acts)
	}
}

// An agent onboarded by an earlier meshctl: both stanzas move out of CLAUDE.md,
// stale ones included, and the human's text around them stays.
func TestOnboard_MovesStandingOrdersOutOfClaudeMd(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	claudePath := filepath.Join(a.Dir(), "CLAUDE.md")
	old := "# HUMAN-HEADER\n\n## Role\n\nHUMAN-ROLE keep me.\n\n" + commsHeading + "\n\n" + oldInboundBullet +
		"\n## Guardrails\n\nHUMAN-GUARD keep me.\n\n" + oldCognitionStanza + "\n## Human notes\n\nHUMAN-TRAILER keep me.\n"
	if err := os.WriteFile(claudePath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if acts := actions(planAgent(a, finishNow)); !strings.Contains(acts, "move the mesh standing orders out of CLAUDE.md") {
		t.Fatalf("plan does not name the move: %s", acts)
	}
	if !strings.Contains(findingsText(doctorAgentFindings(a, PoolConfig{}, nil, false)), "CLAUDE.md still carries the mesh standing orders") {
		t.Fatal("doctor does not flag standing orders left in CLAUDE.md")
	}
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, claudePath)
	want := "# HUMAN-HEADER\n\n## Role\n\nHUMAN-ROLE keep me.\n\n## Guardrails\n\nHUMAN-GUARD keep me.\n\n## Human notes\n\nHUMAN-TRAILER keep me.\n"
	if got != want {
		t.Fatalf("CLAUDE.md after the move:\n%s\nwant:\n%s", got, want)
	}
	if local := readFile(t, filepath.Join(a.Dir(), ClaudeLocalFile)); local != standingOrdersBlock(dir, true) {
		t.Fatalf("%s is not the current block:\n%s", ClaudeLocalFile, local)
	}
	// Idempotent: nothing left to do, nothing rewritten.
	res, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Join(res.Notes, "\n"); strings.Contains(n, "standing orders") {
		t.Fatalf("second onboard touched the standing orders: %s", n)
	}
	if readFile(t, claudePath) != want {
		t.Fatal("second onboard changed CLAUDE.md")
	}
}

func TestOnboard_MovesLegacyCognitionStanza(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	claudePath := filepath.Join(a.Dir(), "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# Role\n\n"+legacyCognitionStanza+"\n## Human notes\n\nHUMAN-TRAILER keep me.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, claudePath); got != "# Role\n\n## Human notes\n\nHUMAN-TRAILER keep me.\n" {
		t.Fatalf("legacy stanza not moved out:\n%s", got)
	}
}

// A stanza whose ending meshctl does not know stays, and is reported.
func TestStandingOrders_UnrecognizedCognitionStanzaIsKeptAndReported(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	claudePath := filepath.Join(a.Dir(), "CLAUDE.md")
	edited := "# Role\n\n" + cognitionStanzaHeading + "\n\nRun the mesh-cognition skill. Hand-rewritten, no known ending.\n"
	if err := os.WriteFile(claudePath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(planAgent(a, finishNow).Warnings, "\n"); !strings.Contains(w, "no ending meshctl recognises") {
		t.Fatalf("plan does not warn: %s", w)
	}
	if !strings.Contains(findingsText(doctorAgentFindings(a, PoolConfig{}, nil, false)), "no ending meshctl recognises") {
		t.Fatal("doctor does not warn")
	}
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, claudePath) != edited {
		t.Fatal("an unrecognised stanza was rewritten")
	}
}

// The human's own text in CLAUDE.local.md stays; a stale or edited block is
// replaced in place.
func TestOnboard_RefreshesBlockKeepsLocalHumanText(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	localPath := filepath.Join(a.Dir(), ClaudeLocalFile)
	block := standingOrdersBlock(dir, true)
	edited := "MY-LOCAL-NOTE keep me.\n\n" + strings.Replace(block, "Align before you act", "EDITED-INSIDE", 1) + "\nMY-TRAILER keep me.\n"
	if err := os.WriteFile(localPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if acts := actions(planAgent(a, finishNow)); !strings.Contains(acts, "refresh the mesh standing orders in "+ClaudeLocalFile) {
		t.Fatalf("plan does not name the refresh: %s", acts)
	}
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, localPath), "MY-LOCAL-NOTE keep me.\n\n"+block+"\nMY-TRAILER keep me.\n"; got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Issue #19: standing orders moved into CLAUDE.local.md by hand become the
// managed block, once, and the tracked CLAUDE.md is never written.
func TestOnboard_HandMovedStandingOrdersBecomeTheBlock(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	claudePath := filepath.Join(a.Dir(), "CLAUDE.md")
	neutral := "# Shared team repo\n\nTool-neutral notes.\n"
	if err := os.WriteFile(claudePath, []byte(neutral), 0o644); err != nil {
		t.Fatal(err)
	}
	hand := "PERSONAL keep me.\n\n" + strings.TrimSpace(renderPoolTokens(dir, commsSection)) + "\n\n" + strings.TrimSpace(renderPoolTokens(dir, oldCognitionStanza)) + "\n"
	localPath := filepath.Join(a.Dir(), ClaudeLocalFile)
	if err := os.WriteFile(localPath, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, claudePath) != neutral {
		t.Fatal("onboard wrote into the tracked CLAUDE.md")
	}
	got := readFile(t, localPath)
	if got != "PERSONAL keep me.\n\n"+standingOrdersBlock(dir, true) {
		t.Fatalf("hand-moved orders not turned into the block:\n%s", got)
	}
	if n := strings.Count(got, commsHeading); n != 1 {
		t.Fatalf("want one Agent Communication section, got %d", n)
	}
}

// opencode loads neither CLAUDE file natively: the V2 plugin injects
// CLAUDE.local.md after CLAUDE.md, and V1 lists both under instructions.
func TestOpencode_LoadsClaudeLocal(t *testing.T) {
	if !strings.Contains(opencodePluginV2JS, "CLAUDE.local.md") {
		t.Fatal("V2 plugin does not inject CLAUDE.local.md")
	}
	dest := t.TempDir()
	if _, err := ensureOpencodeConfig(dest, 1); err != nil {
		t.Fatal(err)
	}
	if cfg := readFile(t, filepath.Join(dest, "opencode.json")); !strings.Contains(cfg, ClaudeLocalFile) {
		t.Fatalf("opencode.json instructions lack %s:\n%s", ClaudeLocalFile, cfg)
	}
}

func findingsText(fs []DoctorFinding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Message)
	}
	return strings.Join(out, "\n")
}

// Issue #19: onboard recreates intake/README.md when intake/ exists without
// it (the gate refuses a hand-written copy); doctor and the plan flag it.
func TestOnboard_RecreatesMissingIntakeReadme(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	readme := filepath.Join(a.Dir(), IntakeDir, "README.md")
	if err := os.Remove(readme); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(findingsText(doctorAgentFindings(a, PoolConfig{}, nil, false)), IntakeDir+"/README.md missing") {
		t.Fatal("doctor does not flag the missing intake README")
	}
	if acts := actions(planAgent(a, finishNow)); !strings.Contains(acts, "recreate "+IntakeDir+"/README.md") {
		t.Fatalf("plan does not name the missing README: %s", acts)
	}
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(readme) {
		t.Fatal("onboard did not recreate intake/README.md")
	}
}

// A customised Agent Communication section holds the human's role-specific
// lines: it stays in CLAUDE.md untouched, the Cognition stanza still moves, and
// the managed block leaves its own Agent Communication section out.
func TestOnboard_KeepsCustomisedCommsSection(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	claudePath := filepath.Join(a.Dir(), "CLAUDE.md")
	custom := commsHeading + "\n\n" + oldInboundBullet + "- Architect proposes/advises; ROLE-SPECIFIC keep me.\n"
	if err := os.WriteFile(claudePath, []byte("# Role\n\n"+custom+"\n"+oldCognitionStanza), 0o644); err != nil {
		t.Fatal(err)
	}
	p := planAgent(a, finishNow)
	if w := strings.Join(p.Warnings, "\n"); !strings.Contains(w, "CLAUDE.md keeps a customised Agent Communication section") {
		t.Fatalf("plan does not warn about the customised section: %s", w)
	}
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, claudePath); got != "# Role\n\n"+custom {
		t.Fatalf("customised section not kept as is, or Cognition not moved:\n%s", got)
	}
	if local := readFile(t, filepath.Join(a.Dir(), ClaudeLocalFile)); local != standingOrdersBlock(dir, false) || strings.Contains(local, commsHeading) {
		t.Fatalf("block must omit its Agent Communication section:\n%s", local)
	}
	if !strings.Contains(findingsText(doctorAgentFindings(a, PoolConfig{}, nil, false)), "customised Agent Communication section") {
		t.Fatal("doctor does not flag the customised section")
	}
}

// meshctl's own older Agent Communication text moves, whichever pool dir name it carries.
func TestKnownCommsSection(t *testing.T) {
	cur := strings.TrimPrefix(strings.TrimSpace(renderPoolTokens("", commsSection)), commsHeading)
	if !knownCommsSection(cur) || !knownCommsSection(strings.ReplaceAll(cur, "../pool/", "../.agentmesh/")) || !knownCommsSection(oldInboundBullet) {
		t.Fatal("meshctl's own text is not recognised")
	}
	if knownCommsSection(cur + "\n- a human line\n") {
		t.Fatal("a human line was taken for meshctl's")
	}
}
