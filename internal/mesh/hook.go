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
func ParseHookPayload(b []byte) HookPayload {
	var p HookPayload
	_ = json.Unmarshal(b, &p) // fail-open: zero value on any error
	return p
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
	if warn != "" {
		b.WriteString(warn)
		b.WriteString("\n\n")
	}
	// Repo-hygiene nag (ADR-0039): persistent until a remote exists.
	if nag := RepoNag(a.Dir()); nag != "" {
		b.WriteString(nag)
		b.WriteString("\n\n")
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

// distillNudgeAllowlist is the set of agents the nudge is piloted on
// ($AGENT_MESH_DISTILL_NUDGE_AGENTS, comma-separated; default pm).
func distillNudgeAllowlist() map[string]bool {
	raw := os.Getenv("AGENT_MESH_DISTILL_NUDGE_AGENTS")
	if raw == "" {
		raw = "pm"
	}
	out := map[string]bool{}
	for _, a := range strings.Split(raw, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out[a] = true
		}
	}
	return out
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
	if agent == "" || !distillNudgeAllowlist()[agent] {
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
