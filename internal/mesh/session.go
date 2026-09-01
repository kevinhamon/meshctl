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

// ResolveSessionID returns $CLAUDE_SESSION_ID or mints a stable-per-process id.
func ResolveSessionID(pid int, now time.Time) string {
	if v := os.Getenv("CLAUDE_SESSION_ID"); v != "" {
		return v
	}
	return fmt.Sprintf("s-%d-%d", pid, now.UnixNano())
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
