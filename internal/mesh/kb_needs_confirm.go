package mesh

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// kb_needs_confirm.go — headless and delegated promotion.
//
// A peer with no human at the prompt (`kb finish --headless`, dispatch depth
// set and non-zero, a distil run, or in-session delegated work, which the
// gate stops) cannot confirm a promotion. The finish parks instead: it stops
// before main moves, leaves the branch and worktree intact and names the
// branch and the summary path, so the peer's own interactive session can run
// `kb finish` there later. A candidates-only finish still merges headless.
//
// `needs-confirm` is a receipt outcome, never an intake status: the delegated
// peer's response uses the existing `needs-human` status with a body that
// opens `needs-confirm:` and carries the confirmation round (the summary
// file's text) and the summary path (RespondNeedsConfirm).

// NeedsConfirmPrefix opens every needs-confirm message, receipt line and
// intake response body.
const NeedsConfirmPrefix = "needs-confirm:"

// ErrKBNeedsConfirm: a promotion was reached with no human at the prompt; the
// branch and worktree are parked for the peer's own session. Match it with
// errors.Is; errors.As to *KBNeedsConfirmError for the parked record.
var ErrKBNeedsConfirm = errors.New("kb finish: needs-confirm")

// NeedsConfirm is a promotion parked for the human: what a peer's own session
// (and its pending-work block) needs to confirm it later.
type NeedsConfirm struct {
	Agent       string   // the peer whose knowledge the branch promotes into
	Session     string   // the KB session id
	Branch      string   // the parked KB branch (mesh/kb/<session>, or legacy)
	Worktree    string   // the parked worktree
	SummaryPath string   // the confirmation summary (written or to be written)
	Promoted    []string // kb-relative promoted paths
	Round       string   // the confirmation round: the summary's text, "" when not yet written
	Related     []string // the ask's related-entries/ownership/inbound-link lines (PromotionRelated)
	Cause       string   // why no human is at the prompt ("--headless", "dispatch depth 1", ...)
}

// FinishCommand is the command the peer's own session runs to confirm it.
func (n NeedsConfirm) FinishCommand() string {
	return fmt.Sprintf("meshctl kb finish --agent %s --session %s --summary-file %s", n.Agent, n.Session, n.SummaryPath)
}

// Headline is the one-line statement after NeedsConfirmPrefix.
func (n NeedsConfirm) Headline() string {
	return fmt.Sprintf("KB branch %s promotes %s into %s's knowledge and waits for the human to confirm it from %s's own session; "+
		"summary %s", n.Branch, quoteList(n.Promoted), n.Agent, n.Agent, n.SummaryPath)
}

// Receipt renders the work-receipt lines for a parked promotion: the outcome
// line first (`- outcome: needs-confirm`), then branch, worktree, summary and
// the command the peer's own session runs.
func (n NeedsConfirm) Receipt() string {
	var b strings.Builder
	b.WriteString("- outcome: needs-confirm (no human at the prompt")
	if n.Cause != "" {
		b.WriteString(": " + n.Cause)
	}
	b.WriteString(")\n")
	fmt.Fprintf(&b, "- KB branch: %s (worktree %s, left intact)\n", n.Branch, n.Worktree)
	fmt.Fprintf(&b, "- promotes: %s\n", quoteList(n.Promoted))
	if n.Round == "" {
		fmt.Fprintf(&b, "- summary: %s (not written yet — write it before confirming)\n", n.SummaryPath)
	} else {
		fmt.Fprintf(&b, "- summary: %s\n", n.SummaryPath)
	}
	fmt.Fprintf(&b, "- to confirm, from %s's own session: read the round out, then `%s`\n", n.Agent, n.FinishCommand())
	return b.String()
}

// ResponseBody is the delegated peer's intake response for a parked
// promotion: it opens `needs-confirm:`, then the confirmation round, then the
// summary path and the command to run.
func (n NeedsConfirm) ResponseBody() string {
	var b strings.Builder
	b.WriteString(NeedsConfirmPrefix + " " + n.Headline() + ".\n\n")
	if r := strings.TrimSpace(n.Round); r != "" {
		b.WriteString("### Confirmation round\n\n" + r + "\n\n")
	} else {
		b.WriteString("_No confirmation summary written yet._\n\n")
	}
	if len(n.Related) > 0 {
		b.WriteString(strings.Join(n.Related, "\n") + "\n\n")
	}
	fmt.Fprintf(&b, "Summary: %s\n\nConfirm from %s's own session: `%s`\n", n.SummaryPath, n.Agent, n.FinishCommand())
	return b.String()
}

// KBNeedsConfirmError is the error KBFinish returns for a parked promotion.
type KBNeedsConfirmError struct {
	NeedsConfirm
}

func (e *KBNeedsConfirmError) Error() string {
	return NeedsConfirmPrefix + " " + e.Headline() + " — main is untouched; the branch and worktree " + e.Worktree +
		" are left intact. Run `" + e.FinishCommand() + "` from " + e.Agent + "'s own interactive session."
}

// Is matches ErrKBNeedsConfirm.
func (e *KBNeedsConfirmError) Is(target error) bool { return target == ErrKBNeedsConfirm }

