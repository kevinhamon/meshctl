package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// gate_promotion_related.go — what the promotion ask shows beyond the peer's
// own summary (ADR-0065, ADR-0069; spec decisions 31 and 32). meshctl lists,
// the agent classifies, the human decides; the gate never judges meaning.
//
//   - Related canonical entries: when a canonical peer promotes, the canonical
//     scope runs over each promoted entry's topic and every cross-peer hit is
//     listed as a `mesh://` pointer plus its first line. The summary's
//     Coherence section classifies each as consistent, conflict or judgment;
//     the ask reproduces the word per entry, or says "coherence not stated"
//     when the section is missing. A conflict is still an ask, never a deny.
//   - Ownership routing: a promoted entry whose topic falls under another
//     canonical peer's `owns` tag gets a line naming that peer and suggesting
//     a candidate to the owner instead of a copy.
//   - Inbound links: a removed or renamed knowledge entry lists the `mesh://`
//     links to it found across the pool (read-only), so the human sees what
//     breaks. Nothing is rewritten; a dangling link is a doctor finding.
//
// Everything is computed for the finishing peer — its kind and its roster
// view — whoever runs the finish: a hub doing a peer's work in-session gets
// that peer's, with no exemption (ADR-0069).

// Caps that keep the ask near its 600–1200 character target.
const (
	promotionRelatedMax   = 3  // related entries shown
	promotionInboundMax   = 5  // inbound links shown per removed entry
	promotionFirstLineMax = 70 // characters of a related entry's first line
)

// CoherenceNotStated is the ask line when related entries exist and the
// summary has no Coherence section.
const CoherenceNotStated = "coherence not stated"

// reCoherenceWord finds the agent's classification in a Coherence line.
var reCoherenceWord = regexp.MustCompile(`(?i)\b(consistent|conflict|judgment)\b`)

// promotionStopwords are dropped from a promoted entry's topic query: they
// would match half the pool's topics.
var promotionStopwords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "from": true,
	"into": true, "this": true, "that": true, "are": true, "was": true, "not": true}

// kbBranchEntry is one changed path of a KB session branch (repo-relative).
type kbBranchEntry struct {
	status   byte   // A, M, D, R, …
	path     string // the path on the branch (the new name of a rename)
	oldPath  string // the old name of a rename
	isRename bool
}

// kbBranchEntries lists main...branch with rename detection (-M), for the
// ask's inbound-link lines, which must name a rename as one. KBBranchChanges
// (gate_promotion.go) diffs the same range with --no-renames on purpose: the
// ledger needs a rename as a deletion plus an addition. Change one, check the
// other.
func kbBranchEntries(agentDir, branch string) ([]kbBranchEntry, error) {
	out, err := git(agentDir, "diff", "--name-status", "-M", mainBranch(agentDir)+"..."+branch, "--")
	if err != nil {
		return nil, err
	}
	var es []kbBranchEntry
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(ln), "\t")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		e := kbBranchEntry{status: f[0][0], path: f[len(f)-1]}
		if e.status == 'R' && len(f) == 3 {
			e.oldPath, e.isRename = f[1], true
		}
		es = append(es, e)
	}
	return es, nil
}

// isKnowledgeEntry reports whether a repo-relative path is a current- or
// log-tier knowledge file (under knowledge/, not candidates/).
func isKnowledgeEntry(repoRel string) bool {
	r := filepath.ToSlash(repoRel)
	return strings.HasPrefix(r, KnowledgeDir+"/") && IsPromotedPath(r) && strings.HasSuffix(r, ".md")
}

// PromotionRelated renders the lines the promotion ask appends after its
// per-file blocks (PromotionAsk.Related) for peer a finishing branch: the
// Related canonical entries section with the Coherence classifications or
// "coherence not stated", the ownership lines, and the inbound links of
// removed or renamed entries. nil when there is nothing to show.
func PromotionRelated(root string, a *Agent, branch string, s Summary) []string {
	entries, err := kbBranchEntries(a.Dir(), branch)
	if err != nil {
		return nil
	}
	cfg := LoadPoolConfig(root)
	var lines []string
	if IsCanonical(cfg, a) {
		lines = append(lines, promotionRelatedLines(root, cfg, a, branch, entries, s)...)
	}
	lines = append(lines, promotionInboundLines(root, a, entries)...)
	return lines
}

// promotionRelatedLines is the canonical-only part: related entries and
// ownership routing.
func promotionRelatedLines(root string, cfg PoolConfig, a *Agent, branch string, entries []kbBranchEntry, s Summary) []string {
	peers, _ := CanonicalPeers(root, cfg, a.Name)
	var refs []string
	seen := map[string]bool{}
	var owners []string
	for _, e := range entries {
		if e.status == 'D' || !isKnowledgeEntry(e.path) {
			continue
		}
		body, err := git(a.Dir(), "show", branch+":"+filepath.ToSlash(e.path))
		if err != nil {
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(e.path), ".md")
		hits, _ := CanonicalPeerHits(a, promotionQuery(stem, body))
		for _, h := range hits {
			for _, w := range h.Where {
				if !seen[w] {
					seen[w] = true
					refs = append(refs, w)
				}
			}
		}
		topics := entryTopics(stem, body)
		for _, p := range peers {
			if tag := ownsMatch(p.Owns, topics); tag != "" {
				owners = append(owners, fmt.Sprintf("%s falls under `%s`'s owns `%s` — propose it to `%s` as a candidate "+
					"(`meshctl ask %s …`) instead of holding a copy", KBRelPath(e.path), p.Name, tag, p.Name, p.Name))
			}
		}
	}
	var lines []string
	if len(refs) > 0 {
		lines = append(lines, "Related canonical entries:")
		for i, ref := range refs {
			if i == promotionRelatedMax {
				lines = append(lines, fmt.Sprintf("… %d more: `meshctl memory recall --scope canonical`", len(refs)-i))
				break
			}
			ln := "- " + ref
			if first := meshRefFirstLine(root, ref); first != "" {
				ln += " — " + first
			}
			if s.HasCoherence {
				ln += " · " + coherenceFor(s.Coherence, ref)
			}
			lines = append(lines, ln)
		}
		if !s.HasCoherence {
			lines = append(lines, CoherenceNotStated)
		}
	}
	return append(lines, owners...)
}

