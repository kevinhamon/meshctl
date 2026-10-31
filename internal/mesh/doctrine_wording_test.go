package mesh

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A pool at the previous doctrine version: `pool upgrade --check` shows the
// doctrine change as a diff, writes nothing, and the apply
// commits `doctrine: upgrade to <version>`.
func TestPoolUpgradeCheck_ShowsDoctrineDiff(t *testing.T) {
	dir, _ := poolCommitFixture(t)
	old := "# Agent Communication Protocol\n\n- **Review gate** — a human or the owning agent approves before a candidate becomes truth.\n"
	stale := filepath.Join(DoctrinePath(dir), CommsFile)
	os.WriteFile(stale, []byte(old), 0o644)
	if err := stampEmitted(DoctrinePath(dir), []string{CommsFile}); err != nil {
		t.Fatal(err)
	}
	gitOut(t, PoolDir(dir), "add", "-A")
	gitOut(t, PoolDir(dir), "commit", "-qm", "seed previous doctrine")

	plan, err := PlanPoolUpgrade(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	diff := plan.DoctrineDiff
	for _, want := range []string{
		"-- **Review gate** — a human or the owning agent approves",
		"+7. **Internal references never leave the mesh",
		"Writes to outbound surfaces are gate-asked",
		CommsFile,
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("--check diff missing %q:\n%s", want, diff)
		}
	}
	if b, _ := os.ReadFile(stale); string(b) != old {
		t.Fatal("--check must not write the doctrine")
	}
	if st := gitOut(t, PoolDir(dir), "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Fatalf("--check left the pool dirty:\n%s", st)
	}

	if _, err := UpgradeDoctrine(dir, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if subject, _, _ := poolHead(t, dir); subject != "doctrine: upgrade to "+doctrineVersion() {
		t.Fatalf("subject = %q", subject)
	}
	plan, _ = PlanPoolUpgrade(dir, time.Now())
	if plan.DoctrineDiff != "" {
		t.Fatalf("a current pool shows no doctrine diff:\n%s", plan.DoctrineDiff)
	}
}

// templateTexts returns every embedded template file plus the Go-held stanzas
// onboard appends, keyed by name: the text a peer is given as standing orders.
func templateTexts(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{
		"cognitionSection": cognitionSection,
		"commsSection":     commsSection,
	}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := templatesFS.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			p := dir + "/" + e.Name()
			if e.IsDir() {
				walk(p)
				continue
			}
			b, err := templatesFS.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			// As scaffolded: the Cognition stanza arrives through its token.
			out[p] = strings.ReplaceAll(string(b), cognitionToken, cognitionSection)
		}
	}
	walk("templates")
	return out
}

// The standing orders cite no meshctl ADRs: a pool's peers and humans cannot
// read them, so each rule carries its reason inline. The one ADR number left
// is the ledger example's refs column, which stands for the pool's own KB.
func TestTemplateStandingOrders_NoMeshctlADRRefs(t *testing.T) {
	adr := regexp.MustCompile(`ADR-[0-9]{4}`)
	for name, text := range templateTexts(t) {
		for i, line := range strings.Split(text, "\n") {
			if adr.MatchString(line) && !strings.Contains(line, "| ADR-0008, R-12 |") {
				t.Errorf("%s:%d cites an ADR: %s", name, i+1, line)
			}
		}
	}
}

