package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// gate_log.go — two things the ADR-0068 amendment asked of the gate after the
// outbound prototype: a hub's hooks STEP ASIDE for its subagents' calls, and
// every decision names the check that produced it, in a per-agent decision log.
//
// Step-aside. Claude Code fires a session's own settings hooks for every tool
// call its subagents make, so an in-session peer is gated twice: by the hub's
// hook (no flags; resolves the hub from cwd) and by its own frontmatter hook
// (`--agent <peer> --in-session [--work]`). Left alone, the hub hook evaluates
// the peer's call as the hub's own: the hub's KB rules fire on the peer's work,
// and once the outbound gate exists, a hub that is itself a mutating peer would
// look for the peer's summary in the hub's confirm dir and deny an acked write.
// So a hook that sees a subagent id in the payload without a peer flag keeps
// only the machine-global rules (competing memory stores, hand-written intake)
// and steps aside for the KB-rule gate and the outbound gate; the peer's own
// hook owns those for that call.
//
// Decision log. Deny wins across the double gate, so a human reading a denied
// retry needs to know WHICH hook and WHICH check decided: a KB rule (deny-once,
// retry allowed), the peer contract, a path rule, or — later — the outbound
// logic. One line per decision, appended to <agent>/.memory/gate.log, names the
// check, the mode and the tool. An allow no check touched says `check=none`.

// Check names: the vocabulary of the decision log's `check=` field. Each is the
// rule that produced a decision; ticket 40 (outbound) and ticket 26 (pool
// path) set the reserved ones.
const (
	GateCheckNone        = "none"         // allow: no check touched the call
	GateCheckMemoryStore = "memory-store" // claude-mem write tool, by name
	GateCheckDelegation  = "delegation"   // Agent/Task to a mutating peer (ask), or from a peer (deny)
	GateCheckPeer        = "peer"         // in-session consult / work contract
	GateCheckPath        = "path"         // file-edit path into a competing store or intake/
	GateCheckShellWrite  = "shell-write"  // shell write into the same stores
	GateCheckKBBranch    = "kb-branch"    // shell command naming a mesh/kb/ branch outside `meshctl kb`
	GateCheckKBRule      = "kb-rule"      // standing rule from the agent's own KB (deny-once)
	GateCheckOutbound    = "outbound"     // reserved: the outbound gate (ticket 40)
	GateCheckPoolPath    = "pool-path"    // pool writes asked with the summary; git in pool/ denied
	GateCheckUnavailable = "unavailable"  // the gate could not evaluate the call at all
)

// Mode names: the `mode=` field, which hook is speaking and in what role.
const (
	GateModeSession   = "session"    // an agent's own session hook
	GateModeConsult   = "consult"    // a peer's frontmatter hook, consult contract
	GateModeWork      = "work"       // a peer's frontmatter hook, delegated work
	GateModeStepAside = "step-aside" // a hub's hook seeing a subagent's call
)

// GateLogFile is the per-agent decision log, under <agent>/.memory/.
const GateLogFile = "gate.log"

// gateLogEnv overrides the log path (tests, headless runs); empty ⇒ per agent.
const gateLogEnv = "AGENT_MESH_GATE_LOG"

// gateLogMaxBytes bounds the log: past it the file rolls to gate.log.1 once.
const gateLogMaxBytes = 1 << 20

// StepAside reports whether this hook invocation is a hub's own settings hook
// seeing one of its subagents' tool calls: the payload carries a subagent id
// (Claude Code sets agent_id inside a subagent) but no peer flag was passed.
// Such a hook applies only the machine-global rules; the KB-rule gate and the
// outbound gate (ticket 40 checks this seam) belong to the subagent's own
// frontmatter hook, which runs with `--agent <peer>`.
func (p HookPayload) StepAside() bool {
	return p.AgentID != "" && p.PeerAgent == ""
}

// GateMode names the role this hook invocation evaluates in.
func (p HookPayload) GateMode() string {
	switch {
	case p.StepAside():
		return GateModeStepAside
	case p.InSession && p.Work:
		return GateModeWork
	case p.InSession:
		return GateModeConsult
	}
	return GateModeSession
}

// GateLogPath is the decision log of the agent whose workspace is agentDir.
func GateLogPath(agentDir string) string {
	return filepath.Join(agentDir, MemoryDir, GateLogFile)
}

// gateDecisionWord is the `decision=` field: the envelope the harness gets.
func gateDecisionWord(d GateDecision) string {
	switch {
	case d.Deny:
		return "deny"
	case d.Ask:
		return "ask"
	}
	return "allow"
}

// logGateDecision appends one attributed line to the deciding agent's log
// (the --agent peer, else the workspace the cwd names). No agent ⇒ no log,
// unless $AGENT_MESH_GATE_LOG names a file. Fail-open: logging never changes
// a decision and never errors.
func logGateDecision(agentsDir string, p HookPayload, d GateDecision, now time.Time) {
	path := os.Getenv(gateLogEnv)
	if path == "" {
		if agentsDir == "" {
			return // outside any pool: nobody's log to write
		}
		a := payloadAgent(agentsDir, p)
		if a == nil {
			return
		}
		path = GateLogPath(a.Dir())
	}
	check := d.Check
	if check == "" {
		check = GateCheckNone
	}
	appendGateLog(path, fmt.Sprintf("%s decision=%s check=%s mode=%s tool=%s session=%s agent_id=%s",
		now.UTC().Format("2006-01-02T15:04:05Z"), gateDecisionWord(d), check, p.GateMode(),
		p.ToolName, orDash(p.SessionID), orDash(p.AgentID)))
}

// gateLogPathFor is the log an agent-scoped line goes to: the env override,
// else the agent's own log; "" when neither (no agent known, no override).
func gateLogPathFor(agentDir string) string {
	if path := os.Getenv(gateLogEnv); path != "" {
		return path
	}
	if agentDir == "" {
		return ""
	}
	return GateLogPath(agentDir)
}

// appendGateLog appends one line to a gate log, rolling it once past the
// size bound. Fail-open: it never errors. path "" ⇒ nothing to write.
func appendGateLog(path, line string) {
	if path == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	if info, err := os.Stat(path); err == nil && info.Size() > gateLogMaxBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
