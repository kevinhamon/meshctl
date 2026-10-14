package mesh

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Hook subcommands carry the mesh's Claude Code integration inside the binary,
// so the mesh needs no python3, no loose script files, and no machine-absolute
// paths baked into a global settings file. Each reads the Claude Code hook JSON
// payload on stdin and returns the additionalContext string to inject (empty ⇒
// nothing to inject). Every hook is FAIL-OPEN: on any error the caller emits
// nothing and exits 0 — a hook must never break a turn.
//
// These replace the former hooks/{handoff-inject,intuition-inject,distill-nudge}.py.
// The contracts underneath (handoff show, intuition recall, presence) are
// harness-neutral; only this file knows the Claude Code payload/output shape.

// HookPayload is the subset of the Claude Code hook stdin JSON the mesh reads.
type HookPayload struct {
	Cwd            string `json:"cwd"`
	Prompt         string `json:"prompt"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	// PreToolUse only: the tool about to run and its raw arguments. ToolInput is
	// kept raw so the gate parses only the keys it needs (file_path), tolerant of
	// per-tool input shapes.
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// ParseHookPayload decodes a hook payload, tolerating an empty/invalid stdin.
// For the advisory hooks only; the gate uses ParseHookPayloadStrict.
func ParseHookPayload(b []byte) HookPayload {
	var p HookPayload
	_ = json.Unmarshal(b, &p) // fail-open: zero value on any error
	return p
}

// ParseHookPayloadStrict decodes a hook payload and reports a malformed one,
// so the gate can refuse instead of evaluating an empty call as "allow".
func ParseHookPayloadStrict(b []byte) (HookPayload, error) {
	var p HookPayload
	if len(strings.TrimSpace(string(b))) == 0 {
		return p, fmt.Errorf("empty payload")
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("payload is not JSON: %w", err)
	}
	if p.ToolName == "" {
		return p, fmt.Errorf("payload has no tool_name")
	}
	return p, nil
}

// HookPreToolUse is the gate entry point for a tool call. Besides deciding, it
// refreshes the session heartbeat: a long autonomous turn makes no prompts, so
// without this its presence would look dead and its claims reclaimable.
func HookPreToolUse(agentsDir string, p HookPayload, now time.Time) GateDecision {
	if agentsDir != "" && p.SessionID != "" {
		if a := hookAgent(agentsDir, p.Cwd); a != nil {
			_ = TouchSession(a, p.SessionID, os.Getppid(), now)
		}
	}
	return GatePreToolUse(agentsDir, p)
}

// hookAgent resolves the agent whose workspace the hook fired in, from the
// payload cwd (its basename is the agent name). Returns nil if none matches.
func hookAgent(agentsDir, cwd string) *Agent {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	name := filepath.Base(strings.TrimRight(cwd, string(os.PathSeparator)))
	if name == "" || name == "." || name == string(os.PathSeparator) {
		return nil
	}
	a, err := FindAgent(agentsDir, name)
	if err != nil {
		return nil
	}
	return a
}

// HookSessionStart registers session presence (side effect) and returns the
// handoff block to inject, prefixed with any live-session overlap warning.
// Empty return ⇒ nothing to inject. Fail-open throughout.
func HookSessionStart(agentsDir string, p HookPayload, pid int, now time.Time) string {
	a := hookAgent(agentsDir, p.Cwd)
	if a == nil {
		return ""
	}
	id := p.SessionID
	if id == "" {
		id = ResolveSessionID(pid, now)
	}
	warn, _ := SessionStart(a, id, pid, now) // presence registration; ignore errors

	handoff, _ := HandoffShow(a)
	handoff = strings.TrimSpace(handoff)

	var b strings.Builder
	b.WriteString(IdentityLine(agentsDir, a))
	b.WriteString("\n\n")
	if warn != "" {
		b.WriteString(warn)
		b.WriteString("\n\n")
	}
	// Repo-hygiene nag (ADR-0039): persistent until a remote exists.
	if nag := RepoNag(a.Dir()); nag != "" {
		b.WriteString(nag)
		b.WriteString("\n\n")
	}
	// Requester read-back: peers' answers/declines on requests this agent sent.
	if sent, err := ListSent(agentsDir, a.Name, true); err == nil {
		if md := SentMarkdown(sent); md != "" {
			b.WriteString(md)
			b.WriteString("\n\n")
		}
	}
	// Tool reference (ADR-0042): mesh-wide + agent tools, self-serve --help.
	if md := ToolsMarkdown(EffectiveTools(LoadPoolConfig(agentsDir), a)); md != "" {
		b.WriteString(md)
		b.WriteString("\n\n")
	}
	if handoff != "" {
		b.WriteString("# Session handoff — resume point (authoritative; rewrite it before you leave)\n\n")
		b.WriteString(handoff)
	}
	return strings.TrimSpace(b.String())
}

// IdentityLine anchors an agent to its own pool. Several pools can share a
// machine, and an agent left to infer which one it belongs to from paths and
// global config can pick the wrong one; state it once, at session start.
func IdentityLine(poolRoot string, a *Agent) string {
	pool := LoadPoolConfig(poolRoot).Name
	if pool == "" {
		pool = filepath.Base(poolRoot)
	}
	return fmt.Sprintf("# Mesh identity\nYou are `%s` in mesh pool `%s` (root `%s`; your workspace `%s`). "+
		"Your peers are exactly the agents `meshctl agent list` shows; other pools on this machine are not yours. "+
		"Run meshctl from your workspace — it resolves this pool from there.", a.Name, pool, poolRoot, a.Dir())
}

// HookSessionEnd captures an extractive session-record (ADR-0033) then
// deregisters session presence. No output. Fail-open throughout.
func HookSessionEnd(agentsDir string, p HookPayload, pid int, now time.Time) {
	a := hookAgent(agentsDir, p.Cwd)
	if a == nil {
		return
	}
	id := p.SessionID
	if id == "" {
		id = ResolveSessionID(pid, now)
	}
	// Read the session's start time from presence BEFORE deregistering, so the
	// record's git-since window and duration are accurate.
	started := ""
	if s, err := readSession(sessionPath(a.Dir(), id)); err == nil && s.Started > 0 {
		started = time.Unix(s.Started, 0).UTC().Format(time.RFC3339)
	}
	_, _ = CaptureSession(a, p.TranscriptPath, id, started, now)
	_ = SessionEnd(a, id)
	// Cognition runs here, not on the model's initiative. No-ops for harnesses
	// whose background primitive is in-session (Claude Code), and for the
	// distillation run itself. See distill.go.
	SpawnDistill(agentsDir, a, id)
}

// HookSessionBeat refreshes presence for a session that is still open. The
// opencode plugins call it when a turn finishes: a finished turn is not the
// end of the session, only proof it is alive. No output. Fail-open.
func HookSessionBeat(agentsDir string, p HookPayload, now time.Time) {
	a := hookAgent(agentsDir, p.Cwd)
	if a == nil || p.SessionID == "" {
		return
	}
	_ = TouchSession(a, p.SessionID, os.Getppid(), now)
}

// HookUserPrompt returns the block to inject on a user prompt: compact intuition
// pointers (ADR-0016) followed by a one-shot distillation boundary nudge
// (ADR-0027) when a work boundary is detected. Either half may be empty.
func HookUserPrompt(agentsDir string, p HookPayload, now time.Time) string {
	a := hookAgent(agentsDir, p.Cwd)
	project := ""
	if a != nil {
		project = a.Name
		if p.SessionID != "" {
			_ = TouchSession(a, p.SessionID, os.Getppid(), now) // presence heartbeat; fail-open
		}
	} else if p.Cwd != "" {
		project = filepath.Base(strings.TrimRight(p.Cwd, string(os.PathSeparator)))
	}

	var parts []string
	// Owned engine by default (session-records + git spine); claude-mem opt-in.
	if len(strings.TrimSpace(p.Prompt)) >= intuitionMinQuery {
		if md := IntuitionMarkdown(RecallIntuition(a, p.Prompt, 3)); md != "" {
			parts = append(parts, strings.TrimRight(md, "\n"))
		}
	}
	if nudge := distillNudge(p, project, a); nudge != "" {
		parts = append(parts, nudge)
	}
	return strings.Join(parts, "\n\n")
}

// --- distillation boundary nudge (ADR-0027) ---

const (
	distillThreshold  = 8 // prompts into a session before a staleness nudge
	distillMinPrompts = 2 // never nudge on the very first exchange
)

var reWindDown = regexp.MustCompile(`(?i)\b(that'?s all|that will be all|we'?re done|done for now|all done|wrap(?:ping)? up|call it (?:a day|here)|nothing else|good ?night|see you|talk later|bye|thanks[,!. ]*that'?s it)\b`)

// distillNudgeAgents is $AGENT_MESH_DISTILL_NUDGE_AGENTS: a comma-separated
// allowlist of agents that get the boundary nudge. Unset or "*" ⇒ every agent
// (the default — a nudge nobody gets is a cognition loop that never runs);
// "none" ⇒ no agent.
const distillNudgeAgentsEnv = "AGENT_MESH_DISTILL_NUDGE_AGENTS"

// distillNudgeEnabled reports whether the boundary nudge fires for an agent.
func distillNudgeEnabled(agent string) bool {
	raw := strings.TrimSpace(os.Getenv(distillNudgeAgentsEnv))
	switch raw {
	case "", "*":
		return agent != ""
	case "none", "off", "0":
		return false
	}
	for _, a := range strings.Split(raw, ",") {
		if strings.TrimSpace(a) == agent {
			return true
		}
	}
	return false
}

type distillState struct {
	Count  int  `json:"count"`
	Nudged bool `json:"nudged"`
}

var reSessionSanitize = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func distillStatePath(cwd, sessionID string) string {
	sid := reSessionSanitize.ReplaceAllString(sessionID, "_")
	if sid == "" {
		sid = "nosession"
	}
	return filepath.Join(cwd, ".memory", ".distill-nudge-"+sid+".json")
}

// distillNudge detects a work boundary deterministically and, at most once per
// session, returns a nudge telling the model to run background distillation.
// Boundary = an explicit wind-down phrase OR a staleness threshold of prompts.
// Allowlist-gated. Fail-open: any error ⇒ "".
func distillNudge(p HookPayload, agent string, a *Agent) string {
	if !distillNudgeEnabled(agent) {
		return ""
	}
	// Harnesses that distil themselves at session end are already covered;
	// asking the model as well would just run the same window twice.
	if harnessAutoDistills(a) {
		return ""
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".memory"), 0o755); err != nil {
		return ""
	}
	sp := distillStatePath(cwd, p.SessionID)

	var st distillState
	if b, err := os.ReadFile(sp); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	st.Count++

	winddown := reWindDown.MatchString(p.Prompt)
	stale := st.Count >= distillThreshold
	fire := !st.Nudged && st.Count >= distillMinPrompts && (winddown || stale)
	if fire {
		st.Nudged = true
	}
	if b, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(sp, b, 0o644)
	}

	if !fire {
		return ""
	}
	why := fmt.Sprintf("%d exchanges into this session", st.Count)
	if winddown {
		why = "you signalled a wind-down"
	}
	return fmt.Sprintf("[work-boundary detected — %s] Before continuing/closing out, run background "+
		"distillation per your mesh-cognition skill: spawn the `distiller` subagent with "+
		"run_in_background:true so it mines this session off the main thread, then surface "+
		"ONLY the decisions it returns (candidates land silently; nothing durable ⇒ it "+
		"no-ops). Do this once — it will not be asked again this session.", why)
}

// EmitHookContext writes the injected context in Claude Code's envelope. Kept as
// the default (claude) emitter; harness-aware callers use HarnessForPayload().
func EmitHookContext(w io.Writer, event, ctx string) error {
	return claudeHarness{}.EmitContext(w, event, ctx)
}

// HarnessForPayload resolves the harness a hook should emit for: an explicit
// flag wins, else the agent (resolved from the payload cwd), else claude.
func HarnessForPayload(agentsDir string, p HookPayload, flagHarness string) Harness {
	if flagHarness != "" {
		return HarnessByName(flagHarness)
	}
	if a := hookAgent(agentsDir, p.Cwd); a != nil {
		return HarnessFor(a)
	}
	return claudeHarness{}
}
