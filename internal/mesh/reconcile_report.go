package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// reconcile_report.go — the reconcile report's grammar (ADR-0070; spec
// decision 45). An agent writes the report beside the worksheet at
// <peer>/.memory/reconcile/<date>.md: an optional `## Standing orders` section
// first (standing orders contradicting doctrine, fixed by hand, never
// ledgered), then one line per entry:
//
//	- <kb-relative path>[#<slug>] · <verdict>[ → <mesh://… | doctrine | owner <peer>, pending>] · "<quoted grounding>"
//
// meshctl judges nothing in it; it validates the grammar, and `kb confirm`
// reads the latest verdict per entry. Other prose (a title, an `## Intent`
// section, blank lines) is ignored; every other `- ` bullet is an entry line.

// Reconcile verdicts.
const (
	ReconcileConsistent = "consistent"
	ReconcileConflict   = "conflict"
	ReconcileJudgment   = "judgment"
	ReconcileUngrounded = "ungrounded"
	ReconcileTermVsFact = "term-vs-fact"
)

var reconcileVerdicts = map[string]bool{
	ReconcileConsistent: true, ReconcileConflict: true, ReconcileJudgment: true,
	ReconcileUngrounded: true, ReconcileTermVsFact: true,
}

// ReconcileDoctrine is the counterpart of a conflict with pool doctrine.
const ReconcileDoctrine = "doctrine"

// ReconcileReportLine is one parsed entry line.
type ReconcileReportLine struct {
	Line    int    // 1-based line number in the report
	Raw     string // the line as written (trimmed)
	Rel     string // kb-relative path
	Anchor  string // section slug, "" = whole entry
	Verdict string
	// Target is what follows the arrow, as written ("" when none).
	Target string
	// Counterpart is the conflicting entry: the arrow's mesh:// target, or for
	// `owner <peer>, pending` the first mesh://<peer>/… link on the line, or
	// ReconcileDoctrine.
	Counterpart string
	// OwnerPending names the fact owner of an `owner <peer>, pending` line.
	OwnerPending string
	Quote        string // the quoted grounding, quotes included
}

// ID is the line's entry address: path or path#anchor.
func (l ReconcileReportLine) ID() string {
	if l.Anchor != "" {
		return l.Rel + "#" + l.Anchor
	}
	return l.Rel
}

// ReconcileReportError is one grammar failure.
type ReconcileReportError struct {
	Line int
	Msg  string
}

// ReconcileReport is a parsed reconcile report.
type ReconcileReport struct {
	Path           string
	Intent         string   // the `## Intent` line, else the first non-empty line, heading marks stripped
	StandingOrders []string // bullets of the `## Standing orders` section
	Lines          []ReconcileReportLine
	Errors         []ReconcileReportError
}

var (
	reReportSection = regexp.MustCompile(`^#{2,6}\s+(.+?)\s*#*\s*$`)
	reReportAddress = regexp.MustCompile(`^[^\s#·]+\.md(#[a-z0-9_][a-z0-9_-]*)?$`)
	reReportOwner   = regexp.MustCompile(`^owner\s+([A-Za-z0-9._-]+)\s*,\s*pending$`)
	reReportQuoted  = regexp.MustCompile(`"[^"]*[^"\s][^"]*"`)
)

