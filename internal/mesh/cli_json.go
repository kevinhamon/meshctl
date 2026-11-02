package mesh

import (
	"os"
	"time"
)

// cli_json.go — the --json output of the commands a program reads to follow a
// pool: inbox list/next, handoff show, state show, pool upgrade --check and
// session list. Each is one object with the report envelope (schema,
// generated) and is versioned by ReportSchema with the other reports. Lists
// are [] when empty, never null; times are RFC 3339 UTC, "" when unknown.

// ClaimJSON is who holds a request.
type ClaimJSON struct {
	Session   string `json:"session"`
	Heartbeat string `json:"heartbeat"`
}

// RequestJSON is one request in an inbox.
type RequestJSON struct {
	ID       string     `json:"id"`
	Path     string     `json:"path"`
	From     string     `json:"from"`
	To       string     `json:"to"`
	Type     string     `json:"type"`
	Status   string     `json:"status"`
	Priority string     `json:"priority"`
	Created  string     `json:"created"`
	Related  []string   `json:"related"`
	Claim    *ClaimJSON `json:"claim"` // null when unclaimed
}

func newRequestJSON(r *Request) RequestJSON {
	out := RequestJSON{ID: r.ID, Path: r.Path(), From: r.From, To: r.To, Type: r.Type,
		Status: canonicalOr(r.Status), Priority: r.Priority, Created: r.Created, Related: r.Related}
	if out.Related == nil {
		out.Related = []string{}
	}
	if c, err := readClaim(claimPath(r.Path())); err == nil {
		out.Claim = &ClaimJSON{Session: c.OwnerSession, Heartbeat: unixRFC3339(c.Heartbeat)}
	}
	return out
}

// InboxListJSON is `inbox list --json`.
type InboxListJSON struct {
	Schema    int           `json:"schema"`
	Generated string        `json:"generated"`
	Agent     string        `json:"agent"`
	Requests  []RequestJSON `json:"requests"`
}

func NewInboxListJSON(a *Agent, q []*Request, now time.Time) InboxListJSON {
	out := InboxListJSON{Schema: ReportSchema, Generated: now.UTC().Format(time.RFC3339), Agent: a.Name, Requests: []RequestJSON{}}
	for _, r := range q {
		out.Requests = append(out.Requests, newRequestJSON(r))
	}
	return out
}

// InboxNextJSON is `inbox next --json`: Request is null when nothing is
// pending; Claimed is true when this call took the claim (--claim).
type InboxNextJSON struct {
	Schema    int          `json:"schema"`
	Generated string       `json:"generated"`
	Agent     string       `json:"agent"`
	Request   *RequestJSON `json:"request"`
	Claimed   bool         `json:"claimed"`
}

func NewInboxNextJSON(a *Agent, r *Request, claimed bool, now time.Time) InboxNextJSON {
	out := InboxNextJSON{Schema: ReportSchema, Generated: now.UTC().Format(time.RFC3339), Agent: a.Name}
	if r != nil {
		rj := newRequestJSON(r)
		out.Request, out.Claimed = &rj, claimed
	}
	return out
}

// DocJSON is `handoff show --json` and `state show --json`: one file of the
// peer's, which may not have been written yet.
type DocJSON struct {
	Schema    int    `json:"schema"`
	Generated string `json:"generated"`
	Agent     string `json:"agent"`
	Exists    bool   `json:"exists"`
	Updated   string `json:"updated"` // the file's modification time
	Path      string `json:"path"`
	Body      string `json:"body"`
}

func NewDocJSON(a *Agent, path string, now time.Time) DocJSON {
	out := DocJSON{Schema: ReportSchema, Generated: now.UTC().Format(time.RFC3339), Agent: a.Name, Path: path}
	if info, err := os.Stat(path); err == nil {
		if b, err := os.ReadFile(path); err == nil {
			out.Exists, out.Body = true, string(b)
			out.Updated = info.ModTime().UTC().Format(time.RFC3339)
		}
	}
	return out
}

