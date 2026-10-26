package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// statusline.go — the Claude Code status line (ADR-0074).
//
// One line under the prompt: the agent this session runs as, then the pending
// work (ADR-0071) that waits on the human, recomputed from Pending on every
// call. No cache and no subprocess: Pending is directory listings plus one hash
// pass, cheap enough to run on every refresh. Claude Code re-runs the command
// on its own events and every StatusLineRefreshSeconds, so counts move while
// the session is idle. Fail-open like the advisory hooks: anything it cannot
// resolve renders as the plain directory name.

// StatusLineRefreshSeconds is the refreshInterval wired with the status line.
const StatusLineRefreshSeconds = 10

const (
	ansiDim   = "\033[2m"
	ansiBold  = "\033[1m"
	ansiRed   = "\033[31m"
	ansiReset = "\033[0m"
)

// StatusLinePayload is the subset of Claude Code's status-line stdin JSON the
// mesh reads.
type StatusLinePayload struct {
	Cwd       string `json:"cwd"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
}

// ParseStatusLinePayload decodes the payload, tolerating an empty/invalid stdin.
func ParseStatusLinePayload(b []byte) StatusLinePayload {
	var p StatusLinePayload
	_ = json.Unmarshal(b, &p) // fail-open: zero value on any error
	return p
}

// StatusLine renders the line for the session the payload describes. An
// unresolvable pool or agent renders the directory name, dimmed.
func StatusLine(agentsDir string, p StatusLinePayload, now time.Time) string {
	dirs := []string{p.Workspace.ProjectDir, p.Workspace.CurrentDir, p.Cwd}
	var a *Agent
	if agentsDir != "" {
		a = statusLineAgent(agentsDir, dirs)
	}
	if a == nil {
		cwd := ""
		for _, d := range dirs {
			if d != "" {
				cwd = d
				break
			}
		}
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		return ansiDim + filepath.Base(cwd) + ansiReset
	}
	return renderStatusLine(Pending(agentsDir, a, now))
}

// statusLineAgent resolves the agent whose workspace holds one of dirs, the
// nearest ancestor first, so a session cd'd into a subdirectory still finds it.
func statusLineAgent(agentsDir string, dirs []string) *Agent {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		for dir := filepath.Clean(d); ; {
			if a, err := FindAgent(agentsDir, filepath.Base(dir)); err == nil && sameDir(a.Dir(), dir) {
				return a
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return nil
}

func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// statusGroup renders [label: value, ...]: labels dim, values in the
// terminal's own color unless color is set (red for what blocks, bold for
// the agent name).
func statusGroup(color string, kv ...string) string {
	var fields []string
	for i := 0; i+1 < len(kv); i += 2 {
		fields = append(fields, fmt.Sprintf("%s%s:%s %s%s%s", ansiDim, kv[i], ansiReset, color, kv[i+1], ansiReset))
	}
	return "[" + strings.Join(fields, ", ") + "]"
}

// StatusLinePhrases is the phrase each group tells the human to say. The
// standing orders define every one (cognitionSection, the mesh-cognition
// skill); a phrase only starts a review, the human still decides.
var StatusLinePhrases = map[PendingKind]string{
	PendingKBBranches: "confirm kb",
	PendingConflicts:  "resolve conflicts",
	PendingIntake:     "check intake",
	PendingDelegated:  "review work",
	PendingHealth:     "consolidate",
}

// sayHint appends the group's phrase, dimmed, inside its closing bracket.
func sayHint(group string, k PendingKind) string {
	return strings.TrimSuffix(group, "]") + fmt.Sprintf(", %ssay \"%s\"%s]", ansiDim, StatusLinePhrases[k], ansiReset)
}

// renderStatusLine: the agent, then what waits on the human, each group with
// the phrase that starts its review. Read-back is left out: answers are read
// at session start, not chased from the prompt.
func renderStatusLine(b PendingBlock) string {
	parts := []string{statusGroup(ansiBold, "agent", b.Agent)}
	for _, l := range b.Lines {
		var g string
		switch l.Kind {
		case PendingKBBranches:
			g = statusGroup(ansiRed, "needs-confirm", fmt.Sprint(l.Count))
		case PendingConflicts:
			g = statusGroup(ansiRed, "conflicts", fmt.Sprint(l.Count))
		case PendingIntake:
			g = statusGroup("", "intake", fmt.Sprint(l.Count), "oldest", fmt.Sprintf("%dd", l.OldestDays))
		case PendingDelegated:
			g = statusGroup("", "dispatched", fmt.Sprint(l.Count), "oldest", fmt.Sprintf("%dd", l.OldestDays))
		case PendingHealth:
			if l.Aged > 0 {
				g = statusGroup("", "kb", fmt.Sprintf("%d overdue", l.Aged))
			}
		}
		if g != "" {
			parts = append(parts, sayHint(g, l.Kind))
		}
	}
	if len(parts) == 1 {
		parts = append(parts, statusGroup("", "pending", "none"))
	}
	return strings.Join(parts, " * ")
}
