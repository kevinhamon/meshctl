package mesh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// adr.go — machine-readable decision records.
//
// A KB's decisions/ area is append-only by design, so it is the log tier of
// the KB: it grows forever and most of it is history. Two things keep history
// from being read as truth. First, every ADR carries a status the tooling can
// read — YAML frontmatter, with the human bullet (`- **Status:** …`) kept for
// the reader — so recall drops superseded decisions instead of ranking them
// beside their replacement. Second, an ADR names the state documents it
// changes (`updates:`), and doctor reports an ADR that never landed there, so
// the current-tier docs (architecture/, direction.md, state.md) cannot drift
// behind the decisions that were meant to change them.

// ADR statuses the tooling recognises. Anything else is kept verbatim and
// treated as active.
const (
	ADRProposed   = "proposed"
	ADRAccepted   = "accepted"
	ADRRejected   = "rejected"
	ADRDeprecated = "deprecated"
	ADRSuperseded = "superseded"
)

// ADR is one decision record as the tooling sees it.
type ADR struct {
	Num          string   // "0045" (zero-padded, as in the filename)
	File         string   // "0045-user-audit-event-durability.md"
	Title        string   // H1 without the number
	Status       string   // lowercase status word
	SupersededBy []string // numbers
	Supersedes   []string // numbers
	Updates      []string // KB-relative paths this decision changes (architecture/x.md, direction.md, state.md)
	Date         string   // YYYY-MM-DD when known
	Frontmatter  bool     // status came from YAML frontmatter (machine-readable)
}

// Active reports whether the decision still states current truth.
func (d ADR) Active() bool {
	switch d.Status {
	case ADRSuperseded, ADRDeprecated, ADRRejected:
		return false
	}
	return true
}

// adrFrontmatter is the YAML the tooling reads from a decision file.
type adrFrontmatter struct {
	Status       string `yaml:"status"`
	SupersededBy any    `yaml:"superseded_by"`
	Supersedes   any    `yaml:"supersedes"`
	Updates      any    `yaml:"updates"`
	Date         any    `yaml:"date"`
}

var (
	reADRTitle      = regexp.MustCompile(`(?m)^#\s+(?:\d{4}\.?\s+)?(.+?)\s*$`)
	reADRStatusLine = regexp.MustCompile(`(?im)^\s*-\s*\*\*status:\*\*\s*(.+)$`)
	reADRDateLine   = regexp.MustCompile(`(?im)^\s*-\s*\*\*date:\*\*\s*(\d{4}-\d{2}-\d{2})`)
	reADRNums       = regexp.MustCompile(`(?i)(?:ADR[-\s]?)?#?0*(\d{1,4})\b`)
	reADRSupBy      = regexp.MustCompile(`(?i)superseded\s+by\s+((?:(?:ADR[-\s]?)?#?\d{1,4}(?:\s*(?:,|and|/)\s*)?)+)`)
	reADRSupersedes = regexp.MustCompile(`(?i)supersedes\s+((?:(?:ADR[-\s]?)?#?\d{1,4}(?:\s*(?:,|and|/)\s*)?)+)`)
)

// padNum normalises an ADR number to the four-digit filename form.
func padNum(n string) string {
	n = strings.TrimLeft(n, "0")
	if n == "" {
		n = "0"
	}
	for len(n) < 4 {
		n = "0" + n
	}
	return n
}

// yamlStrings reads a scalar-or-list YAML value as strings.
func yamlStrings(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if s := strings.TrimSpace(x); s != "" {
			return []string{s}
		}
		return nil
	case int:
		return []string{fmt.Sprint(x)}
	case time.Time:
		return []string{x.Format("2006-01-02")} // yaml.v3 decodes a bare date as a timestamp
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, yamlStrings(e)...)
		}
		return out
	default:
		return []string{fmt.Sprint(x)}
	}
}

