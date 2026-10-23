package mesh

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// pool_commit.go — the pool repo's history is the audit trail (ADR-0066, spec
// decision 12). After the human confirms an agent's write under pool/ (the
// gate asked, the tool ran), the PostToolUse hook commits exactly the files
// that write touched, with the user's git identity (none is set here) and a
// subject naming what changed: `glossary: <term>`, `doctrine: <section>`,
// `config: <key>`. Agents never run git in pool/ (the gate denies it); meshctl's
// own writes commit the same way (`doctrine: upgrade to <version>`). Ignored
// runtime state is never staged.

// doctrineUpgradeSubject is the subject of the commit `pool upgrade` makes when
// it rewrites stamped doctrine (spec decision 67).
func doctrineUpgradeSubject(version string) string { return "doctrine: upgrade to " + version }

// poolToolFailed reports whether a PostToolUse tool_response says the call did
// not do what it asked: an explicit success:false, an is_error flag, a non-empty
// error, an interrupted shell, or a bare error string. Unknown shapes count as
// success — the gate already let the call through, and the commit only ever
// takes what actually changed on disk.
func poolToolFailed(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return false
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(str)), "error")
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	if v, ok := obj["success"].(bool); ok && !v {
		return true
	}
	if v, ok := obj["is_error"].(bool); ok && v {
		return true
	}
	if v, ok := obj["interrupted"].(bool); ok && v {
		return true
	}
	if v, ok := obj["error"].(string); ok && strings.TrimSpace(v) != "" {
		return true
	}
	return false
}

// hookPoolCommit is the pool-commit half of HookPostToolUse: one commit for a
// successful write under pool/, nothing otherwise. Best-effort: errors are
// swallowed (a hook never fails a turn; the repo nag reports what is left dirty).
func hookPoolCommit(agentsDir string, p HookPayload, now time.Time) {
	t := poolPathTargets(agentsDir, p)
	if len(t.writes) == 0 || poolToolFailed(p.ToolResponse) {
		return
	}
	pool := PoolDir(agentsDir)
	if !poolRepoPresent(pool) {
		return
	}
	paths := make([]string, 0, len(t.writes))
	for _, w := range t.writes {
		// poolPathTargets names targets as "<pooldir>/<rel>"; git wants <rel>.
		if i := strings.Index(w, "/"); i >= 0 {
			paths = append(paths, w[i+1:])
		}
	}
	peer, session := "unknown", p.SessionID
	if a := payloadAgent(agentsDir, p); a != nil {
		peer = a.Name
		if session == "" {
			session = ResolveAgentSessionID(a, os.Getpid(), now)
		}
	}
	if session == "" {
		session = "unknown"
	}
	trailer := "Peer: " + peer + "\nSession: " + session
	if p.ToolName != "" {
		trailer += "\nTool: " + p.ToolName
	}
	_, _ = commitPoolPaths(pool, paths, "", trailer)
}

// commitPoolPaths stages paths (relative to the pool repo) — skipping anything
// the ignore file covers — and commits only those paths, leaving any other
// uncommitted change in the repo alone. subject "" derives it from the staged
// diff (poolChangeItems); the remaining items and trailer go in the body.
// Returns false when nothing changed. Needs a repo with a first commit; before
// that, `pool upgrade`'s first commit carries everything.
func commitPoolPaths(pool string, paths []string, subject, trailer string) (bool, error) {
	if _, err := git(pool, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return false, nil
	}
	var keep []string
	for _, rel := range paths {
		if rel == "" || rel == "." || strings.HasPrefix(rel, ".git/") || rel == ".git" {
			continue
		}
		if _, err := git(pool, "check-ignore", "-q", "--", rel); err == nil {
			continue // ignored runtime/scratch state is never staged
		}
		// Per path, so one target that is neither on disk nor tracked (an
		// `rm` of an untracked file) does not abort the rest.
		if _, err := git(pool, "add", "-A", "--", rel); err == nil {
			keep = append(keep, rel)
		}
	}
	if len(keep) == 0 {
		return false, nil
	}
	out, err := git(pool, append([]string{"diff", "--cached", "--name-only", "--"}, keep...)...)
	if err != nil {
		return false, err
	}
	var changed []string
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		if ln != "" {
			changed = append(changed, ln)
		}
	}
	if len(changed) == 0 {
		return false, nil
	}
	var rest []string
	if subject == "" {
		var items []string
		for _, f := range changed {
			kind, names := poolChangeItems(pool, f)
			for _, n := range names {
				items = append(items, kind+": "+n)
			}
		}
		subject, rest = items[0], items[1:]
	}
	var body strings.Builder
	if len(rest) > 0 {
		body.WriteString("Also:\n")
		for _, r := range rest {
			body.WriteString("- " + r + "\n")
		}
		body.WriteString("\n")
	}
	body.WriteString(trailer)
	args := []string{"commit", "-q", "-m", subject}
	if b := strings.TrimSpace(body.String()); b != "" {
		args = append(args, "-m", b)
	}
	// Pathspec ⇒ `--only`: whatever else the human has staged stays out.
	args = append(append(args, "--"), changed...)
	if _, err := git(pool, args...); err != nil {
		return false, err
	}
	return true, nil
}

