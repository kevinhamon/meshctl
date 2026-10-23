package mesh

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// triage.go — the intake triage pass, worksheet half (ADR-0072; spec
// decisions 55, 56). `meshctl inbox triage` without a peer, from a hub,
// prints pool-wide open counts and a walk order (oldest stale first) and
// judges nothing. With a peer it gathers, per `pending` or `in-progress`
// request, the cheap pool-local signals an agent needs to judge whether the
// request still stands, and writes them as a worksheet under the peer's
// runtime memory. meshctl lists; the agent judges and writes the triage
// report (ticket 46). Nothing outside the pool is queried.

const (
	// TriageDir holds a peer's triage worksheets and reports:
	// <peer>/.memory/triage/. Runtime state, never committed.
	TriageDir = "triage"
	// TriageWorksheetExt suffixes a dated worksheet (<date>.worksheet.md); the
	// report beside it is <date>.md (TriageReportPath).
	TriageWorksheetExt = ".worksheet.md"
	// TriageStaleDays is the age past which an open request is marked stale.
	TriageStaleDays = 14
	// GateCheckTriage tags gate decisions on `meshctl inbox triage`.
	GateCheckTriage = "triage"
)

// TriageWorksheetPath is <agentDir>/.memory/triage/<date>.worksheet.md.
func TriageWorksheetPath(agentDir string, now time.Time) string {
	return filepath.Join(TriageDirPath(agentDir), reconcileDate(now)+TriageWorksheetExt)
}

// TriageDirPath is <peer>/.memory/triage (ReconcileDir's counterpart).
func TriageDirPath(agentDir string) string {
	return filepath.Join(agentDir, MemoryDir, TriageDir)
}

// TriageReportPath is <agentDir>/.memory/triage/<date>.md, where the agent
// writes the triage report for that day's pass.
func TriageReportPath(agentDir string, now time.Time) string {
	return filepath.Join(TriageDirPath(agentDir), reconcileDate(now)+".md")
}

// TriageOpen reports whether a request status is in the triage pass:
// `pending` or `in-progress`. `needs-info` and `needs-human` wait on the
// requester and are theirs to triage from read-back.
func TriageOpen(status string) bool {
	s, _ := CanonicalStatus(status)
	return s == StatusPending || s == StatusInProgress
}

// --- refusal (cross-cutting decision 68) ---

// TriageMode is the in-session mode of the calling session, as the hook
// flags carry it: InSession with Work is a hub doing the peer's delegated
// work (allowed); InSession alone is a consult (refused). The CLI, which
// cannot see those flags, passes the zero value and the gate covers consult.
type TriageMode struct {
	InSession bool
	Work      bool
}

// TriageRefusal returns why the triage pass may not run here, "" when it
// may: it runs only where the human is — the owner's own interactive session
// or a hub session doing that peer's work — never under dispatch depth, a
// distil run, or a consult.
func TriageRefusal(m TriageMode) string {
	cause := ""
	switch {
	case envInt("AGENT_DISPATCH_DEPTH", 0) != 0:
		cause = "dispatch depth " + strconv.Itoa(envInt("AGENT_DISPATCH_DEPTH", 0))
	case DistillingNow():
		cause = "a distil run (" + distillGuardEnv + "=1)"
	case m.InSession && !m.Work:
		cause = "consult mode in-session (a consult is read-only)"
	}
	if cause == "" {
		return ""
	}
	return "mesh: `meshctl inbox triage` runs only where the human is — the owner's own interactive session, " +
		"or a hub session doing that peer's work — and is refused under " + cause + "."
}

// gateTriage denies a consulted in-session peer invoking `meshctl inbox
// triage` (any subcommand): the worksheet and apply step need the human.
func gateTriage(p HookPayload) GateDecision {
	if !p.InSession || p.Work || !ShellInvokes(shellCommandInput(p), "meshctl inbox triage") {
		return GateDecision{}
	}
	return GateDecision{Deny: true, Check: GateCheckTriage, Reason: TriageRefusal(TriageMode{InSession: true})}
}

