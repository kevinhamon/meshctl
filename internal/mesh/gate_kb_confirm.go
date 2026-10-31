package mesh

import (
	"fmt"
)

// gate_kb_confirm.go — the ask at `meshctl kb confirm`. Bulk confirmation is a
// promotion of existing entries, so it meets the `kb finish` ask: the report is
// the summary, and the ask text is the report's Intent line and its lines for
// exactly the named paths, then the deny-if-not-read line. No --report, an
// unreadable or malformed report, more than KBConfirmMax paths, or no human at
// the prompt is a deny. The command enforces the same refusals (KBConfirm); the
// gate is where the human reads the verdicts.

// kbConfirmCall is a parsed `meshctl kb confirm` invocation.
type kbConfirmCall struct {
	agent, report string
	paths         []string
}

// parseKBConfirm finds the first `meshctl kb confirm` simple command of a
// shell line (shellparse.go) and reads its paths and flags.
func parseKBConfirm(command string) (c kbConfirmCall, ok bool) {
	call, ok := parseMeshctlCall(command, "meshctl kb confirm")
	if !ok {
		return c, false
	}
	return kbConfirmCall{agent: call.flag("agent"), report: call.flag("report"), paths: call.args}, true
}

// gateKBConfirm is the ask rule for a Bash `meshctl kb confirm` (gate step 5d).
func gateKBConfirm(agentsDir string, p HookPayload) GateDecision {
	if agentsDir == "" {
		return GateDecision{}
	}
	command := shellCommandInput(p)
	if command == "" {
		return GateDecision{}
	}
	call, ok := parseKBConfirm(command)
	if !ok {
		return GateDecision{}
	}
	deny := func(reason string) GateDecision {
		return GateDecision{Deny: true, Check: GateCheckPromotion, Reason: "mesh: " + reason}
	}
	if cause := noHumanCause(p); cause != "" {
		return deny("`meshctl kb confirm` confirms knowledge, which the human does in the confirmation round — a human must " +
			"be at the prompt, and this call runs under " + cause + ". Leave it for an interactive session.")
	}
	var a *Agent
	if call.agent != "" {
		a, _ = FindAgent(agentsDir, call.agent)
	} else {
		a = payloadAgent(agentsDir, p)
	}
	if a == nil {
		return deny("this `meshctl kb confirm` confirms knowledge, but the gate cannot tell which peer it is for — run it " +
			"from your workspace or pass `--agent <you>`, so it can be asked with your report.")
	}
	if len(call.paths) == 0 {
		return deny("`kb confirm` names no paths: `meshctl kb confirm <path>... --report <file>`.")
	}
	if len(call.paths) > KBConfirmMax {
		return deny(fmt.Sprintf("`kb confirm` names %d paths; at most %d per call (a bare sectioned path counts as one) — "+
			"split it into rounds of five.", len(call.paths), KBConfirmMax))
	}
	if call.report == "" {
		return deny("`kb confirm` needs `--report <file>`: the reconcile report holding these entries' verdicts (" +
			ReconcileDir(a.Dir()) + "/<date>.md). Read the round out in chat, then re-run with the report: the gate asks the " +
			"human with its lines for these paths.")
	}
	repPath := resolveShellPath(call.report, p.Cwd)
	r, err := ReadReconcileReport(repPath)
	if err != nil {
		return deny("`kb confirm --report " + call.report + "`: no readable report at " + repPath + "." + writeFirstHint(repPath))
	}
	if err := r.Err(); err != nil {
		return deny("`kb confirm`: the report is malformed — fix these lines, then re-run:\n" + err.Error())
	}
	body := []string{fmt.Sprintf("kb confirm confirms %d path(s) in `%s`'s knowledge from %s:", len(call.paths), a.Name, repPath)}
	body = append(body, KBConfirmAskLines(a, call.paths, r)...)
	text := RenderAsk(a.Dir(), AskText{Check: GateCheckPromotion, Intent: r.Intent, Body: body, Footer: PromotionAskFooter})
	return GateDecision{Ask: true, Check: GateCheckPromotion, Reason: text}
}
