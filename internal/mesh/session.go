package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Session is a live presence record. One file per session id for the whole
// pool, under pool/sessions/<session-id>, naming the agent it belongs to: a
// session id can belong to one agent only, so it can never be live under
// several (it was <agent>/.sessions/<session-id>).
type Session struct {
	ID           string `json:"id"`
	Agent        string `json:"agent"`
	PID          int    `json:"pid"`
	Started      int64  `json:"started"`
	LastBeat     int64  `json:"last_beat"`
	CurrentClaim string `json:"current_claim,omitempty"`
	KBBranch     string `json:"kb_branch,omitempty"`
	// HarnessPID and TTY are the harness process that runs the session and
	// its terminal, found by walking up from the hook (hooks run without a
	// terminal of their own). The console uses them to show a session that
	// is open but idle, and to bring its terminal to the front.
	HarnessPID int    `json:"harness_pid,omitempty"`
	TTY        string `json:"tty,omitempty"`
	// Background is set when the harness runs without a terminal window
	// (harnessInBackground): its TTY is a pseudo-terminal no window shows, so
	// there is nothing to bring to the front.
	Background bool `json:"background,omitempty"`
	// ProcScope is where HarnessPID is valid (procScope); empty on records
	// written before scopes existed.
	ProcScope string `json:"proc_scope,omitempty"`
	path      string `json:"-"`
}

// Scheduled jobs belong to the harness process, not the conversation:
// Claude Code keeps a session's jobs across /clear, which ends one session id
// and starts another in the same process. So they are stored per harness
// process (per session id when the process is unknown), under
// pool/sessions/jobs/, a folder the session listings skip, each file prefixed
// with its agent.

// JobSet is the scheduled jobs one harness process runs, as of At.
type JobSet struct {
	HarnessPID int          `json:"harness_pid,omitempty"`
	ProcScope  string       `json:"proc_scope,omitempty"` // where HarnessPID is valid
	Session    string       `json:"session,omitempty"`    // when the harness process is unknown
	Jobs       []SessionJob `json:"jobs"`
	At         int64        `json:"at"`
}

func jobsDir(agentDir string) string { return filepath.Join(sessionsDir(agentDir), "jobs") }

// sessionAgent is the agent key a workspace's presence records carry: its
// directory name, which is the agent's name.
func sessionAgent(agentDir string) string { return filepath.Base(agentDir) }

// jobsPath is where the jobs of session s are kept: by its harness process,
// else by its id. A scoped pid carries its scope's tag, since the same pid in
// two namespaces is two processes.
func jobsPath(agentDir string, s *Session) string {
	if s.HarnessPID > 0 {
		name := sessionAgent(agentDir) + "-pid-" + strconv.Itoa(s.HarnessPID)
		if s.ProcScope != "" {
			name += "-" + scopeTag(s.ProcScope)
		}
		return filepath.Join(jobsDir(agentDir), name+".json")
	}
	return filepath.Join(jobsDir(agentDir), sessionAgent(agentDir)+"-session-"+s.ID+".json")
}

// SessionJobs returns the scheduled jobs of the process running session s.
func SessionJobs(agent *Agent, s *Session) (JobSet, bool) {
	b, err := os.ReadFile(jobsPath(agent.Dir(), s))
	if err != nil {
		return JobSet{}, false
	}
	var js JobSet
	if json.Unmarshal(b, &js) != nil {
		return JobSet{}, false
	}
	return js, true
}

// harnessState reports whether the session's harness process is still
// running. known is false when the pid was never recorded or was recorded in
// another scope, where probing it would ask about some other process.
func (s *Session) harnessState() (alive, known bool) {
	if s.HarnessPID <= 0 || !scopeLocal(s.ProcScope) {
		return false, false
	}
	return processAlive(s.HarnessPID), true
}

// harnessAlive reports whether the harness is known to be running here.
func (s *Session) harnessAlive() bool {
	alive, known := s.harnessState()
	return known && alive
}

// SessionJob is one scheduled job a session runs, as the harness reports it.
type SessionJob struct {
	ID        string `json:"id"`
	Schedule  string `json:"schedule"`
	Recurring bool   `json:"recurring"`
	Prompt    string `json:"prompt"`
}

// sessionsDir is the pool's session store; agents sit directly under the
// pool root.
func sessionsDir(agentDir string) string { return SessionStorePath(filepath.Dir(agentDir)) }
func sessionPath(agentDir, id string) string {
	return filepath.Join(sessionsDir(agentDir), id)
}

// errOtherAgent is a session id registered to another agent.
type errOtherAgent struct{ id, agent string }

func (e errOtherAgent) Error() string {
	return fmt.Sprintf("session %s belongs to agent %q", e.id, e.agent)
}