// InboxTriage is the `meshctl inbox triage` command: with peer nil the walk
// order (writes nothing), else the peer's worksheet, written and returned as
// text. Refused with TriageRefusal's reason where no human is at the prompt.
func InboxTriage(agentsDir string, peer *Agent, mode TriageMode, now time.Time) (string, error) {
	if r := TriageRefusal(mode); r != "" {
		return "", fmt.Errorf("%s", r)
	}
	if peer == nil {
		walk, err := TriageWalkOrder(agentsDir, now)
		if err != nil {
			return "", err
		}
		return TriageWalkMarkdown(walk), nil
	}
	ws, err := WriteTriageWorksheet(agentsDir, peer, mode, now)
	if err != nil {
		return "", err
	}
	return ws.Markdown(), nil
}

// --- walk order (hub, no peer) ---

// TriageWalkEntry is one peer's line in the hub walk order.
type TriageWalkEntry struct {
	Peer       string
	Open       int    // pending + in-progress requests
	Stale      int    // of those, older than TriageStaleDays
	Oldest     string // created date of the oldest open request, "" when none
	OldestDays int
}

// TriageWalkOrder counts every peer's open requests, ordered oldest open
// request first (peers with none last, by name). Reads only; judges nothing.
func TriageWalkOrder(agentsDir string, now time.Time) ([]TriageWalkEntry, error) {
	agents, _, err := ScanAgents(agentsDir)
	if err != nil {
		return nil, err
	}
	var out []TriageWalkEntry
	for _, a := range agents {
		e := TriageWalkEntry{Peer: a.Name, OldestDays: -1}
		reqs, _ := ListRequests(a.InboxPath())
		for _, r := range reqs {
			if !TriageOpen(r.Status) {
				continue
			}
			e.Open++
			d := requestAgeDays(r, now)
			if d > TriageStaleDays {
				e.Stale++
			}
			if d > e.OldestDays {
				e.OldestDays, e.Oldest = d, r.Created
			}
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OldestDays != out[j].OldestDays {
			return out[i].OldestDays > out[j].OldestDays
		}
		return out[i].Peer < out[j].Peer
	})
	return out, nil
}

