package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// summary.go — the confirmation summary file and the ask-text renderer.
//
// Before a peer changes what others treat as true — a promotion at `kb
// finish`, a `state set`, a pool write, an outbound write, a `kb confirm` or
// a triage apply — the human has read a confirmation round in chat, and the
// record of that round is ONE file per session, written by the peer in its own
// words: <peer>/.memory/confirm/<session>.md. Every gate that asks renders
// its ask text from this file, so the permission prompt is the signature of
// what was read, never the reading surface. A gate that finds no summary, an
// empty one, or one with no Intent line denies, naming the path, so the peer
// learns its session id from the first deny.
//
// Required sections: `Intent` (one line, first), `Grounding`, `Precedent`.
// `Coherence` is required when related entries exist and parsed
// when present; `## Changes` holds one `### <path>` block per promoted file
// with added/removed bullets and is parsed when present (its full use lands
// with the promotion gate). Headings are matched case-insensitively at any
// `#` level, with or without a trailing colon; a labelled line (`Intent: …`,
// `- **Intent**: …`) is accepted for the one-line sections, since the doctrine
// template lists them that way.

// SummaryDir is the per-session confirm directory under <peer>/.memory/.
const SummaryDir = "confirm"

// Summary is a parsed confirmation summary.
type Summary struct {
	// Text is the file's text as written (verbatim uses, hashing).
	Text string
	// Intent is the one-line intent: the first non-empty line of the Intent
	// section. "" means the summary is unusable (HasIntent).
	Intent string
	// Grounding and Precedent are the section bodies as written, trimmed.
	Grounding string
	Precedent string
	// Coherence is the section body when present; HasCoherence tells an
	// absent section from an empty one.
	Coherence    string
	HasCoherence bool
	// Changes holds the `## Changes` blocks in file order, when present.
	Changes []SummaryChange
}

// SummaryChange is one `### <path>` block of the Changes section: the plain
// words for what the file gains and loses.
type SummaryChange struct {
	Path    string
	Added   []string
	Removed []string
}

// HasIntent reports whether the summary carries the required Intent line;
// an empty file or one with no Intent line is not a summary a gate can show.
func (s Summary) HasIntent() bool { return strings.TrimSpace(s.Intent) != "" }

// SectionLines renders the sections after the Intent line as the ask body:
// Grounding, Precedent, Coherence (when present) and the Changes blocks, each
// under a one-word label, for gates that show the whole summary.
func (s Summary) SectionLines() []string {
	var lines []string
	section := func(label, body string) {
		if strings.TrimSpace(body) == "" {
			return
		}
		lines = append(lines, label+":")
		lines = append(lines, strings.Split(strings.TrimSpace(body), "\n")...)
	}
	section("Grounding", s.Grounding)
	section("Precedent", s.Precedent)
	if s.HasCoherence {
		section("Coherence", s.Coherence)
	}
	if len(s.Changes) > 0 {
		lines = append(lines, "Changes:")
		for _, c := range s.Changes {
			lines = append(lines, "  "+c.Path+fmt.Sprintf("  (+%d -%d)", len(c.Added), len(c.Removed)))
			for _, a := range c.Added {
				lines = append(lines, "    + "+a)
			}
			for _, r := range c.Removed {
				lines = append(lines, "    - "+r)
			}
		}
	}
	return lines
}

// SummaryPath is the peer's summary for one session:
// <agentDir>/.memory/confirm/<session>.md. The id is sanitised the way the
// KB-rule ack file's is, so it can never leave the confirm directory.
func SummaryPath(agentDir, session string) string {
	if session == "" {
		session = "no-session"
	}
	return filepath.Join(agentDir, MemoryDir, SummaryDir, sanitizeID(session)+".md")
}

// ReadSummary reads and parses the summary at path. A missing file is an
// error (the caller names the path in its deny); an empty or Intent-less file
// is not — it parses to a Summary whose HasIntent is false.
func ReadSummary(path string) (Summary, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Summary{}, err
	}
	return ParseSummary(string(b)), nil
}

var (
	// reSummaryHeading: `## Intent`, `# Grounding:`, `### Changes` …
	reSummaryHeading = regexp.MustCompile(`^#+\s*([A-Za-z]+)\s*:?\s*$`)
	// reSummaryLabel: `Intent: …`, `**Intent**: …`, `- **Intent**: …`, `* Intent — …`
	reSummaryLabel = regexp.MustCompile(`^(?:[-*]\s+)?\**(Intent|Grounding|Precedent|Coherence)\**\s*[:—-]\s*(.*)$`)
	// reSummaryFile: a `### <path>` block heading inside Changes.
	reSummaryFile = regexp.MustCompile(`^#{3,}\s*` + "`?" + `([^` + "`" + `\s]+)` + "`?" + `\s*$`)
	// reSummaryBullet: the bullet grammar, normalised generously — `+ text`,
	// `- text`, `- + text`, `- − text`, `- - text`, `* + text`.
	reSummaryBullet = regexp.MustCompile(`^\s*(?:[-*]\s+)?([+\-−])\s+(.*\S)\s*$`)
)

var summarySections = map[string]bool{"intent": true, "grounding": true, "precedent": true, "coherence": true, "changes": true}