// promotionQuery is the canonical-scope query for one promoted entry: its
// first heading (else its file stem) plus its front-matter tags, stopwords
// dropped.
func promotionQuery(stem, body string) string {
	title := stem
	if m := reHeading.FindStringSubmatch(body); m != nil {
		title = m[1]
	}
	var words []string
	for _, w := range strings.Split(normTopic(title), "-") {
		if !promotionStopwords[w] {
			words = append(words, w)
		}
	}
	words = append(words, frontMatterTags(body)...)
	return strings.Join(words, " ")
}

// ownsMatch returns the first owns tag that one of topics falls under: the
// tag equals the topic or is a whole kebab segment run of it.
func ownsMatch(owns, topics []string) string {
	for _, o := range owns {
		tag := normTopic(o)
		if tag == "" {
			continue
		}
		for _, t := range topics {
			if strings.Contains("-"+t+"-", "-"+tag+"-") {
				return tag
			}
		}
	}
	return ""
}

// coherenceFor returns the agent's classification of ref from the Coherence
// section: the word on the first line naming the reference (slug ignored),
// else "not classified".
func coherenceFor(coherence, ref string) string {
	want := strings.SplitN(ref, "#", 2)[0]
	for _, ln := range strings.Split(coherence, "\n") {
		named := false
		for _, r := range MeshRefsIn(ln) {
			if strings.SplitN(r, "#", 2)[0] == want {
				named = true
			}
		}
		if !named && !strings.Contains(ln, strings.TrimPrefix(want, MeshRefScheme)) {
			continue
		}
		if m := reCoherenceWord.FindStringSubmatch(ln); m != nil {
			return strings.ToLower(m[1])
		}
	}
	return "not classified"
}

// meshRefFirstLine is the first non-empty body line of the entry ref names,
// heading marks stripped, cut to promotionFirstLineMax.
func meshRefFirstLine(root, ref string) string {
	text, ok, err := ResolveMeshRef(root, ref)
	if err != nil || !ok {
		return ""
	}
	_, body := splitFrontmatter(text)
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "#"))
		if ln == "" {
			continue
		}
		if r := []rune(ln); len(r) > promotionFirstLineMax {
			ln = string(r[:promotionFirstLineMax-1]) + "…"
		}
		return ln
	}
	return ""
}

// promotionInboundLines lists, per removed or renamed knowledge entry, the
// `mesh://` links to it across the pool.
func promotionInboundLines(root string, a *Agent, entries []kbBranchEntry) []string {
	var lines []string
	for _, e := range entries {
		gone, head := "", ""
		switch {
		case e.status == 'D' && isKnowledgeEntry(e.path):
			gone = e.path
			head = "removes " + KBRelPath(e.path)
		case e.isRename && isKnowledgeEntry(e.oldPath):
			gone = e.oldPath
			head = "renames " + KBRelPath(e.oldPath) + " → " + KBRelPath(e.path)
		default:
			continue
		}
		links := InboundMeshLinks(root, a.Name, KBRelPath(gone))
		if len(links) == 0 {
			continue
		}
		lines = append(lines, head+" — inbound links:")
		for i, l := range links {
			if i == promotionInboundMax {
				lines = append(lines, fmt.Sprintf("  … %d more", len(links)-i))
				break
			}
			lines = append(lines, "  "+l)
		}
	}
	return lines
}

// InboundMeshLinks greps the pool, read-only, for `mesh://` links to owner's
// kb-relative entry: every peer's knowledge/ on its main checkout and the
// pool directory's markdown (worktrees excluded). Each result is
// `<ref> in <root-relative file>`, in walk order; the entry itself is skipped.
func InboundMeshLinks(root, owner, kbRel string) []string {
	var out []string
	self := ""
	scan := func(p string) {
		if p == self {
			return
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return
		}
		rel, _ := filepath.Rel(root, p)
		for _, ref := range MeshRefsIn(string(b)) {
			if r, err := ParseMeshRef(ref); err == nil && r.Owner == owner && r.Path == kbRel {
				out = append(out, ref+" in "+filepath.ToSlash(rel))
			}
		}
	}
	walk := func(dir string, skip ...string) {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				for _, s := range skip {
					if p == s {
						return filepath.SkipDir
					}
				}
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".md") {
				scan(p)
			}
			return nil
		})
	}
	agents, _, _ := ScanAgents(root)
	for _, ag := range agents {
		if ag.Name == owner {
			self = filepath.Join(ag.Dir(), KnowledgeDir, filepath.FromSlash(kbRel))
		}
	}
	for _, ag := range agents {
		walk(filepath.Join(ag.Dir(), KnowledgeDir))
	}
	walk(PoolDir(root), WorktreesPath(root))
	return out
}
