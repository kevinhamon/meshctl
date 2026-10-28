package console

import (
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// SessionReport is one open session of a peer and the jobs it has
// scheduled (tickets 11–12).
type SessionReport struct {
	Agent       string      `json:"agent"`
	ID          string      `json:"id"`
	Started     string      `json:"started"`
	LastActive  string      `json:"last_active"`
	IdleSeconds int64       `json:"idle_seconds"`
	TTY         string      `json:"tty,omitempty"`
	Background  bool        `json:"background,omitempty"` // running with no terminal window
	Focusable   bool        `json:"focusable"`
	Claim       string      `json:"claim,omitempty"`
	KBBranch    string      `json:"kb_branch,omitempty"`
	Jobs        []JobReport `json:"jobs"`
	JobsAt      string      `json:"jobs_at,omitempty"`
}

// JobReport is one scheduled job.
type JobReport struct {
	ID       string `json:"id"`
	Schedule string `json:"schedule"`
	// ScheduleText is the schedule in plain words; empty when DescribeCron
	// cannot phrase it, so the page shows the expression.
	ScheduleText string `json:"schedule_text,omitempty"`
	Recurring    bool   `json:"recurring"`
	Prompt       string `json:"prompt"`
}

// Sessions lists every peer's open sessions, in roster order.
func Sessions(dir string, now time.Time) []SessionReport {
	out := []SessionReport{}
	agents, _, err := mesh.ScanAgents(dir)
	if err != nil {
		return out
	}
	unix := func(s int64) string {
		if s == 0 {
			return ""
		}
		return time.Unix(s, 0).UTC().Format(time.RFC3339)
	}
	for _, a := range agents {
		for _, s := range mesh.OpenSessions(a, now) {
			r := SessionReport{Agent: a.Name, ID: s.ID, Started: unix(s.Started), LastActive: unix(s.LastBeat),
				IdleSeconds: now.Unix() - s.LastBeat, TTY: s.TTY, Background: s.Background, Focusable: s.TTY != "" && !s.Background && focusSupported(),
				Claim: s.CurrentClaim, KBBranch: s.KBBranch, Jobs: []JobReport{}}
			// Jobs belong to the harness process, so a session started by
			// /clear shows the jobs its process kept.
			if js, ok := mesh.SessionJobs(a, s); ok {
				r.JobsAt = unix(js.At)
				for _, j := range js.Jobs {
					text, _ := DescribeCron(j.Schedule, j.Recurring)
					r.Jobs = append(r.Jobs, JobReport{ID: j.ID, Schedule: j.Schedule, ScheduleText: text, Recurring: j.Recurring, Prompt: j.Prompt})
				}
			}
			out = append(out, r)
		}
	}
	return out
}

func focusSupported() bool { return runtime.GOOS == "darwin" }

// focusScript selects the iTerm2 session on the terminal given as its
// argument and raises its window. The tty is an argument, never spliced into
// the script.
const focusScript = `on run argv
	set ttyName to item 1 of argv
	if application "iTerm2" is not running then return "no-iterm"
	tell application "iTerm2"
		repeat with w in windows
			repeat with t in tabs of w
				repeat with s in sessions of t
					if tty of s is ttyName then
						select w
						select t
						select s
						activate
						return "ok"
					end if
				end repeat
			end repeat
		end repeat
	end tell
	return "not-found"
end run`

// Osascript runs an AppleScript with arguments; tests replace it.
var Osascript = func(script string, args ...string) (string, error) {
	out, err := exec.Command("osascript", append([]string{"-e", script}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

var (
	errFocusUnsupported = errors.New("bringing a terminal to the front works with iTerm2 on macOS only; the session's terminal is shown instead")
	errNoITerm          = errors.New("iTerm2 is not running")
	errTTYNotFound      = errors.New("no iTerm2 session is on that terminal (another terminal app, or the tab was closed)")
	errNoWindow         = errors.New("this session runs in the background with no window; reopen it from its harness")
)

// FocusTTY brings the iTerm2 tab on tty to the front.
func FocusTTY(tty string) error {
	if !focusSupported() {
		return errFocusUnsupported
	}
	if !strings.HasPrefix(tty, "/dev/tty") {
		return errTTYNotFound
	}
	out, err := Osascript(focusScript, tty)
	if err != nil {
		return err
	}
	switch out {
	case "ok":
		return nil
	case "no-iterm":
		return errNoITerm
	}
	return errTTYNotFound
}

func (s *Server) focus(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	var req struct{ Agent, Session string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, sr := range Sessions(e.dir, time.Now()) {
		if sr.Agent == req.Agent && sr.ID == req.Session {
			if sr.Background {
				http.Error(w, errNoWindow.Error(), http.StatusConflict)
				return
			}
			if err := FocusTTY(sr.TTY); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	http.Error(w, "no such open session", http.StatusNotFound)
}
