package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// triage_report.go — the triage report. An agent
// writes it at TriageReportPath beside the worksheet: an optional
// `Consulted:` section first, quoting each requester peer consulted
// in-session, then one line per request:
//
//	- <id> · <stands | superseded → <ref> | done → <ref> | withdrawn → <ref> | unclear> · "<quoted grounding>" · <source>
//
// `superseded`, `done` and `withdrawn` close the request and need a ref and a
// quote; `stands` and `unclear` keep it open and take no ref (the quote is the
// live need, or the signals checked). A source of `human, confirmation round`
// is grounding like any other: the human's override in the round. A line may
// end with `· branch live`, which `inbox triage apply` appends when a live work
// branch blocks the close. The report is runtime state and a hypothesis;
// meshctl only checks its grammar and the refs.

// Triage verdicts. Request state (`in-progress`, …) is never a verdict.
const (
	TriageStands     = "stands"
	TriageSuperseded = "superseded"
	TriageDone       = "done"
	TriageWithdrawn  = "withdrawn"
	TriageUnclear    = "unclear"

	// TriageSourceHuman is the source of a verdict the human set or
	// overrode in the confirmation round.
	TriageSourceHuman = "human, confirmation round"
	// TriageBranchLive is the suffix apply appends to a line whose request
	// still has a live mesh/work/<id> branch or worktree.
	TriageBranchLive = "branch live"
)

// TriageClosing reports whether a verdict closes the request (needs a ref
// and a quote, and is what `inbox triage apply` acts on).
func TriageClosing(verdict string) bool {
	return verdict == TriageSuperseded || verdict == TriageDone || verdict == TriageWithdrawn
}

// TriageReportLine is one request's verdict line.
type TriageReportLine struct {
	N          int // 1-based line number in the report (0 when built in code)
	ID         string
	Verdict    string
	Ref        string // "" for stands / unclear
	Quote      string
	Source     string
	BranchLive bool
	Raw        string // the line as written
}

// String renders the line in the report grammar.
func (l TriageReportLine) String() string {
	v := l.Verdict
	if l.Ref != "" {
		v += " → " + l.Ref
	}
	s := fmt.Sprintf("- %s · %s · %q · %s", l.ID, v, l.Quote, l.Source)
	if l.BranchLive {
		s += " · " + TriageBranchLive
	}
	return s
}

// TriageReportError is one validation failure, by line.
type TriageReportError struct {
	Line int
	Msg  string
}

