package mesh

import (
	"fmt"
	"strings"
	"time"
)

// triage_apply.go — `meshctl inbox triage apply --report <file> <id>...`
// (ADR-0072; spec decisions 59–61, cross-cutting 70). After the human has
// confirmed the report item by item in the confirmation round, apply closes
// the confirmed requests through the respond path, so claim rules and
// read-back work unchanged: `superseded` and `done` set `filed`, `withdrawn`
// sets `declined`, each stamped `triage: <verdict> → <ref>` with a response
// body stating the verdict, the grounding and how to dispute. Everything is
// checked before the first request changes; the one write on a refusal is the
// `· branch live` suffix on the lines whose work branch is still live.

// TriageApplyCap is the most request ids one apply call takes.
const TriageApplyCap = 5

// TriageApplyOptions parameterizes an apply.
type TriageApplyOptions struct {
	Report  string   // the triage report file (required)
	IDs     []string // 1..TriageApplyCap request ids
	Mode    TriageMode
	Session string // the applying session (claim ownership check)
	Now     time.Time
}

// TriageApplyResult lists the closed requests, re-read after the close.
type TriageApplyResult struct {
	Closed []*Request
}

// TriageStatus maps a closing verdict to the intake status it sets.
func TriageStatus(verdict string) string {
	if verdict == TriageWithdrawn {
		return StatusDeclined
	}
	return StatusFiled
}

// TriageResponseBody is the response body a triage close writes.
func TriageResponseBody(l TriageReportLine) string {
	return fmt.Sprintf("Closed by triage as %s → %s. Grounding: %q. Dispute: re-ask with related: %s.", l.Verdict, l.Ref, l.Quote, l.ID)
}

// TriageApply closes the named requests of peer a per the report.
func TriageApply(agentsDir string, a *Agent, o TriageApplyOptions) (*TriageApplyResult, error) {
	if r := TriageRefusal(o.Mode); r != "" {
		return nil, fmt.Errorf("%s", r)
	}
	if strings.TrimSpace(o.Report) == "" {
		return nil, fmt.Errorf("`inbox triage apply` needs the triage report: --report %s", TriageReportPath(a.Dir(), o.Now))
	}
	switch {
	case len(o.IDs) == 0:
		return nil, fmt.Errorf("name at least one request id to close")
	case len(o.IDs) > TriageApplyCap:
		return nil, fmt.Errorf("at most %d ids per call (got %d) — apply the rest in another call", TriageApplyCap, len(o.IDs))
	}
	rep, err := ReadTriageReport(o.Report)
	if err != nil {
		return nil, err
	}

	type closing struct {
		line TriageReportLine
		ref  TriageRef
		req  *Request
	}
	var todo []closing
	var problems, live []string
	seen := map[string]bool{}
	for _, id := range o.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		l, ok := rep.Line(id)
		if !ok {
			problems = append(problems, id+": no report line in "+o.Report)
			continue
		}
		if !TriageClosing(l.Verdict) {
			problems = append(problems, fmt.Sprintf("%s: verdict is %s — nothing to apply (the request stays open)", id, l.Verdict))
			continue
		}
		ref, err := ResolveTriageRef(agentsDir, l.Ref)
		if err != nil {
			problems = append(problems, id+": "+err.Error())
			continue
		}
		req, err := findRequest(a, id)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if !TriageOpen(req.Status) {
			problems = append(problems, fmt.Sprintf("%s: is %s — triage closes only pending or in-progress requests", id, req.Status))
			continue
		}
		if err := claimBlocks(req, o.Session, o.Now); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if triageWorkLive(agentsDir, a, id) {
			live = append(live, id)
			problems = append(problems, fmt.Sprintf("%s: %s · %s — merge or discard it first (`meshctl work merge %s`), then re-apply; nothing deletes a branch",
				id, WorkBranchPrefix+id, TriageBranchLive, id))
			continue
		}
		todo = append(todo, closing{line: l, ref: ref, req: req})
	}
	if len(live) > 0 {
		if err := markTriageBranchLive(rep, live); err != nil {
			problems = append(problems, "could not mark the report: "+err.Error())
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("triage apply refused (nothing closed):\n  %s", strings.Join(problems, "\n  "))
	}

	res := &TriageApplyResult{}
	for _, c := range todo {
		body := TriageResponseBody(c.line)
		if c.ref.Kind == TriageRefRequest && c.ref.ID != c.req.ID {
			if other := triageRequestFrom(agentsDir, c.ref); other != "" && other != c.req.From {
				body += fmt.Sprintf("\n\nThe same ask is open as %s (from %s); read back on that request.", c.ref.Raw, other)
			}
		}
		r, err := RespondRequest(a, c.req.ID, RespondOptions{
			Status: TriageStatus(c.line.Verdict), Response: body, Session: o.Session, Now: o.Now,
			extra: map[string]string{"triage": yamlQuote(c.line.Verdict + " → " + c.line.Ref)},
		})
		if err != nil {
			return res, fmt.Errorf("%s: %w", c.req.ID, err)
		}
		res.Closed = append(res.Closed, r)
	}
	return res, nil
}

