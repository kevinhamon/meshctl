package mesh

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ledger.go — the confirmation ledger. Confirmed
// knowledge is recorded per entry in a tracked content-hash ledger,
// knowledge/.confirmed.yaml, one scheme for every entry type: path → hash of
// the file bytes, or, for a sectioned file (the state digest, a legacy peer
// glossary), heading anchor → hash of the trimmed section text with the state
// header comment excluded. An entry is confirmed when its current hash matches
// the ledger, so an edit after confirmation unconfirms it by construction, and
// a knowledge base with no ledger reads as entirely unconfirmed.
//
// This file is the read side and the hasher; the write side lands with
// `kb finish`, `state set` and `kb confirm`, which build entries with
// CurrentLedgerEntry and persist them with WriteLedger.

// LedgerFile is the ledger's name under knowledge/.
const LedgerFile = ".confirmed.yaml"

// LedgerPreamble is the anchor of a sectioned file's text before its first
// heading. KebabSlug never yields a leading underscore, so it cannot collide
// with a heading anchor.
const LedgerPreamble = "_preamble"

// LedgerPath is <agentDir>/knowledge/.confirmed.yaml.
func LedgerPath(agentDir string) string {
	return filepath.Join(agentDir, KnowledgeDir, LedgerFile)
}

// LedgerEntry is one ledger line: a whole-file Hash, or per-anchor Sections
// for a sectioned file. Confirmed (YYYY-MM-DD) and Session record when and in
// which session the human confirmed it.
type LedgerEntry struct {
	Hash      string            `yaml:"hash,omitempty"`
	Confirmed string            `yaml:"confirmed,omitempty"`
	Session   string            `yaml:"session,omitempty"`
	Sections  map[string]string `yaml:"sections,omitempty"`
}

// Ledger is the parsed ledger keyed by knowledge-relative path
// (playbooks/release.md, state.md). Present is false when the file does not
// exist: absence of a record is never confirmation.
type Ledger struct {
	Present bool
	Entries map[string]LedgerEntry
}

// HashBytes is the ledger hash of b: "sha256:<hex>".
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// LedgerSectioned reports whether a knowledge-relative path is ledgered per
// section: the state digest and a legacy peer glossary, at the KB root.
func LedgerSectioned(rel string) bool {
	rel = kbRelPath(rel)
	return rel == StateFile || rel == PeerGlossaryFile
}

// ledgerSection is one heading-anchored slice of a sectioned file.
type ledgerSection struct {
	Anchor  string // KebabSlug of the heading, de-duplicated with -2, -3…
	Heading string // heading text, for matching a recall topic
	Text    string // trimmed section text, heading line included
}

// reMDHeading matches a markdown ATX heading line, capturing its text
// without closing hashes. Multi-line mode, so it also finds headings in a
// whole body (FindAll); on one line it is the line's own test. The one
// heading regex the ledger, reconcile and triage share.
var reMDHeading = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*#*\s*$`)

// splitSections cuts a sectioned file at every heading (outside code fences),
// after dropping the state header comment. Text before the first heading is
// the LedgerPreamble section when non-empty.
func splitSections(body string) []ledgerSection {
	body = stripStateHeader(strings.ReplaceAll(body, "\r\n", "\n"))
	var out []ledgerSection
	seen := map[string]int{}
	cur := ledgerSection{Anchor: LedgerPreamble}
	var buf []string
	flush := func() {
		cur.Text = strings.TrimSpace(strings.Join(buf, "\n"))
		if cur.Text != "" || cur.Anchor != LedgerPreamble {
			out = append(out, cur)
		}
	}
	fence := false
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			fence = !fence
		}
		if m := reMDHeading.FindStringSubmatch(ln); m != nil && !fence {
			flush()
			anchor := KebabSlug(m[1])
			seen[anchor]++
			if n := seen[anchor]; n > 1 {
				anchor = fmt.Sprintf("%s-%d", anchor, n)
			}
			cur, buf = ledgerSection{Anchor: anchor, Heading: m[1]}, nil
		}
		buf = append(buf, ln)
	}
	flush()
	return out
}

// SectionHashes maps each heading anchor of a sectioned file's body to the
// hash of its trimmed text, the state header comment excluded.
func SectionHashes(body string) map[string]string {
	out := map[string]string{}
	for _, s := range splitSections(body) {
		out[s.Anchor] = HashBytes([]byte(s.Text))
	}
	return out
}