// adrNums extracts ADR numbers (padded) from a free-form list ("ADR-0050",
// "50, 51", "#0012 and 0013").
func adrNums(items []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, it := range items {
		for _, m := range reADRNums.FindAllStringSubmatch(it, -1) {
			n := padNum(m[1])
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// statusWord picks the status vocabulary word out of a human status line such
// as "accepted — **implemented** (…)" or "**superseded by ADR-0050** (2026-08-10)".
func statusWord(line string) string {
	l := strings.ToLower(strings.ReplaceAll(line, "*", ""))
	l = strings.TrimSpace(l)
	for _, s := range []string{ADRSuperseded, ADRDeprecated, ADRRejected, ADRAccepted, ADRProposed} {
		if strings.HasPrefix(l, s) {
			return s
		}
	}
	// Not at the start — the template line lists all of them; fall back to
	// the first word so an unknown vocabulary is still visible to doctor.
	if i := strings.IndexAny(l, " \t("); i > 0 {
		return l[:i]
	}
	return l
}

// ParseADR reads one decision file. The YAML frontmatter wins when present;
// otherwise the human bullet form is parsed.
func ParseADR(path string) (ADR, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ADR{}, err
	}
	name := filepath.Base(path)
	d := ADR{File: name}
	if m := reLeadingNum.FindStringSubmatch(name); m != nil {
		d.Num = padNum(m[1])
	}
	fm, body := splitFrontmatter(string(b))
	if m := reADRTitle.FindStringSubmatch(body); m != nil {
		d.Title = strings.TrimSpace(m[1])
	}
	if fm != "" {
		var y adrFrontmatter
		if err := decodeYAML(fm, &y); err == nil && strings.TrimSpace(y.Status) != "" {
			d.Frontmatter = true
			d.Status = statusWord(y.Status)
			d.SupersededBy = adrNums(yamlStrings(y.SupersededBy))
			d.Supersedes = adrNums(yamlStrings(y.Supersedes))
			for _, u := range yamlStrings(y.Updates) {
				d.Updates = append(d.Updates, strings.TrimPrefix(strings.TrimSpace(u), KnowledgeDir+"/"))
			}
			if ds := yamlStrings(y.Date); len(ds) > 0 {
				d.Date = ds[0]
			}
			// The bullet form may still carry the successor when the
			// frontmatter does not name it.
			if d.Status == ADRSuperseded && len(d.SupersededBy) == 0 {
				d.SupersededBy = bulletSupersededBy(body)
			}
			if d.Date == "" {
				if m := reADRDateLine.FindStringSubmatch(body); m != nil {
					d.Date = m[1]
				}
			}
			return d, nil
		}
	}
	if m := reADRStatusLine.FindStringSubmatch(body); m != nil {
		d.Status = statusWord(m[1])
		d.SupersededBy = bulletSupersededBy(m[1])
		if sm := reADRSupersedes.FindStringSubmatch(m[1]); sm != nil {
			d.Supersedes = adrNums([]string{sm[1]})
		}
	}
	if m := reADRDateLine.FindStringSubmatch(body); m != nil {
		d.Date = m[1]
	}
	return d, nil
}

func bulletSupersededBy(s string) []string {
	if m := reADRSupBy.FindStringSubmatch(s); m != nil {
		return adrNums([]string{m[1]})
	}
	return nil
}

// LoadADRs reads every numbered decision under <kb>/decisions, sorted by number.
// README.md and template.md are skipped. A missing decisions/ yields nil.
func LoadADRs(kbRoot string) []ADR {
	dir := filepath.Join(kbRoot, "decisions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []ADR
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || reLeadingNum.FindStringSubmatch(e.Name()) == nil {
			continue
		}
		d, err := ParseADR(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Num < out[j].Num })
	return out
}

// inactiveKBFiles returns the KB-relative paths of decisions that no longer
// state current truth (superseded, deprecated, rejected), so recall can drop
// them. Keyed "decisions/<file>".
func inactiveKBFiles(kbRoot string) map[string]bool {
	out := map[string]bool{}
	for _, d := range LoadADRs(kbRoot) {
		if !d.Active() {
			out["decisions/"+d.File] = true
		}
	}
	return out
}

// --- frontmatter migration (doctor --fix) ---

// adrNeedsFrontmatter reports the numbered decisions whose status is only in
// the human bullet form.
func adrNeedsFrontmatter(kbRoot string) []ADR {
	var out []ADR
	for _, d := range LoadADRs(kbRoot) {
		if !d.Frontmatter && d.Status != "" {
			out = append(out, d)
		}
	}
	return out
}

// adrFrontmatterBlock renders the YAML block for a decision parsed from its
// bullet form. The bullet stays in the body, so the file reads as before.
func adrFrontmatterBlock(d ADR) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("status: " + d.Status + "\n")
	if d.Date != "" {
		b.WriteString("date: " + d.Date + "\n")
	}
	if len(d.SupersededBy) > 0 {
		b.WriteString("superseded_by: [" + strings.Join(d.SupersededBy, ", ") + "]\n")
	}
	if len(d.Supersedes) > 0 {
		b.WriteString("supersedes: [" + strings.Join(d.Supersedes, ", ") + "]\n")
	}
	b.WriteString("---\n")
	return b.String()
}

// migrateADRFrontmatter prepends the machine-readable block to a decision file
// that has none. Additive: nothing the human wrote is changed or moved.
func migrateADRFrontmatter(path string) error {
	d, err := ParseADR(path)
	if err != nil {
		return err
	}
	if d.Frontmatter {
		return nil
	}
	if d.Status == "" {
		return fmt.Errorf("%s: no `- **Status:**` bullet to migrate", filepath.Base(path))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := strings.TrimPrefix(string(b), "\ufeff")
	if fm, body := splitFrontmatter(s); fm != "" {
		// Frontmatter exists but carries no status: add it rather than nest.
		s = "---\n" + fm + "\nstatus: " + d.Status + "\n---\n" + body
		return writeFileAtomic(path, []byte(s), 0o644)
	}
	return writeFileAtomic(path, []byte(adrFrontmatterBlock(d)+s), 0o644)
}

// --- "an ADR must land in state" ---

// kbFileDate is the date a KB file last changed: its last commit when the
// workspace is a git repo (mtime lies after a clone), else its mtime.
func kbFileDate(agentDir, rel string) (time.Time, bool) {
	p := filepath.Join(agentDir, KnowledgeDir, rel)
	info, err := os.Stat(p)
	if err != nil {
		return time.Time{}, false
	}
	if dirExists(filepath.Join(agentDir, ".git")) {
		out, err := exec.Command("git", "-C", agentDir, "log", "-1", "--format=%cI", "--", filepath.Join(KnowledgeDir, rel)).Output()
		if err == nil {
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(out))); err == nil {
				// An uncommitted edit is newer than the last commit.
				if info.ModTime().After(t) && uncommitted(agentDir, filepath.Join(KnowledgeDir, rel)) {
					return info.ModTime(), true
				}
				return t, true
			}
		}
	}
	return info.ModTime(), true
}

