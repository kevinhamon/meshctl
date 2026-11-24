package mesh

import "strings"

// parity.go — what works on which harness. The table is rendered
// into the README between the parity markers, and a test in cmd/meshctl walks
// the command tree: every meshctl command is either covered by a row or listed
// as harness-neutral, and the README block must match ParityMarkdown. A new
// command or a harness-specific change therefore cannot ship without saying
// where it works.
//
// The columns are the registry's (HarnessColumns): registering a harness adds
// a column, and a test fails until every row has a cell for it. A row tied to
// a capability (Cap) must agree with each column's caps: a cell starting with
// ✅ needs the cap, one starting with ✗ must lack it.

// ParityRow is one feature and its support per harness.
type ParityRow struct {
	Feature string
	// Cells is the support text per column, keyed by HarnessColumn.Key()
	// ("claude", "opencode@2"). Each cell starts with ✅, ✗ or a plain word
	// ("untested").
	Cells map[string]string
	// Cap, when set, is the capability this row reports; the parity test
	// checks every ✅/✗ cell against the column's HarnessCaps.
	Cap func(HarnessCaps) bool
	// Commands are the meshctl command paths whose behaviour this row
	// describes ("kb finish", "hook status-line").
	Commands []string
}

// Column keys of the built-in harnesses (HarnessColumn.Key).
const (
	colClaude     = HarnessClaude
	colOpencodeV2 = HarnessOpencode + "@2"
	colOpencodeV1 = HarnessOpencode + "@1"
)

// HarnessParity is the feature × harness table, in README order.
var HarnessParity = []ParityRow{
	{Feature: "Doctrine and mesh skills loaded",
		Cells: map[string]string{
			colClaude:     "✅ CLAUDE.md + CLAUDE.local.md, `.claude/skills/`",
			colOpencodeV2: "✅ plugin injects CLAUDE.md + CLAUDE.local.md; `.opencode/skills/`",
			colOpencodeV1: "✅ `instructions`; `.opencode/skills/`"},
		Commands: []string{"agent new", "agent onboard", "agent bootstrap", "pool upgrade"}},
	{Feature: "Session start: handoff, state digest, pending work, intuition",
		Cells:    map[string]string{colClaude: "✅", colOpencodeV2: "✅", colOpencodeV1: "✅"},
		Commands: []string{"hook session-start", "hook user-prompt"}},
	{Feature: "Session presence and capture",
		Cells:    map[string]string{colClaude: "✅ SessionEnd", colOpencodeV2: "✅ heartbeat, idle window", colOpencodeV1: "✅ idle event"},
		Commands: []string{"hook session-beat", "hook session-end", "hook event", "session"}},
	{Feature: "Console \"Bring to front\" (iTerm2 on macOS)",
		Cells: map[string]string{
			colClaude:     "✅",
			colOpencodeV2: "✅ with `--standalone`; ✗ in the background-service mode (no terminal)",
			colOpencodeV1: "untested"}},
	{Feature: "Pre-tool gate: memory stores, intake, KB rules, peer contracts",
		Cells:    map[string]string{colClaude: "✅", colOpencodeV2: "✅", colOpencodeV1: "✗ no pre-tool hook"},
		Cap:      func(c HarnessCaps) bool { return c.PreToolGate },
		Commands: []string{"hook pre-tool-use"}},
	{Feature: "Promotion asks: `kb finish`, `kb confirm`, `state set`, `inbox triage apply`",
		Cells:    map[string]string{colClaude: "✅ gate ask", colOpencodeV2: "✅ opencode's permission ask", colOpencodeV1: "✗ refused"},
		Cap:      func(c HarnessCaps) bool { return c.PromotionAsk },
		Commands: []string{"kb finish", "kb confirm", "state set", "inbox triage apply"}},
	{Feature: "Agent writes to `pool/`",
		Cells: map[string]string{colClaude: "✅ asked, committed after", colOpencodeV2: "✗ refused; edit by hand", colOpencodeV1: "✗"},
		Cap:   func(c HarnessCaps) bool { return c.GateAsk }},
	{Feature: "Outbound-write gate",
		Cells: map[string]string{colClaude: "✅ asked once per summary", colOpencodeV2: "✗ refused, no ask path", colOpencodeV1: "✗"},
		Cap:   func(c HarnessCaps) bool { return c.GateAsk }},
	{Feature: "After-call record: pool commits, outbound acks",
		Cells:    map[string]string{colClaude: "✅ PostToolUse", colOpencodeV2: "✗ not called", colOpencodeV1: "✗"},
		Cap:      func(c HarnessCaps) bool { return c.PostToolHook },
		Commands: []string{"hook post-tool-use"}},
	{Feature: "Headless dispatch",
		Cells: map[string]string{
			colClaude:     "✅ `claude -p`",
			colOpencodeV2: "✅ `opencode run --standalone`, locked-down permissions",
			colOpencodeV1: "✅ `opencode run --pure`"},
		Commands: []string{"ask", "msg ask"}},
	{Feature: "In-session peers, consult and work (`ask --in-session [--work]`)",
		Cells:    map[string]string{colClaude: "✅ Agent tool", colOpencodeV2: "✅ `subagent` tool; mutating peers asked by opencode", colOpencodeV1: "✗ refused"},
		Cap:      func(c HarnessCaps) bool { return c.InSessionPeers },
		Commands: []string{"agent brief"}},
	{Feature: "Distillation at a boundary",
		Cells: map[string]string{colClaude: "✅ distiller subagent", colOpencodeV2: "✅ at session end", colOpencodeV1: "✅ at session end"}},
	{Feature: "Status line",
		Cells: map[string]string{
			colClaude:     "✅ every 10s",
			colOpencodeV2: "✅ TUI panel (sidebar + prompt footer) every 10s; not in `opencode run`",
			colOpencodeV1: "✗ none"},
		Cap:      func(c HarnessCaps) bool { return c.StatusLine },
		Commands: []string{"hook status-line"}},
	{Feature: "Rebuild a pool elsewhere: member list (`pool/members.yaml`), `pool clone`",
		Cells:    map[string]string{colClaude: "✅", colOpencodeV2: "✅", colOpencodeV1: "✅"},
		Commands: []string{"pool clone"}},
	{Feature: "Console: live pool view, open sessions (`meshctl console`)",
		Cells:    map[string]string{colClaude: "✅ with scheduled jobs", colOpencodeV2: "✅ no scheduled jobs", colOpencodeV1: "✅ no scheduled jobs"},
		Commands: []string{"console"}},
}

// ParityNeutral lists the commands that behave the same on every harness:
// plain CLI over files and git.
var ParityNeutral = []string{
	"agent identity", "agent list", "console install", "console uninstall", "doctor",
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

// ParityMarkdown renders HarnessParity as the README table, one column per
// registered harness variant (HarnessColumns).
func ParityMarkdown() string {
	cols := HarnessColumns()
	var b strings.Builder
	b.WriteString("| Feature |")
	for _, c := range cols {
		b.WriteString(" " + c.Label + " |")
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(cols)) + "\n")
	for _, r := range HarnessParity {
		b.WriteString("| " + r.Feature + " |")
		for _, c := range cols {
			b.WriteString(" " + r.Cells[c.Key()] + " |")
		}
		b.WriteString("\n")
	}
	return b.String()
}
