package mesh

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// reconcile.go — the reconciliation pass's walk order and worksheet.
// meshctl judges nothing here: it ranks canonical
// peers from pool facts and, for one peer, gathers the evidence an agent needs
// to write the reconcile report (kb_confirm.go validates that report and
// confirms in bulk). Perspective peers are never walked and never read.

// ReconcileDirName is the runtime directory under a peer's .memory/ holding
// worksheets and reconcile reports (never committed: .memory/ is ignored).
const ReconcileDirName = "reconcile"

// ReconcileWorksheetExt suffixes a dated worksheet: <date>.worksheet.md. The
// report beside it is <date>.md.
const ReconcileWorksheetExt = ".worksheet.md"

// ReconcileDir is <peer>/.memory/reconcile.
func ReconcileDir(agentDir string) string {
	return filepath.Join(agentDir, ".memory", ReconcileDirName)
}

func reconcileDate(now time.Time) string { return now.Format("2006-01-02") }

// ReconcileWorksheetPath is the dated worksheet path for now.
func ReconcileWorksheetPath(agentDir string, now time.Time) string {
	return filepath.Join(ReconcileDir(agentDir), reconcileDate(now)+ReconcileWorksheetExt)
}

// ReconcileReportPath is the dated reconcile report path for now — where the
// agent writes its verdicts beside the worksheet.
func ReconcileReportPath(agentDir string, now time.Time) string {
	return filepath.Join(ReconcileDir(agentDir), reconcileDate(now)+".md")
}

// reDatedReportName is a dated report's file name (<date>.md), never its
// <date>.worksheet.md; reconcile and triage reports share it.
var reDatedReportName = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.md$`)

// LatestReconcileReport is the newest dated report (<date>.md, worksheets
// excluded) in the peer's reconcile directory; ok=false when there is none.
func LatestReconcileReport(agentDir string) (path string, ok bool) {
	ents, err := os.ReadDir(ReconcileDir(agentDir))
	if err != nil {
		return "", false
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && reDatedReportName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", false
	}
	sort.Strings(names)
	return filepath.Join(ReconcileDir(agentDir), names[len(names)-1]), true
}

// reconcileReportConflicts counts the open conflicts of a report (entries
// whose latest verdict is `conflict`, ReconcileReport.OpenConflicts) and names
// the peers on the other side of them (a `mesh://<peer>/…` counterpart or
// `owner <peer>, pending`; `doctrine` counts but names no peer). Malformed
// lines are not counted.
func reconcileReportConflicts(path string) (n int, with []string) {
	r, err := ReadReconcileReport(path)
	if err != nil {
		return 0, nil
	}
	seen := map[string]bool{}
	for _, l := range r.openConflictLines() {
		peer := l.OwnerPending
		if peer == "" && l.Counterpart != ReconcileDoctrine {
			if ref, err := ParseMeshRef(l.Counterpart); err == nil && ref.Owner != ReservedOwner {
				peer = ref.Owner
			}
		}
		if peer != "" && !seen[peer] {
			seen[peer] = true
			with = append(with, peer)
		}
	}
	sort.Strings(with)
	return len(r.openConflictLines()), with
}

// ReconcilePeer is one canonical peer in the walk with the facts that ranked it.
type ReconcilePeer struct {
	Name          string   `json:"name"`
	Mutates       int      `json:"mutates"`        // declared `mutates` surfaces
	Conflicts     int      `json:"conflicts"`      // conflict lines in its latest reconcile report
	ConflictsWith []string `json:"conflicts_with"` // peers on the other side of those conflicts
	Inbound       int      `json:"inbound"`        // `mesh://` links into it from other canonical knowledge
	Unconfirmed   int      `json:"unconfirmed"`    // unconfirmed current-tier entries
	Pinned        bool     `json:"pinned"`         // placed by the pool config's reconcile.order
}

// ReconcileWalk is the peer walk order for a reconciliation pass.
type ReconcileWalk struct {
	Peers   []ReconcilePeer `json:"peers"`
	Skipped []string        `json:"skipped"` // perspective peers, never walked
}

