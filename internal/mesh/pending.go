package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// pending.go — the pending-work block.
//
// One renderer computes a peer's pending work: what waits on the human, how
// old it is and the command that clears it. The session-start hook injects it
// in the peer's own interactive session; `meshctl pending` prints the same
// text. Every kind is one directory listing or one hash pass over the peer's
// own knowledge base, plus the two cheap triage signals on the intake line.
// Nothing is configurable and nothing snoozes.

// PendingStaleDays is the one age threshold: an item older than this renders
// `⚠ stale`, for intake, KB branches, work branches and candidates alike, and
// doctor warns on KB and work branches past it.
const PendingStaleDays = 14

// BranchMaxAge is doctor's branch-age warning for KB and delegated-work
// branches and worktrees (PendingStaleDays).
const BranchMaxAge = PendingStaleDays * 24 * time.Hour

// pendingInline is how many items a line lists inline before it collapses to
// a count, the oldest age and the list command.
const pendingInline = 3

// PendingKind is one line of the block, in render order.
type PendingKind int

const (
	PendingKBBranches PendingKind = iota // KB branches parked needs-confirm
	PendingConflicts                     // open conflicts in the latest reconcile report
	PendingIntake                        // the peer's own `pending` requests
	PendingReadBack                      // answers awaiting read-back on requests it sent
	PendingDelegated                     // delegated work branches awaiting merge
	PendingHealth                        // KB health: unconfirmed, candidates
)

// PendingItem is one inline item: an id, its requester/branch/detail, its age.
type PendingItem struct {
	ID      string
	Who     string // "from <requester>", "→ <peer>: <status>", "summary <path>", ...
	AgeDays int
}

// PendingLine is one kind's line.
type PendingLine struct {
	Kind        PendingKind
	Label       string
	Count       int    // the items the line stands for (it adds to the header total)
	Noun        string // "2 pending", "1 answer", ... ("" ⇒ the bare count)
	OldestDays  int
	Items       []PendingItem // oldest first
	Command     string        // the clearing command
	ListCommand string        // shown instead of items above pendingInline
	// Detail replaces count/oldest/items/commands (the health line).
	Detail string
	// TriageSignals counts open requests with a triage signal (intake only).
	TriageSignals int
	// Aged counts candidates past PendingStaleDays (health only).
	Aged int
	// Unconfirmed counts current-tier entries no confirmation has covered
	// (health only); the reconcile pass works them off.
	Unconfirmed int
}

// PendingBlock is a peer's pending work.
type PendingBlock struct {
	Agent string
	Lines []PendingLine
}

// Total is the header's item count.
func (b PendingBlock) Total() int {
	n := 0
	for _, l := range b.Lines {
		n += l.Count
	}
	return n
}

// Empty reports a block with nothing to render.
func (b PendingBlock) Empty() bool { return len(b.Lines) == 0 }

func pendingAge(days int) string {
	s := fmt.Sprintf("%dd", days)
	if days > PendingStaleDays {
		s += " ⚠ stale"
	}
	return s
}

// Markdown renders the block, "" when every kind is zero.
func (b PendingBlock) Markdown() string {
	if b.Empty() {
		return ""
	}
	var s strings.Builder
	fmt.Fprintf(&s, "# Pending work (%d items)\n\n", b.Total())
	for _, l := range b.Lines {
		s.WriteString(l.Markdown())
		s.WriteString("\n")
	}
	return strings.TrimRight(s.String(), "\n")
}

// Markdown renders one line.
func (l PendingLine) Markdown() string {
	var s strings.Builder
	fmt.Fprintf(&s, "- **%s**", l.Label)
	if l.Detail != "" {
		s.WriteString(" " + l.Detail)
		return s.String()
	}
	noun := l.Noun
	if noun == "" {
		noun = fmt.Sprint(l.Count)
	}
	fmt.Fprintf(&s, " %s, oldest %s", noun, pendingAge(l.OldestDays))
	if l.Count > pendingInline && l.ListCommand != "" {
		fmt.Fprintf(&s, " — list: `%s`", l.ListCommand)
	} else {
		var items []string
		for _, it := range l.Items {
			x := "`" + it.ID + "`"
			if it.Who != "" {
				x += " " + it.Who
			}
			items = append(items, x+" · "+pendingAge(it.AgeDays))
		}
		s.WriteString(": " + strings.Join(items, "; "))
	}
	if l.Command != "" {
		s.WriteString(" — " + l.Command)
	}
	if l.TriageSignals > 0 {
		fmt.Fprintf(&s, "; %d have triage signals, run meshctl inbox triage", l.TriageSignals)
	}
	return s.String()
}

// PendingBlockOmitted reports the audiences that get no block: dispatch depth
// set and non-zero, or a distil run. `agent brief`
// never renders it.
func PendingBlockOmitted() bool { return HeadlessNow() }

// PendingMarkdown is the block the session-start hook injects and `meshctl
// pending` prints for a peer, "" when nothing is pending.
func PendingMarkdown(agentsDir string, a *Agent, now time.Time) string {
	return Pending(agentsDir, a, now).Markdown()
}

