package mesh

import (
	"time"
)

// report_json.go — the machine-readable forms of `meshctl pending --json` and
// `meshctl doctor --json`. Agents read them as well as the console (ticket
// mesh-console/01), so the schema is versioned: ReportSchema changes on any
// incompatible change, and a field is only ever added within one version.

// ReportSchema versions PendingReport and DoctorReportJSON.
const ReportSchema = 1

// PendingReport is the pending work of one peer, or of every peer (--all).
type PendingReport struct {
	Schema    int    `json:"schema"`
	Generated string `json:"generated"` // RFC 3339, UTC
	// StaleDays is the age at which an item renders ⚠ stale.
	StaleDays int                  `json:"stale_days"`
	Agents    []PendingAgentReport `json:"agents"`
}

// PendingAgentReport is one peer's block. Lines is empty, never null, when
// nothing is pending, so a reader can list every peer.
type PendingAgentReport struct {
	Agent string              `json:"agent"`
	Total int                 `json:"total"`
	Lines []PendingLineReport `json:"lines"`
}

// PendingLineReport is one kind's line. Say holds the status-line phrases
// that start its review (StatusLinePhrases), empty when the line has none.
type PendingLineReport struct {
	Kind          string              `json:"kind"`
	Label         string              `json:"label"`
	Count         int                 `json:"count"`
	Noun          string              `json:"noun,omitempty"`
	OldestDays    int                 `json:"oldest_days"`
	Items         []PendingItemReport `json:"items"`
	Command       string              `json:"command,omitempty"`
	ListCommand   string              `json:"list_command,omitempty"`
	Detail        string              `json:"detail,omitempty"`
	TriageSignals int                 `json:"triage_signals,omitempty"`
	Aged          int                 `json:"aged,omitempty"`
	Unconfirmed   int                 `json:"unconfirmed,omitempty"`
	Say           []string            `json:"say"`
}

// PendingItemReport is one item of a line.
type PendingItemReport struct {
	ID      string `json:"id"`
	Who     string `json:"who"`
	AgeDays int    `json:"age_days"`
}

// pendingKindNames are the stable kind ids in the JSON form.
var pendingKindNames = map[PendingKind]string{
	PendingKBBranches: "kb-branches",
	PendingConflicts:  "conflicts",
	PendingIntake:     "intake",
	PendingReadBack:   "read-back",
	PendingDelegated:  "delegated",
	PendingHealth:     "kb-health",
}

// Report renders a peer's block in the JSON form.
func (b PendingBlock) Report() PendingAgentReport {
	r := PendingAgentReport{Agent: b.Agent, Total: b.Total(), Lines: []PendingLineReport{}}
	for _, l := range b.Lines {
		lr := PendingLineReport{Kind: pendingKindNames[l.Kind], Label: l.Label, Count: l.Count, Noun: l.Noun,
			OldestDays: l.OldestDays, Items: []PendingItemReport{}, Command: l.Command, ListCommand: l.ListCommand,
			Detail: l.Detail, TriageSignals: l.TriageSignals, Aged: l.Aged, Unconfirmed: l.Unconfirmed, Say: []string{}}
		for _, it := range l.Items {
			lr.Items = append(lr.Items, PendingItemReport{ID: it.ID, Who: it.Who, AgeDays: it.AgeDays})
		}
		for _, g := range statusGroups(l) {
			lr.Say = append(lr.Say, StatusLinePhrases[g.label])
		}
		r.Lines = append(r.Lines, lr)
	}
	return r
}

// NewPendingReport wraps peer blocks in the versioned envelope.
func NewPendingReport(blocks []PendingBlock, now time.Time) PendingReport {
	r := PendingReport{Schema: ReportSchema, Generated: now.UTC().Format(time.RFC3339), StaleDays: PendingStaleDays,
		Agents: []PendingAgentReport{}}
	for _, b := range blocks {
		r.Agents = append(r.Agents, b.Report())
	}
	return r
}

// PendingAll computes every peer's block, in roster order, including peers
// with nothing pending.
func PendingAll(agentsDir string, now time.Time) ([]PendingBlock, error) {
	agents, _, err := ScanAgents(agentsDir)
	if err != nil {
		return nil, err
	}
	out := make([]PendingBlock, 0, len(agents))
	for _, a := range agents {
		out = append(out, Pending(agentsDir, a, now))
	}
	return out, nil
}

// DoctorReportJSON is `meshctl doctor --json`.
type DoctorReportJSON struct {
	Schema    int                 `json:"schema"`
	Generated string              `json:"generated"`
	OK        bool                `json:"ok"` // no error-level finding
	Info      []string            `json:"info"`
	Findings  []DoctorFindingJSON `json:"findings"`
}

// DoctorFindingJSON is one finding. Fixable marks a hygiene item that
// `meshctl doctor --fix` repairs.
type DoctorFindingJSON struct {
	Agent   string `json:"agent"`
	Level   string `json:"level"` // error | warn
	Message string `json:"message"`
	Fixable bool   `json:"fixable"`
	Action  string `json:"action,omitempty"` // the console button it carries: commit | push
	Kind    string `json:"kind,omitempty"`   // a finding the console counts: not-restorable
}

// NewDoctorReport wraps findings in the versioned envelope.
func NewDoctorReport(findings []DoctorFinding, ok bool, info []string, now time.Time) DoctorReportJSON {
	r := DoctorReportJSON{Schema: ReportSchema, Generated: now.UTC().Format(time.RFC3339), OK: ok,
		Info: append([]string{}, info...), Findings: []DoctorFindingJSON{}}
	for _, f := range findings {
		r.Findings = append(r.Findings, DoctorFindingJSON{Agent: f.Agent, Level: f.Level, Message: f.Message, Fixable: f.Fixable, Action: f.Action, Kind: f.Kind})
	}
	return r
}

// DoctorWithHygiene is Doctor plus the pool hygiene items, deduplicated, as
// `meshctl doctor` reports them. fix applies the automatic repairs first and
// returns what it fixed and what failed.
func DoctorWithHygiene(agentsDir string, fix bool, now time.Time) (findings []DoctorFinding, ok bool, fixed []string, fixErrs []error) {
	findings, ok = Doctor(agentsDir)
	hyg := PoolHygiene(agentsDir, now)
	if fix {
		fixed, fixErrs = ApplyHygiene(hyg)
		hyg = ManualItems(hyg)
	}
	for _, h := range hyg {
		if doctorFindingSeen(findings, h.Agent, h.Message) {
			continue // reported by Doctor already (the pool repo nag lives in both)
		}
		findings = append(findings, DoctorFinding{Agent: h.Agent, Level: "warn", Message: h.Message, Fixable: h.Fixable()})
	}
	return findings, ok, fixed, fixErrs
}

// doctorFindingSeen reports whether this exact finding is already listed.
func doctorFindingSeen(findings []DoctorFinding, agent, msg string) bool {
	for _, f := range findings {
		if f.Agent == agent && f.Message == msg {
			return true
		}
	}
	return false
}
