package mesh

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// grants_lift_test.go — grants lift the "no human at the prompt" deny,
// at the gate and in the command, for each ask point: no
// grant leaves the deny as it was; a grant lets the action through, recorded
// once in the policy log and, for knowledge, in the ledger as by policy; a
// paused pool or a reached cap keeps the deny and says why; a human at the
// prompt is asked exactly as before.

// writeGrants writes the pool config: extra (e.g. an `outbound:` block), then
// an `grants:` block granting peer each of grants (YAML map entries).
func writeGrants(t *testing.T, dir, extra string, paused bool, peer string, grants ...string) {
	t.Helper()
	body := extra + "grants:\n"
	if paused {
		body += "  paused: true\n"
	}
	body += "  peers:\n    " + peer + ":\n"
	for _, g := range grants {
		body += "      " + g + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(PoolConfigPath(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PoolConfigPath(dir), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fillCap records n actions under grant for peer on day.
func fillCap(t *testing.T, dir, peer, grant string, day time.Time, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := RecordPolicyAction(dir, PolicyAction{Time: day.UTC().Format(time.RFC3339), Peer: peer, Grant: grant}); err != nil {
			t.Fatal(err)
		}
	}
}

// onePolicyAction asserts the policy log holds exactly one line, for peer
// under grant, and returns it.
func onePolicyAction(t *testing.T, dir, peer, grant string) PolicyAction {
	t.Helper()
	got := ReadPolicyActions(dir)
	if len(got) != 1 || got[0].Peer != peer || got[0].Grant != grant {
		t.Fatalf("policy log: want one %s/%s line, got %+v", peer, grant, got)
	}
	return got[0]
}

func noPolicyActions(t *testing.T, dir string) {
	t.Helper()
	if got := ReadPolicyActions(dir); len(got) != 0 {
		t.Fatalf("policy log must stay empty, got %+v", got)
	}
}

// --- kb finish (kb-promote) ---

func TestGrant_KBFinishCommand(t *testing.T) {
	promote := func(t *testing.T) (string, *Agent, string, string) {
		dir, a, wt := finishFixture(t)
		writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n\nTag then push.\n")
		return dir, a, wt, writeFinishSummary(t, a, "sess-1", "Record the release playbook.")
	}

	t.Run("no grant parks unchanged", func(t *testing.T) {
		dir, a, wt, sum := promote(t)
		before := mainRef(t, a)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		_, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: sum, Now: finishNow})
		assertParked(t, a, wt, before, err)
		if strings.Contains(err.Error(), "grant does not apply") {
			t.Errorf("no grant: the message must read as before: %v", err)
		}
		noPolicyActions(t, dir)
	})

	t.Run("grant lets it through by policy", func(t *testing.T) {
		dir, a, _, sum := promote(t)
		writeGrants(t, dir, "", false, "architect", "kb-promote: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		res, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: sum, Now: finishNow})
		if err != nil {
			t.Fatalf("granted finish: %v", err)
		}
		if res.Grant != GrantKBPromote || res.Confirm == "" {
			t.Fatalf("result: %+v", res)
		}
		e := ledgerOnMain(t, a).Entries["playbooks/release.md"]
		if e.By != LedgerByPolicy || e.Grant != GrantKBPromote {
			t.Fatalf("ledger entry not by policy: %+v", e)
		}
		if body := gitOut(t, a.Dir(), "log", "-1", "--format=%b", "main"); !strings.Contains(body, "Confirmed-by: policy (grant kb-promote)") {
			t.Errorf("Confirm commit does not name the grant:\n%s", body)
		}
		pa := onePolicyAction(t, dir, "architect", GrantKBPromote)
		if pa.Session != "sess-1" || !strings.Contains(pa.Detail, "playbooks/release.md") || !strings.Contains(pa.Detail, sum) {
			t.Errorf("policy line: %+v", pa)
		}
	})

	t.Run("grant still needs the summary", func(t *testing.T) {
		dir, a, _, _ := promote(t)
		writeGrants(t, dir, "", false, "architect", "kb-promote: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if _, err := KBFinish(dir, a, "sess-1", KBFinishOptions{Now: finishNow}); !errors.Is(err, ErrKBSummaryRequired) {
			t.Fatalf("want the summary refusal, got %v", err)
		}
		noPolicyActions(t, dir)
	})

	t.Run("paused parks and says why", func(t *testing.T) {
		dir, a, wt, sum := promote(t)
		before := mainRef(t, a)
		writeGrants(t, dir, "", true, "architect", "kb-promote: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		_, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: sum, Now: finishNow})
		assertParked(t, a, wt, before, err)
		if !strings.Contains(err.Error(), "grants are paused") {
			t.Errorf("paused: %v", err)
		}
	})

	t.Run("cap reached parks and says why", func(t *testing.T) {
		dir, a, wt, sum := promote(t)
		before := mainRef(t, a)
		writeGrants(t, dir, "", false, "architect", "kb-promote: { max_per_day: 1 }")
		fillCap(t, dir, "architect", GrantKBPromote, finishNow, 1)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		_, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: sum, Now: finishNow})
		assertParked(t, a, wt, before, err)
		if !strings.Contains(err.Error(), "grant cap reached") {
			t.Errorf("cap: %v", err)
		}
	})

	t.Run("human at the prompt confirms as a human", func(t *testing.T) {
		dir, a, _, sum := promote(t)
		writeGrants(t, dir, "", false, "architect", "kb-promote: {}")
		res, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: sum, Now: finishNow})
		if err != nil || res.Grant != "" {
			t.Fatalf("interactive finish: %+v %v", res, err)
		}
		if e := ledgerOnMain(t, a).Entries["playbooks/release.md"]; e.By != "" || e.Grant != "" {
			t.Fatalf("human confirmation marked by policy: %+v", e)
		}
		noPolicyActions(t, dir)
	})
}

