package mesh

import (
	"os"
	"path/filepath"
	"time"
)

// gate_state.go — the promotion ask at `meshctl state set`. The digest is
// current-tier knowledge, so `state set` is a promotion: asked with the peer's
// summary as the signature (the Intent line, the `### state.md` bullets, the
// deny-if-not-read line), denied naming the summary path when there is no
// usable summary, and denied when no human is at the prompt. The command
// enforces the same (StateSetConfirmed); the gate is where the human sees the
// summary. Same check name as `kb finish`.

// stateSetCall is a parsed `meshctl state set` invocation.
type stateSetCall struct {
	agent, summaryFile string
}

// parseStateSet finds the first `meshctl state set` simple command of a
// shell line (shellparse.go: heredoc bodies dropped, subcommand-anchored).
func parseStateSet(command string) (c stateSetCall, ok bool) {
	call, ok := parseMeshctlCall(command, "meshctl state set")
	if !ok {
		return c, false
	}
	return stateSetCall{agent: call.flag("agent"), summaryFile: call.flag("summary-file")}, true
}

// gateStateSet is the promotion rule for a Bash `meshctl state set`.
func gateStateSet(agentsDir string, p HookPayload) GateDecision {
	if agentsDir == "" {
		return GateDecision{}
	}
	command := shellCommandInput(p)
	if command == "" {
		return GateDecision{}
	}
	call, ok := parseStateSet(command)
	if !ok {
		return GateDecision{}
	}
	deny := func(reason string) GateDecision {
		return GateDecision{Deny: true, Check: GateCheckPromotion, Reason: "mesh: " + reason}
	}
	if cause := noHumanCause(p); cause != "" {
		return deny("`meshctl state set` rewrites your current-state digest, a promotion the human confirms — a human must " +
			"be at the prompt, and this call runs under " + cause + ". Leave the digest for an interactive session.")
	}

	var a *Agent
	if call.agent != "" {
		a, _ = FindAgent(agentsDir, call.agent)
	} else {
		a = payloadAgent(agentsDir, p)
	}
	if a == nil {
		return deny("this `meshctl state set` promotes a current-state digest, but the gate cannot tell which peer it is for — " +
			"run it from your workspace or pass `--agent <you>`, so it can be asked with your summary.")
	}
	harness := p.SessionID
	if harness == "" {
		harness = ResolveAgentSessionID(a, os.Getpid(), time.Now())
	}
	expected := SummaryPath(a.Dir(), harness)
	if call.summaryFile == "" {
		return deny("`state set` rewrites your current-state digest and needs your confirmation summary: write it at " +
			expected + " (`## Intent` one line first, `## Grounding`, `## Precedent`, and `## Changes` with a `### " +
			StateFile + "` block), read the confirmation round out in chat, then re-run `meshctl state set --summary-file " +
			expected + "`.")
	}
	sumPath := resolveShellPath(call.summaryFile, p.Cwd)
	s, why := usableSummary(sumPath)
	if why != "" {
		named := ""
		if filepath.Clean(sumPath) != filepath.Clean(expected) {
			named = " (expected " + expected + ")"
		}
		return deny("`state set` needs your confirmation summary first — " + why + named + ". Write `## Intent` (one line, " +
			"first), `## Grounding`, `## Precedent` and `## Changes`, read the confirmation round out in chat, then re-run: " +
			"the gate asks the human with your summary." + writeFirstHint(sumPath))
	}
	body := []string{"state set rewrites `" + a.Name + "`'s current-state digest (" + KnowledgeDir + "/" + StateFile + "):"}
	body = append(body, PromotionAskBody(s, []string{StateFile})...)
	text := RenderAsk(a.Dir(), AskText{Check: GateCheckPromotion, Intent: s.Intent, Body: body, Footer: PromotionAskFooter})
	return GateDecision{Ask: true, Check: GateCheckPromotion, Reason: text}
}