func (e TriageReportError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// TriageReport is a parsed report. Lines keep file order; Line returns the
// latest line for an id.
type TriageReport struct {
	Path      string
	Consulted []string
	Lines     []TriageReportLine
}

// Line returns the latest (last-written) line for id.
func (r *TriageReport) Line(id string) (TriageReportLine, bool) {
	for i := len(r.Lines) - 1; i >= 0; i-- {
		if r.Lines[i].ID == id {
			return r.Lines[i], true
		}
	}
	return TriageReportLine{}, false
}

var (
	reTriageConsulted = regexp.MustCompile(`^(?:#{1,6}\s*)?Consulted:?\s*$`)
	reTriageLine      = regexp.MustCompile(`^- (\S+) · (.+?) · "(.*)" · (.+?)$`)
	reTriageBullet    = regexp.MustCompile(`^- \S+ · `)
	reTriageVerdict   = regexp.MustCompile(`^([a-z-]+)(?:\s*(?:→|->)\s*(\S+))?$`)
	reTriageRefSha    = regexp.MustCompile(`^[0-9a-f]{4,40}$`)
)

// ParseTriageReport parses and validates a report. Lines that are not
// verdict bullets (headings, prose, blank) are ignored; every bullet outside
// the Consulted section must be a valid verdict line. Errors carry the line
// number; the report returned holds the lines that parsed.
func ParseTriageReport(text string) (*TriageReport, []TriageReportError) {
	r := &TriageReport{}
	var errs []TriageReportError
	inConsulted := false
	for i, raw := range strings.Split(text, "\n") {
		n := i + 1
		ln := strings.TrimRight(raw, " \t\r")
		trim := strings.TrimSpace(ln)
		if reTriageConsulted.MatchString(trim) {
			if len(r.Lines) > 0 {
				errs = append(errs, TriageReportError{n, "the Consulted: section must come first, before any verdict line"})
				continue
			}
			inConsulted = true
			continue
		}
		if !strings.HasPrefix(trim, "- ") {
			if strings.HasPrefix(trim, "#") {
				inConsulted = false
			}
			continue
		}
		if inConsulted && !looksLikeVerdictLine(trim) {
			r.Consulted = append(r.Consulted, strings.TrimSpace(strings.TrimPrefix(trim, "- ")))
			continue
		}
		inConsulted = false
		l, msg := parseTriageLine(trim)
		if msg != "" {
			errs = append(errs, TriageReportError{n, msg})
			continue
		}
		l.N, l.Raw = n, ln
		r.Lines = append(r.Lines, l)
	}
	return r, errs
}

// looksLikeVerdictLine: `- <id> · <verdict word>` — what ends the Consulted
// section.
func looksLikeVerdictLine(s string) bool {
	if !reTriageBullet.MatchString(s) {
		return false
	}
	parts := strings.SplitN(s, " · ", 3)
	if len(parts) < 2 {
		return false
	}
	m := reTriageVerdict.FindStringSubmatch(strings.TrimSpace(parts[1]))
	return m != nil && validTriageVerdict(m[1])
}

func validTriageVerdict(v string) bool {
	switch v {
	case TriageStands, TriageSuperseded, TriageDone, TriageWithdrawn, TriageUnclear:
		return true
	}
	return false
}

func parseTriageLine(s string) (TriageReportLine, string) {
	const grammar = "want `- <id> · <verdict>[ → <ref>] · \"<quoted grounding>\" · <source>`"
	body, live := s, false
	if strings.HasSuffix(body, " · "+TriageBranchLive) {
		body, live = strings.TrimSuffix(body, " · "+TriageBranchLive), true
	}
	m := reTriageLine.FindStringSubmatch(body)
	if m == nil {
		return TriageReportLine{}, "not a verdict line: " + grammar
	}
	l := TriageReportLine{ID: m[1], Quote: strings.TrimSpace(m[3]), Source: strings.TrimSpace(m[4]), BranchLive: live}
	vm := reTriageVerdict.FindStringSubmatch(strings.TrimSpace(m[2]))
	if vm == nil {
		return l, fmt.Sprintf("%s: unreadable verdict %q (want stands, superseded → <ref>, done → <ref>, withdrawn → <ref> or unclear)", l.ID, m[2])
	}
	l.Verdict, l.Ref = vm[1], vm[2]
	if !validTriageVerdict(l.Verdict) {
		if _, isStatus := CanonicalStatus(l.Verdict); isStatus {
			return l, fmt.Sprintf("%s: %q is a request state, never a verdict (want stands, superseded, done, withdrawn or unclear)", l.ID, l.Verdict)
		}
		return l, fmt.Sprintf("%s: unknown verdict %q (want stands, superseded, done, withdrawn or unclear)", l.ID, l.Verdict)
	}
	if l.Source == "" {
		return l, l.ID + ": no source"
	}
	if TriageClosing(l.Verdict) {
		if l.Ref == "" {
			return l, fmt.Sprintf("%s: %s closes the request and needs a ref (`%s → <ref>`)", l.ID, l.Verdict, l.Verdict)
		}
		if l.Quote == "" {
			return l, fmt.Sprintf("%s: %s closes the request and needs a quote from the artifact that grounds it", l.ID, l.Verdict)
		}
		if _, err := ParseTriageRef(l.Ref); err != nil {
			return l, l.ID + ": " + err.Error()
		}
		return l, ""
	}
	if l.Ref != "" {
		return l, fmt.Sprintf("%s: %s keeps the request open and takes no ref", l.ID, l.Verdict)
	}
	if l.Quote == "" {
		if l.Verdict == TriageStands {
			return l, l.ID + ": stands needs a quote of the live need, or \"no supersession signal\" with the signals checked"
		}
		return l, l.ID + ": unclear needs the signals checked, quoted"
	}
	return l, ""
}

// ReadTriageReport reads and validates a report file. Any grammar failure is
// an error naming each failing line.
func ReadTriageReport(path string) (*TriageReport, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, errs := ParseTriageReport(string(b))
	r.Path = path
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return r, fmt.Errorf("triage report %s is invalid:\n  %s", path, strings.Join(msgs, "\n  "))
	}
	return r, nil
}

