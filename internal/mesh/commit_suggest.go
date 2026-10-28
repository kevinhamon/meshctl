package mesh

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// commit_suggest.go — the message the console's Commit button offers for an
// agent repo. Commit subjects are read back by recall's git spine, session
// records and the console's recent commits, so the suggestion names what
// changed instead of "Update <agent> workspace". Built from the paths and
// their diffs alone: deterministic, no model call. The person can edit it.

// Kinds of change, in the order a tie between group sizes is broken.
const (
	changeWiring    = iota // files and blocks meshctl writes
	changeKnowledge        // knowledge/ entries, named by slug
	changeOther            // anything else, named by top-level path
)

const (
	gitignoreManagedBegin = "# meshctl-managed ignores"
	gitignoreManagedEnd   = "# end meshctl-managed ignores"
	suggestSubjectMax     = 72
)

// wiringCompanions are files meshctl writes into but a person may also edit:
// they count as wiring only alongside a change that is certainly meshctl's.
var wiringCompanions = map[string]bool{
	"CLAUDE.md": true, "AGENTS.md": true, ".claude/settings.json": true, "opencode.json": true,
	"docs/README.md": true, "intake/README.md": true,
}

type changeGroup struct {
	kind  int
	label string // "meshctl wiring", "Add candidates", "Update"
	names []string
	paths int
}

// SuggestCommitMessage proposes a commit message for dir's uncommitted
// changes: the largest kind of change as the subject, the rest as an
// "Also:" list. version names a wiring refresh no new heading describes.
// "" on a clean tree.
func SuggestCommitMessage(dir, version string) string {
	out, err := git(dir, "-c", "core.quotePath=false", "status", "--porcelain", "-uall")
	if err != nil {
		return ""
	}
	type change struct{ code, path string }
	var changes []change
	for _, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if len(ln) < 4 {
			continue
		}
		p := ln[3:]
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		changes = append(changes, change{ln[:2], strings.Trim(p, `"`)})
	}
	if len(changes) == 0 {
		return ""
	}

	groups := map[string]*changeGroup{}
	var order []string
	add := func(kind int, label string, names ...string) {
		g, ok := groups[label]
		if !ok {
			g = &changeGroup{kind: kind, label: label}
			groups[label] = g
			order = append(order, label)
		}
		g.paths++
		for _, n := range names {
			if n != "" && !slices.Contains(g.names, n) {
				g.names = append(g.names, n)
			}
		}
	}

	wiring := false
	var companions []change
	for _, c := range changes {
		if names, ok := wiringChange(dir, c.code, c.path); ok {
			wiring = true
			add(changeWiring, "meshctl wiring", names...)
			continue
		}
		if wiringCompanions[c.path] {
			companions = append(companions, c)
			continue
		}
		if label, name, ok := knowledgeChange(c.code, c.path); ok {
			add(changeKnowledge, label, name)
			continue
		}
		top, _, _ := strings.Cut(c.path, "/")
		add(changeOther, "Update", top)
	}
	for _, c := range companions {
		if wiring {
			add(changeWiring, "meshctl wiring")
		} else {
			add(changeOther, "Update", c.path)
		}
	}
	if g := groups["meshctl wiring"]; g != nil && len(g.names) == 0 {
		if version != "" && version != "dev" {
			g.names = []string{"v" + strings.TrimPrefix(version, "v")}
		} else {
			g.names = []string{"refresh"}
		}
	}

	list := make([]*changeGroup, 0, len(order))
	for _, l := range order {
		list = append(list, groups[l])
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].paths != list[j].paths {
			return list[i].paths > list[j].paths
		}
		return list[i].kind < list[j].kind
	})

	subject, cut := groupLine(list[0], suggestSubjectMax)
	var also []string
	if cut {
		also = append(also, list[0].label+": "+strings.Join(list[0].names, ", "))
	}
	for _, g := range list[1:] {
		line, _ := groupLine(g, 0)
		also = append(also, line)
	}
	if len(also) == 0 {
		return subject
	}
	return subject + "\n\nAlso:\n- " + strings.Join(also, "\n- ")
}