// RespondNeedsConfirm writes the delegated peer's outcome for a parked
// promotion (or any needs-confirm stop) onto its intake request: status
// needs-human with NeedsConfirm.ResponseBody. o.Status and o.Response are
// overridden; Session and Now are used as given.
func RespondNeedsConfirm(agent *Agent, id string, n NeedsConfirm, o RespondOptions) (*Request, error) {
	o.Status = StatusNeedsHuman
	o.Response = n.ResponseBody()
	return RespondRequest(agent, id, o)
}

// kbNoHumanCause names why a finish has no human at the prompt, "" when one
// is: --headless, dispatch depth set and non-zero, or a distil run.
func kbNoHumanCause(headless bool) string {
	switch {
	case envInt("AGENT_DISPATCH_DEPTH", 0) != 0:
		return fmt.Sprintf("dispatch depth %d", envInt("AGENT_DISPATCH_DEPTH", 0))
	case DistillingNow():
		return "a distil run"
	case headless:
		return "--headless"
	}
	return ""
}

// parkNeedsConfirm builds the parked record for a promotion KBFinish stops.
func parkNeedsConfirm(agentsDir string, agent *Agent, session, branch, wt, summaryFile string, promoted []string, cause string) *KBNeedsConfirmError {
	path := summaryFile
	if path == "" {
		path = SummaryPath(agent.Dir(), session)
	}
	round := ""
	var s Summary
	if sum, err := ReadSummary(path); err == nil {
		s, round = sum, strings.TrimSpace(sum.Text)
	}
	nc := &KBNeedsConfirmError{NeedsConfirm{
		Agent: agent.Name, Session: session, Branch: branch, Worktree: wt, SummaryPath: path,
		Promoted: append([]string(nil), promoted...), Round: round, Cause: cause,
		Related: PromotionRelated(agentsDir, agent, branch, s),
	}}
	_ = writeParkMarker(agent.Dir(), nc.NeedsConfirm, time.Now())
	return nc
}

// ParkMarkerExt names the park marker beside a session's summary:
// <agentDir>/.memory/confirm/<session>.needs-confirm. It is written when a
// promotion parks (KBFinish with no human, or the gate's no-human deny) and
// removed when the branch is finished or aborted; ParkedKBBranches lists the
// markers instead of diffing every unheld KB branch.
const ParkMarkerExt = ".needs-confirm"

// parkMarker is the marker's JSON body.
type parkMarker struct {
	Session  string    `json:"session"`
	Branch   string    `json:"branch"`
	Summary  string    `json:"summary"`
	Promoted []string  `json:"promoted"`
	Cause    string    `json:"cause"`
	Parked   time.Time `json:"parked"`
}

// ParkMarkerPath is the park marker for one KB session.
func ParkMarkerPath(agentDir, session string) string {
	return strings.TrimSuffix(SummaryPath(agentDir, session), ".md") + ParkMarkerExt
}

func writeParkMarker(agentDir string, n NeedsConfirm, now time.Time) error {
	b, err := json.MarshalIndent(parkMarker{Session: n.Session, Branch: n.Branch, Summary: n.SummaryPath,
		Promoted: n.Promoted, Cause: n.Cause, Parked: now.UTC()}, "", "  ")
	if err != nil {
		return err
	}
	p := ParkMarkerPath(agentDir, n.Session)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

func clearParkMarker(agentDir, session string) {
	_ = os.Remove(ParkMarkerPath(agentDir, session))
}

// readParkMarkers lists a peer's park markers (one directory listing); a
// marker's age is its file's modification time.
func readParkMarkers(agentDir string) []parkMarker {
	dir := filepath.Dir(SummaryPath(agentDir, "x"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []parkMarker
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ParkMarkerExt) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m parkMarker
		if json.Unmarshal(b, &m) != nil || m.Session == "" {
			continue
		}
		if fi, err := e.Info(); err == nil {
			m.Parked = fi.ModTime()
		}
		out = append(out, m)
	}
	return out
}

func quoteList(xs []string) string {
	if len(xs) == 0 {
		return "(nothing)"
	}
	return "`" + strings.Join(xs, "`, `") + "`"
}

// gateNoHumanPromotion is the gate's promotion rule with no human at the
// prompt (headless, distil, in-session work or consult): the promotion cannot
// be asked, so it is denied with the parked needs-confirm record and the
// instruction to respond needs-human — never relayed for confirmation.
func gateNoHumanPromotion(agentsDir string, a *Agent, p HookPayload, session, branch, summaryFile, expected string, promoted []string, cause string) GateDecision {
	path := expected
	if summaryFile != "" {
		path = resolveShellPath(summaryFile, p.Cwd)
	}
	nc := parkNeedsConfirm(agentsDir, a, session, branch, worktreePath(agentsDir, a.Name, session), path, promoted, cause)
	related := ""
	if len(nc.Related) > 0 {
		related = " Carry these lines with the summary:\n" + strings.Join(nc.Related, "\n") + "\n"
	}
	return GateDecision{Deny: true, Check: GateCheckPromotion, Reason: "mesh: " + nc.Error() + related +
		" This promotion has no human at the prompt (" + cause + "), so it is not asked and never relayed: stop, write the " +
		"summary at " + path + " if it is not there, and respond `needs-human` with a body opening `" + NeedsConfirmPrefix +
		"` that carries the confirmation round and the summary path (`meshctl inbox respond <id> --agent " + a.Name +
		" --status needs-human --response-file -`); list it as `needs-confirm` in your receipt."}
}