// readAgentSession reads session id when it belongs to the agent at agentDir;
// a record of another agent is an errOtherAgent, never this agent's.
func readAgentSession(agentDir, id string) (*Session, error) {
	s, err := readSession(sessionPath(agentDir, id))
	if err != nil {
		return nil, err
	}
	if s.Agent != sessionAgent(agentDir) {
		return nil, errOtherAgent{id, s.Agent}
	}
	return s, nil
}

// agentSessions lists the records of the agent at agentDir.
func agentSessions(agentDir string) []*Session {
	entries, err := os.ReadDir(sessionsDir(agentDir))
	if err != nil {
		return nil
	}
	var out []*Session
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if s, err := readSession(sessionPath(agentDir, e.Name())); err == nil && s.Agent == sessionAgent(agentDir) {
			out = append(out, s)
		}
	}
	return out
}

// sessionIDEnv lists the env vars that carry the harness session id, in
// precedence order. CLAUDE_SESSION_ID is the explicit override; Claude Code
// exports CLAUDE_CODE_SESSION_ID and opencode V2 OPENCODE_SESSION_ID to every
// tool subprocess, each matching the session_id its hooks register presence
// under.
var sessionIDEnv = []string{"CLAUDE_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "OPENCODE_SESSION_ID"}

// ResolveSessionID returns the harness session id from the environment, or
// mints a per-process id. The minted id is unique to one meshctl invocation, so
// a claim made under it can only be released by that same process.
func ResolveSessionID(pid int, now time.Time) string {
	for _, k := range sessionIDEnv {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return fmt.Sprintf("s-%d-%d", pid, now.UnixNano())
}

// ResolveAgentSessionID resolves the session a claim/release acts on in an
// agent workspace. Precedence: the harness session id from the environment;
// else the sole live registered session (hooks register presence under the
// harness id, so a harness that does not export it to tool shells — opencode
// V1 — still resolves to its session); else a minted per-process id.
func ResolveAgentSessionID(agent *Agent, pid int, now time.Time) string {
	for _, k := range sessionIDEnv {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	if live := liveSessions(agent.Dir(), now); len(live) == 1 {
		return live[0].ID
	}
	return ResolveSessionID(pid, now)
}

// SessionStart registers presence and returns any overlap warning (other live
// sessions in the same workspace + their outstanding claims). Empty string ⇒
// this is the only live session.
func SessionStart(agent *Agent, id string, pid int, now time.Time) (warning string, err error) {
	dir := sessionsDir(agent.Dir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	// One session, one agent: an id another agent registered stays theirs.
	if _, err := readAgentSession(agent.Dir(), id); err != nil {
		if o, ok := err.(errOtherAgent); ok {
			return fmt.Sprintf("⚠ session %s is already registered to agent %q, so it is not registered as %q; if this session is %q's, run `meshctl doctor`.", id, o.agent, agent.Name, agent.Name), nil
		}
	}

	others := liveSessions(agent.Dir(), now)
	var live []*Session
	for _, s := range others {
		if s.ID != id {
			live = append(live, s)
		}
	}

	s := &Session{ID: id, Agent: sessionAgent(agent.Dir()), PID: pid, Started: now.Unix(), LastBeat: now.Unix(), path: sessionPath(agent.Dir(), id)}
	s.HarnessPID, s.TTY = harnessTerminal(os.Getpid())
	s.Background = harnessInBackground(s.HarnessPID)
	s.ProcScope = procScope()
	if err := writeSession(s); err != nil {
		return "", err
	}

	if len(live) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "⚠ %d other live session(s) in %q:\n", len(live), agent.Name)
		for _, o := range live {
			claim := o.CurrentClaim
			if claim == "" {
				claim = "(no active claim)"
			}
			fmt.Fprintf(&b, "  - session %s pid=%d, claim=%s\n", o.ID, o.PID, claim)
		}
		claims := outstandingClaims(agent, now)
		if len(claims) > 0 {
			fmt.Fprintf(&b, "  outstanding claims: %s\n", strings.Join(claims, ", "))
		}
		warning = strings.TrimRight(b.String(), "\n")
	}
	return warning, nil
}

// SessionEnd deregisters a session.
func SessionEnd(agent *Agent, id string) error {
	s, err := readAgentSession(agent.Dir(), id)
	if err != nil {
		return nil // not registered, or another agent's
	}
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SessionBeat refreshes a session's heartbeat (and optionally its claim/branch).
func SessionBeat(agent *Agent, id string, now time.Time) error {
	s, err := readAgentSession(agent.Dir(), id)
	if err != nil {
		return err
	}
	s.LastBeat = now.Unix()
	return writeSession(s)
}

// TouchSession refreshes a session's heartbeat, re-registering it if it was
// never registered or was reaped as stale. Hooks call this on every prompt so a
// long-running session stays alive (and its claims unstealable).
func TouchSession(agent *Agent, id string, pid int, now time.Time) error {
	p := sessionPath(agent.Dir(), id)
	s, err := readAgentSession(agent.Dir(), id)
	if _, other := err.(errOtherAgent); other {
		return nil // another agent's session: leave its record alone
	}
	if err != nil {
		if err := os.MkdirAll(sessionsDir(agent.Dir()), 0o755); err != nil {
			return err
		}
		s = &Session{ID: id, Agent: sessionAgent(agent.Dir()), PID: pid, Started: now.Unix(), path: p}
		s.HarnessPID, s.TTY = harnessTerminal(os.Getpid())
		s.Background = harnessInBackground(s.HarnessPID)
		s.ProcScope = procScope()
	}
	s.LastBeat = now.Unix()
	return writeSession(s)
}

// SetSessionJobs records the scheduled jobs of the process running session
// id (an empty list clears them), touching its presence.
func SetSessionJobs(agent *Agent, id string, pid int, jobs []SessionJob, now time.Time) error {
	if err := TouchSession(agent, id, pid, now); err != nil {
		return err
	}
	s, err := readAgentSession(agent.Dir(), id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(jobsDir(agent.Dir()), 0o755); err != nil {
		return err
	}
	js := JobSet{HarnessPID: s.HarnessPID, ProcScope: s.ProcScope, Jobs: jobs, At: now.Unix()}
	if s.HarnessPID == 0 {
		js.Session = s.ID
	}
	b, _ := json.Marshal(js)
	return writeFileAtomic(jobsPath(agent.Dir(), s), b, 0o644)
}

// OpenSessions lists a peer's sessions that are still open, without reaping
// anything (the console only reads): those whose harness process is alive,
// or, when that is unknown (not recorded, or recorded in another scope), whose
// heartbeat is fresh. Oldest first.
func OpenSessions(agent *Agent, now time.Time) []*Session {
	var out []*Session
	for _, s := range agentSessions(agent.Dir()) {
		fresh := int(now.Unix()-s.LastBeat) <= staleSec()
		if alive, known := s.harnessState(); (known && alive) || (!known && fresh) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started < out[j].Started })
	return out
}

// SessionList returns the live sessions for an agent.
func SessionList(agent *Agent, now time.Time) []*Session {
	return liveSessions(agent.Dir(), now)
}

// sessionAlive reports whether a session is registered and either heartbeating
// or run by a harness process that is known to be running here. The second
// clause is the one liveSessions uses to keep an open, idle session on the
// roster; a claim must not be stealable from a session the roster still lists.
// Heartbeats only arrive on prompts, so a user who claims a request and walks
// away for the stale window would otherwise lose it to the next dispatch.
func sessionAlive(agentDir, id string, now time.Time) bool {
	if id == "" {
		return false
	}
	s, err := readAgentSession(agentDir, id)
	if err != nil {
		return false
	}
	return int(now.Unix()-s.LastBeat) <= staleSec() || s.harnessAlive()
}

// countLiveSessions counts what liveSessions returns, without reaping.
func countLiveSessions(agentDir string, now time.Time) int {
	n := 0
	for _, s := range agentSessions(agentDir) {
		if int(now.Unix()-s.LastBeat) <= staleSec() {
			n++
		}
	}
	return n
}

func liveSessions(agentDir string, now time.Time) []*Session {
	var out []*Session
	for _, s := range agentSessions(agentDir) {
		if int(now.Unix()-s.LastBeat) > staleSec() {
			// Stale: reap it opportunistically — unless its harness is still
			// running (an open, idle session keeps its terminal and jobs).
			if !s.harnessAlive() {
				_ = os.Remove(s.path)
			}
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started < out[j].Started })
	return out
}

// outstandingClaims lists request ids currently claimed in this inbox.
func outstandingClaims(agent *Agent, now time.Time) []string {
	entries, err := os.ReadDir(agent.InboxPath())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".claim") {
			continue
		}
		cp := filepath.Join(agent.InboxPath(), e.Name())
		if c, err := readClaim(cp); err == nil && reclaimable(strings.TrimSuffix(cp, ".claim"), c, now) {
			continue // abandoned: owner dead and heartbeat stale
		}
		out = append(out, strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".claim"), ".md"))
	}
	return out
}

func readSession(p string) (*Session, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	s.path = p
	return &s, nil
}

func writeSession(s *Session) error {
	b, _ := json.Marshal(s)
	return writeFileAtomic(s.path, b, 0o644)
}