// The scaffolded standing orders carry the align-before-act order, the
// summary-first KB and outbound flow, and the pending-work order.
func TestTemplateStandingOrders_AlignBeforeAct(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	b, err := os.ReadFile(filepath.Join(a.Dir(), "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	claude := string(b)
	if strings.Contains(strings.ToLower(claude), "plan before acting") {
		t.Fatal("standing orders still say plan before acting")
	}
	for _, want := range []string{
		"**Align before you act.**",
		"**shared understanding**",
		"intent", "grounding", "*assumption*", "**precedent**", "*new*",
		"more than one artifact", "any outbound write", "any promotion", "any claim you cannot ground",
		"may tighten this in `local.md`, never loosen it",
		// KB flow: summary, round, then finish with the summary.
		"write the summary file", "read the confirmation round", "`meshctl kb finish --summary-file",
		"Before any outbound write",
		// Pending-work order.
		"# Pending work", "triage now, a subset, or deferral", "`Deferred: <ids>`", "no snooze",
	} {
		if !strings.Contains(claude, want) {
			t.Errorf("template CLAUDE.md missing %q", want)
		}
	}
	// The onboard stanza carries the same orders for onboarded peers.
	for _, want := range []string{"**Align before you act.**", "`meshctl kb finish --summary-file", "`Deferred: <ids>`"} {
		if !strings.Contains(cognitionSection, want) {
			t.Errorf("onboard cognition stanza missing %q", want)
		}
	}
}

// The mesh-cognition skill matches the gates.
func TestCognitionSkill_AlignStepAndConfirmationRound(t *testing.T) {
	skill, err := renderedSkill(meshRoot(t), "mesh-cognition")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Recall → Retrieve → Reconcile → Verify → **Align** → Act → Learn",
		"📝 **C<n> — <entry>** (new|amended|retired)",
		"From now on it says: …",
		"It no longer says: …",
		"Grounded in: …",
		"Precedent: …",
		"➡️ ✅ Confirm",
		"\"yes\" confirms the round",
		"\"C2 no: reason\"",
		"back to `candidates/`",
		"denied with no change",
		"ask which entry is disputed",
		"triage-closed request owes no distillation",
		"`meshctl kb finish --summary-file",
		"# Pending work",
		"`Deferred: <ids>`",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("mesh-cognition skill missing %q", want)
		}
	}
}

// Doctrine and templates name the human as the only approver and agree with
// the outbound and delegated-work gates.
func TestDoctrine_HumanConfirmsAndOutboundIsGateAsked(t *testing.T) {
	for name, text := range templateTexts(t) {
		if strings.Contains(text, "or the owning agent") || strings.Contains(text, "human/the owning agent") {
			t.Errorf("%s still names the owning agent as an approver", name)
		}
	}
	comms, _ := templatesFS.ReadFile("templates/doctrine/agent-comms.md")
	distiller, _ := templatesFS.ReadFile("templates/doctrine/distiller.md")
	work, _ := templatesFS.ReadFile("templates/claude/mesh-peer-work.md")
	rule7 := string(comms)[strings.Index(string(comms), "7. **Internal references"):]
	rule7 = rule7[:strings.Index(rule7, "\n8. ")]
	if !strings.Contains(rule7, "Writes to outbound surfaces are gate-asked") {
		t.Errorf("agent-comms rule 7 lacks the outbound-ask sentence:\n%s", rule7)
	}
	if !strings.Contains(string(distiller), "the human confirms at `kb finish`") {
		t.Error("distiller doctrine does not say the human confirms at kb finish")
	}
	for name, text := range map[string]string{"agent-comms.md": string(comms), "mesh-peer-work.md": string(work)} {
		for _, want := range []string{"outbound write", "`needs-confirm`", "confirmation round", "summary path", "never relay"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s delegated-work definition missing %q", name, want)
			}
		}
	}
}

// Every `meshctl state set` example shows the summary it is gated on
// (the gate denies the bare form), and the triage grammar says
// which verdicts carry a ref.
func TestTemplates_StateSetAndTriageGrammar(t *testing.T) {
	texts := templateTexts(t)
	texts["consolidate"] = ConsolidateReport{Agent: "x", StateMissing: true,
		StateProblems: []string{"p"}, NotInDigest: []string{"d"}}.Markdown()
	for name, text := range texts {
		for i := strings.Index(text, "meshctl state set"); i >= 0; {
			rest := text[i+len("meshctl state set"):]
			if !strings.HasPrefix(rest, " --summary-file") {
				t.Errorf("%s: `meshctl state set` without --summary-file: %q", name, text[i:min(len(text), i+60)])
			}
			j := strings.Index(rest, "meshctl state set")
			if j < 0 {
				break
			}
			i += len("meshctl state set") + j
		}
	}
	skill := texts["templates/skills/mesh-cognition/SKILL.md"]
	for _, want := range []string{"withdrawn → <ref>", "`stands` and `unclear` carry no ref"} {
		if !strings.Contains(skill, want) {
			t.Errorf("mesh-cognition triage grammar missing %q", want)
		}
	}
	ws := (&TriageWorksheet{Peer: "p", Date: "2026-10-18"}).Markdown()
	if !strings.Contains(ws, "`stands` and `unclear` carry no ref") {
		t.Errorf("triage worksheet grammar does not say stands/unclear carry no ref:\n%s", ws)
	}
}