// PendingAllMarkdown prints the block per peer across the pool, each under a
// `## <peer>` heading, skipping peers with nothing pending.
func PendingAllMarkdown(agentsDir string, now time.Time) (string, error) {
	agents, _, err := ScanAgents(agentsDir)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, a := range agents {
		if md := PendingMarkdown(agentsDir, a, now); md != "" {
			parts = append(parts, "## "+a.Name+"\n\n"+md)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// Pending computes a peer's pending work, kinds in the fixed order; a kind
// with nothing in it has no line.
func Pending(agentsDir string, a *Agent, now time.Time) PendingBlock {
	b := PendingBlock{Agent: a.Name}
	add := func(l *PendingLine) {
		if l != nil {
			b.Lines = append(b.Lines, *l)
		}
	}
	add(pendingKBBranches(agentsDir, a, now))
	report, hasReport := LatestReconcileReport(a.Dir())
	add(pendingConflicts(a, report, hasReport, now))
	add(pendingIntake(agentsDir, a, now))
	add(pendingReadBack(agentsDir, a, now))
	add(pendingDelegated(agentsDir, a, now))
	add(pendingHealth(a, report, hasReport, now))
	return b
}

func daysSince(t, now time.Time) int {
	if t.IsZero() {
		return 0
	}
	if d := int(now.Sub(t).Hours() / 24); d > 0 {
		return d
	}
	return 0
}

func dateDays(date string, now time.Time) int {
	d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), now.Location())
	if err != nil {
		return 0
	}
	y, m, dd := now.Date()
	if n := int(time.Date(y, m, dd, 0, 0, 0, 0, now.Location()).Sub(d).Hours() / 24); n > 0 {
		return n
	}
	return 0
}

// finishLine sorts items oldest first and fills count and oldest age.
func finishLine(l *PendingLine) *PendingLine {
	if len(l.Items) == 0 {
		return nil
	}
	sort.SliceStable(l.Items, func(i, j int) bool { return l.Items[i].AgeDays > l.Items[j].AgeDays })
	l.Count = len(l.Items)
	l.OldestDays = l.Items[0].AgeDays
	if len(l.Items) > pendingInline {
		l.Items = l.Items[:pendingInline]
	}
	return l
}

// PendingKBBranch is a KB branch parked for the human: its worktree exists,
// no live session holds it, and it promotes into the current or log tier.
type PendingKBBranch struct {
	Session     string
	Branch      string
	Worktree    string
	SummaryPath string
	Promoted    []string
	Modified    time.Time
}

// ParkedKBBranches lists a peer's KB branches awaiting confirmation (the
// `needs-confirm` parks), by session id: the park markers
// (ParkMarkerPath) whose worktree still exists and that no live session
// holds. An unheld branch that never parked is not listed.
func ParkedKBBranches(agentsDir string, a *Agent, now time.Time) []PendingKBBranch {
	var out []PendingKBBranch
	for _, m := range readParkMarkers(a.Dir()) {
		wt := worktreePath(agentsDir, a.Name, m.Session)
		if !dirExists(wt) || KBHolder(agentsDir, a, m.Session, now) != nil {
			continue
		}
		summary := m.Summary
		if summary == "" {
			summary = SummaryPath(a.Dir(), m.Session)
		}
		out = append(out, PendingKBBranch{Session: m.Session, Branch: m.Branch, Worktree: wt, SummaryPath: summary,
			Promoted: m.Promoted, Modified: m.Parked})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Session < out[j].Session })
	return out
}

func pendingKBBranches(agentsDir string, a *Agent, now time.Time) *PendingLine {
	parked := ParkedKBBranches(agentsDir, a, now)
	l := &PendingLine{Kind: PendingKBBranches, Label: "KB branches needs-confirm",
		Command: fmt.Sprintf("read the round out, then `meshctl kb finish --agent %s --session <session> --summary-file <summary>`", a.Name)}
	for _, p := range parked {
		l.Items = append(l.Items, PendingItem{ID: p.Branch, Who: "summary `" + p.SummaryPath + "`", AgeDays: daysSince(p.Modified, now)})
	}
	if len(parked) == 1 {
		n := NeedsConfirm{Agent: a.Name, Session: parked[0].Session, SummaryPath: parked[0].SummaryPath}
		l.Command = "read the round out, then `" + n.FinishCommand() + "`"
	}
	l.ListCommand = "meshctl pending"
	return finishLine(l)
}

func pendingConflicts(a *Agent, report string, ok bool, now time.Time) *PendingLine {
	if !ok {
		return nil
	}
	r, err := ReadReconcileReport(report)
	if err != nil {
		return nil
	}
	age := dateDays(strings.TrimSuffix(filepath.Base(report), ".md"), now)
	rel := ".memory/" + ReconcileDirName + "/" + filepath.Base(report)
	l := &PendingLine{Kind: PendingConflicts, Label: "Open conflicts",
		Command:     fmt.Sprintf("resolve each in the fact owner's session, re-verdict in `%s`, then `meshctl kb confirm <path> --report %s`", rel, report),
		ListCommand: "meshctl kb reconcile"}
	lines := r.openConflictLines()
	for i := len(lines) - 1; i >= 0; i-- { // report order
		who := "latest reconcile report"
		if lines[i].Target != "" {
			who = "→ " + lines[i].Target
		}
		l.Items = append(l.Items, PendingItem{ID: lines[i].ID(), Who: who, AgeDays: age})
	}
	return finishLine(l)
}

