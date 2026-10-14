package mesh

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// insession.go — `meshctl msg ask --in-session` and `meshctl agent brief`
//. The headless path (dispatch.go) spawns a second harness process
// in the peer's workspace; the in-session path keeps one process and delegates
// to the peer's subagent definition (peers.go). Both write the same intake file,
// log to the same dispatch.log and count against the same hourly cap; only the
// runtime differs, so the audit trail reads the same either way.
//
// What the in-session path does NOT need: the single-flight lock (no process
// to serialize) and the timeout (the caller's harness owns the subagent).
// What it cannot do: the peer's own SessionStart/UserPrompt hooks do not fire
// inside a subagent, so `agent brief` delivers that context on demand instead.

// InSessionOptions parameterizes an in-session ask.
type InSessionOptions struct {
	Type     string
	Prompt   string
	From     string
	Priority string
	Related  []string

	Now    time.Time
	Stdout io.Writer
}

// AskInSession writes the intake request, runs the anti-cascade guards that
// apply without a process (depth, hourly cap), claims the request for the
// caller's harness session, logs the dispatch, and prints the exact delegation
// the caller's model must now make with its Agent tool. It never runs a model.
func AskInSession(agentsDir string, target *Agent, o InSessionOptions) (*Request, int, error) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.From == "" {
		o.From = CallerName()
	}
	caller := o.From

	if !target.IsDispatchable() {
		reason := "not dispatchable"
		if len(target.Mutates) > 0 {
			reason = fmt.Sprintf("mutates %v — state-mutating agents stay human-triggered", target.Mutates)
		}
		return nil, 1, fmt.Errorf("refusing to dispatch %q: %s. Use `meshctl send %s %s \"…\"` and let the human trigger it",
			target.Name, reason, target.Name, o.Type)
	}
	// The caller must be a Claude Code workspace carrying the peer's subagent
	// definition; otherwise the delegation line names something that does
	// not exist. An opencode caller has no Agent tool at all.
	callerAgent, err := FindAgent(agentsDir, caller)
	if err != nil {
		return nil, 1, fmt.Errorf("in-session dispatch needs a caller workspace in this pool: %w (run from your workspace, or set --from)", err)
	}
	if callerAgent.HarnessName() != HarnessClaude {
		return nil, 1, fmt.Errorf("in-session dispatch runs the peer as a Claude Code subagent; %q runs on %s — use `meshctl ask %s %s \"…\"` (headless) instead",
			caller, callerAgent.HarnessName(), target.Name, o.Type)
	}
	if !fileExists(PeerAgentFile(callerAgent.Dir(), target.Name)) {
		return nil, 1, fmt.Errorf("no in-session definition for %q in %s/.claude/agents/ — run `meshctl agent onboard %s` (or `meshctl pool upgrade`) to render the peer subagents, then retry",
			target.Name, caller, caller)
	}

	depth := envInt("AGENT_DISPATCH_DEPTH", 0)
	if depth >= 1 {
		dispatchLog(agentsDir, caller, target.Name, depth, now, "BLOCKED reason=depth mode=in-session")
		return nil, ExitDepth, coded(ExitDepth,
			"BLOCKED — dispatch depth %d>=1. A dispatched agent may not dispatch again.\n"+
				"  Resolve by returning 'needs-human' in the request file (name the agent + question); the human drives the next hop.", depth)
	}
	recent, err := hourlyCount(agentsDir, now)
	if err != nil {
		return nil, 1, err
	}
	if recent >= hourlyMax() {
		dispatchLog(agentsDir, caller, target.Name, depth, now, "BLOCKED reason=hourly-cap mode=in-session recent=%d max=%d", recent, hourlyMax())
		return nil, ExitHourlyCap, coded(ExitHourlyCap,
			"BLOCKED — hourly dispatch cap reached (%d/%d in the last hour).\n"+
				"  This is a budget circuit-breaker. Wait, raise AGENT_DISPATCH_HOURLY_MAX, or resolve remaining requests with the human.", recent, hourlyMax())
	}

	req, err := WriteRequest(target, o.Type, o.Prompt, caller, o.Priority, o.Related, now)
	if err != nil {
		return nil, 1, err
	}
	_ = recordDispatch(agentsDir, now)

	// The subagent's `meshctl inbox respond` runs in the same harness session,
	// so a claim under the harness session id is one it can release. Without
	// an exported id the claim would be unreleasable by anyone but this
	// process, so mark the request in-progress instead — `inbox next` skips it
	// either way.
	if sid := harnessSessionID(); sid != "" {
		_, _ = ClaimRequest(req.Path(), sid, os.Getpid(), now)
	}
	_ = setFrontmatterFields(req.Path(), map[string]string{"status": StatusInProgress})
	req.Status = StatusInProgress
	dispatchLog(agentsDir, caller, target.Name, depth, now, "DISPATCH mode=in-session id=%s", req.ID)

	fmt.Fprintf(o.Stdout, "wrote intake %s → %s (%s)\n", req.ID, target.Name, req.Path())
	fmt.Fprintf(o.Stdout, "in-session dispatch recorded (%d/%d this hour). Now delegate with the Agent tool:\n", recent+1, hourlyMax())
	fmt.Fprintf(o.Stdout, "  subagent_type: %s\n", PeerAgentName(target.Name))
	fmt.Fprintf(o.Stdout, "  prompt: %s\n", DelegationPrompt(target.Name, req.ID))
	fmt.Fprintf(o.Stdout, "When it returns, read the answer: meshctl sent   (then `meshctl sent ack %s`)\n", req.ID)
	return req, 0, nil
}