var (
	reYAMLTopKey = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.-]*)\s*:`)
	reHunk       = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)
)

// poolChangeItems names what a staged change to one pool file touched: its
// kind (glossary, doctrine, config, or pool for anything else) and, in order,
// the terms, sections or top-level keys. A changed term line, heading or key
// names itself; any other changed line names the entry it sits in. No name
// found ⇒ the file's path.
func poolChangeItems(pool, rel string) (kind string, names []string) {
	var primary, anchor func(string) (string, bool)
	heading := func(ln string) (string, bool) {
		if m := reAnyHeading.FindStringSubmatch(ln); m != nil {
			return m[2], true
		}
		return "", false
	}
	switch {
	case rel == PoolGlossaryFile:
		kind = "glossary"
		primary = func(ln string) (string, bool) {
			if m := reTermLine.FindStringSubmatch(ln); m != nil {
				return strings.TrimSpace(m[1]), true
			}
			return "", false
		}
		anchor = func(ln string) (string, bool) {
			if n, ok := primary(ln); ok {
				return n, true
			}
			return heading(ln)
		}
	case strings.HasPrefix(rel, DoctrineDirName+"/"):
		kind, primary, anchor = "doctrine", heading, heading
	case rel == PoolConfigFile:
		kind = "config"
		primary = func(ln string) (string, bool) {
			if m := reYAMLTopKey.FindStringSubmatch(ln); m != nil {
				return m[1], true
			}
			return "", false
		}
		anchor = primary
	default:
		return "pool", []string{rel}
	}

	diff, _ := git(pool, "diff", "--cached", "-U0", "--no-color", "--no-ext-diff", "--", rel)
	newLines := readLines(filepath.Join(pool, rel))
	enclosing := func(lineNo int) (string, bool) {
		for i := lineNo - 1; i >= 0 && i < len(newLines); i-- {
			if n, ok := anchor(newLines[i]); ok {
				return n, true
			}
		}
		return "", false
	}
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	type hunk struct {
		start, count  int
		added, remved []string
	}
	var hunks []*hunk
	for _, ln := range strings.Split(diff, "\n") {
		if m := reHunk.FindStringSubmatch(ln); m != nil {
			h := &hunk{count: 1}
			h.start, _ = strconv.Atoi(m[1])
			if m[2] != "" {
				h.count, _ = strconv.Atoi(m[2])
			}
			hunks = append(hunks, h)
			continue
		}
		if len(hunks) == 0 || strings.HasPrefix(ln, "+++") || strings.HasPrefix(ln, "---") {
			continue
		}
		h := hunks[len(hunks)-1]
		switch {
		case strings.HasPrefix(ln, "+"):
			h.added = append(h.added, ln[1:])
		case strings.HasPrefix(ln, "-"):
			h.remved = append(h.remved, ln[1:])
		}
	}
	// Changed term lines, headings or keys name themselves.
	for _, h := range hunks {
		for _, ln := range append(append([]string{}, h.added...), h.remved...) {
			if n, ok := primary(ln); ok {
				add(n)
			}
		}
	}
	if len(names) == 0 {
		// Otherwise each changed line names the entry it sits in.
		for _, h := range hunks {
			if h.count == 0 {
				if n, ok := enclosing(h.start); ok {
					add(n)
				}
				continue
			}
			for i, ln := range h.added {
				if strings.TrimSpace(ln) == "" {
					continue
				}
				if n, ok := enclosing(h.start + i); ok {
					add(n)
				}
			}
		}
	}
	if len(names) == 0 {
		names = []string{rel}
	}
	return kind, names
}

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// commitDoctrineUpgrade commits the doctrine files an upgrade rewrote (and the
// stamp file) as `doctrine: upgrade to <version>`, so `pool upgrade` leaves a
// reviewable diff. Inert on a pool that is not its own repo yet.
func commitDoctrineUpgrade(poolRoot string, written []string) error {
	pool := PoolDir(poolRoot)
	if len(written) == 0 || !poolRepoPresent(pool) {
		return nil
	}
	paths := make([]string, 0, len(written)+1)
	for _, n := range written {
		paths = append(paths, DoctrineDirName+"/"+n)
	}
	paths = append(paths, DoctrineDirName+"/"+doctrineStampFile)
	if _, err := commitPoolPaths(pool, paths, doctrineUpgradeSubject(doctrineVersion()), "Written by `meshctl pool upgrade`."); err != nil {
		return fmt.Errorf("doctrine upgraded but the commit in %s/ failed (%v) — set git user.name and user.email, then commit it by hand", PoolDirName(poolRoot), err)
	}
	return nil
}

// SessionAckFileExt is the extension of the per-session acknowledgement file
// beside the session's summary: <peer>/.memory/confirm/<session>.acks.
const SessionAckFileExt = ".acks"

// SessionAckPath is the per-session acknowledgement file: one hash per line,
// each a batch the human already confirmed in that session (ADR-0068 decision
// 37 uses the summary hash). Session-scoped; hygiene removes old ones.
func SessionAckPath(agentDir, session string) string {
	if session == "" {
		session = "no-session"
	}
	return filepath.Join(agentDir, MemoryDir, SummaryDir, sanitizeID(session)+SessionAckFileExt)
}

// RecordSessionAck records the (session, hash) pair. Idempotent.
func RecordSessionAck(agentDir, session, hash string) error {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return fmt.Errorf("empty ack hash")
	}
	if HasSessionAck(agentDir, session, hash) {
		return nil
	}
	p := SessionAckPath(agentDir, session)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(hash + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// HasSessionAck reports whether (session, hash) was recorded.
func HasSessionAck(agentDir, session, hash string) bool {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return false
	}
	for _, ln := range readLines(SessionAckPath(agentDir, session)) {
		if strings.TrimSpace(ln) == hash {
			return true
		}
	}
	return false
}
