package mesh

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Canonical intake statuses (agent-comms.md). `pending` is set by send/ask; the
// inbox owner moves a request through the rest with `meshctl inbox respond`.
const (
	StatusPending    = "pending"
	StatusInProgress = "in-progress"
	StatusNeedsInfo  = "needs-info"
	StatusNeedsHuman = "needs-human"
	StatusAnswered   = "answered"
	StatusFiled      = "filed"
	StatusDeclined   = "declined"
)

var canonicalStatuses = []string{StatusPending, StatusInProgress, StatusNeedsInfo, StatusNeedsHuman, StatusAnswered, StatusFiled, StatusDeclined}

// statusAliases maps legacy/hand-written spellings found in real inboxes onto
// the canonical vocabulary, so old files keep working.
var statusAliases = map[string]string{
	"rejected":    StatusDeclined,
	"done":        StatusAnswered,
	"resolved":    StatusAnswered,
	"in_progress": StatusInProgress,
	"inprogress":  StatusInProgress,
	"needs_info":  StatusNeedsInfo,
	"needs_human": StatusNeedsHuman,
}

// CanonicalStatus normalizes a status to the canonical vocabulary. ok=false for
// a status that is neither canonical nor a known alias.
func CanonicalStatus(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, c := range canonicalStatuses {
		if s == c {
			return c, true
		}
	}
	if c, ok := statusAliases[s]; ok {
		return c, true
	}
	return s, false
}

// StatusClosed reports whether a status ends the exchange (no more owner work).
func StatusClosed(s string) bool {
	c, _ := CanonicalStatus(s)
	return c == StatusAnswered || c == StatusFiled || c == StatusDeclined
}

// statusAwaitsRequester reports whether the owner has handed the request back:
// closed, or blocked on the requester/human.
func statusAwaitsRequester(s string) bool {
	c, _ := CanonicalStatus(s)
	return StatusClosed(c) || c == StatusNeedsInfo || c == StatusNeedsHuman
}

// awaitingReadBack reports whether the requester still owes a read-back. Only
// responses written by `inbox respond` (stamped `responded`) count — requests
// closed by hand before read-back existed are grandfathered, so upgrading does
// not flood every session start with long-settled exchanges.
func awaitingReadBack(r *Request) bool {
	return r.Responded != "" && r.ReadBack == "" && statusAwaitsRequester(r.Status)
}

// RespondOptions parameterizes an owner's write-back.
type RespondOptions struct {
	Status   string // required; canonical or alias
	Response string // markdown appended to the body; required for closing/blocking statuses
	Session  string // the responding session (claim ownership check)
	Now      time.Time

	extra map[string]string // additional frontmatter stamps (e.g. redirected_to)
}

// RespondRequest is the owner's write-back: it sets status, stamps `responded`,
// and appends a response section. It refuses when another live session holds
// the claim, and releases this session's claim when the status closes the
// request. Intake is meshctl-owned (the PreToolUse gate blocks hand edits), so
// this is the only sanctioned way to write an outcome.
func RespondRequest(agent *Agent, id string, o RespondOptions) (*Request, error) {
	status, ok := CanonicalStatus(o.Status)
	if !ok {
		return nil, fmt.Errorf("unknown status %q (want one of: %s)", o.Status, strings.Join(canonicalStatuses, ", "))
	}
	if status == StatusPending {
		return nil, fmt.Errorf("status %q is set by send/ask, not by the owner", status)
	}
	if strings.TrimSpace(o.Response) == "" && status != StatusInProgress {
		return nil, fmt.Errorf("a response is required for status %q", status)
	}
	r, err := findRequest(agent, id)
	if err != nil {
		return nil, err
	}
	if err := claimBlocks(r, o.Session, o.Now); err != nil {
		return nil, err
	}

	heading := "Response"
	if status == StatusDeclined {
		heading = "Declined"
	}
	fields := map[string]string{"status": status, "responded": o.Now.Format("2006-01-02")}
	for k, v := range o.extra {
		fields[k] = v
	}
	if err := updateRequestFile(r.path, fields, heading, o.Response, agent.Name, o.Now); err != nil {
		return nil, err
	}
	if StatusClosed(status) {
		_ = ReleaseClaim(r.path, o.Session, o.Now)
	}
	return ReadRequest(r.path)
}