// DelegationPrompt is the task text the caller hands the peer subagent.
func DelegationPrompt(peer, id string) string {
	return fmt.Sprintf("Process intake request %s. Start with: meshctl agent brief --agent %s --request %s", id, peer, id)
}

// harnessSessionID returns the harness session id from the environment, or "".
func harnessSessionID() string {
	for _, k := range sessionIDEnv {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// Brief renders the context a peer gets at session start, on demand: identity,
// role and accepted types, the request (when given) with recall pointers for
// its ask, the current handoff, and the write-back contract. It replaces the
// SessionStart/UserPromptSubmit injections a subagent never receives.
func Brief(agentsDir string, a *Agent, requestID string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Brief for `%s` (in-session)\n\n", a.Name)
	b.WriteString(IdentityLine(agentsDir, a))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "**Role:** %s — %s\n\n", firstNonEmpty(a.Title, a.Name), strings.TrimSpace(a.Role))
	if len(a.Accepts) > 0 {
		b.WriteString("**Accepts:**")
		for _, x := range a.Accepts {
			fmt.Fprintf(&b, " `%s`", x.Type)
			if x.Desc != "" {
				fmt.Fprintf(&b, " (%s)", x.Desc)
			}
			b.WriteString(";")
		}
		b.WriteString("\n\n")
	}
	if len(a.Owns) > 0 || len(a.Domains) > 0 {
		fmt.Fprintf(&b, "**Owns:** %s · **Domains:** %s\n\n", strings.Join(a.Owns, ", "), strings.Join(a.Domains, ", "))
	}

	query := ""
	if requestID != "" {
		r, err := findRequest(a, requestID)
		if err != nil {
			return "", err
		}
		ask := strings.TrimSpace(askSection(r.Body))
		query = ask
		fmt.Fprintf(&b, "## Request `%s`\n\nfrom: `%s` · type: `%s` · priority: %s · status: %s · file: `%s`\n\n",
			r.ID, r.From, r.Type, r.Priority, r.Status, r.Path())
		if len(r.Related) > 0 {
			fmt.Fprintf(&b, "related: %s\n\n", strings.Join(r.Related, ", "))
		}
		b.WriteString("### Ask\n\n")
		b.WriteString(ask)
		b.WriteString("\n\n")
		if ctx := contextSection(r.Body); ctx != "" {
			b.WriteString("### Context\n\n")
			b.WriteString(ctx)
			b.WriteString("\n\n")
		}
	}
	if query != "" {
		if hits, err := Recall(a, query, 8); err == nil && len(hits) > 0 {
			b.WriteString("## Recall for this request (pointers — read the KB files, they are authoritative)\n\n")
			for _, h := range hits {
				fmt.Fprintf(&b, "- [%s] %s", h.Kind, h.Title)
				if len(h.Where) > 0 {
					fmt.Fprintf(&b, " — %s", strings.Join(h.Where, "  "))
				}
				if h.Date != "" {
					fmt.Fprintf(&b, " (%s)", h.Date)
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}
	if handoff, _ := HandoffShow(a); strings.TrimSpace(handoff) != "" {
		b.WriteString("## Handoff (your last resume point)\n\n")
		b.WriteString(strings.TrimSpace(handoff))
		b.WriteString("\n\n")
	}
	b.WriteString("## Write-back contract\n\n")
	id := "<id>"
	if requestID != "" {
		id = requestID
	}
	fmt.Fprintf(&b, "- Answer ONLY through `meshctl inbox respond %s --agent %s --status answered|needs-info|needs-human --response-file -` (response markdown on stdin).\n", id, a.Name)
	fmt.Fprintf(&b, "- Not yours: `meshctl inbox decline %s --agent %s --reason \"…\" --redirect <owner>`.\n", id, a.Name)
	b.WriteString("- No file edits, commits, KB writes or dispatch (`meshctl ask|send`) — the gate denies them. Needs another agent or a new consequential decision ⇒ `needs-human` with the question and your recommendation.\n")
	b.WriteString("- Finish with a short summary (status, one-paragraph answer, request path).\n")
	return b.String(), nil
}

// contextSection extracts the "## Context" section of a request body, or ""
// when absent or still the placeholder.
func contextSection(body string) string {
	loc := strings.Index(body, "\n## Context")
	if loc < 0 {
		return ""
	}
	rest := body[loc+len("\n## Context"):]
	if n := reNextHeading.FindStringIndex(rest); n != nil {
		rest = rest[:n[0]]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" || strings.HasPrefix(rest, "_(none supplied)_") {
		return ""
	}
	return rest
}
