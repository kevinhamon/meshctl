package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Session is a live presence record (ADR-0011 substrate). One file per session
// under <agent>/.sessions/<session-id>.
type Session struct {
	ID           string `json:"id"`
	PID          int    `json:"pid"`
	Started      int64  `json:"started"`
	LastBeat     int64  `json:"last_beat"`
	CurrentClaim string `json:"current_claim,omitempty"`
	KBBranch     string `json:"kb_branch,omitempty"`
	path         string `json:"-"`
}

func sessionsDir(agentDir string) string { return filepath.Join(agentDir, SessionsDir) }
func sessionPath(agentDir, id string) string {
	return filepath.Join(sessionsDir(agentDir), id)
}

// sessionIDEnv lists the env vars that carry the harness session id, in
// precedence order. CLAUDE_SESSION_ID is the explicit override; Claude Code
// exports CLAUDE_CODE_SESSION_ID to every tool subprocess, and it matches the
// session_id the hooks register presence under.
var sessionIDEnv = []string{"CLAUDE_SESSION_ID", "CLAUDE_CODE_SESSION_ID"}

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
// harness id, so a harness that does not export it to tool shells — opencode —
// still resolves to its session); else a minted per-process id.
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

	others := liveSessions(agent.Dir(), now)
	var live []*Session
	for _, s := range others {
		if s.ID != id {
			live = append(live, s)
		}
	}

	s := &Session{ID: id, PID: pid, Started: now.Unix(), LastBeat: now.Unix(), path: sessionPath(agent.Dir(), id)}
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
	err := os.Remove(sessionPath(agent.Dir(), id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// SessionBeat refreshes a session's heartbeat (and optionally its claim/branch).
func SessionBeat(agent *Agent, id string, now time.Time) error {
	s, err := readSession(sessionPath(agent.Dir(), id))
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
	s, err := readSession(p)
	if err != nil {
		if err := os.MkdirAll(sessionsDir(agent.Dir()), 0o755); err != nil {
			return err
		}
		s = &Session{ID: id, PID: pid, Started: now.Unix(), path: p}
	}
	s.LastBeat = now.Unix()
	return writeSession(s)
}

// SessionList returns the live sessions for an agent.
func SessionList(agent *Agent, now time.Time) []*Session {
	return liveSessions(agent.Dir(), now)
}

// sessionAlive reports whether a session is registered and heartbeating.
func sessionAlive(agentDir, id string, now time.Time) bool {
	if id == "" {
		return false
	}
	s, err := readSession(sessionPath(agentDir, id))
	if err != nil {
		return false
	}
	return int(now.Unix()-s.LastBeat) <= staleSec()
}

func liveSessions(agentDir string, now time.Time) []*Session {
	entries, err := os.ReadDir(sessionsDir(agentDir))
	if err != nil {
		return nil
	}
	var out []*Session
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		s, err := readSession(sessionPath(agentDir, e.Name()))
		if err != nil {
			continue
		}
		if int(now.Unix()-s.LastBeat) > staleSec() {
			// Stale: reap it opportunistically.
			_ = os.Remove(s.path)
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
	return os.WriteFile(s.path, b, 0o644)
}