// ReconcileWalkOrder computes the walk across the pool's canonical peers:
// more declared `mutates` surfaces first, then more conflicts in
// the latest reconcile report, then more inbound `mesh://` links, then more
// unconfirmed entries, then name. A peer's conflict counterparts are walked
// right after it (pairs interleaved). The pool config's `reconcile.order`
// overrides: its canonical peers come first in that order, the rest follow in
// computed order; perspective or unknown names in it are ignored. Read-only.
func ReconcileWalkOrder(root string) (ReconcileWalk, error) {
	cfg := LoadPoolConfig(root)
	agents, _, err := ScanAgents(root)
	if err != nil {
		return ReconcileWalk{}, err
	}
	var w ReconcileWalk
	var canon []*Agent
	for _, a := range agents {
		if IsCanonical(cfg, a) {
			canon = append(canon, a)
		} else {
			w.Skipped = append(w.Skipped, a.Name)
		}
	}
	sort.Strings(w.Skipped)
	inbound := inboundMeshLinks(root, canon)

	byName := map[string]ReconcilePeer{}
	var ranked []ReconcilePeer
	for _, a := range canon {
		p := ReconcilePeer{
			Name: a.Name, Mutates: len(a.Mutates), Inbound: inbound[a.Name],
			Unconfirmed: len(UnconfirmedCurrentEntries(a.Dir())),
		}
		if rp, ok := LatestReconcileReport(a.Dir()); ok {
			p.Conflicts, p.ConflictsWith = reconcileReportConflicts(rp)
		}
		byName[a.Name] = p
		ranked = append(ranked, p)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		switch {
		case a.Mutates != b.Mutates:
			return a.Mutates > b.Mutates
		case a.Conflicts != b.Conflicts:
			return a.Conflicts > b.Conflicts
		case a.Inbound != b.Inbound:
			return a.Inbound > b.Inbound
		case a.Unconfirmed != b.Unconfirmed:
			return a.Unconfirmed > b.Unconfirmed
		}
		return a.Name < b.Name
	})

	done := map[string]bool{}
	emit := func(p ReconcilePeer) {
		if !done[p.Name] {
			done[p.Name] = true
			w.Peers = append(w.Peers, p)
		}
	}
	for _, name := range cfg.Settings.Reconcile.Order {
		if p, ok := byName[name]; ok && !done[name] {
			p.Pinned = true
			emit(p)
		}
	}
	for _, p := range ranked {
		if done[p.Name] {
			continue
		}
		emit(p)
		for _, other := range p.ConflictsWith {
			if q, ok := byName[other]; ok {
				emit(q)
			}
		}
	}
	return w, nil
}

// inboundMeshLinks counts, per peer, the distinct (file, reference) pairs in
// other canonical peers' current and log tiers and in the pool glossary that
// point at it. Perspective knowledge is never read.
func inboundMeshLinks(root string, canon []*Agent) map[string]int {
	out := map[string]int{}
	count := func(from, body string) {
		for _, ref := range MeshRefsIn(body) {
			if r, err := ParseMeshRef(ref); err == nil && r.Owner != from && r.Owner != ReservedOwner {
				out[r.Owner]++
			}
		}
	}
	for _, a := range canon {
		for _, f := range knowledgeFiles(a.Dir()) {
			if kbTier(f) == TierInbox {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(a.Dir(), KnowledgeDir, filepath.FromSlash(f))); err == nil {
				count(a.Name, string(b))
			}
		}
	}
	if b, err := os.ReadFile(PoolGlossaryPath(root)); err == nil {
		count(ReservedOwner, string(b))
	}
	return out
}

// knowledgeFiles lists a peer's knowledge/**/*.md, knowledge-relative, sorted.
func knowledgeFiles(agentDir string) []string {
	kb := filepath.Join(agentDir, KnowledgeDir)
	var out []string
	_ = filepath.WalkDir(kb, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		r, _ := filepath.Rel(kb, p)
		out = append(out, filepath.ToSlash(r))
		return nil
	})
	sort.Strings(out)
	return out
}

