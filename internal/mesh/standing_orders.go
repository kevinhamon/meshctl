package mesh

import (
	"os"
	"path/filepath"
	"strings"
)

// standing_orders.go — the mesh standing orders (the Agent Communication
// section and the Cognition stanza) live in a block meshctl owns inside the
// agent's gitignored CLAUDE.local.md, never in the tracked CLAUDE.md (issue
// #12). Onboard and `pool upgrade` rewrite the block whole, so a meshctl change
// needs no commit in any agent repo, copies cannot drift, and an agent repo is
// safe to share with people who don't run meshctl (issue #19). Claude Code
// loads CLAUDE.local.md natively, headless runs included; the opencode plugin
// injects it after CLAUDE.md.

// ClaudeLocalFile is Claude Code's gitignored, auto-loaded companion to
// CLAUDE.md, and the home of the managed standing orders.
const ClaudeLocalFile = "CLAUDE.local.md"

// The managed block's bounds. Text outside them is the human's.
const (
	standingOrdersBegin = "<!-- meshctl:standing-orders begin — managed by meshctl, rewritten on onboard and pool upgrade; edits inside are replaced. Put your own notes in CLAUDE.md or outside this block. -->"
	standingOrdersEnd   = "<!-- meshctl:standing-orders end -->"
)

// commsHeading opens the Agent Communication section.
const commsHeading = "## Agent Communication (intake)"

// standingOrdersBlock is the managed block as it should read in this pool.
// withComms=false leaves the Agent Communication section out, for an agent
// whose CLAUDE.md keeps a customised one (two versions would conflict).
func standingOrdersBlock(poolRoot string, withComms bool) string {
	b := standingOrdersBegin + "\n\n"
	if withComms {
		b += strings.TrimSpace(renderPoolTokens(poolRoot, commsSection)) + "\n\n"
	}
	return b + strings.TrimSpace(renderPoolTokens(poolRoot, cognitionSection)) + "\n\n" + standingOrdersEnd + "\n"
}

// retiredCommsLines are Agent Communication lines earlier meshctl versions
// wrote and the current section no longer has. A section made only of these
// and current lines is meshctl's own text and moves; any other line is the
// human's, and the section stays where it is.
var retiredCommsLines = []string{
	"Bidirectional file-based handoff — canonical protocol: your pool's `../__POOL_DIR__/doctrine/agent-comms.md`. Inbox: `intake/` (in this workspace). The roster is computed on demand — run `meshctl agent list` (do not maintain a peer list here).",
	"- **Outbound:** `meshctl send <target> <type> \"…\"` writes a request into a peer's inbox; `meshctl ask <target> <type> \"…\"` also dispatches an advisory peer headlessly and blocks for the answer. State-mutating peers (pm→tracker, developer→code) are `send`-only and human-triggered.",
	"- **Inbound:** at session start / on \"check intake\", run `meshctl inbox next --claim` (atomic claim) or scan `intake/*.md` for `status: pending`; process by `type`, then write the outcome back with `meshctl inbox respond <id> --status answered|filed|needs-info|needs-human --response \"…\"` (intake files are meshctl-owned — never hand-edit them). **Not yours?** `meshctl inbox decline <id> --reason \"…\" --redirect <owner>` — don't half-answer out-of-domain work.",
}

// knownCommsSection reports whether every non-blank line of a section body is
// one some meshctl version wrote, under either pool directory name.
func knownCommsSection(body string) bool {
	known := map[string]bool{}
	for _, src := range append([]string{commsSection}, retiredCommsLines...) {
		for _, dir := range []string{CurrentPoolDir, LegacyPoolDir} {
			for _, l := range strings.Split(strings.ReplaceAll(src, poolDirToken, dir), "\n") {
				known[strings.TrimSpace(l)] = true
			}
		}
	}
	for _, l := range strings.Split(body, "\n") {
		if l = strings.TrimSpace(l); l != "" && !known[l] {
			return false
		}
	}
	return true
}

// standingOrdersBounds locates the managed block, end exclusive and past the
// end marker's line.
func standingOrdersBounds(text string) (start, end int, ok bool) {
	start = strings.Index(text, standingOrdersBegin)
	if start < 0 {
		return 0, 0, false
	}
	n := strings.Index(text[start:], standingOrdersEnd)
	if n < 0 {
		return 0, 0, false
	}
	end = start + n + len(standingOrdersEnd)
	if end < len(text) && text[end] == '\n' {
		end++
	}
	return start, end, true
}