// TriageWalkMarkdown renders the walk order.
func TriageWalkMarkdown(walk []TriageWalkEntry) string {
	var b strings.Builder
	total := 0
	for _, e := range walk {
		total += e.Open
	}
	fmt.Fprintf(&b, "# Intake triage walk order (%d open requests, oldest stale first)\n\n", total)
	for i, e := range walk {
		if e.Open == 0 {
			fmt.Fprintf(&b, "%d. %s · 0 open\n", i+1, e.Peer)
			continue
		}
		line := fmt.Sprintf("%d. %s · %d open · oldest %dd (%s)", i+1, e.Peer, e.Open, e.OldestDays, e.Oldest)
		if e.Stale > 0 {
			line += fmt.Sprintf(" ⚠ stale (%d past %dd)", e.Stale, TriageStaleDays)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nNothing is judged here. Triage a peer in its own session (or a hub doing its work): `meshctl inbox triage --peer <name>`.\n")
	return b.String()
}

func requestAgeDays(r *Request, now time.Time) int {
	c, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(r.Created), now.Location())
	if err != nil {
		return 0
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	if n := int(today.Sub(c).Hours() / 24); n > 0 {
		return n
	}
	return 0
}

// --- worksheet ---

// TriageWorkBranch is a `mesh/work/<id>` branch naming the request id or one
// of its related ids, in any peer's repo.
type TriageWorkBranch struct {
	Peer   string
	Branch string
	State  string // "live" (branch exists) or "merged" (landed and removed)
}

// TriageKnowledgeHit points at a Confirm: commit (Ref `<peer>@<sha>`) or a
// knowledge entry (Ref `mesh://<peer>/<path>[#<slug>]`) naming an id.
type TriageKnowledgeHit struct {
	Ref  string
	Text string // the commit subject or the matching line
}

// TriageSibling is a closed request from the same requester that shares a
// related id or has the same type and a similar ask. A candidate only.
type TriageSibling struct {
	Ref    string // <peer>:<id>
	Status string
	Why    string // "related <id>" / "same type, similar ask"
}

// TriageMention is a line of the requester's handoff or state digest naming
// the id or several words of the ask.
type TriageMention struct {
	Source string // "handoff" or "state"
	Line   string
}

// TriageItem is one open request with its signal groups.
type TriageItem struct {
	Request      *Request
	AgeDays      int
	WorkBranches []TriageWorkBranch
	Knowledge    []TriageKnowledgeHit
	Siblings     []TriageSibling
	Redirects    []string // `a → b → c (status)` chains ending in a closed request
	Mentions     []TriageMention
	// RequesterPeer is false when the requester is not a pool peer (`user`),
	// so there is no handoff or state to read.
	RequesterPeer bool
}

// SignalHash digests the item's signal groups (not its age), so a later pass
// can tell whether anything changed since the last verdict.
func (it TriageItem) SignalHash() string {
	var b strings.Builder
	for _, w := range it.WorkBranches {
		fmt.Fprintf(&b, "w %s %s %s\n", w.Peer, w.Branch, w.State)
	}
	for _, k := range it.Knowledge {
		fmt.Fprintf(&b, "k %s %s\n", k.Ref, k.Text)
	}
	for _, s := range it.Siblings {
		fmt.Fprintf(&b, "s %s %s\n", s.Ref, s.Status)
	}
	for _, r := range it.Redirects {
		fmt.Fprintf(&b, "r %s\n", r)
	}
	for _, m := range it.Mentions {
		fmt.Fprintf(&b, "m %s %s\n", m.Source, m.Line)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TriageWorksheet is a peer's worksheet for one pass.
type TriageWorksheet struct {
	Peer  string
	Date  string
	Path  string // where WriteTriageWorksheet wrote it
	Items []TriageItem
	// Skipped are open requests whose latest report line is `stands` and
	// whose signals hash unchanged; listed (with the hash) so a re-run on the
	// same day still knows them.
	Skipped []TriageSkip
}

// TriageSkip is an open request a pass skipped (incremental).
type TriageSkip struct {
	ID   string
	Hash string
}

// WriteTriageWorksheet builds the peer's worksheet and writes it at
// TriageWorksheetPath. Refused (nothing written) where TriageRefusal says so.
func WriteTriageWorksheet(agentsDir string, peer *Agent, mode TriageMode, now time.Time) (*TriageWorksheet, error) {
	if r := TriageRefusal(mode); r != "" {
		return nil, fmt.Errorf("%s", r)
	}
	ws, err := BuildTriageWorksheet(agentsDir, peer, now)
	if err != nil {
		return nil, err
	}
	ws.Path = TriageWorksheetPath(peer.Dir(), now)
	if err := os.MkdirAll(filepath.Dir(ws.Path), 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(ws.Path, []byte(ws.Markdown()), 0o644); err != nil {
		return nil, err
	}
	return ws, nil
}

// BuildTriageWorksheet gathers the signals for every open request of peer,
// oldest first. Reads only.
func BuildTriageWorksheet(agentsDir string, peer *Agent, now time.Time) (*TriageWorksheet, error) {
	reqs, err := ListRequests(peer.InboxPath())
	if err != nil {
		return nil, err
	}
	ws := &TriageWorksheet{Peer: peer.Name, Date: now.Format("2006-01-02")}
	var open []*Request
	for _, r := range reqs {
		if TriageOpen(r.Status) {
			open = append(open, r)
		}
	}
	if len(open) == 0 {
		return ws, nil
	}
	pool := loadTriageSnapshot(agentsDir, peer)
	sort.SliceStable(open, func(i, j int) bool {
		if open[i].Created != open[j].Created {
			return open[i].Created < open[j].Created
		}
		return open[i].ID < open[j].ID
	})
	prior := TriagePriorStands(peer.Dir())
	for _, r := range open {
		ids := triageIDs(r)
		it := TriageItem{Request: r, AgeDays: requestAgeDays(r, now)}
		it.WorkBranches = pool.workBranches(ids)
		it.Knowledge = pool.knowledge(ids)
		it.Siblings = pool.siblings(r)
		it.Redirects = pool.redirects(r)
		it.Mentions, it.RequesterPeer = pool.mentions(r)
		// Incremental (spec decision 62): latest verdict `stands` and the
		// signals unchanged since that pass — nothing new to judge.
		if h := it.SignalHash(); prior[r.ID] != "" && prior[r.ID] == h {
			ws.Skipped = append(ws.Skipped, TriageSkip{ID: r.ID, Hash: h})
			continue
		}
		ws.Items = append(ws.Items, it)
	}
	return ws, nil
}

// Markdown renders the worksheet: a header naming the report to write, then
// one section per request with every signal group, empty ones labelled.
func (w *TriageWorksheet) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Triage worksheet — %s, %s\n\n", w.Peer, w.Date)
	fmt.Fprintf(&b, "%d open requests (pending + in-progress). meshctl gathered the pool-local signals below; it judges nothing. "+
		"Judge each request and write the triage report at `.memory/%s/%s.md`, one line per request:\n"+
		"`- <id> · <stands | superseded → <ref> | done → <ref> | withdrawn → <ref> | unclear> · \"<quoted grounding>\" · <source>`. "+
		"`superseded`, `done` and `withdrawn` need a ref; `stands` and `unclear` carry no ref. "+
		"Siblings are candidates only. When the signals are inconclusive and the requester is a peer, consult it in-session once, "+
		"batching its requests.\n", len(w.Items), TriageDir, w.Date)
	for _, it := range w.Items {
		r := it.Request
		fmt.Fprintf(&b, "\n## %s\n\n", r.ID)
		stale := ""
		if it.AgeDays > TriageStaleDays {
			stale = " ⚠ stale"
		}
		fmt.Fprintf(&b, "- requester %s · type %s · priority %s · status %s · age %dd%s\n", r.From, r.Type, r.Priority, r.Status, it.AgeDays, stale)
		meta := "- created " + r.Created
		if len(r.Related) > 0 {
			meta += " · related: " + strings.Join(r.Related, ", ")
		}
		if r.Mode != "" {
			meta += " · mode " + r.Mode
		}
		b.WriteString(meta + "\n")
		fmt.Fprintf(&b, "- ask: %s\n", askGist(r.Body))
		fmt.Fprintf(&b, "- signals: %s\n", it.SignalHash())

		var lines []string
		for _, x := range it.WorkBranches {
			lines = append(lines, fmt.Sprintf("%s@%s (%s)", x.Peer, x.Branch, x.State))
		}
		writeGroup(&b, "Work branches", lines, "(none)")
		lines = nil
		for _, x := range it.Knowledge {
			lines = append(lines, x.Ref+" — "+x.Text)
		}
		writeGroup(&b, "Knowledge", lines, "(none)")
		lines = nil
		for _, x := range it.Siblings {
			lines = append(lines, fmt.Sprintf("%s (%s; %s)", x.Ref, x.Status, x.Why))
		}
		writeGroup(&b, "Siblings", lines, "(none)")
		writeGroup(&b, "Redirects", it.Redirects, "(none)")
		lines = nil
		for _, x := range it.Mentions {
			lines = append(lines, x.Source+": "+x.Line)
		}
		empty := "(none)"
		if !it.RequesterPeer {
			empty = "(none — requester " + r.From + " is not a peer; no handoff or state to read)"
		}
		writeGroup(&b, "Requester mentions", lines, empty)
	}
	if len(w.Skipped) > 0 {
		b.WriteString("\n## Skipped\n\nLatest verdict `stands` and no signal changed since; nothing new to judge:\n")
		for _, s := range w.Skipped {
			fmt.Fprintf(&b, "- %s · %s · signals: %s\n", s.ID, triageSkippedNote, s.Hash)
		}
	}
	return b.String()
}

func writeGroup(b *strings.Builder, title string, lines []string, empty string) {
	if len(lines) == 0 {
		fmt.Fprintf(b, "\n%s: %s\n", title, empty)
		return
	}
	fmt.Fprintf(b, "\n%s:\n", title)
	for _, l := range lines {
		fmt.Fprintf(b, "- %s\n", l)
	}
}

// askGist is the request's ask section on one line, capped.
func askGist(body string) string {
	s := strings.Join(strings.Fields(askSection(body)), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// triageIDs are the ids a request is known by: its own and its related ids
// (the `<peer>:` prefix dropped; mesh:// and other URL refs skipped).
func triageIDs(r *Request) []string {
	ids := []string{r.ID}
	for _, rel := range r.Related {
		if id := relatedID(rel); id != "" && id != r.ID {
			ids = append(ids, id)
		}
	}
	return ids
}

func relatedID(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.Contains(rel, "://") {
		return ""
	}
	if i := strings.LastIndex(rel, ":"); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

// --- pool snapshot for one pass ---

type poolRequest struct {
	peer string
	req  *Request
}

type triageSnapshot struct {
	root     string
	self     *Agent
	agents   []*Agent
	requests []poolRequest           // live inboxes + archives, every peer
	byID     map[string]poolRequest  // first occurrence wins
	byRef    map[string]poolRequest  // <peer>:<id>, exact
	merged   map[string][]string     // peer -> work branches landed (reflog)
	live     map[string][]string     // peer -> work branches present
	kbFiles  map[string][]kbFileText // peer -> knowledge files read for this pass
}

type kbFileText struct {
	rel  string // knowledge-relative
	body string
}

// loadTriageSnapshot is loadTriageRequests plus the knowledge in scope: the
// full snapshot a worksheet reads.
func loadTriageSnapshot(root string, self *Agent) *triageSnapshot {
	p := loadTriageRequests(root, self)
	p.kbFiles = map[string][]kbFileText{}
	// Knowledge scope: the peer's own KB (every tier) and the canonical peers'
	// current and log tiers (candidates are proposals, never read across peers).
	p.kbFiles[self.Name] = readKnowledge(self, true)
	if peers, err := CanonicalPeers(root, LoadPoolConfig(root), self.Name); err == nil {
		for _, a := range peers {
			p.kbFiles[a.Name] = readKnowledge(a, false)
		}
	}
	return p
}

// loadTriageRequests is the snapshot without knowledge: every peer's live
// and archived requests and its work branches (live and merged). Enough for
// the work-branch and redirect signals (pendingTriageSignals).
func loadTriageRequests(root string, self *Agent) *triageSnapshot {
	p := &triageSnapshot{root: root, self: self, byID: map[string]poolRequest{}, byRef: map[string]poolRequest{},
		merged: map[string][]string{}, live: map[string][]string{}}
	p.agents, _, _ = ScanAgents(root)
	for _, a := range p.agents {
		for _, dir := range []string{a.InboxPath(), filepath.Join(a.Dir(), IntakeArchiveDir)} {
			reqs, _ := ListRequests(dir)
			for _, r := range reqs {
				pr := poolRequest{peer: a.Name, req: r}
				p.requests = append(p.requests, pr)
				p.byRef[a.Name+":"+r.ID] = pr
				if _, ok := p.byID[r.ID]; !ok {
					p.byID[r.ID] = pr
				}
			}
		}
	}
	p.live, p.merged = poolWorkBranchRefs(p.agents)
	return p
}

// poolWorkBranchRefs runs workBranchRefs for every peer repo concurrently: each
// costs a few git calls, and the pending block reads a whole pool's worth.
func poolWorkBranchRefs(agents []*Agent) (live, merged map[string][]string) {
	type refs struct{ live, merged []string }
	got := make([]refs, len(agents))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, a := range agents {
		if !isGitRepo(a.Dir()) {
			continue
		}
		wg.Add(1)
		go func(i int, dir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			got[i].live, got[i].merged = workBranchRefs(dir)
		}(i, a.Dir())
	}
	wg.Wait()
	live, merged = map[string][]string{}, map[string][]string{}
	for i, a := range agents {
		if isGitRepo(a.Dir()) {
			live[a.Name], merged[a.Name] = got[i].live, got[i].merged
		}
	}
	return live, merged
}

var reWorkMergeReflog = regexp.MustCompile(`^merge (` + regexp.QuoteMeta(WorkBranchPrefix) + `[^:\s]+)`)

// workBranchRefs lists a repo's mesh/work/* branches that exist, and those
// its main branch's reflog records as fast-forward merged (`work merge`
// lands a branch then deletes it, so the reflog is the only trace).
func workBranchRefs(dir string) (live, merged []string) {
	if live, merged, ok := workBranchRefFiles(filepath.Join(dir, ".git")); ok {
		return live, merged
	}
	if out, err := git(dir, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+WorkBranchPrefix); err == nil {
		for _, ln := range strings.Split(out, "\n") {
			if ln = strings.TrimSpace(ln); ln != "" {
				live = append(live, ln)
			}
		}
	}
	if out, err := git(dir, "reflog", "show", "--format=%gs", "refs/heads/"+mainBranch(dir), "--"); err == nil {
		seen := map[string]bool{}
		for _, ln := range strings.Split(out, "\n") {
			if m := reWorkMergeReflog.FindStringSubmatch(strings.TrimSpace(ln)); m != nil && !seen[m[1]] {
				seen[m[1]] = true
				merged = append(merged, m[1])
			}
		}
	}
	return live, merged
}

// workBranchRefFiles answers workBranchRefs from a plain .git directory's files
// (loose refs, packed-refs, the main branch's reflog) without launching git: the
// pending block asks it of every peer, and each git launch costs tens of
// milliseconds. ok is false when git must answer instead (a worktree's .git
// file, a reftable repo, no main/master branch).
func workBranchRefFiles(gitDir string) (live, merged []string, ok bool) {
	if !dirExists(gitDir) || dirExists(filepath.Join(gitDir, "reftable")) {
		return nil, nil, false
	}
	heads := map[string]bool{} // branch names, from loose and packed refs
	headsDir := filepath.Join(gitDir, "refs", "heads")
	_ = filepath.WalkDir(headsDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasSuffix(d.Name(), ".lock") {
			if rel, err := filepath.Rel(headsDir, p); err == nil {
				heads[filepath.ToSlash(rel)] = true
			}
		}
		return nil
	})
	if b, err := os.ReadFile(filepath.Join(gitDir, "packed-refs")); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			if f := strings.Fields(ln); len(f) == 2 && !strings.HasPrefix(ln, "#") && !strings.HasPrefix(ln, "^") {
				if name, ok := strings.CutPrefix(f[1], "refs/heads/"); ok {
					heads[name] = true
				}
			}
		}
	}
	for name := range heads {
		if strings.HasPrefix(name, WorkBranchPrefix) {
			live = append(live, name)
		}
	}
	sort.Strings(live) // for-each-ref order
	main := ""
	for _, b := range []string{"main", "master"} {
		if heads[b] {
			main = b
			break
		}
	}
	if main == "" {
		return nil, nil, false
	}
	b, err := os.ReadFile(filepath.Join(gitDir, "logs", "refs", "heads", main))
	if err != nil {
		return live, nil, true // no reflog: nothing recorded as merged
	}
	// Newest entry last on disk; `reflog show` lists newest first.
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	seen := map[string]bool{}
	for i := len(lines) - 1; i >= 0; i-- {
		_, msg, found := strings.Cut(lines[i], "\t")
		if !found {
			continue
		}
		if m := reWorkMergeReflog.FindStringSubmatch(strings.TrimSpace(msg)); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			merged = append(merged, m[1])
		}
	}
	return live, merged, true
}

func readKnowledge(a *Agent, own bool) []kbFileText {
	base := filepath.Join(a.Dir(), KnowledgeDir)
	var out []kbFileText
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != base && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		rel = filepath.ToSlash(rel)
		if rel == HandoffFile || rel == StateFile {
			return nil // the requester-mentions signal reads these
		}
		if !own && kbTier(rel) == TierInbox {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil {
			out = append(out, kbFileText{rel: rel, body: string(b)})
		}
		return nil
	})
	return out
}

func (p *triageSnapshot) workBranches(ids []string) []TriageWorkBranch {
	want := map[string]bool{}
	for _, id := range ids {
		want[WorkBranchPrefix+id] = true
	}
	var out []TriageWorkBranch
	for _, a := range p.agents {
		present := map[string]bool{}
		for _, br := range p.live[a.Name] {
			present[br] = true
			if want[br] {
				out = append(out, TriageWorkBranch{Peer: a.Name, Branch: br, State: "live"})
			}
		}
		for _, br := range p.merged[a.Name] {
			if want[br] && !present[br] {
				out = append(out, TriageWorkBranch{Peer: a.Name, Branch: br, State: "merged"})
			}
		}
	}
	return out
}

func (p *triageSnapshot) knowledge(ids []string) []TriageKnowledgeHit {
	var out []TriageKnowledgeHit
	// Confirm: commits on the peer's own main.
	if dir := p.self.Dir(); isGitRepo(dir) {
		if log, err := git(dir, "log", mainBranch(dir), "--format=%h%x1f%s%x1f%b%x1e"); err == nil {
			for _, rec := range strings.Split(log, "\x1e") {
				f := strings.SplitN(strings.TrimSpace(rec), "\x1f", 3)
				if len(f) < 2 || !strings.HasPrefix(f[1], ConfirmSubjectPrefix) {
					continue
				}
				if containsAny(strings.Join(f[1:], "\n"), ids) {
					out = append(out, TriageKnowledgeHit{Ref: p.self.Name + "@" + f[0], Text: f[1]})
				}
			}
		}
	}
	// Knowledge entry bodies: own first, then canonical peers by name.
	owners := []string{p.self.Name}
	for name := range p.kbFiles {
		if name != p.self.Name {
			owners = append(owners, name)
		}
	}
	sort.Strings(owners[1:])
	for _, owner := range owners {
		for _, f := range p.kbFiles[owner] {
			slug := ""
			for _, ln := range strings.Split(f.body, "\n") {
				if m := reMDHeading.FindStringSubmatch(ln); m != nil {
					slug = KebabSlug(m[1])
					continue
				}
				if containsAny(ln, ids) {
					ref := MeshRef{Owner: owner, Path: f.rel, Slug: slug}.String()
					out = append(out, TriageKnowledgeHit{Ref: ref, Text: strings.TrimSpace(ln)})
				}
			}
		}
	}
	return out
}

func containsAny(s string, ids []string) bool {
	for _, id := range ids {
		if id != "" && strings.Contains(s, id) {
			return true
		}
	}
	return false
}

func (p *triageSnapshot) siblings(r *Request) []TriageSibling {
	rel := map[string]bool{}
	for _, x := range r.Related {
		rel[strings.TrimSpace(x)] = true
		if id := relatedID(x); id != "" {
			rel[id] = true
		}
	}
	words := askWords(r.Body)
	var out []TriageSibling
	for _, pr := range p.requests {
		s := pr.req
		if s.ID == r.ID || s.From != r.From || !StatusClosed(canonicalOr(s.Status)) {
			continue
		}
		why := ""
		for _, x := range s.Related {
			if rel[strings.TrimSpace(x)] || rel[relatedID(x)] {
				why = "related " + strings.TrimSpace(x)
				break
			}
		}
		if why == "" && (rel[s.ID] || containsAny(strings.Join(s.Related, " "), []string{r.ID})) {
			why = "related " + s.ID
		}
		if why == "" && s.Type == r.Type && similarAsk(words, askWords(s.Body)) {
			why = "same type, similar ask"
		}
		if why != "" {
			out = append(out, TriageSibling{Ref: pr.peer + ":" + s.ID, Status: canonicalOr(s.Status), Why: why})
		}
	}
	return out
}

func canonicalOr(s string) string {
	if c, ok := CanonicalStatus(s); ok {
		return c
	}
	return s
}

// lookup resolves a `<peer>:<id>` ref exactly, else a bare id.
func (p *triageSnapshot) lookup(ref string) (poolRequest, bool) {
	if pr, ok := p.byRef[strings.TrimSpace(ref)]; ok {
		return pr, true
	}
	pr, ok := p.byID[relatedID(ref)]
	return pr, ok
}

// redirects follows `redirected_to` from the request and from each request it
// names in related:, listing every chain whose last hop is closed.
func (p *triageSnapshot) redirects(r *Request) []string {
	starts := []poolRequest{{peer: p.self.Name, req: r}}
	for _, x := range r.Related {
		if relatedID(x) != "" {
			if pr, ok := p.lookup(x); ok && pr.req.ID != r.ID {
				starts = append(starts, pr)
			}
		}
	}
	var out []string
	seenChain := map[string]bool{}
	for _, st := range starts {
		chain := []string{st.peer + ":" + st.req.ID}
		cur := st.req
		visited := map[string]bool{chain[0]: true}
		for cur.RedirectedTo != "" {
			next, ok := p.lookup(cur.RedirectedTo)
			if !ok {
				cur = nil
				break
			}
			key := next.peer + ":" + next.req.ID
			if visited[key] {
				cur = nil
				break
			}
			visited[key] = true
			chain = append(chain, next.peer+":"+next.req.ID)
			cur = next.req
		}
		if cur == nil || len(chain) < 2 {
			continue
		}
		status := canonicalOr(cur.Status)
		if !StatusClosed(status) {
			continue
		}
		line := strings.Join(chain, " → ") + " (" + status + ")"
		if !seenChain[line] {
			seenChain[line] = true
			out = append(out, line)
		}
	}
	return out
}

// mentions reads the requester's handoff and state digest for lines naming
// the request (or a related) id or several words of the ask.
func (p *triageSnapshot) mentions(r *Request) ([]TriageMention, bool) {
	var req *Agent
	for _, a := range p.agents {
		if a.Name == r.From {
			req = a
		}
	}
	if req == nil {
		return nil, false
	}
	ids := triageIDs(r)
	words := askWords(r.Body)
	var out []TriageMention
	for _, src := range []struct{ name, path string }{{"handoff", HandoffPath(req)}, {"state", StatePath(req)}} {
		b, err := os.ReadFile(src.path)
		if err != nil {
			continue
		}
		n := 0
		for _, ln := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "<!--") || reMDHeading.MatchString(t) {
				continue
			}
			if containsAny(t, ids) || similarAsk(words, askWords(t)) {
				out = append(out, TriageMention{Source: src.name, Line: t})
				if n++; n >= 5 {
					break
				}
			}
		}
	}
	return out, true
}

var triageStopwords = map[string]bool{
	"about": true, "again": true, "also": true, "does": true, "from": true, "have": true, "into": true,
	"just": true, "more": true, "need": true, "needs": true, "please": true, "should": true, "some": true,
	"still": true, "than": true, "that": true, "their": true, "them": true, "then": true, "there": true,
	"these": true, "they": true, "this": true, "what": true, "when": true, "where": true, "which": true,
	"will": true, "with": true, "would": true, "your": true, "none": true, "supplied": true,
}

var reAskWord = regexp.MustCompile(`[a-z0-9][a-z0-9_-]*`)

// askWords are the distinctive words of a request's ask (or of a line): four
// letters or more, not a stopword.
func askWords(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range reAskWord.FindAllString(strings.ToLower(askSection(text)), -1) {
		if len(w) >= 4 && !triageStopwords[w] {
			out[w] = true
		}
	}
	return out
}

// similarAsk: at least two shared distinctive words (one when the ask has
// only one).
func similarAsk(a, b map[string]bool) bool {
	need := 2
	if len(a) < 2 {
		need = len(a)
	}
	if need == 0 {
		return false
	}
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return n >= need
}