func pendingIntake(agentsDir string, a *Agent, now time.Time) *PendingLine {
	reqs, _ := ListRequests(a.InboxPath())
	var open []*Request
	l := &PendingLine{Kind: PendingIntake, Label: "Intake",
		Command: "show the queue (`meshctl inbox list`), propose an order and wait; then `meshctl inbox claim <id>` in the human's order", ListCommand: "meshctl inbox list"}
	for _, r := range reqs {
		if canonicalOr(r.Status) != StatusPending {
			continue
		}
		open = append(open, r)
		l.Items = append(l.Items, PendingItem{ID: r.ID, Who: "from " + r.From, AgeDays: requestAgeDays(r, now)})
	}
	if finishLine(l) == nil {
		return nil
	}
	l.Noun = fmt.Sprintf("%d pending", l.Count)
	l.TriageSignals = pendingTriageSignals(agentsDir, a, open)
	return l
}

// pendingTriageSignals counts open requests with one of the two exact, cheap
// triage signals: the request's id names a
// `mesh/work/<id>` branch, live or merged, in any peer's repo; or a
// `redirected_to` chain from the request or a request it names in related:
// ends in a closed request. The full worksheet stays in `inbox triage`.
func pendingTriageSignals(agentsDir string, a *Agent, open []*Request) int {
	if len(open) == 0 {
		return 0
	}
	p := loadTriageRequests(agentsDir, a)
	n := 0
	for _, r := range open {
		if len(p.workBranches([]string{r.ID})) > 0 || len(p.redirects(r)) > 0 {
			n++
		}
	}
	return n
}

func pendingReadBack(agentsDir string, a *Agent, now time.Time) *PendingLine {
	sent, err := ListSent(agentsDir, a.Name, true)
	if err != nil {
		return nil
	}
	l := &PendingLine{Kind: PendingReadBack, Label: "Read-back",
		Command:     "read each response (a `declined` one may name the right agent), then `meshctl sent ack <id>`",
		ListCommand: "meshctl sent"}
	for _, s := range sent {
		l.Items = append(l.Items, PendingItem{ID: s.ID, Who: "→ " + s.To + ": " + canonicalOr(s.Status), AgeDays: dateDays(s.Responded, now)})
	}
	return finishLine(l)
}

func pendingDelegated(agentsDir string, a *Agent, now time.Time) *PendingLine {
	ws := workWorktrees(agentsDir, a.Name)
	l := &PendingLine{Kind: PendingDelegated, Label: "Delegated work",
		Command:     "review, then `meshctl work merge <id>` (or `--abort`)",
		ListCommand: "meshctl work list --agent " + a.Name}
	for _, w := range ws {
		l.Items = append(l.Items, PendingItem{ID: w.Branch, AgeDays: daysSince(w.Modified, now)})
	}
	if len(ws) == 1 {
		l.Command = fmt.Sprintf("review, then `meshctl work merge %s` (or `--abort`)", ws[0].Request)
	}
	return finishLine(l)
}

// pendingHealth is the KB health line: only when candidates or a reconcile
// report exist, and only when it has something to count.
func pendingHealth(a *Agent, report string, hasReport bool, now time.Time) *PendingLine {
	cands := candidateFiles(a.Dir())
	if len(cands) == 0 && !hasReport {
		return nil
	}
	unconfirmed := len(UnconfirmedCurrentEntries(a.Dir()))
	if len(cands) == 0 && unconfirmed == 0 {
		return nil
	}
	aged := len(agedCandidates(a.Dir(), now))
	var parts []string
	parts = append(parts, fmt.Sprintf("%d candidates (%d past %d days)", len(cands), aged, PendingStaleDays))
	parts = append(parts, fmt.Sprintf("%d unconfirmed current entries", unconfirmed))
	if hasReport {
		parts = append(parts, "reconcile report "+fmt.Sprintf("%dd ago", dateDays(strings.TrimSuffix(filepath.Base(report), ".md"), now)))
	} else {
		parts = append(parts, "no reconcile report yet")
	}
	return &PendingLine{Kind: PendingHealth, Label: "KB health", Count: len(cands) + unconfirmed, Aged: aged, Unconfirmed: unconfirmed,
		Detail: strings.Join(parts, ", ") + " — `meshctl kb consolidate` (candidates), `meshctl kb reconcile` (unconfirmed)"}
}

// candidateFiles lists knowledge/candidates/*.md (README excluded).
func candidateFiles(agentDir string) []string {
	entries, err := os.ReadDir(filepath.Join(agentDir, KnowledgeDir, "candidates"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}