// triageWorkLive: the request's mesh/work/<id> branch exists in the peer's
// repo, or its delegated-work worktree exists.
func triageWorkLive(agentsDir string, a *Agent, id string) bool {
	if dirExists(workWorktreePath(agentsDir, a.Name, id)) {
		return true
	}
	if !isGitRepo(a.Dir()) {
		return false
	}
	_, err := git(a.Dir(), "rev-parse", "--verify", "--quiet", "refs/heads/"+workBranch(id))
	return err == nil
}

// triageRequestFrom is the requester of a `<peer>:<id>` ref, "" if unknown.
func triageRequestFrom(agentsDir string, ref TriageRef) string {
	peer, err := FindAgent(agentsDir, ref.Peer)
	if err != nil {
		return ""
	}
	if r, err := findRequest(peer, ref.ID); err == nil {
		return r.From
	}
	return ""
}

// TriageApplyMarkdown summarizes an apply for the CLI.
func TriageApplyMarkdown(res *TriageApplyResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Closed %d request(s) by triage:\n", len(res.Closed))
	for _, r := range res.Closed {
		fmt.Fprintf(&b, "- %s → %s (triage: %s)\n", r.ID, r.Status, r.Triage)
	}
	b.WriteString("\nEach is on its requester's read-back list (`meshctl sent`); no distillation is owed before archive.\n")
	return b.String()
}

// --- gate (step 5e) ---

// triageApplyCall is a parsed `meshctl inbox triage apply` invocation.
type triageApplyCall struct {
	agent, report string
	ids           []string
}

// parseTriageApply finds the first `meshctl inbox triage apply` simple
// command of a shell line (shellparse.go: subcommand-anchored, heredoc
// bodies dropped) and reads its --agent, --report and id arguments.
func parseTriageApply(command string) (c triageApplyCall, ok bool) {
	call, ok := parseMeshctlCall(command, "meshctl inbox triage apply")
	if !ok {
		return c, false
	}
	return triageApplyCall{agent: call.flag("agent"), report: call.flag("report"), ids: call.args}, true
}

// gateTriageApply asks on a Bash `meshctl inbox triage apply` with the report
// lines for the named ids as the ask text; no report, an unreadable one, or
// no human at the prompt is a deny. The command refuses the rest itself.
func gateTriageApply(agentsDir string, p HookPayload) GateDecision {
	if agentsDir == "" {
		return GateDecision{}
	}
	command := shellCommandInput(p)
	if command == "" {
		return GateDecision{}
	}
	call, ok := parseTriageApply(command)
	if !ok {
		return GateDecision{}
	}
	deny := func(reason string) GateDecision {
		return GateDecision{Deny: true, Check: GateCheckTriage, Reason: reason}
	}
	if r := TriageRefusal(TriageMode{InSession: p.InSession, Work: p.Work}); r != "" {
		return deny(r)
	}
	var a *Agent
	if call.agent != "" {
		a, _ = FindAgent(agentsDir, call.agent)
	} else {
		a = payloadAgent(agentsDir, p)
	}
	where := "<peer>/" + MemoryDir + "/" + TriageDir + "/<date>.md"
	if a != nil {
		where = TriageReportPath(a.Dir(), time.Now())
	}
	if call.report == "" {
		return deny("mesh: `inbox triage apply` closes requests on the triage report's word — pass it: `--report " + where +
			"`, after reading its lines to the human in the confirmation round (ADR-0072).")
	}
	path := resolveShellPath(call.report, p.Cwd)
	rep, err := ReadTriageReport(path)
	if rep == nil {
		return deny("mesh: `inbox triage apply` cannot read the triage report " + path + " (" + err.Error() + ") — write it at " + where + " first.")
	}
	peer := "the peer"
	agentDir := ""
	if a != nil {
		peer, agentDir = a.Name, a.Dir()
	}
	body := []string{fmt.Sprintf("Closes %d of %s's requests per %s:", len(call.ids), peer, path)}
	for _, id := range call.ids {
		if l, ok := rep.Line(id); ok {
			body = append(body, strings.TrimSpace(l.Raw))
		} else {
			body = append(body, "- "+id+" · (no report line — apply will refuse it)")
		}
	}
	if err != nil {
		body = append(body, "", "The report has grammar errors; apply will refuse until they are fixed.")
	}
	text := RenderAsk(agentDir, AskText{
		Check:  GateCheckTriage,
		Intent: fmt.Sprintf("close %d triaged request(s) of %s (superseded/done → filed, withdrawn → declined)", len(call.ids), peer),
		Body:   body,
		Footer: "Allow only if you confirmed each line in the confirmation round; the requester sees the close on read-back.",
	})
	return GateDecision{Ask: true, Check: GateCheckTriage, Reason: text}
}