// groupLine is "<label>: a, b, c", cut to max (0: no limit) with "and N
// more"; cut reports whether names were left out.
func groupLine(g *changeGroup, max int) (line string, cut bool) {
	line = g.label + ": " + g.names[0]
	for i, n := range g.names[1:] {
		next := line + ", " + n
		if max > 0 && len(next) > max {
			return line + " and " + strconv.Itoa(len(g.names)-1-i) + " more", true
		}
		line = next
	}
	return line, false
}

// wiringChange reports whether a change is certainly meshctl's: a mesh-*
// skill (named by the headings it adds), a file carrying the meshctl-managed
// marker, or .gitignore edits that stay inside its managed block.
func wiringChange(dir, code, rel string) (names []string, ok bool) {
	parts := strings.Split(rel, "/")
	if len(parts) >= 4 && (parts[0] == ".claude" || parts[0] == ".opencode") &&
		parts[1] == "skills" && strings.HasPrefix(parts[2], "mesh-") {
		if code == "??" {
			return []string{parts[2] + " skill"}, true
		}
		return addedHeadings(dir, rel), true
	}
	if rel == ".gitignore" {
		return nil, code != "??" && gitignoreManagedOnly(dir)
	}
	if code != " D" && code != "D " {
		if b, err := os.ReadFile(filepath.Join(dir, rel)); err == nil && strings.Contains(string(b), distillerAgentMarker) {
			return nil, true
		}
	}
	return nil, false
}

// addedHeadings are the level-2 and level-3 headings rel's diff adds.
func addedHeadings(dir, rel string) []string {
	diff, _ := git(dir, "diff", "-U0", "--no-color", "--no-ext-diff", "HEAD", "--", rel)
	var out []string
	for _, ln := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(ln, "+") || strings.HasPrefix(ln, "+++") {
			continue
		}
		if m := reAnyHeading.FindStringSubmatch(ln[1:]); m != nil && (len(m[1]) == 2 || len(m[1]) == 3) {
			out = append(out, m[2])
		}
	}
	return out
}

// gitignoreManagedOnly reports whether every hunk of .gitignore's diff lies
// inside the meshctl-managed block.
func gitignoreManagedOnly(dir string) bool {
	lines := readLines(filepath.Join(dir, ".gitignore"))
	begin, end := -1, -1
	for i, ln := range lines {
		switch ln = strings.TrimSpace(ln); {
		case strings.HasPrefix(ln, gitignoreManagedBegin): // "… — do not edit"
			begin = i + 1
		case ln == gitignoreManagedEnd:
			end = i + 1
		}
	}
	if begin < 0 || end < begin {
		return false
	}
	diff, err := git(dir, "diff", "-U0", "--no-color", "--no-ext-diff", "HEAD", "--", ".gitignore")
	if err != nil {
		return false
	}
	hunks := 0
	for _, ln := range strings.Split(diff, "\n") {
		m := reHunk.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		hunks++
		start, _ := strconv.Atoi(m[1])
		count := 1
		if m[2] != "" {
			count, _ = strconv.Atoi(m[2])
		}
		last := start + count - 1
		if count == 0 {
			last = start // a pure deletion sits after line start
		}
		if start < begin || last > end {
			return false
		}
	}
	return hunks > 0
}

// knowledgeChange names a knowledge/ entry: knowledge/<area>/<slug>.md is
// "<verb> <area>" + slug; knowledge/<file>.md is "<verb> knowledge" + file.
func knowledgeChange(code, rel string) (label, name string, ok bool) {
	rest, found := strings.CutPrefix(rel, KnowledgeDir+"/")
	if !found {
		return "", "", false
	}
	verb := "Update"
	switch {
	case code == "??" || code[0] == 'A':
		verb = "Add"
	case code[0] == 'D' || code[1] == 'D':
		verb = "Remove"
	}
	area, file, nested := strings.Cut(rest, "/")
	if !nested {
		area, file = "knowledge", rest
	}
	return verb + " " + area, strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)), true
}