// The handoff template has a place for the deferral line, and a filled
// handoff carrying one lints clean.
func TestHandoffTemplate_DeferredLine(t *testing.T) {
	if !strings.Contains(HandoffTemplate, "Deferred: <ids>") {
		t.Fatalf("handoff template has no Deferred line:\n%s", HandoffTemplate)
	}
	filled := "## Current State\nDone.\n\n## Next Step\nShip it.\n\n## Open Threads\n- Deferred: req-1, req-2\n\n## Pointers\n- none\n"
	if ws := HandoffLint(filled); len(ws) != 0 {
		t.Fatalf("a handoff with a Deferred line must lint clean: %v", ws)
	}
}

// TestTemplates_ConfirmNotApprove: "approve/approval" is an _Avoid_ term for
// confirming knowledge (GLOSSARY: Confirmed knowledge, Shared understanding,
// Confirmation round). The cognition skill and the work brief never use it;
// agent-comms keeps it only for the delegation ask, which is not a
// confirmation of knowledge.
func TestTemplates_ConfirmNotApprove(t *testing.T) {
	for _, name := range []string{"templates/skills/mesh-cognition/SKILL.md", "templates/claude/mesh-peer-work.md"} {
		b, _ := templatesFS.ReadFile(name)
		if i := strings.Index(strings.ToLower(string(b)), "approv"); i >= 0 {
			t.Errorf("%s says approve/approval: %q", name, string(b)[max(0, i-60):min(len(b), i+40)])
		}
	}
	b, _ := templatesFS.ReadFile("templates/doctrine/agent-comms.md")
	for _, ln := range strings.Split(string(b), "\n") {
		l := strings.ToLower(ln)
		if strings.Contains(l, "approv") && !strings.Contains(l, "delegat") && !strings.Contains(l, "agent call") {
			t.Errorf("agent-comms says approve/approval outside the delegation ask: %q", ln)
		}
	}
}

// "check intake" shows the queue and stops; work runs in the human's order
// by id; decisions are asked as a decision round.
func TestStandingOrders_IntakeOrderAndDecisionRound(t *testing.T) {
	texts := templateTexts(t)
	for name, text := range texts {
		if strings.Contains(text, "inbox next --claim") {
			t.Errorf("%s still claims the oldest request on check intake", name)
		}
	}
	intake := []string{"`meshctl inbox list`", "an order (priority, then age", "`meshctl inbox claim <id>`", "`Deferred: <ids>`"}
	round := []string{"📝 **C<n> — <short title>**", "From now on it says: …", "It no longer says: …", "➡️ ✅ Confirm", "`yes`", "`C<n> no: <reason>`"}
	for _, name := range []string{"commsSection", "templates/CLAUDE.md", "templates/skills/mesh-cognition/SKILL.md", "templates/doctrine/agent-comms.md"} {
		text := texts[name]
		for _, want := range intake {
			if !strings.Contains(strings.ReplaceAll(text, "\n   ", " "), want) {
				t.Errorf("%s missing intake order %q", name, want)
			}
		}
		for _, want := range round {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing decision round %q", name, want)
			}
		}
	}
	for _, name := range []string{"commsSection", "templates/CLAUDE.md"} {
		if !strings.Contains(texts[name], inboundStanza) {
			t.Errorf("%s does not carry the managed Inbound order verbatim", name)
		}
	}
	for _, want := range []string{"`meshctl inbox list`", "claim by id in the human's order", "decision round"} {
		if !strings.Contains(strings.ReplaceAll(cognitionSection, "\n  ", " "), want) {
			t.Errorf("onboard cognition stanza missing %q", want)
		}
	}
	skill := texts["templates/skills/mesh-cognition/SKILL.md"]
	if !strings.Contains(skill, "this same format with its extra fields") {
		t.Error("skill does not say the confirmation round is the decision-round format")
	}
	for _, brief := range []string{onlyThisRequest} {
		if !strings.Contains(brief, "work the one request you were given") {
			t.Errorf("in-session contracts must keep a peer on its one request: %s", brief)
		}
	}
}