// Markdown renders the walk, one peer per line with its ranking facts.
func (w ReconcileWalk) Markdown() string {
	var b strings.Builder
	for i, p := range w.Peers {
		conf := fmt.Sprintf("conflicts %d", p.Conflicts)
		if len(p.ConflictsWith) > 0 {
			conf += " (with " + strings.Join(p.ConflictsWith, ", ") + ")"
		}
		fmt.Fprintf(&b, "%d. %s — mutates %d · %s · inbound %d · unconfirmed %d", i+1, p.Name, p.Mutates, conf, p.Inbound, p.Unconfirmed)
		if p.Pinned {
			b.WriteString(" · pinned by reconcile.order")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ReconcileWalkRank is an entry's place in the in-peer walk: state,
// handoff, glossary, decisions, architecture and reference, playbooks,
// experience, then anything else.
func ReconcileWalkRank(rel string) int {
	rel = kbRelPath(rel)
	switch {
	case rel == StateFile:
		return 0
	case rel == HandoffFile:
		return 1
	case rel == PeerGlossaryFile:
		return 2
	case strings.HasPrefix(rel, "decisions/"):
		return 3
	case strings.HasPrefix(rel, "architecture/"), strings.HasPrefix(rel, "reference/"):
		return 4
	case strings.HasPrefix(rel, "playbooks/"):
		return 5
	case strings.HasPrefix(rel, "experience/"):
		return 6
	}
	return 7
}

// Citation kinds.
const (
	CitationMesh = "mesh"
	CitationPath = "path"
	CitationURL  = "url"
)

// Citation is one reference an entry carries, marked resolved or not. No
// content is verified and no URL is fetched.
type Citation struct {
	Kind     string `json:"kind"` // CitationMesh | CitationPath | CitationURL
	Ref      string `json:"ref"`  // as written
	Resolved bool   `json:"resolved"`
	Note     string `json:"note,omitempty"` // why unresolved
}

// ReconcilePassage is a doctrine or standing-order paragraph that shares
// terms with an entry.
type ReconcilePassage struct {
	Source string   `json:"source"` // pool-relative doctrine path, or the peer's CLAUDE.md
	Terms  []string `json:"terms"`  // the shared terms
	Text   string   `json:"text"`
}

// ReconcileEntry is one worksheet entry: a whole file, or one section of a
// sectioned file (state.md, glossary.md).
type ReconcileEntry struct {
	Rel       string             `json:"rel"`              // knowledge-relative
	Anchor    string             `json:"anchor,omitempty"` // section anchor of a sectioned file
	Tier      string             `json:"tier"`
	Confirmed bool               `json:"confirmed"`
	Body      string             `json:"body"`
	Hits      []RecallHit        `json:"hits"` // canonical-scope hits (other canonical peers)
	Citations []Citation         `json:"citations"`
	Passages  []ReconcilePassage `json:"passages"`
}

// ID is the entry's report address: path or path#anchor.
func (e ReconcileEntry) ID() string {
	if e.Anchor != "" {
		return e.Rel + "#" + e.Anchor
	}
	return e.Rel
}

// ReconcileWorksheet is the evidence for one peer's reconciliation pass.
type ReconcileWorksheet struct {
	Peer    string           `json:"peer"`
	Date    string           `json:"date"`
	Report  string           `json:"report"` // where the agent writes the report
	Entries []ReconcileEntry `json:"entries"`
	// Skipped are confirmed entries whose every pair is confirmed in both
	// ledgers and unchanged (incremental pass); not listed as entries.
	Skipped []string `json:"skipped,omitempty"`
}

// reconcilePeer loads a peer for reconciliation and refuses a perspective one.
func reconcilePeer(root, peer string) (*Agent, error) {
	a, err := FindAgent(root, peer)
	if err != nil {
		return nil, err
	}
	if !IsCanonical(LoadPoolConfig(root), a) {
		return nil, fmt.Errorf("%s is a perspective peer (knowledge: %s) — reconciliation reads and confirms canonical knowledge only; nothing to reconcile",
			peer, KnowledgePerspective)
	}
	return a, nil
}

// BuildReconcileWorksheet gathers, for a canonical peer, every current- and
// log-tier entry in walk order with its body, canonical-scope hits, citation
// marks and the doctrine or standing-order passages sharing its terms. A
// perspective peer is refused. Writes nothing.
func BuildReconcileWorksheet(root, peer string, now time.Time) (*ReconcileWorksheet, error) {
	a, err := reconcilePeer(root, peer)
	if err != nil {
		return nil, err
	}
	ws := &ReconcileWorksheet{Peer: a.Name, Date: reconcileDate(now), Report: ReconcileReportPath(a.Dir(), now)}
	kb := filepath.Join(a.Dir(), KnowledgeDir)
	inactive := inactiveKBFiles(kb)
	var files []string
	for _, rel := range knowledgeFiles(a.Dir()) {
		base := filepath.Base(rel)
		if inactive[rel] || kbTier(rel) == TierInbox || strings.EqualFold(base, "README.md") || base == "template.md" {
			continue
		}
		files = append(files, rel)
	}
	sort.SliceStable(files, func(i, j int) bool {
		ri, rj := ReconcileWalkRank(files[i]), ReconcileWalkRank(files[j])
		if ri != rj {
			return ri < rj
		}
		return files[i] < files[j]
	})

	passages := standingPassages(root, a)
	ledger := newLedgerCheck(a.Dir())
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(kb, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		var units []ReconcileEntry
		if LedgerSectioned(rel) {
			for _, s := range splitSections(string(b)) {
				units = append(units, ReconcileEntry{Rel: rel, Anchor: s.Anchor, Body: s.Text,
					Confirmed: ledger.sectionConfirmed(rel, s.Anchor)})
			}
		} else {
			_, body := splitFrontmatter(string(b))
			units = append(units, ReconcileEntry{Rel: rel, Body: strings.TrimSpace(body), Confirmed: ledger.confirmed(rel)})
		}
		for _, e := range units {
			e.Tier = kbTier(rel)
			terms := entryTerms(rel, e.Anchor, string(b), e.Body)
			if len(terms) > 0 {
				e.Hits, _ = CanonicalPeerHits(a, strings.Join(terms, " "))
			}
			// Incremental: a confirmed entry's pairs with
			// entries confirmed in their owners' ledgers were reconciled and
			// have not changed since; skip them, and skip the entry when no
			// open pair is left. An edit on either side unconfirms it.
			if e.Confirmed {
				open := e.Hits[:0:0]
				for _, h := range e.Hits {
					if !h.Confirmed {
						open = append(open, h)
					}
				}
				if len(open) == 0 {
					ws.Skipped = append(ws.Skipped, e.ID())
					continue
				}
				e.Hits = open
			}
			e.Citations = CheckCitations(root, a, rel, e.Body)
			e.Passages = matchPassages(passages, terms)
			ws.Entries = append(ws.Entries, e)
		}
	}
	return ws, nil
}

// KBReconcileWorksheet builds the worksheet for a canonical peer and writes it
// to ReconcileWorksheetPath, returning that path (`kb reconcile --peer`).
func KBReconcileWorksheet(root, peer string, now time.Time) (string, error) {
	ws, err := BuildReconcileWorksheet(root, peer, now)
	if err != nil {
		return "", err
	}
	a, err := FindAgent(root, peer)
	if err != nil {
		return "", err
	}
	path := ReconcileWorksheetPath(a.Dir(), now)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, []byte(ws.Markdown()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// reconcileStopTerms are tokens too generic to tie an entry to a passage.
var reconcileStopTerms = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true, "from": true,
	"are": true, "was": true, "not": true, "into": true, "its": true, "our": true, "you": true,
	"now": true, "active": true, "recently": true, "decided": true, "open": true, "questions": true,
	"state": true, "handoff": true, "glossary": true, "readme": true, "run": true, "notes": true,
	"preamble": true, "use": true, "how": true, "what": true, "why": true,
}

// entryTerms are an entry's identifying terms: its file stem (whole files),
// section heading, headings, frontmatter tags and `**Term**:` names, as
// lowercase tokens of three or more letters, generic words dropped.
func entryTerms(rel, anchor, file, body string) []string {
	var src []string
	if anchor == "" {
		src = append(src, strings.TrimSuffix(filepath.Base(rel), ".md"))
		src = append(src, frontMatterTags(file)...)
	}
	for _, m := range reMDHeading.FindAllStringSubmatch(body, -1) {
		src = append(src, m[1])
	}
	for _, m := range reTermLine.FindAllStringSubmatch(body, -1) {
		src = append(src, m[1])
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range src {
		for _, t := range queryTokens(s) {
			if !seen[t] && !reconcileStopTerms[t] && !isDigits(t) {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

type standingPassage struct {
	source string
	text   string
	tokens map[string]bool
}

// standingPassages splits pool doctrine (every *.md under the doctrine
// directory) and the peer's standing orders (CLAUDE.md) into paragraphs.
func standingPassages(root string, a *Agent) []standingPassage {
	type src struct{ label, path string }
	var srcs []src
	dd := DoctrinePath(root)
	if ents, err := os.ReadDir(dd); err == nil {
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				rel, _ := filepath.Rel(root, filepath.Join(dd, e.Name()))
				srcs = append(srcs, src{filepath.ToSlash(rel), filepath.Join(dd, e.Name())})
			}
		}
	}
	srcs = append(srcs, src{a.Name + "/CLAUDE.md (standing orders)", filepath.Join(a.Dir(), "CLAUDE.md")})
	var out []standingPassage
	for _, s := range srcs {
		b, err := os.ReadFile(s.path)
		if err != nil {
			continue
		}
		for _, para := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n\n") {
			para = strings.TrimSpace(para)
			if para == "" || reMDHeading.MatchString(para) && !strings.Contains(para, "\n") {
				continue
			}
			toks := map[string]bool{}
			for _, t := range queryTokens(para) {
				toks[t] = true
			}
			out = append(out, standingPassage{source: s.label, text: para, tokens: toks})
		}
	}
	return out
}

// ReconcilePassageCap bounds the passages listed per entry.
const ReconcilePassageCap = 5

const reconcilePassageMax = 600 // characters of a passage quoted in the worksheet

func matchPassages(ps []standingPassage, terms []string) []ReconcilePassage {
	var out []ReconcilePassage
	for _, p := range ps {
		var shared []string
		for _, t := range terms {
			if p.tokens[t] {
				shared = append(shared, t)
			}
		}
		if len(shared) == 0 {
			continue
		}
		text := p.text
		if len(text) > reconcilePassageMax {
			text = text[:reconcilePassageMax] + " …"
		}
		out = append(out, ReconcilePassage{Source: p.source, Terms: shared, Text: text})
		if len(out) == ReconcilePassageCap {
			break
		}
	}
	return out
}

var (
	reCiteURL       = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://[^\s<>"'` + "`" + `)\]]*`)
	reCiteLinkDest  = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	reCiteInline    = regexp.MustCompile("`([^`\\n]+)`")
	reCiteLineSuf   = regexp.MustCompile(`:\d+(:\d+)?$`)
	reCiteHostname  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)
	reCitePathBlock = regexp.MustCompile(`[\s<>*{}$|"']`)
)

// CheckCitations lists every reference an entry body carries — `mesh://`
// links, repository paths (inline code or link targets that look like file
// paths) and URLs — each marked resolved or unresolved. A `mesh://` link
// resolves when its target entry exists, a path when it exists relative to
// the peer's workspace, the entry's directory or a declared workdir (or as
// an absolute / ~ path), a URL when it is well formed. Nothing is fetched and
// no content is checked. Fenced code is an example, not a citation.
func CheckCitations(root string, a *Agent, rel, body string) []Citation {
	body = reFencedCode.ReplaceAllString(body, "")
	var out []Citation
	seen := map[string]bool{}
	add := func(c Citation) {
		if !seen[c.Ref] {
			seen[c.Ref] = true
			out = append(out, c)
		}
	}
	for _, ref := range MeshRefsIn(body) {
		_, ok, err := ResolveMeshRef(root, ref)
		c := Citation{Kind: CitationMesh, Ref: ref, Resolved: err == nil && ok}
		switch {
		case err != nil:
			c.Note = err.Error()
		case !ok:
			c.Note = "no such entry"
		}
		add(c)
	}
	for _, u := range reCiteURL.FindAllString(body, -1) {
		u = strings.TrimRight(u, ".,;:!?*_")
		if strings.HasPrefix(u, MeshRefScheme) {
			continue
		}
		c := Citation{Kind: CitationURL, Ref: u, Resolved: wellFormedURL(u)}
		if !c.Resolved {
			c.Note = "malformed URL"
		}
		add(c)
	}
	var cands []string
	for _, m := range reCiteLinkDest.FindAllStringSubmatch(body, -1) {
		cands = append(cands, m[1])
	}
	for _, m := range reCiteInline.FindAllStringSubmatch(body, -1) {
		cands = append(cands, m[1])
	}
	entryDir := filepath.Dir(filepath.Join(a.Dir(), KnowledgeDir, filepath.FromSlash(rel)))
	for _, cand := range cands {
		p, ok := citationPath(cand)
		if !ok || seen[cand] {
			continue
		}
		c := Citation{Kind: CitationPath, Ref: cand, Resolved: pathExistsFrom(p, a, entryDir)}
		if !c.Resolved {
			c.Note = "not found in the workspace, beside the entry, or in a workdir"
		}
		add(c)
	}
	return out
}

// citationPath reports whether s looks like a repository path and returns it
// without a :line suffix or #fragment.
func citationPath(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "://") || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "-") ||
		strings.HasPrefix(s, "mailto:") || reCitePathBlock.MatchString(s) {
		return "", false
	}
	if i := strings.Index(s, "#"); i >= 0 {
		s = s[:i]
	}
	s = reCiteLineSuf.ReplaceAllString(s, "")
	if !strings.Contains(s, "/") {
		return "", false
	}
	prefixed := strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")
	last := s[strings.LastIndex(strings.TrimSuffix(s, "/"), "/")+1:]
	if !prefixed && !strings.Contains(last, ".") && !strings.HasSuffix(s, "/") {
		return "", false
	}
	return s, true
}

func pathExistsFrom(p string, a *Agent, entryDir string) bool {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		return fileExists(filepath.Join(home, p[2:]))
	}
	if filepath.IsAbs(p) {
		return fileExists(p)
	}
	bases := append([]string{a.Dir(), entryDir}, a.WorkdirPaths()...)
	for _, b := range bases {
		if fileExists(filepath.Join(b, filepath.FromSlash(p))) {
			return true
		}
	}
	return false
}

// wellFormedURL: parses, has a scheme and a syntactically valid host.
func wellFormedURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	return reCiteHostname.MatchString(u.Hostname())
}

// Markdown renders the worksheet: instructions, then one section per entry.
func (ws *ReconcileWorksheet) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Reconcile worksheet — %s (%s)\n\n", ws.Peer, ws.Date)
	fmt.Fprintf(&b, "Evidence for the reconciliation pass. meshctl judged nothing: read each entry against its "+
		"canonical hits, citations and doctrine or standing-order passages, then write the reconcile report at `%s`, "+
		"an optional `## Standing orders` section first, then one line per entry:\n\n", ws.Report)
	b.WriteString("    - <kb-relative path>[#<slug>] · <verdict>[ → <mesh://… | doctrine | owner <peer>, pending>] · \"<quoted grounding>\"\n\n")
	b.WriteString("Verdicts: consistent, conflict, judgment, ungrounded, term-vs-fact. Citations below are resolve checks only — no content was verified, no URL fetched.\n\n")
	fmt.Fprintf(&b, "%d entries, in walk order: state, handoff, glossary, decisions, architecture and reference, playbooks, experience.\n", len(ws.Entries))
	if len(ws.Skipped) > 0 {
		fmt.Fprintf(&b, "%d confirmed entries skipped (every pair confirmed in both ledgers and unchanged): %s\n", len(ws.Skipped), groupList(ws.Skipped, 6))
	}
	for _, e := range ws.Entries {
		status := "unconfirmed"
		if e.Confirmed {
			status = "confirmed"
		}
		fmt.Fprintf(&b, "\n## %s · %s · %s\n\n", e.ID(), e.Tier, status)
		if strings.TrimSpace(e.Body) == "" {
			b.WriteString("> (empty)\n")
		}
		for _, ln := range strings.Split(strings.TrimSpace(e.Body), "\n") {
			if strings.TrimSpace(e.Body) == "" {
				break
			}
			b.WriteString(strings.TrimRight("> "+ln, " ") + "\n")
		}
		b.WriteString("\n### Canonical hits\n\n")
		if len(e.Hits) == 0 {
			b.WriteString("- none\n")
		}
		for _, h := range e.Hits {
			c := "unconfirmed"
			if h.Confirmed {
				c = "confirmed"
			}
			fmt.Fprintf(&b, "- %s — %s (via %s, %s, %s)\n", strings.Join(h.Where, "  "), h.Title, h.Via, h.Tier, c)
		}
		b.WriteString("\n### Citations\n\n")
		if len(e.Citations) == 0 {
			b.WriteString("- none\n")
		}
		for _, c := range e.Citations {
			mark := "resolved"
			if !c.Resolved {
				mark = "unresolved"
			}
			fmt.Fprintf(&b, "- %s · %s (%s", mark, c.Ref, c.Kind)
			if c.Note != "" {
				b.WriteString(" — " + c.Note)
			}
			b.WriteString(")\n")
		}
		b.WriteString("\n### Doctrine and standing orders\n\n")
		if len(e.Passages) == 0 {
			b.WriteString("- none\n")
		}
		for _, p := range e.Passages {
			fmt.Fprintf(&b, "- %s (shares: %s)\n", p.Source, strings.Join(p.Terms, ", "))
			for _, ln := range strings.Split(p.Text, "\n") {
				b.WriteString(strings.TrimRight("  > "+ln, " ") + "\n")
			}
		}
	}
	return b.String()
}