// DeclineOptions parameterizes a decline, optionally redirecting the request to
// the agent that actually owns it.
type DeclineOptions struct {
	Reason     string // required
	RedirectTo string // optional: agent to forward the request to
	Type       string // optional: request type for the redirect (default: the original's)
	Session    string
	Now        time.Time
}

// DeclineRequest declines a request that is not this agent's to handle. With a
// redirect target it re-sends the original ask to that agent (keeping the
// original requester as `from`, linking back via `related`), then declines the
// original pointing at the new request. The redirect is send-only — it never
// dispatches, so a headless peer declining cannot start a cascade.
func DeclineRequest(agentsDir string, agent *Agent, id string, o DeclineOptions) (orig, redirected *Request, err error) {
	if strings.TrimSpace(o.Reason) == "" {
		return nil, nil, fmt.Errorf("a decline needs a reason (--reason)")
	}
	r, err := findRequest(agent, id)
	if err != nil {
		return nil, nil, err
	}
	if StatusClosed(r.Status) {
		return nil, nil, fmt.Errorf("%s is already %s", id, r.Status)
	}
	// Check before redirecting, so a refused decline never leaves a duplicate
	// request in the redirect target's inbox.
	if err := claimBlocks(r, o.Session, o.Now); err != nil {
		return nil, nil, err
	}
	msg := o.Reason
	var extra map[string]string
	if o.RedirectTo != "" {
		if o.RedirectTo == agent.Name {
			return nil, nil, fmt.Errorf("cannot redirect %s to its own inbox", id)
		}
		target, err := FindAgent(agentsDir, o.RedirectTo)
		if err != nil {
			return nil, nil, err
		}
		typ := o.Type
		if typ == "" {
			typ = r.Type
		}
		redirected, err = writeRequest(target, typ, strings.TrimSpace(askSection(r.Body)), r.From, r.Priority, append([]string{r.ID}, r.Related...), o.Now, "redirect")
		if err != nil {
			return nil, nil, fmt.Errorf("redirect to %s: %w", o.RedirectTo, err)
		}
		note := fmt.Sprintf("Declined by **%s** as `%s` and forwarded here: %s", agent.Name, r.ID, o.Reason)
		if err := updateRequestFile(redirected.path, nil, "Redirected", note, agent.Name, o.Now); err != nil {
			return nil, redirected, err
		}
		msg += fmt.Sprintf("\n\nRedirected to **%s** as `%s`.", target.Name, redirected.ID)
		extra = map[string]string{"redirected_to": target.Name + ":" + redirected.ID}
	}
	orig, err = RespondRequest(agent, id, RespondOptions{Status: StatusDeclined, Response: msg, Session: o.Session, Now: o.Now, extra: extra})
	return orig, redirected, err
}

