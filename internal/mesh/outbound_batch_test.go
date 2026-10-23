package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const outboundSummary = `## Intent
File the release checklist as tracker items for 2.4

## Grounding
The release playbook lists five steps.

## Precedent
2.3 was filed the same way.

## Changes
### tracker/REL
+ item: freeze branch
+ item: cut tag
+ item: build artifacts
+ item: smoke test
+ item: publish notes
+ item: announce
+ item: seventh bullet never shown
`

func TestGate_OutboundAskCarriesSummarySignature(t *testing.T) {
	dir, pm, _ := outboundPool(t, outboundTestConfig)
	writeSummary(t, pm, "sess-1", outboundSummary)
	long := strings.Repeat("word ", 100) // 500 chars
	p := outboundCall(pm.Dir(), "mcp__tracker__createIssue", map[string]string{"summary": "Freeze\nbranch", "title": long})

	d := GatePreToolUse(dir, p)
	if !d.Ask || d.Check != GateCheckOutbound {
		t.Fatalf("want outbound ask, got %+v", d)
	}
	for _, want := range []string{
		"Intent: File the release checklist as tracker items for 2.4",
		"+ item: freeze branch",
		"+ item: announce",
		"Outbound: tracker via mcp__tracker__createIssue",
	} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("ask lacks %q:\n%s", want, d.Reason)
		}
	}
	if strings.Contains(d.Reason, "seventh bullet") {
		t.Errorf("ask shows more than six bullets:\n%s", d.Reason)
	}
	if strings.Contains(d.Reason, "Grounding") {
		t.Errorf("ask is the signature, not the whole summary:\n%s", d.Reason)
	}
	var input string
	for _, ln := range strings.Split(d.Reason, "\n") {
		if strings.HasPrefix(ln, "Input: ") {
			input = strings.TrimPrefix(ln, "Input: ")
		}
	}
	if input == "" || !strings.HasPrefix(input, "Freeze branch word word") {
		t.Fatalf("input line missing or not flattened: %q", input)
	}
	if n := len([]rune(strings.TrimSuffix(input, "…"))); n > 300 {
		t.Errorf("input not cut at 300 characters: %d", n)
	}
}

func TestGate_OutboundSummaryRequired(t *testing.T) {
	dir, pm, _ := outboundPool(t, outboundTestConfig)
	p := outboundCall(pm.Dir(), "mcp__tracker__createIssue", nil)
	for name, text := range map[string]string{"missing": "", "empty": "  \n", "no intent": "## Grounding\nx\n"} {
		t.Run(name, func(t *testing.T) {
			if name == "missing" {
				_ = os.Remove(SummaryPath(pm.Dir(), "sess-1"))
			} else {
				writeSummary(t, pm, "sess-1", text)
			}
			d := GatePreToolUse(dir, p)
			if !d.Deny || d.Check != GateCheckOutbound || !strings.Contains(d.Reason, "summary required") ||
				!strings.Contains(d.Reason, SummaryPath(pm.Dir(), "sess-1")) {
				t.Fatalf("want deny \"summary required\" naming the path, got %+v", d)
			}
		})
	}
}

func outboundDecision(d GateDecision) string {
	switch {
	case d.Deny:
		return "deny"
	case d.Ask:
		return "ask"
	}
	return "allow"
}

