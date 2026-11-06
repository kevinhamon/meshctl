package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// statusline.go — the Claude Code status line.
//
// One line under the prompt: the agent this session runs as, then the pending
// work that waits on the human, recomputed on every call from the peer's own
// workspace only (scopeOwn). No cache and no subprocess: that scope is
// directory listings of the peer's own inbox and worktrees plus one hash pass
// over its own knowledge, cheap enough to run on every refresh. The pool-wide
// kinds (read-back, triage signals) are left out: the line never renders them,
// and they cost every peer's inbox and archive plus git in every peer repo.
// Claude Code re-runs the command on its own events and every
// StatusLineRefreshSeconds, so counts move while the session is idle.
// Fail-open like the advisory hooks: anything it cannot resolve renders as the
// plain directory name. When the pool's grants are paused, a red
// [grants: paused] group ends the line.

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
	line := renderStatusLine(pendingScoped(agentsDir, a, now, scopeOwn))
	// The grants kill switch: one pool-config read, the same
	// file read every gate call makes.
	if LoadPoolConfig(agentsDir).Settings.Grants.Paused {
		line += " * " + statusGroup(ansiRed, "grants", "paused")
	}
	return line
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

// StatusLinePhrases is the phrase each group tells the human to say, keyed
// by the group's label. The standing orders define every one
// (cognitionSection, the mesh-cognition skill); a phrase only starts a
// review, the human still decides.
var StatusLinePhrases = map[string]string{
	"needs-confirm": "confirm kb",
	"conflicts":     "resolve conflicts",
	"intake":        "check intake",
	"dispatched":    "review work",
	"kb":            "consolidate",
	"unconfirmed":   "reconcile",
}

// sayHint appends the group's phrase, dimmed, inside its closing bracket.
func sayHint(group, label string) string {
	return strings.TrimSuffix(group, "]") + fmt.Sprintf(", %ssay \"%s\"%s]", ansiDim, StatusLinePhrases[label], ansiReset)
}

// statusGroupSpec is one status-line group a pending line renders as: its
// label (the StatusLinePhrases key), color and the value pairs after it.
type statusGroupSpec struct {
	label, color string
	kv           []string
}

// statusGroups maps a pending line to its status-line groups (none for
// read-back). `pending --json` reuses it for each line's phrases, so the line
// and the report cannot disagree.
func statusGroups(l PendingLine) []statusGroupSpec {
	switch l.Kind {
	case PendingKBBranches:
		return []statusGroupSpec{{"needs-confirm", ansiRed, []string{fmt.Sprint(l.Count)}}}
	case PendingConflicts:
		return []statusGroupSpec{{"conflicts", ansiRed, []string{fmt.Sprint(l.Count)}}}
	case PendingIntake:
		return []statusGroupSpec{{"intake", "", []string{fmt.Sprint(l.Count), "oldest", fmt.Sprintf("%dd", l.OldestDays)}}}
	case PendingDelegated:
		return []statusGroupSpec{{"dispatched", "", []string{fmt.Sprint(l.Count), "oldest", fmt.Sprintf("%dd", l.OldestDays)}}}
	case PendingHealth:
		var out []statusGroupSpec
		if l.Aged > 0 {
			out = append(out, statusGroupSpec{"kb", "", []string{fmt.Sprintf("%d overdue", l.Aged)}})
		}
		if l.Unconfirmed > 0 {
			out = append(out, statusGroupSpec{"unconfirmed", "", []string{fmt.Sprint(l.Unconfirmed)}})
		}
		return out
	}
	return nil
}

// renderStatusLine: the agent, then what waits on the human, each group with
// the phrase that starts its review. Read-back is left out: answers are read
// at session start, not chased from the prompt. Candidates show only once
// overdue (a fresh draft is not yet debt); unconfirmed entries show whenever
// there are any, since nothing else ever surfaces them between sessions.
func renderStatusLine(b PendingBlock) string {
	parts := []string{statusGroup(ansiBold, "agent", b.Agent)}
	for _, l := range b.Lines {
		for _, g := range statusGroups(l) {
			parts = append(parts, sayHint(statusGroup(g.color, append([]string{g.label}, g.kv...)...), g.label))
		}
	}
	if len(parts) == 1 {
		parts = append(parts, statusGroup("", "pending", "none"))
	}
	return strings.Join(parts, " * ")
}

// StatusReport is the status line as data, for a harness that draws its own
// view (the opencode TUI panel): the same agent and groups, no ANSI.
type StatusReport struct {
	Agent  string        `json:"agent,omitempty"` // "" when the directory is not an agent
	Pool   string        `json:"pool,omitempty"`
	Dir    string        `json:"dir"`
	Groups []StatusEntry `json:"groups"`
}

// StatusEntry is one group: its label, the value text, the phrase that
// starts its review, and whether it blocks (rendered red on the line).
type StatusEntry struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Say   string `json:"say,omitempty"`
	Alert bool   `json:"alert,omitempty"`
}

// StatusLineReport resolves the session the payload describes the way
// StatusLine does and returns the groups as data.
func StatusLineReport(agentsDir string, p StatusLinePayload, now time.Time) StatusReport {
	dirs := []string{p.Workspace.ProjectDir, p.Workspace.CurrentDir, p.Cwd}
	r := StatusReport{Groups: []StatusEntry{}}
	for _, d := range dirs {
		if d != "" {
			r.Dir = d
			break
		}
	}
	if r.Dir == "" {
		r.Dir, _ = os.Getwd()
		dirs = append(dirs, r.Dir)
	}
	if agentsDir == "" {
		return r
	}
	a := statusLineAgent(agentsDir, dirs)
	if a == nil {
		return r
	}
	cfg := LoadPoolConfig(agentsDir)
	r.Agent, r.Pool = a.Name, cfg.Name
	if r.Pool == "" {
		r.Pool = filepath.Base(agentsDir)
	}
	for _, l := range pendingScoped(agentsDir, a, now, scopeOwn).Lines {
		for _, g := range statusGroups(l) {
			val := ""
			for i := 0; i < len(g.kv); i++ {
				if i%2 == 1 { // a sub-label ("oldest") joins its value
					val += " " + g.kv[i] + " " + g.kv[i+1]
					i++
					continue
				}
				val += g.kv[i]
			}
			r.Groups = append(r.Groups, StatusEntry{Label: g.label, Value: val, Say: StatusLinePhrases[g.label], Alert: g.color == ansiRed})
		}
	}
	if cfg.Settings.Grants.Paused {
		r.Groups = append(r.Groups, StatusEntry{Label: "grants", Value: "paused", Alert: true})
	}
	return r
}