func TestGrant_KBFinishGate(t *testing.T) {
	setup := func(t *testing.T, paused bool, grant string) (string, HookPayload) {
		dir, a, wt := promotionFixture(t)
		commitKB(t, wt, "playbooks/release.md")
		sum := writeSummary(t, a, "sess-1", promotionSummary)
		if grant != "" {
			writeGrants(t, dir, "", paused, "architect", grant)
		}
		return dir, bash(a, "sess-1", "meshctl kb finish --agent architect --session sess-1 --summary-file "+sum)
	}

	t.Run("no grant denies unchanged", func(t *testing.T) {
		dir, p := setup(t, false, "")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		d := GatePreToolUse(dir, p)
		if !d.Deny || !strings.Contains(d.Reason, NeedsConfirmPrefix) || strings.Contains(d.Reason, "grant does not apply") {
			t.Fatalf("want today's deny, got %+v", d)
		}
	})
	t.Run("grant allows, the command records", func(t *testing.T) {
		dir, p := setup(t, false, "kb-promote: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, p); d.Deny || d.Ask {
			t.Fatalf("want allow, got %+v", d)
		}
		noPolicyActions(t, dir)
	})
	t.Run("grant still needs the summary", func(t *testing.T) {
		dir, a, wt := promotionFixture(t)
		commitKB(t, wt, "playbooks/release.md")
		writeGrants(t, dir, "", false, "architect", "kb-promote: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		d := GatePreToolUse(dir, bash(a, "sess-1", "meshctl kb finish --agent architect --session sess-1"))
		if !d.Deny || !strings.Contains(d.Reason, "summary") {
			t.Fatalf("want the summary deny, got %+v", d)
		}
	})
	t.Run("paused denies and says why", func(t *testing.T) {
		dir, p := setup(t, true, "kb-promote: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, p); !d.Deny || !strings.Contains(d.Reason, "grants are paused") {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("cap reached denies and says why", func(t *testing.T) {
		dir, p := setup(t, false, "kb-promote: { max_per_day: 1 }")
		fillCap(t, dir, "architect", GrantKBPromote, time.Now(), 1)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, p); !d.Deny || !strings.Contains(d.Reason, "grant cap reached") {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("human at the prompt is asked", func(t *testing.T) {
		dir, p := setup(t, false, "kb-promote: {}")
		if d := GatePreToolUse(dir, p); !d.Ask {
			t.Fatalf("want ask, got %+v", d)
		}
	})
	t.Run("in-session work keeps its deny", func(t *testing.T) {
		dir, p := setup(t, false, "kb-promote: {}")
		p.InSession, p.Work = true, true
		if d := GatePreToolUse(dir, p); !d.Deny || strings.Contains(d.Reason, "grant does not apply") {
			t.Fatalf("want today's deny, got %+v", d)
		}
	})
}

// --- state set (state-set) ---

func TestGrant_StateSetCommand(t *testing.T) {
	run := func(t *testing.T, a *Agent) (StateSetResult, error) {
		return StateSetConfirmed(a, stateDigest, StateSetOptions{SummaryFile: writeSummary(t, a, "sess-1", stateSummary), Session: "sess-1", Now: finishNow})
	}
	pool := func(a *Agent) string { return filepath.Dir(a.Dir()) }

	t.Run("no grant refuses unchanged", func(t *testing.T) {
		a := stateFixture(t)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		_, err := run(t, a)
		if !errors.Is(err, ErrStateHeadless) || strings.Contains(err.Error(), "grant does not apply") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("grant lets it through by policy", func(t *testing.T) {
		a := stateFixture(t)
		writeGrants(t, pool(a), "", false, "architect", "state-set: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		res, err := run(t, a)
		if err != nil || res.Grant != GrantStateSet {
			t.Fatalf("granted state set: %+v %v", res, err)
		}
		if e := ledgerOnMain(t, a).Entries[StateFile]; e.By != LedgerByPolicy || e.Grant != GrantStateSet {
			t.Fatalf("ledger entry not by policy: %+v", e)
		}
		onePolicyAction(t, pool(a), "architect", GrantStateSet)
	})
	t.Run("paused refuses and says why", func(t *testing.T) {
		a := stateFixture(t)
		writeGrants(t, pool(a), "", true, "architect", "state-set: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if _, err := run(t, a); !errors.Is(err, ErrStateHeadless) || !strings.Contains(err.Error(), "grants are paused") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("cap reached refuses and says why", func(t *testing.T) {
		a := stateFixture(t)
		writeGrants(t, pool(a), "", false, "architect", "state-set: { max_per_day: 1 }")
		fillCap(t, pool(a), "architect", GrantStateSet, finishNow, 1)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if _, err := run(t, a); !errors.Is(err, ErrStateHeadless) || !strings.Contains(err.Error(), "grant cap reached") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("human at the prompt confirms as a human", func(t *testing.T) {
		a := stateFixture(t)
		writeGrants(t, pool(a), "", false, "architect", "state-set: {}")
		res, err := run(t, a)
		if err != nil || res.Grant != "" || ledgerOnMain(t, a).Entries[StateFile].By != "" {
			t.Fatalf("interactive state set: %+v %v", res, err)
		}
		noPolicyActions(t, pool(a))
	})
}

func TestGrant_StateSetGate(t *testing.T) {
	setup := func(t *testing.T, paused bool, grant string) (string, HookPayload) {
		dir, a := poolGateFixture(t)
		sum := writeSummary(t, a, "sess-1", stateSummary)
		if grant != "" {
			writeGrants(t, dir, "", paused, "architect", grant)
		}
		return dir, bash(a, "sess-1", "meshctl state set --summary-file "+sum+" <<'EOF'\n## Now\nx\nEOF")
	}
	t.Run("no grant denies unchanged", func(t *testing.T) {
		dir, p := setup(t, false, "")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, p); !d.Deny || !strings.Contains(d.Reason, "a human must be at the prompt") || strings.Contains(d.Reason, "grant does not apply") {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("grant allows", func(t *testing.T) {
		dir, p := setup(t, false, "state-set: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, p); d.Deny || d.Ask {
			t.Fatalf("want allow, got %+v", d)
		}
		noPolicyActions(t, dir)
	})
	t.Run("paused denies and says why", func(t *testing.T) {
		dir, p := setup(t, true, "state-set: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, p); !d.Deny || !strings.Contains(d.Reason, "grants are paused") {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("cap reached denies and says why", func(t *testing.T) {
		dir, p := setup(t, false, "state-set: { max_per_day: 1 }")
		fillCap(t, dir, "architect", GrantStateSet, time.Now(), 1)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, p); !d.Deny || !strings.Contains(d.Reason, "grant cap reached") {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("human at the prompt is asked", func(t *testing.T) {
		dir, p := setup(t, false, "state-set: {}")
		if d := GatePreToolUse(dir, p); !d.Ask {
			t.Fatalf("want ask, got %+v", d)
		}
	})
}

// --- kb confirm (kb-confirm) ---

func TestGrant_KBConfirmCommand(t *testing.T) {
	t.Run("no grant refuses unchanged", func(t *testing.T) {
		dir, a := confirmFixture(t)
		rep := writeConfirmReport(t, a, confirmReport)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if _, err := confirmPaths(dir, a, rep, "reference/a.md"); !errors.Is(err, ErrKBConfirmRefused) || strings.Contains(err.Error(), "grant does not apply") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("grant lets it through by policy", func(t *testing.T) {
		dir, a := confirmFixture(t)
		rep := writeConfirmReport(t, a, confirmReport)
		writeGrants(t, dir, "", false, "architect", "kb-confirm: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		res, err := confirmPaths(dir, a, rep, "reference/a.md", "glossary.md#circulation-window")
		if err != nil || res.Grant != GrantKBConfirm {
			t.Fatalf("granted confirm: %+v %v", res, err)
		}
		l := ledgerOnMain(t, a)
		for _, k := range []string{"reference/a.md", PeerGlossaryFile} {
			if e := l.Entries[k]; e.By != LedgerByPolicy || e.Grant != GrantKBConfirm {
				t.Fatalf("%s not by policy: %+v", k, e)
			}
		}
		pa := onePolicyAction(t, dir, "architect", GrantKBConfirm)
		if !strings.Contains(pa.Detail, "reference/a.md") || pa.Session != "sess-1" {
			t.Errorf("policy line: %+v", pa)
		}
		// A later confirmation by a human makes it a human one.
		t.Setenv("AGENT_DISPATCH_DEPTH", "")
		if _, err := confirmPaths(dir, a, rep, "reference/a.md"); err != nil {
			t.Fatal(err)
		}
		if e := ledgerOnMain(t, a).Entries["reference/a.md"]; e.By != "" || e.Grant != "" {
			t.Fatalf("human re-confirmation kept by policy: %+v", e)
		}
	})
	t.Run("grant takes no ungrounded override", func(t *testing.T) {
		dir, a := confirmFixture(t)
		rep := writeConfirmReport(t, a, confirmReport)
		writeGrants(t, dir, "", false, "architect", "kb-confirm: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		_, err := KBConfirm(dir, a, []string{"reference/a.md"}, KBConfirmOptions{Report: rep, AcceptUngrounded: []string{"reference/a.md"}, Session: "sess-1", Now: finishNow})
		if !errors.Is(err, ErrKBConfirmRefused) || !strings.Contains(err.Error(), "--accept-ungrounded") {
			t.Fatalf("got %v", err)
		}
		noPolicyActions(t, dir)
	})
	t.Run("paused refuses and says why", func(t *testing.T) {
		dir, a := confirmFixture(t)
		rep := writeConfirmReport(t, a, confirmReport)
		writeGrants(t, dir, "", true, "architect", "kb-confirm: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if _, err := confirmPaths(dir, a, rep, "reference/a.md"); !errors.Is(err, ErrKBConfirmRefused) || !strings.Contains(err.Error(), "grants are paused") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("cap reached refuses and says why", func(t *testing.T) {
		dir, a := confirmFixture(t)
		rep := writeConfirmReport(t, a, confirmReport)
		writeGrants(t, dir, "", false, "architect", "kb-confirm: { max_per_day: 1 }")
		fillCap(t, dir, "architect", GrantKBConfirm, finishNow, 1)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if _, err := confirmPaths(dir, a, rep, "reference/a.md"); !errors.Is(err, ErrKBConfirmRefused) || !strings.Contains(err.Error(), "grant cap reached") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestGrant_KBConfirmGate(t *testing.T) {
	setup := func(t *testing.T, paused bool, grant string) (string, HookPayload) {
		dir, a := confirmFixture(t)
		rep := writeConfirmReport(t, a, confirmReport)
		if grant != "" {
			writeGrants(t, dir, "", paused, "architect", grant)
		}
		return dir, bash(a, "sess-1", "meshctl kb confirm reference/a.md --report "+rep)
	}
	t.Run("no grant denies unchanged", func(t *testing.T) {
		dir, p := setup(t, false, "")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, p); !d.Deny || strings.Contains(d.Reason, "grant does not apply") {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("grant allows", func(t *testing.T) {
		dir, p := setup(t, false, "kb-confirm: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, p); d.Deny || d.Ask {
			t.Fatalf("want allow, got %+v", d)
		}
		noPolicyActions(t, dir)
	})
	t.Run("paused denies and says why", func(t *testing.T) {
		dir, p := setup(t, true, "kb-confirm: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, p); !d.Deny || !strings.Contains(d.Reason, "grants are paused") {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("cap reached denies and says why", func(t *testing.T) {
		dir, p := setup(t, false, "kb-confirm: { max_per_day: 1 }")
		fillCap(t, dir, "architect", GrantKBConfirm, time.Now(), 1)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, p); !d.Deny || !strings.Contains(d.Reason, "grant cap reached") {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("human at the prompt is asked", func(t *testing.T) {
		dir, p := setup(t, false, "kb-confirm: {}")
		if d := GatePreToolUse(dir, p); !d.Ask {
			t.Fatalf("want ask, got %+v", d)
		}
	})
}

// --- inbox triage apply (triage-apply) ---

func TestGrant_TriageApply(t *testing.T) {
	setup := func(t *testing.T, paused bool, grant string) (dir string, arch *Agent, report, id string) {
		dir, arch, _, _ = triagePool(t)
		r := triageAsk(t, arch, "developer", "guidance", "first ask", nil, triageDaysAgo(10))
		report = triageReport(t, arch,
			TriageReportLine{ID: r.ID, Verdict: TriageWithdrawn, Ref: "architect:" + r.ID, Quote: "not needed any more", Source: TriageSourceHuman})
		if grant != "" {
			writeGrants(t, dir, "", paused, "architect", grant)
		}
		return dir, arch, report, r.ID
	}
	apply := func(dir string, a *Agent, report, id string) (*TriageApplyResult, error) {
		return TriageApply(dir, a, TriageApplyOptions{Report: report, IDs: []string{id}, Session: "s1", Now: triageNow})
	}
	gateCall := func(a *Agent, report, id string) HookPayload {
		return bash(a, "s1", "meshctl inbox triage apply --report "+report+" "+id)
	}

	t.Run("no grant refuses unchanged", func(t *testing.T) {
		dir, a, report, id := setup(t, false, "")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if _, err := apply(dir, a, report, id); err == nil || !strings.Contains(err.Error(), "dispatch depth") || strings.Contains(err.Error(), "grant does not apply") {
			t.Fatalf("command: %v", err)
		}
		if d := gateEvaluate(dir, gateCall(a, report, id)); !d.Deny || strings.Contains(d.Reason, "grant does not apply") {
			t.Fatalf("gate: %+v", d)
		}
	})
	t.Run("grant lets it through by policy", func(t *testing.T) {
		dir, a, report, id := setup(t, false, "triage-apply: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := gateEvaluate(dir, gateCall(a, report, id)); d.Deny || d.Ask {
			t.Fatalf("gate: want allow, got %+v", d)
		}
		if _, err := InboxTriage(dir, a, TriageMode{}, triageNow); err != nil {
			t.Fatalf("worksheet under the grant: %v", err)
		}
		noPolicyActions(t, dir)
		res, err := apply(dir, a, report, id)
		if err != nil || len(res.Closed) != 1 || res.Grant != GrantTriageApply {
			t.Fatalf("command: %+v %v", res, err)
		}
		if r, _ := readReq(t, a, id); r.Status != StatusDeclined {
			t.Fatalf("not closed: %s", r.Status)
		}
		if pa := onePolicyAction(t, dir, "architect", GrantTriageApply); !strings.Contains(pa.Detail, id) || pa.Session != "s1" {
			t.Errorf("policy line: %+v", pa)
		}
	})
	t.Run("paused refuses and says why", func(t *testing.T) {
		dir, a, report, id := setup(t, true, "triage-apply: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if _, err := apply(dir, a, report, id); err == nil || !strings.Contains(err.Error(), "grants are paused") {
			t.Fatalf("command: %v", err)
		}
		if d := gateEvaluate(dir, gateCall(a, report, id)); !d.Deny || !strings.Contains(d.Reason, "grants are paused") {
			t.Fatalf("gate: %+v", d)
		}
	})
	t.Run("cap reached refuses and says why", func(t *testing.T) {
		dir, a, report, id := setup(t, false, "triage-apply: { max_per_day: 1 }")
		fillCap(t, dir, "architect", GrantTriageApply, triageNow, 1)
		fillCap(t, dir, "architect", GrantTriageApply, time.Now(), 1)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if _, err := apply(dir, a, report, id); err == nil || !strings.Contains(err.Error(), "grant cap reached") {
			t.Fatalf("command: %v", err)
		}
		if d := gateEvaluate(dir, gateCall(a, report, id)); !d.Deny || !strings.Contains(d.Reason, "grant cap reached") {
			t.Fatalf("gate: %+v", d)
		}
	})
	t.Run("human at the prompt is asked", func(t *testing.T) {
		dir, a, report, id := setup(t, false, "triage-apply: {}")
		if d := gateEvaluate(dir, gateCall(a, report, id)); !d.Ask {
			t.Fatalf("gate: want ask, got %+v", d)
		}
		res, err := apply(dir, a, report, id)
		if err != nil || res.Grant != "" {
			t.Fatalf("command: %+v %v", res, err)
		}
		noPolicyActions(t, dir)
	})
	t.Run("a consult keeps its deny", func(t *testing.T) {
		dir, a, report, id := setup(t, false, "triage-apply: {}")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		p := gateCall(a, report, id)
		p.InSession = true
		if d := gateEvaluate(dir, p); !d.Deny {
			t.Fatalf("gate: %+v", d)
		}
	})
}

// --- outbound (outbound:<surface>) ---

func TestGrant_Outbound(t *testing.T) {
	const mcp = "mcp__tracker__createIssue"
	setup := func(t *testing.T, paused bool, grant string) (string, *Agent) {
		dir, pm, _ := outboundPool(t, outboundTestConfig)
		if grant != "" {
			writeGrants(t, dir, outboundTestConfig, paused, "pm", grant)
		}
		return dir, pm
	}

	t.Run("no grant denies unchanged", func(t *testing.T) {
		dir, pm := setup(t, false, "")
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		d := GatePreToolUse(dir, outboundCall(pm.Dir(), mcp, nil))
		if !d.Deny || !strings.Contains(d.Reason, "no human at the prompt") || strings.Contains(d.Reason, "grant does not apply") {
			t.Fatalf("got %+v", d)
		}
		noPolicyActions(t, dir)
	})
	t.Run("grant allows and the gate records it", func(t *testing.T) {
		dir, pm := setup(t, false, `"outbound:tracker": {}`)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		d := GatePreToolUse(dir, outboundCall(pm.Dir(), mcp, nil))
		if d.Deny || d.Ask {
			t.Fatalf("want allow, got %+v", d)
		}
		pa := onePolicyAction(t, dir, "pm", OutboundGrant("tracker"))
		if !strings.Contains(pa.Detail, "tracker") || !strings.Contains(pa.Detail, mcp) || pa.Session != "sess-1" {
			t.Errorf("policy line: %+v", pa)
		}
		// A shell write to the surface is recorded with its command.
		if d := GatePreToolUse(dir, outboundBash(pm.Dir(), "gh issue create --title x")); d.Deny || d.Ask {
			t.Fatalf("shell: want allow, got %+v", d)
		}
		if got := ReadPolicyActions(dir); len(got) != 2 || !strings.Contains(got[1].Detail, "gh issue create") {
			t.Fatalf("policy log: %+v", got)
		}
	})
	t.Run("paused denies and says why", func(t *testing.T) {
		dir, pm := setup(t, true, `"outbound:tracker": {}`)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, outboundCall(pm.Dir(), mcp, nil)); !d.Deny || !strings.Contains(d.Reason, "grants are paused") {
			t.Fatalf("got %+v", d)
		}
		noPolicyActions(t, dir)
	})
	t.Run("cap reached denies and says why", func(t *testing.T) {
		dir, pm := setup(t, false, `"outbound:tracker": { max_per_day: 1 }`)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, outboundCall(pm.Dir(), mcp, nil)); d.Deny {
			t.Fatalf("first call: %+v", d)
		}
		if d := GatePreToolUse(dir, outboundCall(pm.Dir(), mcp, nil)); !d.Deny || !strings.Contains(d.Reason, "grant cap reached") {
			t.Fatalf("second call: %+v", d)
		}
		if got := ReadPolicyActions(dir); len(got) != 1 {
			t.Fatalf("a refused call must not be recorded: %+v", got)
		}
	})
	t.Run("human at the prompt is gated as before", func(t *testing.T) {
		dir, pm := setup(t, false, `"outbound:tracker": {}`)
		d := GatePreToolUse(dir, outboundCall(pm.Dir(), mcp, nil))
		if !d.Deny || !strings.Contains(d.Reason, "summary required") {
			t.Fatalf("want today's summary deny, got %+v", d)
		}
		noPolicyActions(t, dir)
	})
	t.Run("in-session work keeps its deny", func(t *testing.T) {
		dir, pm := setup(t, false, `"outbound:tracker": {}`)
		p := outboundCall(pm.Dir(), mcp, nil)
		p.PeerAgent, p.InSession, p.Work = "pm", true, true
		if d := GatePreToolUse(dir, p); !d.Deny || !strings.Contains(d.Reason, "no human at the prompt") {
			t.Fatalf("got %+v", d)
		}
		noPolicyActions(t, dir)
	})
}

// A human finish that re-confirms some sections of an entry a grant confirmed
// drops the carried-over sections rather than relabel them human-confirmed.
func TestGrant_KBFinishHumanDoesNotRelabelPolicySections(t *testing.T) {
	gitIdentity(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())
	wt := promoteOnMain(t, dir, a, "knowledge/"+StateFile, stateTwoSections)

	// Mark the seeded entry as confirmed by policy.
	l, _ := ReadLedger(a.Dir())
	l.Entries[StateFile] = policyEntry(l.Entries[StateFile], GrantKBPromote)
	if err := WriteLedger(a.Dir(), l); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, a.Dir(), "policy ledger")
	if _, err := git(wt, "rebase", "main"); err != nil {
		t.Fatal(err)
	}

	writeWT(t, wt, "knowledge/"+StateFile, "## Current focus\n\nShipping v3.\n\n## Open questions\n\n- none\n")
	res, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: writeFinishSummary(t, a, "sess-1", "Move focus to v3."), Now: finishNow})
	if err != nil {
		t.Fatalf("kb finish: %v", err)
	}
	e := ledgerOnMain(t, a).Entries[StateFile]
	if e.By != "" || e.Grant != "" || e.Sections["current-focus"] == "" {
		t.Fatalf("human-confirmed section: %+v", e)
	}
	if _, ok := e.Sections["open-questions"]; ok {
		t.Fatalf("a policy-confirmed section was relabelled human: %+v", e)
	}
	if !strings.Contains(strings.Join(res.Dropped, ","), "state.md#open-questions") {
		t.Fatalf("Dropped = %v", res.Dropped)
	}
}