// ParseReconcileReport parses report text; grammar failures are collected in
// Errors with their line numbers (Err renders them), never fatal.
func ParseReconcileReport(text string) ReconcileReport {
	var r ReconcileReport
	section, first, intent := "", "", ""
	sawEntry := false
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		n := i + 1
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if first == "" {
			first = strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
		if m := reReportSection.FindStringSubmatch(line); m != nil {
			section = strings.ToLower(m[1])
			if section == "standing orders" && sawEntry {
				r.Errors = append(r.Errors, ReconcileReportError{n, "`## Standing orders` must come first, before any entry line"})
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		switch section {
		case "intent":
			// The Intent is one line; entry lines may follow it directly.
			intent, section = strings.TrimSpace(strings.TrimPrefix(line, "- ")), ""
			continue
		case "standing orders":
			if strings.HasPrefix(line, "- ") {
				r.StandingOrders = append(r.StandingOrders, strings.TrimSpace(line[2:]))
			}
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		sawEntry = true
		l, msg := parseReportLine(line)
		l.Line = n
		if msg != "" {
			r.Errors = append(r.Errors, ReconcileReportError{n, msg})
			continue
		}
		r.Lines = append(r.Lines, l)
	}
	r.Intent = intent
	if r.Intent == "" {
		r.Intent = first
	}
	return r
}

// parseReportLine parses one `- ` entry line; msg names what is wrong.
func parseReportLine(line string) (l ReconcileReportLine, msg string) {
	l.Raw = line
	parts := strings.SplitN(strings.TrimSpace(line[2:]), " · ", 3)
	addr := strings.TrimSpace(parts[0])
	if !reReportAddress.MatchString(addr) {
		return l, fmt.Sprintf("%q is not a kb-relative path[#slug]", addr)
	}
	addr = kbRelPath(addr)
	l.Rel, l.Anchor, _ = strings.Cut(addr, "#")
	if len(parts) < 2 {
		return l, "no verdict (want `<path> · <verdict> · \"<quoted grounding>\"`)"
	}
	verdict, target, hasArrow := strings.Cut(parts[1], "→")
	l.Verdict = strings.TrimSpace(verdict)
	l.Target = strings.TrimSpace(target)
	if !reconcileVerdicts[l.Verdict] {
		return l, fmt.Sprintf("unknown verdict %q (want consistent, conflict, judgment, ungrounded or term-vs-fact)", l.Verdict)
	}
	if hasArrow {
		switch {
		case l.Target == ReconcileDoctrine:
			l.Counterpart = ReconcileDoctrine
		case strings.HasPrefix(l.Target, MeshRefScheme):
			if _, err := ParseMeshRef(l.Target); err != nil {
				return l, "counterpart: " + err.Error()
			}
			l.Counterpart = l.Target
		default:
			m := reReportOwner.FindStringSubmatch(l.Target)
			if m == nil {
				return l, fmt.Sprintf("counterpart %q: want mesh://…, doctrine, or owner <peer>, pending", l.Target)
			}
			l.OwnerPending = m[1]
			for _, ref := range MeshRefsIn(line) {
				if r, err := ParseMeshRef(ref); err == nil && r.Owner == l.OwnerPending {
					l.Counterpart = ref
					break
				}
			}
			if l.Counterpart == "" {
				return l, fmt.Sprintf("conflict names no counterpart: `owner %s, pending` needs the mesh://%s/… entry on the line", l.OwnerPending, l.OwnerPending)
			}
		}
	}
	if len(parts) < 3 || len(reReportQuoted.FindAllString(parts[2], -1)) == 0 {
		return l, "no quoted grounding — a verdict line quotes its grounding: `· \"<quote>\"`"
	}
	l.Quote = strings.TrimSpace(parts[2])
	if l.Verdict == ReconcileConflict {
		if l.Counterpart == "" {
			return l, "conflict names no counterpart — `conflict → <mesh://… | doctrine | owner <peer>, pending>`"
		}
		if len(reReportQuoted.FindAllString(l.Quote, -1)) < 2 {
			return l, "a conflict line quotes both claims (`\"ours\" vs \"theirs\"`)"
		}
	}
	return l, ""
}

// ReadReconcileReport reads and parses a report file.
func ReadReconcileReport(path string) (ReconcileReport, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ReconcileReport{Path: path}, err
	}
	r := ParseReconcileReport(string(b))
	r.Path = path
	return r, nil
}

// Err is nil when the report is well formed, else one error naming every
// failing line.
func (r ReconcileReport) Err() error {
	if len(r.Errors) == 0 {
		return nil
	}
	var b strings.Builder
	name := r.Path
	if name == "" {
		name = "reconcile report"
	}
	fmt.Fprintf(&b, "%s: %d malformed line(s):", name, len(r.Errors))
	for _, e := range r.Errors {
		fmt.Fprintf(&b, "\n  line %d: %s", e.Line, e.Msg)
	}
	return fmt.Errorf("%s", b.String())
}

// Latest is the last line addressing id (path or path#anchor), the entry's
// latest verdict.
func (r ReconcileReport) Latest(id string) (ReconcileReportLine, bool) {
	id = kbRelPath(id)
	for i := len(r.Lines) - 1; i >= 0; i-- {
		if r.Lines[i].ID() == id {
			return r.Lines[i], true
		}
	}
	return ReconcileReportLine{}, false
}

// OpenConflicts counts entries whose latest verdict is `conflict`.
func (r ReconcileReport) OpenConflicts() int { return len(r.openConflictLines()) }

// openConflictLines are the latest lines per entry whose verdict is conflict.
func (r ReconcileReport) openConflictLines() []ReconcileReportLine {
	seen := map[string]bool{}
	var out []ReconcileReportLine
	for i := len(r.Lines) - 1; i >= 0; i-- {
		l := r.Lines[i]
		if seen[l.ID()] {
			continue
		}
		seen[l.ID()] = true
		if l.Verdict == ReconcileConflict {
			out = append(out, l)
		}
	}
	return out
}

// reconcileFindings is doctor's reconcile line for one peer (spec decision
// 49): the days since its latest reconcile report and that report's open
// conflicts, or the report's malformed lines. Nothing when it has no report.
// Reads only the peer's own reconcile directory.
func reconcileFindings(a *Agent, now time.Time) []DoctorFinding {
	path, ok := LatestReconcileReport(a.Dir())
	if !ok {
		return nil
	}
	rel := ".memory/" + ReconcileDirName + "/" + filepath.Base(path)
	r, err := ReadReconcileReport(path)
	if err != nil {
		return []DoctorFinding{{Agent: a.Name, Level: "warn", Message: "latest reconcile report " + rel + " is unreadable: " + err.Error()}}
	}
	if err := r.Err(); err != nil {
		var lines []string
		for _, e := range r.Errors {
			lines = append(lines, fmt.Sprintf("line %d: %s", e.Line, e.Msg))
		}
		return []DoctorFinding{{Agent: a.Name, Level: "warn", Message: fmt.Sprintf("latest reconcile report %s is malformed (%s) — fix it before `kb confirm`",
			rel, strings.Join(lines, "; "))}}
	}
	days := 0
	if d, err := time.ParseInLocation("2006-01-02", strings.TrimSuffix(filepath.Base(path), ".md"), now.Location()); err == nil {
		y, m, dd := now.Date()
		days = int(time.Date(y, m, dd, 0, 0, 0, 0, now.Location()).Sub(d).Hours() / 24)
	}
	msg := fmt.Sprintf("latest reconcile report %s is %d day(s) ago with %d open conflict(s)", rel, days, r.OpenConflicts())
	if n := r.OpenConflicts(); n > 0 {
		msg += " — resolve them in the fact owner's session (ADR-0070), then re-verdict and `kb confirm`"
	}
	return []DoctorFinding{{Agent: a.Name, Level: "warn", Message: msg}}
}