// WriteTriageReport writes a report in the grammar (the agent normally
// writes it by hand; tests and tools use this). consulted are the Consulted:
// bullets, without the leading "- ".
func WriteTriageReport(path string, consulted []string, lines []TriageReportLine) error {
	var b strings.Builder
	b.WriteString("# Triage report\n\n")
	if len(consulted) > 0 {
		b.WriteString("Consulted:\n")
		for _, c := range consulted {
			b.WriteString("- " + c + "\n")
		}
		b.WriteString("\n")
	}
	for _, l := range lines {
		b.WriteString(l.String() + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(b.String()), 0o644)
}

// markTriageBranchLive rewrites the latest line of each id with the
// `· branch live` suffix, leaving every other line as written.
func markTriageBranchLive(r *TriageReport, ids []string) error {
	b, err := os.ReadFile(r.Path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	for _, id := range ids {
		l, ok := r.Line(id)
		if !ok || l.BranchLive || l.N < 1 || l.N > len(lines) {
			continue
		}
		lines[l.N-1] = strings.TrimRight(lines[l.N-1], " \t\r") + " · " + TriageBranchLive
	}
	return writeFileAtomic(r.Path, []byte(strings.Join(lines, "\n")), 0o644)
}

// --- refs ---

// Triage ref kinds.
const (
	TriageRefRequest   = "request"   // <peer>:<id>
	TriageRefKnowledge = "knowledge" // mesh://<peer>/<path>#<slug>
	TriageRefBranch    = "branch"    // <peer>@mesh/work/<id>
	TriageRefCommit    = "commit"    // <peer>@<sha>
)

// TriageRef is a parsed ref.
type TriageRef struct {
	Kind string
	Peer string
	ID   string // request id, or the work branch's request id
	SHA  string
	Raw  string
}

// ParseTriageRef reads the four ref forms syntactically.
func ParseTriageRef(s string) (TriageRef, error) {
	s = strings.TrimSpace(s)
	r := TriageRef{Raw: s}
	bad := func() (TriageRef, error) {
		return r, fmt.Errorf("unreadable ref %q (want <peer>:<id>, mesh://<peer>/<path>#<slug>, <peer>@mesh/work/<id> or <peer>@<sha>)", s)
	}
	if s == "" || strings.ContainsAny(s, " \t") {
		return bad()
	}
	if strings.HasPrefix(s, MeshRefScheme) {
		m, err := ParseMeshRef(s)
		if err != nil {
			return r, err
		}
		r.Kind, r.Peer = TriageRefKnowledge, m.Owner
		return r, nil
	}
	if peer, rest, ok := strings.Cut(s, "@"); ok {
		if ValidName("peer", peer) != nil || rest == "" {
			return bad()
		}
		r.Peer = peer
		if strings.HasPrefix(rest, WorkBranchPrefix) {
			r.Kind, r.ID = TriageRefBranch, strings.TrimPrefix(rest, WorkBranchPrefix)
			if r.ID == "" {
				return bad()
			}
			return r, nil
		}
		if !reTriageRefSha.MatchString(rest) {
			return bad()
		}
		r.Kind, r.SHA = TriageRefCommit, rest
		return r, nil
	}
	if peer, id, ok := strings.Cut(s, ":"); ok && ValidName("peer", peer) == nil && id != "" && !strings.Contains(id, ":") {
		r.Kind, r.Peer, r.ID = TriageRefRequest, peer, id
		return r, nil
	}
	return bad()
}

// ResolveTriageRef checks a ref against the pool: the request exists in the
// peer's inbox or archive; the knowledge file exists; the work branch exists
// or was merged into the peer's main; the commit exists in the peer's repo.
// The error names the ref.
func ResolveTriageRef(agentsDir, ref string) (TriageRef, error) {
	r, err := ParseTriageRef(ref)
	if err != nil {
		return r, err
	}
	unresolved := func(why string) (TriageRef, error) {
		return r, fmt.Errorf("ref %s does not resolve: %s", ref, why)
	}
	if r.Kind == TriageRefKnowledge {
		if _, ok, err := ResolveMeshRef(agentsDir, ref); err != nil {
			return unresolved(err.Error())
		} else if !ok {
			return unresolved("no such knowledge entry")
		}
		return r, nil
	}
	peer, err := FindAgent(agentsDir, r.Peer)
	if err != nil {
		return unresolved("no peer " + r.Peer)
	}
	switch r.Kind {
	case TriageRefRequest:
		for _, dir := range []string{peer.InboxPath(), filepath.Join(peer.Dir(), IntakeArchiveDir)} {
			if fileExists(filepath.Join(dir, r.ID+".md")) {
				return r, nil
			}
		}
		return unresolved("no request " + r.ID + " in " + r.Peer + "'s inbox or archive")
	case TriageRefBranch:
		if !isGitRepo(peer.Dir()) {
			return unresolved(r.Peer + " has no git repo")
		}
		live, merged := workBranchRefs(peer.Dir())
		for _, br := range append(live, merged...) {
			if br == WorkBranchPrefix+r.ID {
				return r, nil
			}
		}
		return unresolved("no branch " + WorkBranchPrefix + r.ID + " (live or merged) in " + r.Peer)
	case TriageRefCommit:
		if !isGitRepo(peer.Dir()) {
			return unresolved(r.Peer + " has no git repo")
		}
		if _, err := git(peer.Dir(), "cat-file", "-e", r.SHA+"^{commit}"); err != nil {
			return unresolved("no commit " + r.SHA + " in " + r.Peer)
		}
		return r, nil
	}
	return unresolved("unknown ref kind")
}

// --- incremental ---

const triageSkippedNote = "stands, signals unchanged"

var (
	reWorksheetSection = regexp.MustCompile(`^## (\S+)\s*$`)
	reWorksheetSignals = regexp.MustCompile(`^- signals: (sha256:[0-9a-f]+)\s*$`)
	reWorksheetSkipped = regexp.MustCompile(`^- (\S+) · ` + regexp.QuoteMeta(triageSkippedNote) + ` · signals: (sha256:[0-9a-f]+)\s*$`)
)

// TriagePriorStands maps each request id whose latest report line (across
// the peer's dated reports) is `stands` to the signal hash recorded in the
// worksheet beside that report. A later pass skips the request while its
// signal hash is unchanged.
func TriagePriorStands(agentDir string) map[string]string {
	out := map[string]string{}
	dir := TriageDirPath(agentDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	type latest struct{ verdict, date string }
	last := map[string]latest{}
	for _, e := range entries { // ReadDir sorts by name: dates ascend
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.HasSuffix(name, TriageWorksheetExt) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		rep, _ := ParseTriageReport(string(b))
		date := strings.TrimSuffix(name, ".md")
		for _, l := range rep.Lines {
			last[l.ID] = latest{l.Verdict, date}
		}
	}
	hashes := map[string]map[string]string{}
	for id, l := range last {
		if l.verdict != TriageStands {
			continue
		}
		h, ok := hashes[l.date]
		if !ok {
			h = worksheetSignalHashes(filepath.Join(dir, l.date+TriageWorksheetExt))
			hashes[l.date] = h
		}
		if h[id] != "" {
			out[id] = h[id]
		}
	}
	return out
}

// worksheetSignalHashes reads `- signals:` per request section and the
// Skipped list of a worksheet.
func worksheetSignalHashes(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	cur := ""
	for _, ln := range strings.Split(string(b), "\n") {
		if m := reWorksheetSection.FindStringSubmatch(ln); m != nil {
			cur = m[1]
			continue
		}
		if m := reWorksheetSignals.FindStringSubmatch(ln); m != nil && cur != "" {
			out[cur] = m[1]
			continue
		}
		if m := reWorksheetSkipped.FindStringSubmatch(ln); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

// --- doctor ---

// LatestTriageReport is the newest dated report (<date>.md, worksheets
// excluded) in the peer's triage directory; ok=false when there is none
// (LatestReconcileReport's counterpart).
func LatestTriageReport(agentDir string) (path string, ok bool) {
	entries, err := os.ReadDir(TriageDirPath(agentDir))
	if err != nil {
		return "", false
	}
	latest := ""
	for _, e := range entries {
		if !e.IsDir() && reDatedReportName.MatchString(e.Name()) && e.Name() > latest {
			latest = e.Name()
		}
	}
	if latest == "" {
		return "", false
	}
	return filepath.Join(TriageDirPath(agentDir), latest), true
}

// TriageDoctorFinding reports, for a peer with `pending` or `in-progress`
// requests older than TriageStaleDays, that count and the days since its
// latest triage report. "" when nothing is stale. Closes nothing.
func TriageDoctorFinding(a *Agent, now time.Time) string {
	reqs, _ := ListRequests(a.InboxPath())
	stale := 0
	for _, r := range reqs {
		if TriageOpen(r.Status) && requestAgeDays(r, now) > TriageStaleDays {
			stale++
		}
	}
	if stale == 0 {
		return ""
	}
	noun := "requests"
	if stale == 1 {
		noun = "request"
	}
	since := "never triaged"
	if p, ok := LatestTriageReport(a.Dir()); ok {
		d := strings.TrimSuffix(filepath.Base(p), ".md")
		since = fmt.Sprintf("last triage report %dd ago", requestAgeDays(&Request{Created: d}, now))
	}
	return fmt.Sprintf("%d open %s older than %dd (pending + in-progress); %s — run `meshctl inbox triage --peer %s` in an interactive session",
		stale, noun, TriageStaleDays, since, a.Name)
}
