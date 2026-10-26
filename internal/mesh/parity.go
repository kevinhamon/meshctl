package mesh

import "strings"

// parity.go — what works on which harness (ADR-0075). The table is rendered
// into the README between the parity markers, and a test in cmd/meshctl walks
// the command tree: every meshctl command is either covered by a row or listed
// as harness-neutral, and the README block must match ParityMarkdown. A new
// command or a harness-specific change therefore cannot ship without saying
// where it works.

// ParityRow is one feature and its support per harness.
type ParityRow struct {
	Feature                        string
	Claude, OpencodeV2, OpencodeV1 string
	// Commands are the meshctl command paths whose behaviour this row
	// describes ("kb finish", "hook status-line").
	Commands []string
}

// HarnessParity is the feature × harness table, in README order.
var HarnessParity = []ParityRow{
	{"Doctrine and mesh skills loaded", "✅ CLAUDE.md, `.claude/skills/`", "✅ plugin injects CLAUDE.md; `.opencode/skills/`", "✅ `instructions`; `.opencode/skills/`",
		[]string{"agent new", "agent onboard", "agent bootstrap", "pool upgrade"}},
	{"Session start: handoff, state digest, pending work, intuition", "✅", "✅", "✅",
		[]string{"hook session-start", "hook user-prompt"}},
	{"Session presence and capture", "✅ SessionEnd", "✅ heartbeat, idle window", "✅ idle event",
		[]string{"hook session-beat", "hook session-end", "session"}},
	{"Pre-tool gate: memory stores, intake, KB rules, peer contracts", "✅", "✅", "✗ no pre-tool hook",
		[]string{"hook pre-tool-use"}},
	{"Promotion asks: `kb finish`, `kb confirm`, `state set`, `inbox triage apply`", "✅ gate ask", "✅ opencode's permission ask", "✗ refused",
		[]string{"kb finish", "kb confirm", "state set", "inbox triage apply"}},
	{"Agent writes to `pool/`", "✅ asked, committed after", "✗ refused; edit by hand", "✗",
		nil},
	{"Outbound-write gate", "✅ asked once per summary", "✗ refused, no ask path", "✗",
		nil},
	{"After-call record: pool commits, outbound acks", "✅ PostToolUse", "✗ not called", "✗",
		[]string{"hook post-tool-use"}},
	{"Headless dispatch", "✅ `claude -p`", "✅ `opencode run --standalone`, locked-down permissions", "✅ `opencode run --pure`",
		[]string{"ask", "msg ask"}},
	{"In-session peers, consult and work (`ask --in-session [--work]`)", "✅ Agent tool", "✅ `subagent` tool; mutating peers asked by opencode", "✗ refused",
		[]string{"agent brief"}},
	{"Distillation at a boundary", "✅ distiller subagent", "✅ at session end", "✅ at session end",
		nil},
	{"Status line", "✅ every 10s", "✗ none", "✗ none",
		[]string{"hook status-line"}},
}

// ParityNeutral lists the commands that behave the same on every harness:
// plain CLI over files and git.
var ParityNeutral = []string{
	"agent identity", "agent list", "doctor",
	"handoff set", "handoff show", "handoff template",
	"inbox", "inbox archive", "inbox claim", "inbox decline", "inbox list", "inbox next", "inbox release", "inbox respond", "inbox triage",
	"intuition recall",
	"kb", "kb begin", "kb consolidate", "kb reconcile",
	"memory rebuild", "memory recall", "memory sessions",
	"msg send", "send", "pending", "sent", "sent ack",
	"pool forget", "pool init", "pool list", "pool migrate", "pool register", "pool teardown",
	"state show", "state template",
	"update", "version", "work list", "work merge",
}

// ParityMarkdown renders HarnessParity as the README table.
func ParityMarkdown() string {
	var b strings.Builder
	b.WriteString("| Feature | Claude Code | opencode V2 (2.x) | opencode V1 (1.x) |\n|---|---|---|---|\n")
	for _, r := range HarnessParity {
		b.WriteString("| " + r.Feature + " | " + r.Claude + " | " + r.OpencodeV2 + " | " + r.OpencodeV1 + " |\n")
	}
	return b.String()
}