func TestGate_OutboundBatchAckedThenSilentAndAudited(t *testing.T) {
	dir, pm, _ := outboundPool(t, outboundTestConfig)
	writeSummary(t, pm, "sess-1", outboundSummary)
	first := outboundCall(pm.Dir(), "mcp__tracker__createIssue", map[string]string{"summary": "freeze branch"})
	first.ToolUseID = "toolu_1"

	if d := GatePreToolUse(dir, first); !d.Ask {
		t.Fatalf("first call must ask, got %+v", d)
	}
	HookPostToolUse(dir, first, time.Now())
	if !HasSessionAck(pm.Dir(), "sess-1", GateAckKey(GateCheckOutbound, HashBytes([]byte(outboundSummary)))) {
		t.Fatalf("PostToolUse after the approved call must record the (session, summary hash) ack")
	}

	second := outboundCall(pm.Dir(), "mcp__tracker__addCommentToIssue", map[string]string{"body": "cut tag"})
	second.ToolUseID = "toolu_2"
	d := GatePreToolUse(dir, second)
	if got := outboundDecision(d); got != "allow" {
		t.Fatalf("same summary must pass silently, got %s: %s", got, d.Reason)
	}
	HookPostToolUse(dir, second, time.Now())
	third := outboundBash(pm.Dir(), "gh issue create --title smoke")
	if got := outboundDecision(GatePreToolUse(dir, third)); got != "allow" {
		t.Fatalf("shell call under the same batch must pass silently, got %s", got)
	}

	audit := ReadOutboundAudit(pm.Dir(), "sess-1")
	if len(audit) != 3 {
		t.Fatalf("want one audit line per allowed call (asked + two silent), got %d: %+v", len(audit), audit)
	}
	raw, err := os.ReadFile(OutboundAuditPath(pm.Dir(), "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var e map[string]string
	if err := json.Unmarshal([]byte(lines[1]), &e); err != nil {
		t.Fatalf("audit line is not JSON: %q", lines[1])
	}
	for _, k := range []string{"time", "surface", "tool", "args_digest", "summary_hash"} {
		if e[k] == "" {
			t.Errorf("audit line lacks %s: %q", k, lines[1])
		}
	}
	if e["tool"] != "mcp__tracker__addCommentToIssue" || e["surface"] != "tracker" ||
		e["summary_hash"] != HashBytes([]byte(outboundSummary)) || e["args_digest"] != HashBytes(second.ToolInput) {
		t.Errorf("audit line has the wrong call: %q", lines[1])
	}
	if audit[0].ArgsDigest == audit[1].ArgsDigest {
		t.Errorf("different inputs must digest differently")
	}
}

func TestGate_OutboundRewrittenSummaryAsksAgain(t *testing.T) {
	dir, pm, _ := outboundPool(t, outboundTestConfig)
	writeSummary(t, pm, "sess-1", outboundSummary)
	p := outboundCall(pm.Dir(), "mcp__tracker__createIssue", map[string]string{"summary": "x"})
	GatePreToolUse(dir, p)
	HookPostToolUse(dir, p, time.Now())
	if got := outboundDecision(GatePreToolUse(dir, p)); got != "allow" {
		t.Fatalf("acked batch should pass, got %s", got)
	}
	writeSummary(t, pm, "sess-1", strings.Replace(outboundSummary, "+ item: announce", "+ item: announce on the list", 1))
	if d := GatePreToolUse(dir, p); !d.Ask || !strings.Contains(d.Reason, "announce on the list") {
		t.Fatalf("a rewritten summary must ask again with the new text, got %+v", d)
	}
}

func TestHook_OutboundDeniedCallLeavesNoAck(t *testing.T) {
	dir, pm, dev := outboundPool(t, outboundTestConfig)
	mcp := "mcp__tracker__createIssue"

	// No summary: the gate denies; a PostToolUse for that call acks nothing.
	p := outboundCall(pm.Dir(), mcp, nil)
	if d := GatePreToolUse(dir, p); !d.Deny {
		t.Fatalf("want deny, got %+v", d)
	}
	writeSummary(t, pm, "sess-1", outboundSummary) // written after the denied call
	HookPostToolUse(dir, p, time.Now())
	if HasSessionAck(pm.Dir(), "sess-1", GateAckKey(GateCheckOutbound, HashBytes([]byte(outboundSummary)))) {
		t.Fatal("a call the gate denied must leave no ack")
	}
	if got := outboundDecision(GatePreToolUse(dir, p)); got != "ask" {
		t.Fatalf("with no ack the next call asks, got %s", got)
	}

	// An untagged peer's denied call writes nothing either.
	q := outboundCall(dev.Dir(), mcp, nil)
	writeSummary(t, dev, "sess-1", outboundSummary)
	GatePreToolUse(dir, q)
	HookPostToolUse(dir, q, time.Now())
	if _, err := os.Stat(SessionAckPath(dev.Dir(), "sess-1")); err == nil {
		t.Fatal("an untagged peer's denied call wrote an ack file")
	}
	if len(ReadOutboundAudit(dev.Dir(), "sess-1")) != 0 {
		t.Fatal("a denied call must not be audited")
	}
}

func TestHook_OutboundAckMatchesTheAskedCall(t *testing.T) {
	dir, pm, _ := outboundPool(t, outboundTestConfig)
	writeSummary(t, pm, "sess-1", outboundSummary)
	p := outboundCall(pm.Dir(), "mcp__tracker__createIssue", nil)
	p.ToolUseID = "toolu_asked"
	GatePreToolUse(dir, p)
	other := p
	other.ToolUseID = "toolu_never_asked"
	HookPostToolUse(dir, other, time.Now())
	if HasSessionAck(pm.Dir(), "sess-1", GateAckKey(GateCheckOutbound, HashBytes([]byte(outboundSummary)))) {
		t.Fatal("a PostToolUse for a call that was never asked must not ack the batch")
	}
	HookPostToolUse(dir, p, time.Now())
	if !HasSessionAck(pm.Dir(), "sess-1", GateAckKey(GateCheckOutbound, HashBytes([]byte(outboundSummary)))) {
		t.Fatal("the asked call's PostToolUse must ack")
	}
}

func TestGate_AcksArePerGate(t *testing.T) {
	t.Run("promotion ack does not cover outbound", func(t *testing.T) {
		dir, pm, _ := outboundPool(t, outboundTestConfig)
		writeSummary(t, pm, "sess-1", outboundSummary)
		h := HashBytes([]byte(outboundSummary))
		for _, key := range []string{GateAckKey(GateCheckPromotion, h), h} {
			if err := RecordSessionAck(pm.Dir(), "sess-1", key); err != nil {
				t.Fatal(err)
			}
		}
		if got := outboundDecision(GatePreToolUse(dir, outboundCall(pm.Dir(), "mcp__tracker__createIssue", nil))); got != "ask" {
			t.Fatalf("a promotion ack must not acknowledge the outbound batch, got %s", got)
		}
	})
	t.Run("outbound ack does not cover promotion", func(t *testing.T) {
		dir, a, wt := promotionFixture(t)
		commitKB(t, wt, "playbooks/release.md")
		sum := writeSummary(t, a, "sess-1", promotionSummary)
		if err := RecordSessionAck(a.Dir(), "sess-1", GateAckKey(GateCheckOutbound, HashBytes([]byte(promotionSummary)))); err != nil {
			t.Fatal(err)
		}
		d := GatePreToolUse(dir, bash(a, "sess-1", "meshctl kb finish --summary-file "+sum))
		if !d.Ask || d.Check != GateCheckPromotion {
			t.Fatalf("an outbound ack must not acknowledge a promotion, got %+v", d)
		}
	})
}

const outboundRuleNote = `---
rule: "Tracker items need the release label."
triggers:
  - 'createIssue'
---

# Release label
`

func TestGate_KBRuleDeniesOnceBeforeOutboundAsk(t *testing.T) {
	dir, pm, _ := outboundPool(t, outboundTestConfig)
	writeKBNote(t, pm, "experience/release-label.md", outboundRuleNote)
	writeSummary(t, pm, "sess-1", outboundSummary)
	log := filepath.Join(t.TempDir(), "gate.log")
	t.Setenv("AGENT_MESH_GATE_LOG", log)
	p := outboundCall(pm.Dir(), "mcp__tracker__createIssue", nil)

	if d := GatePreToolUse(dir, p); !d.Deny || d.Check != GateCheckKBRule {
		t.Fatalf("the KB rule must deny once first, got %+v", d)
	}
	if d := GatePreToolUse(dir, p); !d.Ask || d.Check != GateCheckOutbound {
		t.Fatalf("the retry must get the outbound ask, got %+v", d)
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	i, j := strings.Index(got, "decision=deny check=kb-rule"), strings.Index(got, "decision=ask check=outbound")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("decision log must show the rule deny, then the outbound ask:\n%s", got)
	}
}

func TestHookSessionEnd_RecordListsOutboundCalls(t *testing.T) {
	dir, pm, _ := outboundPool(t, outboundTestConfig)
	writeSummary(t, pm, "sess-1", outboundSummary)
	p := outboundCall(pm.Dir(), "mcp__tracker__createIssue", map[string]string{"summary": "a"})
	GatePreToolUse(dir, p)
	HookPostToolUse(dir, p, time.Now())
	q := outboundCall(pm.Dir(), "mcp__tracker__addCommentToIssue", map[string]string{"body": "b"})
	GatePreToolUse(dir, q)

	HookSessionEnd(dir, HookPayload{Cwd: pm.Dir(), SessionID: "sess-1"}, 1, time.Now())
	recs := ListSessionRecords(pm)
	if len(recs) != 1 {
		t.Fatalf("want one session record, got %d", len(recs))
	}
	got := recs[0].Outbound
	if len(got) != 2 || got[0].Tool != "mcp__tracker__createIssue" || got[1].Tool != "mcp__tracker__addCommentToIssue" ||
		got[1].SummaryHash != HashBytes([]byte(outboundSummary)) {
		t.Fatalf("session record must list the session's outbound calls in order, got %+v", got)
	}
	if lines := OutboundAuditLines(got); len(lines) != 2 || !strings.Contains(lines[1], "tracker via mcp__tracker__addCommentToIssue") {
		t.Fatalf("receipt lines: %q", lines)
	}
}

func TestWorkBrief_ReceiptListsOutboundCalls(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	req, _ := askWork(t, dir, caller, arch, "File the release items")
	brief, err := Brief(dir, arch, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, "outbound calls") || !strings.Contains(brief, filepath.Join(MemoryDir, OutboundDir)) {
		t.Fatalf("work receipt must list the session's outbound calls:\n%s", brief)
	}
}

func TestHygiene_CleansOldOutboundState(t *testing.T) {
	dir, pm, _ := outboundPool(t, outboundTestConfig)
	writeSummary(t, pm, "old", outboundSummary)
	writeSummary(t, pm, "fresh", outboundSummary)
	for _, s := range []string{"old", "fresh"} {
		p := outboundCall(pm.Dir(), "mcp__tracker__createIssue", nil)
		p.SessionID = s
		GatePreToolUse(dir, p)
		HookPostToolUse(dir, p, time.Now())
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	_ = os.Chtimes(OutboundAuditPath(pm.Dir(), "old"), old, old)
	_ = os.Chtimes(SessionAckPath(pm.Dir(), "old"), old, old)

	items := PoolHygiene(dir, time.Now())
	if !strings.Contains(hygieneMessages(items), "outbound audit") {
		t.Fatalf("missing outbound-audit hygiene item:\n%s", hygieneMessages(items))
	}
	if _, errs := ApplyHygiene(items); len(errs) != 0 {
		t.Fatal(errs)
	}
	if fileExists(OutboundAuditPath(pm.Dir(), "old")) || fileExists(SessionAckPath(pm.Dir(), "old")) {
		t.Fatal("old outbound audit and ack must be removed")
	}
	if !fileExists(OutboundAuditPath(pm.Dir(), "fresh")) || !fileExists(SessionAckPath(pm.Dir(), "fresh")) {
		t.Fatal("fresh session state must stay")
	}
}