// stripStandingOrders removes the unmanaged copies of the standing orders an
// earlier meshctl wrote into a file: the bounded Cognition stanza, and the
// Agent Communication section (its heading through the next level-2 heading)
// when every line of it is meshctl's own text. A customised Agent
// Communication section, or a Cognition stanza whose ending meshctl does not
// recognise, is left alone and reported in kept.
func stripStandingOrders(text string) (out string, removed bool, kept []string) {
	out = text
	if s, e, ok := cognitionStanzaBounds(out); ok {
		out, removed = cutSection(out, s, e), true
	} else if strings.Contains(out, cognitionStanzaHeading) {
		kept = append(kept, "a Cognition stanza with no ending meshctl recognises")
	}
	if s := headingAt(out, commsHeading); s >= 0 {
		e := len(out)
		if n := strings.Index(out[s+len(commsHeading):], "\n## "); n >= 0 {
			e = s + len(commsHeading) + n + 1
		}
		if knownCommsSection(out[s+len(commsHeading) : e]) {
			out, removed = cutSection(out, s, e), true
		} else {
			kept = append(kept, customCommsKept)
		}
	}
	return out, removed, kept
}

// customCommsKept names a customised Agent Communication section left in place.
const customCommsKept = "a customised Agent Communication section (left in place, so the managed block omits its own; move your role-specific lines under another heading and delete the rest to get the current intake order)"

// headingAt returns the offset of heading at a line start, or -1.
func headingAt(text, heading string) int {
	for off := 0; ; {
		i := strings.Index(text[off:], heading)
		if i < 0 {
			return -1
		}
		i += off
		if i == 0 || text[i-1] == '\n' {
			return i
		}
		off = i + len(heading)
	}
}

// cutSection removes text[s:e] and closes the gap to one blank line.
func cutSection(text string, s, e int) string {
	before := strings.TrimRight(text[:s], "\n")
	after := strings.TrimLeft(text[e:], "\n")
	switch {
	case before == "":
		return after
	case after == "":
		return before + "\n"
	}
	return before + "\n\n" + after
}

// standingOrdersChange is what syncing one workspace changes.
type standingOrdersChange struct {
	claude, local        string // new contents; "" when unchanged
	movedOut, wroteLocal bool
	freshLocal           bool     // CLAUDE.local.md had no managed block before
	kept                 []string // "<file> keeps <what>" for text left in place
}

// planStandingOrders computes the sync without writing.
func planStandingOrders(dir, poolRoot string) standingOrdersChange {
	var c standingOrdersChange
	customComms := false
	note := func(file string, kept []string) {
		for _, k := range kept {
			customComms = customComms || k == customCommsKept
			c.kept = append(c.kept, file+" keeps "+k)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); err == nil {
		out, removed, kept := stripStandingOrders(string(b))
		if removed {
			c.claude, c.movedOut = out, true
		}
		note("CLAUDE.md", kept)
	}
	lb, _ := os.ReadFile(filepath.Join(dir, ClaudeLocalFile))
	local := string(lb)
	// The human's part of CLAUDE.local.md: around the block, or, before the
	// first sync, the whole file less the standing orders moved there by hand
	// (issue #19).
	var head, tail string
	if s, e, ok := standingOrdersBounds(local); ok {
		head, tail = local[:s], local[e:]
		_, _, kept := stripStandingOrders(head + tail)
		note(ClaudeLocalFile, kept)
	} else {
		c.freshLocal = true
		stripped, _, kept := stripStandingOrders(local)
		note(ClaudeLocalFile, kept)
		if strings.TrimSpace(stripped) != "" {
			head = strings.TrimRight(stripped, "\n") + "\n\n"
		}
	}
	if next := head + standingOrdersBlock(poolRoot, !customComms) + tail; next != local {
		c.local, c.wroteLocal = next, true
	}
	return c
}

// syncStandingOrders moves the standing orders out of CLAUDE.md and writes the
// managed block into CLAUDE.local.md. Returns human-readable notes.
func syncStandingOrders(dir, poolRoot string) ([]string, error) {
	c := planStandingOrders(dir, poolRoot)
	var notes []string
	if c.movedOut {
		if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(c.claude), 0o644); err != nil {
			return notes, err
		}
		notes = append(notes, "moved the mesh standing orders out of CLAUDE.md into "+ClaudeLocalFile)
	}
	if c.wroteLocal {
		if err := os.WriteFile(filepath.Join(dir, ClaudeLocalFile), []byte(c.local), 0o644); err != nil {
			return notes, err
		}
		if c.freshLocal {
			notes = append(notes, "wrote the mesh standing orders into "+ClaudeLocalFile)
		} else {
			notes = append(notes, "refreshed the mesh standing orders in "+ClaudeLocalFile)
		}
	}
	notes = append(notes, c.kept...)
	return notes, nil
}