func uncommitted(agentDir, rel string) bool {
	out, err := exec.Command("git", "-C", agentDir, "status", "--porcelain", "--", rel).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// adrDate is when a decision took effect: its stated date, else the file date.
func adrDate(agentDir string, d ADR) (time.Time, bool) {
	if d.Date != "" {
		if t, err := time.Parse("2006-01-02", d.Date); err == nil {
			return t, true
		}
	}
	return kbFileDate(agentDir, "decisions/"+d.File)
}

// ADRDebt is one accepted decision that has not landed in the state docs it
// changes (or in the state digest).
type ADRDebt struct {
	ADR     ADR
	Targets []string // KB-relative docs older than the decision
}

// adrLandingDebt lists accepted decisions whose `updates:` targets are older
// than the decision (or missing). Only decisions that declare `updates:` are
// checked here; the digest-level check is stateDigestDebt.
func adrLandingDebt(agentDir string) []ADRDebt {
	kb := filepath.Join(agentDir, KnowledgeDir)
	var out []ADRDebt
	for _, d := range LoadADRs(kb) {
		if d.Status != ADRAccepted || len(d.Updates) == 0 {
			continue
		}
		when, ok := adrDate(agentDir, d)
		if !ok {
			continue
		}
		var stale []string
		for _, u := range d.Updates {
			t, exists := kbFileDate(agentDir, u)
			if !exists {
				stale = append(stale, u+" (missing)")
				continue
			}
			if t.Before(when) {
				stale = append(stale, u)
			}
		}
		if len(stale) > 0 {
			out = append(out, ADRDebt{ADR: d, Targets: stale})
		}
	}
	return out
}

// stateDigestDebt lists accepted decisions newer than knowledge/state.md —
// the digest has not absorbed them. An agent without a state digest gets
// every accepted decision back (the digest is yet to be written).
func stateDigestDebt(agentDir string) []ADR {
	kb := filepath.Join(agentDir, KnowledgeDir)
	stateAt, hasState := kbFileDate(agentDir, StateFile)
	var out []ADR
	for _, d := range LoadADRs(kb) {
		if d.Status != ADRAccepted {
			continue
		}
		if !hasState {
			out = append(out, d)
			continue
		}
		when, ok := adrDate(agentDir, d)
		if ok && when.After(stateAt) {
			out = append(out, d)
		}
	}
	return out
}

// --- candidate ageing ---

// CandidateMaxAge is how long a candidate may wait before doctor calls it
// unconsolidated: the inbox is a holding area, not a graveyard.
const CandidateMaxAge = 14 * 24 * time.Hour

// agedCandidates lists knowledge/candidates/*.md older than CandidateMaxAge,
// each with its age in days.
func agedCandidates(agentDir string, now time.Time) []string {
	dir := filepath.Join(agentDir, KnowledgeDir, "candidates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		t, ok := kbFileDate(agentDir, "candidates/"+e.Name())
		if !ok {
			continue
		}
		if age := now.Sub(t); age > CandidateMaxAge {
			out = append(out, fmt.Sprintf("candidates/%s (%d days)", e.Name(), int(age.Hours()/24)))
		}
	}
	return out
}