// UpgradeStepJSON is one change `pool upgrade` would make. Area is pool,
// doctrine or agent; Conflict marks locally-edited doctrine it leaves alone.
type UpgradeStepJSON struct {
	Area     string `json:"area"`
	Agent    string `json:"agent"`
	Action   string `json:"action"`
	Conflict bool   `json:"conflict"`
}

// UpgradeWarningJSON is something the upgrade does not change but the user
// must handle; Agent is "" for the pool as a whole.
type UpgradeWarningJSON struct {
	Agent   string `json:"agent"`
	Message string `json:"message"`
}

// UpgradeCheckJSON is `pool upgrade --check --json`.
type UpgradeCheckJSON struct {
	Schema    int                  `json:"schema"`
	Generated string               `json:"generated"`
	PoolRoot  string               `json:"pool_root"`
	Pending   bool                 `json:"pending"`
	Steps     []UpgradeStepJSON    `json:"steps"`
	Warnings  []UpgradeWarningJSON `json:"warnings"`
}

// NewUpgradeCheckJSON reports the plan as --check prints it: doctrineOnly
// leaves the agent steps out, and Pending is the command's exit status.
func NewUpgradeCheckJSON(p PoolUpgradePlan, doctrineOnly bool, now time.Time) UpgradeCheckJSON {
	out := UpgradeCheckJSON{Schema: ReportSchema, Generated: now.UTC().Format(time.RFC3339), PoolRoot: p.PoolRoot,
		Pending: p.Pending(), Steps: []UpgradeStepJSON{}, Warnings: []UpgradeWarningJSON{}}
	for _, s := range p.Pool {
		out.Steps = append(out.Steps, UpgradeStepJSON{Area: "pool", Action: s})
	}
	for _, d := range p.Doctrine {
		out.Steps = append(out.Steps, UpgradeStepJSON{Area: "doctrine", Action: "update " + d})
	}
	for _, c := range p.Conflicts {
		out.Steps = append(out.Steps, UpgradeStepJSON{Area: "doctrine", Action: c, Conflict: true})
	}
	if !doctrineOnly {
		for _, a := range p.Agents {
			for _, x := range a.Actions {
				out.Steps = append(out.Steps, UpgradeStepJSON{Area: "agent", Agent: a.Name, Action: x})
			}
			for _, w := range a.Warnings {
				out.Warnings = append(out.Warnings, UpgradeWarningJSON{Agent: a.Name, Message: w})
			}
		}
	}
	for _, w := range p.Warnings {
		out.Warnings = append(out.Warnings, UpgradeWarningJSON{Message: w})
	}
	if nag := PoolRepoNag(p.PoolRoot); nag != "" {
		out.Warnings = append(out.Warnings, UpgradeWarningJSON{Message: nag})
	}
	return out
}

// SessionJSON is one live session. PIDCheckable is false when its process was
// recorded in another scope (procScope), so its liveness is its heartbeat.
type SessionJSON struct {
	ID           string `json:"id"`
	Started      string `json:"started"`
	LastBeat     string `json:"last_beat"`
	CurrentClaim string `json:"current_claim"`
	ProcScope    string `json:"proc_scope"`
	PIDCheckable bool   `json:"pid_checkable"`
}

// SessionListJSON is `session list --json`.
type SessionListJSON struct {
	Schema    int           `json:"schema"`
	Generated string        `json:"generated"`
	Agent     string        `json:"agent"`
	Sessions  []SessionJSON `json:"sessions"`
}

func NewSessionListJSON(a *Agent, sessions []*Session, now time.Time) SessionListJSON {
	out := SessionListJSON{Schema: ReportSchema, Generated: now.UTC().Format(time.RFC3339), Agent: a.Name, Sessions: []SessionJSON{}}
	for _, s := range sessions {
		_, known := s.harnessState()
		out.Sessions = append(out.Sessions, SessionJSON{ID: s.ID, Started: unixRFC3339(s.Started),
			LastBeat: unixRFC3339(s.LastBeat), CurrentClaim: s.CurrentClaim, ProcScope: s.ProcScope, PIDCheckable: known})
	}
	return out
}

func unixRFC3339(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}
