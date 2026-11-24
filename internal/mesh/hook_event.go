package mesh

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hook_event.go — the neutral hook event protocol. A harness bridge (a plugin
// the host loads) translates each host hook or event into one HookEvent, pipes
// it as JSON to `meshctl --harness <id> hook event`, and applies the reply: it
// injects the text a context event returns and blocks a tool call whose
// tool.before returns a reason. The bridge keeps no mesh state and makes no
// mesh decisions; everything a session needs across events (whether it has
// started, its cached session-start block, its latest prompt, when it last
// did anything) lives here, in the pool's session store, because every event
// is a separate meshctl process.
//
// Claude Code keeps its per-event subcommands (`hook session-start`, …): its
// host calls them directly, so its bridge was already pass-through.

// HookEvent is one normalized event from a harness bridge.
type HookEvent struct {
	// Event is the kind (the HookEvent* constants).
	Event string `json:"event"`
	// Harness and Variant name the host (the --harness flag wins over
	// Harness). Variant pins the release line, so no version probe runs on
	// the hook path.
	Harness string `json:"harness,omitempty"`
	Variant int    `json:"variant,omitempty"`
	// Instance identifies one loaded copy of the bridge (one host process,
	// or one plugin instance in a host that serves many workspaces). A
	// shutdown closes only the sessions this instance last saw.
	Instance string `json:"instance,omitempty"`
	// Cwd is the agent workspace the bridge serves.
	Cwd string `json:"cwd"`
	// SessionID is the host session the event belongs to. A subagent's own
	// (child) session sets ParentSessionID to the session that started it.
	SessionID       string `json:"session_id,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	// Agent is the host's agent or mode name for the session ("build",
	// "mesh-<peer>", "general"); empty when the host does not say.
	Agent string `json:"agent,omitempty"`
	// SessionUnknown: the bridge could not look the session up, so it cannot
	// tell a subagent from a primary session. The gate refuses such a call
	// when it also has no Agent.
	SessionUnknown bool `json:"session_unknown,omitempty"`
	// Prompt is the user's prompt text (prompt; context may carry it too).
	Prompt string `json:"prompt,omitempty"`
	// Source is why a session started (session.start): startup, resume, …
	Source string `json:"source,omitempty"`
	// Tool is the call a tool.before / tool.after event is about.
	Tool *HookEventTool `json:"tool,omitempty"`
	// Doctrine asks a context reply to lead with the agent's CLAUDE.md and
	// CLAUDE.local.md: the host does not load them itself.
	Doctrine bool `json:"doctrine,omitempty"`
}

// HookEventTool is a tool call in a HookEvent.
type HookEventTool struct {
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input,omitempty"`
	UseID    string          `json:"use_id,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

// The event kinds. Replies: context and session.start → context text in the
// harness's envelope (EmitContext); tool.before → a gate decision
// (EmitGate); every other kind → nothing.
const (
	// HookEventSessionStart: the host opened a session. Starts it (presence,
	// session-start block) and replies with the block.
	HookEventSessionStart = "session.start"
	// HookEventPrompt: the user sent a prompt. Recorded for the next context
	// event; the session is busy until turn.end.
	HookEventPrompt = "prompt"
	// HookEventContext: the host is building a model request and takes text
	// to add to its system prompt. Replies with the doctrine (if asked), the
	// session-start block (starting the session on its first context event,
	// cached and repeated on every later one: the host rebuilds the system
	// prompt per request), then intuition for the latest prompt.
	HookEventContext = "context"
	// HookEventToolBefore: the fail-closed gate.
	HookEventToolBefore = "tool.before"
	// HookEventToolAfter: the after-call record (pool commits, outbound acks).
	HookEventToolAfter = "tool.after"
	// HookEventTurnEnd: a turn finished. Proof of life, not the end of the
	// session; the idle window starts. A mesh peer subagent's finished run
	// is captured as the peer's record.
	HookEventTurnEnd = "turn.end"
	// HookEventHeartbeat: proof of life without a finished turn.
	HookEventHeartbeat = "heartbeat"
	// HookEventTick: a bridge timer firing. No proof of life; it only lets
	// meshctl close sessions whose idle window has passed.
	HookEventTick = "tick"
	// HookEventSessionEnd: the host ended (deleted) the session.
	HookEventSessionEnd = "session.end"
	// HookEventShutdown: the bridge is being unloaded; every session its
	// instance last saw is closed.
	HookEventShutdown = "shutdown"
)

// idleWindowEnv sets how long a quiet session stays open on a harness with no
// session-end event (named for the first harness that needed it).
const idleWindowEnv = "AGENT_MESH_OPENCODE_IDLE_SEC"

// bridgeIdleWindow is the idle window, default 10 minutes.
func bridgeIdleWindow() time.Duration {
	if n := envInt(idleWindowEnv, 0); n > 0 {
		return time.Duration(n) * time.Second
	}
	return 10 * time.Minute
}

// bridgeOrphanAfter is how long a session left mid-turn (its host gone before
// the turn ended) keeps its state before a sweep closes it.
const bridgeOrphanAfter = 24 * time.Hour

// RunHookEvent handles one bridge event: it parses stdin, dispatches, and
// writes the reply in the harness's form. flagHarness is --harness ("" ⇒ the
// event's, else the agent's). Advisory events fail open (no reply); a
// tool.before, or input that cannot be parsed at all (it may have been one),
// fails closed with a deny.
func RunHookEvent(w io.Writer, agentsDir, flagHarness string, stdin []byte, now time.Time) {
	var ev HookEvent
	if err := json.Unmarshal(stdin, &ev); err != nil || strings.TrimSpace(ev.Event) == "" {
		why := "event is not JSON"
		if err == nil {
			why = "event has no kind"
		}
		_ = harnessForEvent(agentsDir, ev, flagHarness).EmitGate(w, GateUnavailable(why))
		return
	}
	id := flagHarness
	if id == "" {
		id = ev.Harness
	}
	if id != "" {
		if _, err := ResolveHarness(id, ev.Variant); err != nil {
			if ev.Event == HookEventToolBefore {
				_ = harnessForEvent(agentsDir, ev, "").EmitGate(w, GateUnavailable(err.Error()))
			}
			return
		}
	}
	h := harnessForEvent(agentsDir, ev, id)
	ev.Harness = h.Name()
	switch ev.Event {
	case HookEventToolBefore:
		_ = h.EmitGate(w, hookEventGate(agentsDir, ev, now))
	case HookEventContext:
		_ = h.EmitContext(w, "UserPromptSubmit", hookEventContext(agentsDir, h, ev, now))
	case HookEventSessionStart:
		_ = h.EmitContext(w, "SessionStart", hookEventSessionStart(agentsDir, h, ev, now))
	default:
		hookEventLifecycle(agentsDir, h, ev, now)
	}
}

// harnessForEvent is the adapter an event's reply is written for, with the
// event's variant pinned.
func harnessForEvent(agentsDir string, ev HookEvent, id string) Harness {
	if id == "" {
		id = ev.Harness
	}
	if id != "" {
		return HarnessByNameVersion(id, ev.Variant)
	}
	return HarnessForPayload(agentsDir, HookPayload{Cwd: ev.Cwd}, "")
}

// hookEventPayload is the HookPayload the per-event handlers take. A child
// session acts under its parent session; only a mesh peer child is flagged
// (agent_type, agent_id), so it is mapped to that peer's gate — any other
// subagent runs as the session that started it.
func hookEventPayload(ev HookEvent) HookPayload {
	p := HookPayload{Cwd: ev.Cwd, SessionID: ev.SessionID, Prompt: ev.Prompt, Source: ev.Source, Harness: ev.Harness}
	if ev.ParentSessionID != "" {
		p.SessionID = ev.ParentSessionID
	}
	if strings.HasPrefix(ev.Agent, PeerAgentPrefix) {
		p.AgentType = ev.Agent
		if ev.ParentSessionID != "" {
			p.AgentID = ev.SessionID
		}
	}
	if ev.Tool != nil {
		p.ToolName, p.ToolInput, p.ToolUseID, p.ToolResponse = ev.Tool.Name, ev.Tool.Input, ev.Tool.UseID, ev.Tool.Response
	}
	return p
}

// hookEventGate decides a tool.before. A subagent whose agent cannot be told
// is refused rather than run under the wrong rules.
func hookEventGate(agentsDir string, ev HookEvent, now time.Time) GateDecision {
	if ev.Tool == nil || ev.Tool.Name == "" {
		return GateUnavailable("event has no tool name")
	}
	if ev.Agent == "" && (ev.SessionUnknown || ev.ParentSessionID != "") {
		return GateDecision{Deny: true, Check: GateCheckUnavailable, Reason: "mesh gate cannot tell which agent runs this session and refuses " +
			"rather than run a peer under the wrong rules; retry, or run meshctl doctor"}
	}
	p, err := ResolvePeerSubagent(agentsDir, hookEventPayload(ev))
	if err != nil {
		return GateUnavailable(err.Error())
	}
	return HookPreToolUse(agentsDir, p, now)
}

// hookEventContext is a context event's reply.
func hookEventContext(agentsDir string, h Harness, ev HookEvent, now time.Time) string {
	var parts []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	doctrine := func() {
		if ev.Doctrine {
			add(workspaceDoctrine(ev.Cwd))
		}
	}
	// A headless run (dispatch, distil) gets its doctrine and the gate only:
	// lifecycle work there would register the run as a live session and its
	// session end would start another distil.
	if HeadlessNow() {
		doctrine()
		return strings.Join(parts, "\n\n")
	}
	if ev.ParentSessionID != "" {
		// A subagent: no session of its own to start or recall for. A mesh
		// peer takes its role from its definition, not this agent's doctrine.
		if !strings.HasPrefix(ev.Agent, PeerAgentPrefix) {
			doctrine()
		}
		return strings.Join(parts, "\n\n")
	}
	doctrine()
	a := hookAgent(agentsDir, ev.Cwd)
	sid := bridgeSessionID(a, ev)
	prompt := ev.Prompt
	if a != nil && sid != "" {
		st := bridgeTouch(agentsDir, a, h, ev, sid, now)
		if ev.Prompt != "" {
			st.Prompt = ev.Prompt
		}
		if !st.Started {
			p := hookEventPayload(ev)
			p.SessionID = sid
			st.StartBlock = HookSessionStart(agentsDir, p, os.Getpid(), now)
			st.Started = true
		}
		_ = st.save()
		add(st.StartBlock)
		prompt = st.Prompt
	}
	add(HookUserPrompt(agentsDir, HookPayload{Cwd: ev.Cwd, SessionID: sid, Prompt: prompt, Harness: ev.Harness}, now))
	return strings.Join(parts, "\n\n")
}

// hookEventSessionStart starts a session on the host's own start event and
// replies with its block (cached, so context events repeat it).
func hookEventSessionStart(agentsDir string, h Harness, ev HookEvent, now time.Time) string {
	a := hookAgent(agentsDir, ev.Cwd)
	sid := bridgeSessionID(a, ev)
	if HeadlessNow() || ev.ParentSessionID != "" || a == nil || sid == "" {
		return ""
	}
	st := bridgeTouch(agentsDir, a, h, ev, sid, now)
	p := hookEventPayload(ev)
	p.SessionID = sid
	st.StartBlock = HookSessionStart(agentsDir, p, os.Getpid(), now)
	st.Started = true
	_ = st.save()
	return st.StartBlock
}

// hookEventLifecycle handles the events with no reply.
func hookEventLifecycle(agentsDir string, h Harness, ev HookEvent, now time.Time) {
	if ev.Event == HookEventToolAfter {
		if p, err := ResolvePeerSubagent(agentsDir, hookEventPayload(ev)); err == nil {
			HookPostToolUse(agentsDir, p, now)
		}
		return
	}
	if HeadlessNow() {
		return
	}
	a := hookAgent(agentsDir, ev.Cwd)
	if a == nil {
		return
	}
	if ev.ParentSessionID != "" {
		// A subagent has no lifecycle of its own. A mesh peer's finished run
		// is captured as the peer's record (Claude Code's SubagentStop).
		if ev.Event == HookEventTurnEnd && strings.HasPrefix(ev.Agent, PeerAgentPrefix) {
			if p, err := ResolvePeerSubagent(agentsDir, hookEventPayload(ev)); err == nil {
				HookSessionEnd(agentsDir, p, os.Getpid(), now)
			}
		}
		return
	}
	sid := bridgeSessionID(a, ev)
	switch ev.Event {
	case HookEventPrompt:
		if sid == "" {
			return
		}
		st := bridgeTouch(agentsDir, a, h, ev, sid, now)
		st.Prompt, st.InTurn = ev.Prompt, true
		_ = st.save()
		bridgeSetLast(a, ev.Instance, sid)
	case HookEventTurnEnd, HookEventHeartbeat:
		if sid != "" {
			HookSessionBeat(agentsDir, HookPayload{Cwd: ev.Cwd, SessionID: sid}, now)
			if st, ok := loadBridgeSession(a, sid); ok {
				if ev.Event == HookEventTurnEnd {
					st.InTurn = false
				}
				st.LastEvent, st.Instance = now.Unix(), ev.Instance
				_ = st.save()
			}
		}
		bridgeSweep(agentsDir, a, h, now)
	case HookEventTick:
		bridgeSweep(agentsDir, a, h, now)
	case HookEventSessionEnd:
		if st, ok := loadBridgeSession(a, sid); ok {
			bridgeClose(agentsDir, a, st, now)
		}
	case HookEventShutdown:
		for _, st := range bridgeSessions(a) {
			if st.Harness == h.Name() && st.Instance == ev.Instance {
				bridgeClose(agentsDir, a, st, now)
			}
		}
		_ = os.Remove(bridgeLastFile(a.Dir(), ev.Instance))
	}
}

// workspaceDoctrine is the agent's role (CLAUDE.md, unless the host already
// loads it as AGENTS.md) then the managed standing orders (CLAUDE.local.md),
// as Claude Code loads them.
func workspaceDoctrine(cwd string) string {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	claude := filepath.Join(cwd, "CLAUDE.md")
	role := read(claude)
	if r1, err := filepath.EvalSymlinks(filepath.Join(cwd, "AGENTS.md")); err == nil {
		if r2, err := filepath.EvalSymlinks(claude); err == nil && r1 == r2 {
			role = ""
		}
	}
	var parts []string
	for _, s := range []string{role, read(filepath.Join(cwd, ClaudeLocalFile))} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// --- per-session bridge state ---

// bridgeSession is what meshctl keeps between a bridge's events for one host
// session: one file per session under pool/sessions/bridge/, beside the
// presence records (which the session listings never confuse with it: they
// skip folders). It lives from the session's first event to its close.
type bridgeSession struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Harness   string `json:"harness"`
	Instance  string `json:"instance,omitempty"`
	// Started: the session-start hook has run; StartBlock is what it returned.
	Started    bool   `json:"started"`
	StartBlock string `json:"start_block,omitempty"`
	// Prompt is the latest user prompt, for intuition on each context event.
	Prompt string `json:"prompt,omitempty"`
	// InTurn: a prompt arrived and its turn has not finished. A session
	// mid-turn is never closed as idle.
	InTurn bool `json:"in_turn,omitempty"`
	// LastEvent is the unix time of the session's latest prompt, context,
	// turn end or heartbeat.
	LastEvent int64  `json:"last_event"`
	path      string `json:"-"`
}

func bridgeDir(agentDir string) string { return filepath.Join(sessionsDir(agentDir), "bridge") }

func bridgeFile(agentDir, sid string) string {
	return filepath.Join(bridgeDir(agentDir), sessionAgent(agentDir)+"."+reSessionSanitize.ReplaceAllString(sid, "_")+".json")
}

func (s *bridgeSession) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	return writeFileAtomic(s.path, b, 0o644)
}

// idle reports whether the session's idle window has passed: quiet since its
// last turn ended, or left mid-turn for bridgeOrphanAfter.
func (s *bridgeSession) idle(now time.Time) bool {
	quiet := now.Sub(time.Unix(s.LastEvent, 0))
	if s.InTurn {
		return quiet > bridgeOrphanAfter
	}
	return quiet > bridgeIdleWindow()
}

func loadBridgeSession(a *Agent, sid string) (*bridgeSession, bool) {
	if a == nil || sid == "" {
		return nil, false
	}
	return readBridgeSession(bridgeFile(a.Dir(), sid), sessionAgent(a.Dir()))
}

func readBridgeSession(path, agent string) (*bridgeSession, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var s bridgeSession
	if json.Unmarshal(b, &s) != nil || s.Agent != agent {
		return nil, false
	}
	s.path = path
	return &s, true
}

// bridgeSessions lists the agent's bridge records.
func bridgeSessions(a *Agent) []*bridgeSession {
	entries, err := os.ReadDir(bridgeDir(a.Dir()))
	if err != nil {
		return nil
	}
	var out []*bridgeSession
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if s, ok := readBridgeSession(filepath.Join(bridgeDir(a.Dir()), e.Name()), sessionAgent(a.Dir())); ok {
			out = append(out, s)
		}
	}
	return out
}

// bridgeTouch loads a session's state for an event that shows it is in use,
// closing it first if its idle window had passed (the event then opens it
// afresh, as a new prompt after a closed session always has), and stamps the
// event. The caller saves it.
func bridgeTouch(agentsDir string, a *Agent, h Harness, ev HookEvent, sid string, now time.Time) *bridgeSession {
	if st, ok := loadBridgeSession(a, sid); ok {
		if !st.idle(now) {
			st.LastEvent, st.Instance = now.Unix(), ev.Instance
			return st
		}
		bridgeClose(agentsDir, a, st, now)
	}
	return &bridgeSession{Agent: sessionAgent(a.Dir()), SessionID: sid, Harness: h.Name(), Instance: ev.Instance,
		LastEvent: now.Unix(), path: bridgeFile(a.Dir(), sid)}
}

// bridgeSweep closes the agent's sessions on this harness whose idle window
// has passed. Only a harness with no session-end event of its own needs it.
func bridgeSweep(agentsDir string, a *Agent, h Harness, now time.Time) {
	if h.Caps().SessionEndEvent {
		return
	}
	for _, st := range bridgeSessions(a) {
		if st.Harness == h.Name() && st.idle(now) {
			bridgeClose(agentsDir, a, st, now)
		}
	}
	// Last-session pointers of bridge instances long gone.
	entries, _ := os.ReadDir(bridgeDir(a.Dir()))
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), sessionAgent(a.Dir())+"@") || !strings.HasSuffix(e.Name(), ".last") {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > bridgeOrphanAfter {
			_ = os.Remove(filepath.Join(bridgeDir(a.Dir()), e.Name()))
		}
	}
}

// bridgeClose ends a session: its record is captured, presence dropped and
// distillation started, as a host session-end does, and its bridge state is
// removed. Claimed by renaming the state file first, so of two processes
// closing the same session only one runs the end.
func bridgeClose(agentsDir string, a *Agent, st *bridgeSession, now time.Time) {
	claim := st.path + fmt.Sprintf(".closing-%d", os.Getpid())
	if os.Rename(st.path, claim) != nil {
		return // already closed by another event
	}
	defer os.Remove(claim)
	if st.Started {
		HookSessionEnd(agentsDir, HookPayload{Cwd: a.Dir(), SessionID: st.SessionID, Harness: st.Harness}, os.Getpid(), now)
	}
}

// bridgeSessionID is the event's session, or, when the host left it out, the
// last session this bridge instance saw a prompt for.
func bridgeSessionID(a *Agent, ev HookEvent) string {
	if ev.SessionID != "" || a == nil {
		return ev.SessionID
	}
	b, err := os.ReadFile(bridgeLastFile(a.Dir(), ev.Instance))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func bridgeLastFile(agentDir, instance string) string {
	return filepath.Join(bridgeDir(agentDir), sessionAgent(agentDir)+"@"+reSessionSanitize.ReplaceAllString(instance, "_")+".last")
}

func bridgeSetLast(a *Agent, instance, sid string) {
	p := bridgeLastFile(a.Dir(), instance)
	if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) == sid {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
		_ = writeFileAtomic(p, []byte(sid+"\n"), 0o644)
	}
}