// ParseSummary parses summary text. Sections are located by heading; the
// Intent, Grounding, Precedent and Coherence labels are also accepted as a
// labelled line. Unknown headings start no section and end the current one.
func ParseSummary(text string) Summary {
	s := Summary{Text: text}
	bodies := map[string][]string{}
	present := map[string]bool{}
	cur := ""
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " \t")
		if m := reSummaryHeading.FindStringSubmatch(line); m != nil {
			name := strings.ToLower(m[1])
			if summarySections[name] {
				cur = name
				present[name] = true
			} else if cur != "changes" {
				cur = ""
			} else {
				// A `### <path>` block inside Changes is reSummaryFile's.
				bodies[cur] = append(bodies[cur], line)
			}
			continue
		}
		if cur != "changes" {
			if m := reSummaryLabel.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				name := strings.ToLower(m[1])
				present[name] = true
				bodies[name] = append(bodies[name], m[2])
				cur = name
				continue
			}
		}
		if cur != "" {
			bodies[cur] = append(bodies[cur], line)
		}
	}
	first := func(lines []string) string {
		for _, l := range lines {
			if t := strings.TrimSpace(l); t != "" {
				return t
			}
		}
		return ""
	}
	s.Intent = first(bodies["intent"])
	s.Grounding = strings.TrimSpace(strings.Join(bodies["grounding"], "\n"))
	s.Precedent = strings.TrimSpace(strings.Join(bodies["precedent"], "\n"))
	s.HasCoherence = present["coherence"]
	s.Coherence = strings.TrimSpace(strings.Join(bodies["coherence"], "\n"))
	s.Changes = parseSummaryChanges(bodies["changes"])
	return s
}

// parseSummaryChanges reads the `### <path>` blocks and their bullets.
func parseSummaryChanges(lines []string) []SummaryChange {
	var out []SummaryChange
	for _, line := range lines {
		if m := reSummaryFile.FindStringSubmatch(line); m != nil {
			out = append(out, SummaryChange{Path: m[1]})
			continue
		}
		if len(out) == 0 {
			continue
		}
		m := reSummaryBullet.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		c := &out[len(out)-1]
		if m[1] == "+" {
			c.Added = append(c.Added, m[2])
		} else {
			c.Removed = append(c.Removed, m[2])
		}
	}
	return out
}

// AskText is one gate's ask, assembled by the gate and rendered by RenderAsk:
// a header naming the gate and the action, the Intent line, the gate's body
// lines (the summary sections, per-file bullets, an `Outbound:` line) and a
// footer saying what a deny means.
type AskText struct {
	Check  string // the GateCheck* constant of the asking gate (logged)
	Header string
	Intent string
	Body   []string
	Footer string
}

// RenderAsk renders the ask text in the one shape every gate shares — header,
// `Intent: …` first, body, footer — and logs the rendered length to the
// agent's gate log (`ask-rendered check=<check> chars=<n>`), since the
// harness prompt truncates past a few thousand characters
// (about 600–1200 render whole) and a truncated signature is otherwise
// invisible. agentDir "" ⇒ only $AGENT_MESH_GATE_LOG receives the line.
func RenderAsk(agentDir string, a AskText) string {
	// An empty header puts the Intent line first (the promotion ask).
	var lines []string
	if a.Header != "" {
		lines = append(lines, a.Header)
	}
	intent := strings.TrimSpace(a.Intent)
	if intent == "" {
		intent = "(none stated)"
	}
	lines = append(lines, "Intent: "+intent)
	if len(a.Body) > 0 {
		lines = append(lines, "")
		lines = append(lines, a.Body...)
	}
	if a.Footer != "" {
		lines = append(lines, "", a.Footer)
	}
	text := strings.Join(lines, "\n")
	check := a.Check
	if check == "" {
		check = GateCheckNone
	}
	appendGateLog(gateLogPathFor(agentDir), fmt.Sprintf("%s ask-rendered check=%s chars=%d",
		time.Now().UTC().Format("2006-01-02T15:04:05Z"), check, len(text)))
	return text
}

// summaryProblem reads the confirmation summary at path; why is "" when it
// is usable, else the short reason a gate names before " at <path>": "no
// file", "empty", "no Intent line".
func summaryProblem(path string) (s Summary, why string) {
	s, err := ReadSummary(path)
	switch {
	case err != nil:
		return s, "no file"
	case strings.TrimSpace(s.Text) == "":
		return s, "empty"
	case !s.HasIntent():
		return s, "no Intent line"
	}
	return s, ""
}

// writeFirstHint is appended to a gate deny when a file the command names
// (--summary-file, --report) does not exist yet. The gate checks the whole
// shell command before any of it runs, so `cp x <path> && meshctl … <path>` is
// always denied: the file must be written by an earlier call (issue #6).
func writeFirstHint(path string) string {
	if fileExists(path) {
		return ""
	}
	return " The gate checks the whole command before any of it runs, so a file created in the same call does not " +
		"exist yet: write it in a separate call first, then run the command on its own."
}

// usableSummary is summaryProblem for a command's --summary-file: why is ""
// when it is usable, else what is wrong with it, path included (none named,
// no file, empty, no Intent line).
func usableSummary(path string) (s Summary, why string) {
	if path == "" {
		return s, "no --summary-file"
	}
	s, why = summaryProblem(path)
	switch why {
	case "no file":
		why = "no file at " + path
	case "empty":
		why = "empty summary at " + path
	case "no Intent line":
		why = "no Intent line in " + path
	}
	return s, why
}