// ListSent returns requests sent by `from` across every agent's live inbox,
// oldest first. With pendingReadBack, only those the owner has handed back
// (closed / needs-info / needs-human) that the requester has not acknowledged.
func ListSent(agentsDir, from string, pendingReadBack bool) ([]*Request, error) {
	agents, _, err := ScanAgents(agentsDir)
	if err != nil {
		return nil, err
	}
	var out []*Request
	for _, a := range agents {
		reqs, err := ListRequests(a.InboxPath())
		if err != nil {
			continue
		}
		for _, r := range reqs {
			if r.From != from {
				continue
			}
			if pendingReadBack && !awaitingReadBack(r) {
				continue
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// AckSent records that the requester has read the owner's response back.
func AckSent(agentsDir, from, id string, now time.Time) (*Request, error) {
	sent, err := ListSent(agentsDir, from, false)
	if err != nil {
		return nil, err
	}
	for _, s := range sent {
		if s.ID == id {
			if err := setFrontmatterFields(s.path, map[string]string{"read_back": now.Format("2006-01-02")}); err != nil {
				return nil, err
			}
			s.ReadBack = now.Format("2006-01-02")
			return s, nil
		}
	}
	return nil, fmt.Errorf("no request %q sent by %q in any live inbox", id, from)
}

// SentMarkdown renders the read-back reminder the SessionStart hook injects.
func SentMarkdown(sent []*Request) string {
	if len(sent) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Responses to your requests (%d awaiting read-back)\n\n", len(sent))
	for _, s := range sent {
		st, _ := CanonicalStatus(s.Status)
		fmt.Fprintf(&b, "- `%s` → %s: **%s** (%s)\n", s.ID, s.To, st, s.path)
	}
	b.WriteString("\nRead each file, act on it (a `declined` one may name the right agent), then `meshctl sent ack <id>`.")
	return b.String()
}

// ArchiveClosed archives every closed request (answered/filed/declined, incl.
// aliases). Unless force, it skips ones the requester has not read back yet, so
// a decline is never swept away before the sender sees it. Returns archived and
// skipped ids.
func ArchiveClosed(agent *Agent, force bool) (archived, skipped []string, err error) {
	reqs, err := ListRequests(agent.InboxPath())
	if err != nil {
		return nil, nil, err
	}
	for _, r := range reqs {
		if !StatusClosed(r.Status) {
			continue
		}
		if !force && awaitingReadBack(r) && r.From != "user" && r.From != agent.Name {
			skipped = append(skipped, r.ID)
			continue
		}
		if _, err := ArchiveRequest(agent, r.ID); err != nil {
			return archived, skipped, err
		}
		archived = append(archived, r.ID)
	}
	return archived, skipped, nil
}

// InboxStatusFindings reports requests whose status is outside the canonical
// vocabulary (doctor, warn-level).
func InboxStatusFindings(agent *Agent) []string {
	reqs, _ := ListRequests(agent.InboxPath())
	var out []string
	for _, r := range reqs {
		if _, ok := CanonicalStatus(r.Status); !ok {
			out = append(out, fmt.Sprintf("intake %s has non-canonical status %q (want one of: %s) — fix with `meshctl inbox respond`", r.ID, r.Status, strings.Join(canonicalStatuses, ", ")))
		}
	}
	return out
}

// claimBlocks refuses when another live session holds the request's claim.
func claimBlocks(r *Request, session string, now time.Time) error {
	if c, err := readClaim(claimPath(r.path)); err == nil && c.OwnerSession != session && !reclaimable(r.path, c, now) {
		return fmt.Errorf("%s is claimed by live session %q — only the claim holder may respond", r.ID, c.OwnerSession)
	}
	return nil
}

func findRequest(agent *Agent, id string) (*Request, error) {
	reqs, err := ListRequests(agent.InboxPath())
	if err != nil {
		return nil, err
	}
	for _, r := range reqs {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, fmt.Errorf("no request %q in %s", id, agent.InboxPath())
}

var reAskHeading = regexp.MustCompile(`(?m)^##\s+Ask\s*$`)
var reNextHeading = regexp.MustCompile(`(?m)^##\s+`)

// askSection extracts the "## Ask" section of a request body (falls back to the
// whole body when there is no such heading).
func askSection(body string) string {
	loc := reAskHeading.FindStringIndex(body)
	if loc == nil {
		return body
	}
	rest := body[loc[1]:]
	if n := reNextHeading.FindStringIndex(rest); n != nil {
		rest = rest[:n[0]]
	}
	return rest
}

// updateRequestFile sets frontmatter fields and appends a titled section.
func updateRequestFile(path string, fields map[string]string, heading, text, by string, now time.Time) error {
	if err := setFrontmatterFields(path, fields); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := strings.TrimRight(string(b), "\n") +
		fmt.Sprintf("\n\n## %s — %s, %s\n\n%s\n", heading, by, now.Format("2006-01-02 15:04"), strings.TrimSpace(text))
	return os.WriteFile(path, []byte(s), 0o644)
}

// setFrontmatterFields replaces (or appends) top-level `key: value` lines in a
// request's frontmatter, leaving every other line — comments included — intact.
func setFrontmatterFields(path string, fields map[string]string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return fmt.Errorf("%s: no frontmatter", path)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return fmt.Errorf("%s: unterminated frontmatter", path)
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		line := k + ": " + fields[k]
		found := false
		for i := 1; i < end; i++ {
			if strings.HasPrefix(lines[i], k+":") {
				lines[i] = line
				found = true
				break
			}
		}
		if !found {
			lines = append(lines[:end], append([]string{line}, lines[end:]...)...)
			end++
		}
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}