// CurrentLedgerEntry is the ledger entry rel (knowledge-relative) would carry
// if confirmed now: its file hash, or its section hashes for a sectioned
// file. Confirmed and Session are left for the writer to fill.
func CurrentLedgerEntry(agentDir, rel string) (LedgerEntry, error) {
	b, err := os.ReadFile(filepath.Join(agentDir, KnowledgeDir, filepath.FromSlash(rel)))
	if err != nil {
		return LedgerEntry{}, err
	}
	if LedgerSectioned(rel) {
		return LedgerEntry{Sections: SectionHashes(string(b))}, nil
	}
	return LedgerEntry{Hash: HashBytes(b)}, nil
}

// ParseLedger decodes the ledger YAML. An empty document is an empty ledger.
func ParseLedger(b []byte) (Ledger, error) {
	l := Ledger{Present: true, Entries: map[string]LedgerEntry{}}
	if strings.TrimSpace(string(b)) == "" {
		return l, nil
	}
	if err := decodeYAML(string(b), &l.Entries); err != nil {
		return Ledger{Present: true, Entries: map[string]LedgerEntry{}}, fmt.Errorf("%s: %w", LedgerFile, err)
	}
	if l.Entries == nil {
		l.Entries = map[string]LedgerEntry{}
	}
	return l, nil
}

// ReadLedger reads a peer's ledger. A missing file is an empty ledger with
// Present false and no error; an unparseable one returns an empty ledger
// (everything unconfirmed) and the parse error.
func ReadLedger(agentDir string) (Ledger, error) {
	b, err := os.ReadFile(LedgerPath(agentDir))
	if errors.Is(err, os.ErrNotExist) {
		return Ledger{Entries: map[string]LedgerEntry{}}, nil
	}
	if err != nil {
		return Ledger{Entries: map[string]LedgerEntry{}}, err
	}
	return ParseLedger(b)
}

var reLedgerPlain = regexp.MustCompile(`^[A-Za-z0-9_./][A-Za-z0-9_./:-]*$`)

// ledgerScalar quotes s unless it is a safe plain YAML scalar.
func ledgerScalar(s string) string {
	if reLedgerPlain.MatchString(s) && !strings.Contains(s, ": ") {
		return s
	}
	return strconv.Quote(s)
}

// RenderLedger writes the ledger with sorted keys, one entry per line. An empty
// ledger renders as `{}`.
func RenderLedger(l Ledger) []byte {
	if len(l.Entries) == 0 {
		return []byte("{}\n")
	}
	var b strings.Builder
	for _, k := range ledgerKeys(l.Entries) {
		e := l.Entries[k]
		fmt.Fprintf(&b, "%s:\n", ledgerScalar(k))
		if e.Hash != "" {
			fmt.Fprintf(&b, "  hash: %s\n", ledgerScalar(e.Hash))
		}
		if e.Confirmed != "" {
			fmt.Fprintf(&b, "  confirmed: %s\n", ledgerScalar(e.Confirmed))
		}
		if e.Session != "" {
			fmt.Fprintf(&b, "  session: %s\n", ledgerScalar(e.Session))
		}
		if len(e.Sections) > 0 {
			b.WriteString("  sections:\n")
			for _, a := range ledgerKeys(e.Sections) {
				fmt.Fprintf(&b, "    %s: %s\n", ledgerScalar(a), ledgerScalar(e.Sections[a]))
			}
		}
	}
	return []byte(b.String())
}

func ledgerKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// WriteLedger renders l to the peer's ledger file (atomic replace).
func WriteLedger(agentDir string, l Ledger) error {
	p := LedgerPath(agentDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(p, RenderLedger(l), 0o644)
}

// Confirmed reports whether rel (knowledge-relative) matches its ledger line
// now: the file hash, or every current section of a sectioned file. A file
// that cannot be read, or has no ledger line, is unconfirmed.
func (l Ledger) Confirmed(agentDir, rel string) bool {
	return l.check(agentDir).confirmed(kbRelPath(rel))
}

// SectionConfirmed reports whether one anchor of a sectioned file matches its
// ledger hash now.
func (l Ledger) SectionConfirmed(agentDir, rel, anchor string) bool {
	return l.check(agentDir).sectionConfirmed(kbRelPath(rel), anchor)
}

func (l Ledger) check(agentDir string) *ledgerCheck {
	if l.Entries == nil {
		l.Entries = map[string]LedgerEntry{}
	}
	return &ledgerCheck{dir: agentDir, ledger: l, files: map[string]*ledgerFileState{}}
}

// kbRelPath strips a leading knowledge/ so agent- and KB-relative paths agree.
func kbRelPath(rel string) string {
	return strings.TrimPrefix(filepath.ToSlash(rel), KnowledgeDir+"/")
}

// Stale lists ledger lines with no matching file or section, as `path` or
// `path#anchor`, sorted.
func (l Ledger) Stale(agentDir string) []string {
	var out []string
	for _, rel := range ledgerKeys(l.Entries) {
		cur, err := CurrentLedgerEntry(agentDir, rel)
		if err != nil {
			out = append(out, rel)
			continue
		}
		if !LedgerSectioned(rel) {
			continue
		}
		for _, a := range ledgerKeys(l.Entries[rel].Sections) {
			if _, ok := cur.Sections[a]; !ok {
				out = append(out, rel+"#"+a)
			}
		}
	}
	return out
}

// ledgerCheck answers confirmation questions for one peer against one read of
// its ledger, hashing each file at most once (recall asks per topic, and many
// topics share a file).
type ledgerCheck struct {
	dir    string
	ledger Ledger
	files  map[string]*ledgerFileState
}

type ledgerFileState struct {
	ok       bool // readable
	entry    LedgerEntry
	sections []ledgerSection
}

// newLedgerCheck reads the peer's ledger; an unreadable or unparseable one
// counts as empty, so everything reads unconfirmed (never the reverse).
func newLedgerCheck(agentDir string) *ledgerCheck {
	l, err := ReadLedger(agentDir)
	if err != nil {
		l = Ledger{}
	}
	return l.check(agentDir)
}

func (c *ledgerCheck) file(rel string) *ledgerFileState {
	if st, ok := c.files[rel]; ok {
		return st
	}
	st := &ledgerFileState{}
	if b, err := os.ReadFile(filepath.Join(c.dir, KnowledgeDir, filepath.FromSlash(rel))); err == nil {
		st.ok = true
		if LedgerSectioned(rel) {
			st.sections = splitSections(string(b))
			st.entry.Sections = map[string]string{}
			for _, s := range st.sections {
				st.entry.Sections[s.Anchor] = HashBytes([]byte(s.Text))
			}
		} else {
			st.entry.Hash = HashBytes(b)
		}
	}
	c.files[rel] = st
	return st
}

// confirmed: the whole entry matches (every section of a sectioned file).
func (c *ledgerCheck) confirmed(rel string) bool {
	e, ok := c.ledger.Entries[rel]
	st := c.file(rel)
	if !ok || !st.ok {
		return false
	}
	if LedgerSectioned(rel) {
		if len(st.entry.Sections) == 0 {
			return false
		}
		for a, h := range st.entry.Sections {
			if e.Sections[a] != h {
				return false
			}
		}
		return true
	}
	return e.Hash != "" && e.Hash == st.entry.Hash
}

// sectionConfirmed: one anchor of a sectioned file matches.
func (c *ledgerCheck) sectionConfirmed(rel, anchor string) bool {
	e, ok := c.ledger.Entries[rel]
	st := c.file(rel)
	h, has := st.entry.Sections[anchor]
	return ok && st.ok && has && e.Sections[anchor] == h
}

// topicConfirmed decides a recall topic: every current- or log-tier source
// must be confirmed. A topic drawn from a section heading of a sectioned file
// answers for that section only; any other topic of the file (its stem, a
// tag) needs the whole file. Inbox sources are proposals and never confirm.
func (c *ledgerCheck) topicConfirmed(topic string, sources []string) bool {
	n := 0
	for _, src := range sources {
		if kbTier(src) == TierInbox {
			continue
		}
		n++
		rel := kbRelPath(src)
		if LedgerSectioned(rel) {
			anchor := ""
			for _, s := range c.file(rel).sections {
				if s.Anchor != LedgerPreamble && normTopic(s.Heading) == topic {
					anchor = s.Anchor
					break
				}
			}
			if anchor != "" {
				if !c.sectionConfirmed(rel, anchor) {
					return false
				}
				continue
			}
		}
		if !c.confirmed(rel) {
			return false
		}
	}
	return n > 0
}
