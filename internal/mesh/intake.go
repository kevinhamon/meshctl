package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Request is the intake file schema shared across the mesh (agent-comms.md).
// The file is the durable, greppable, git-tracked audit trail.
type Request struct {
	ID       string   `yaml:"id"`
	From     string   `yaml:"from"`
	To       string   `yaml:"to"`
	Type     string   `yaml:"type"`
	Created  string   `yaml:"created"`
	Status   string   `yaml:"status"`
	Priority string   `yaml:"priority"`
	Related  []string `yaml:"related"`
	Response string   `yaml:"response"`

	// Set by meshctl on write-back: `inbox respond` stamps Responded, a
	// redirecting decline records RedirectedTo (<agent>:<id>), and the requester's
	// `sent ack` stamps ReadBack.
	Responded    string `yaml:"responded,omitempty"`
	RedirectedTo string `yaml:"redirected_to,omitempty"`
	ReadBack     string `yaml:"read_back,omitempty"`

	// Set by `ask --in-session --work` (ADR-0063): Mode "work" marks delegated
	// write-capable work; Worktree is the peer's delegated-work checkout when
	// its own session was live at dispatch.
	Mode     string `yaml:"mode,omitempty"`
	Worktree string `yaml:"worktree,omitempty"`

	// path is the on-disk file (set on read); Body is the markdown after the
	// frontmatter.
	path string `yaml:"-"`
	Body string `yaml:"-"`
}

// Path returns the file this request was read from / written to.
func (r *Request) Path() string { return r.path }

// IntakeArchiveDir is the local, git-ignored graveyard for closed intake requests
// (ADR-0043). Intake is an ephemeral discussion; its durable residue is distilled
// into the KB before archiving. Archiving (not deleting) keeps a recoverable trail
// without polluting git or the live inbox.
const IntakeArchiveDir = ".intake-archive"

// ArchiveRequest moves a closed intake request out of the live inbox into
// <agent>/.intake-archive/, and removes its runtime .claim sidecar. Returns the
// archived path. (Distill the durable residue into the KB *before* calling this —
// ADR-0043; the archive is a recoverable trail, not the record.)
func ArchiveRequest(agent *Agent, id string) (string, error) {
	reqs, err := ListRequests(agent.InboxPath())
	if err != nil {
		return "", err
	}
	for _, r := range reqs {
		if r.ID != id {
			continue
		}
		dst := filepath.Join(agent.Dir(), IntakeArchiveDir)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return "", err
		}
		_ = os.Remove(r.path + ".claim") // drop the runtime lock, don't archive it
		out := filepath.Join(dst, filepath.Base(r.path))
		if err := os.Rename(r.path, out); err != nil {
			return "", err
		}
		return out, nil
	}
	return "", fmt.Errorf("no request %q in %s", id, agent.InboxPath())
}

// ArchiveAnswered archives every request whose status is "answered". Unless
// force, it keeps ones the requester has not read back yet (`meshctl sent ack`):
// an archived request drops out of the requester's `sent` view, so sweeping it
// early would lose the answer. Returns archived and skipped ids.
func ArchiveAnswered(agent *Agent, force bool) (archived, skipped []string, err error) {
	reqs, err := ListRequests(agent.InboxPath())
	if err != nil {
		return nil, nil, err
	}
	for _, r := range reqs {
		if c, _ := CanonicalStatus(r.Status); c != StatusAnswered {
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

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug normalizes a prompt into a short filename-safe slug.
func Slug(s string) string {
	s = strings.ToLower(s)
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
		s = strings.Trim(s, "-")
	}
	if s == "" {
		s = "request"
	}
	return s
}

// CallerName resolves the requesting agent name: $AGENT_NAME, else the current
// working directory's basename. A value that is not a safe identifier falls
// back to "unknown" rather than reaching a filename or frontmatter line.
func CallerName() string {
	if v := os.Getenv("AGENT_NAME"); v != "" && ValidName("agent name", v) == nil {
		return v
	}
	if cwd, err := os.Getwd(); err == nil {
		if b := filepath.Base(cwd); ValidName("agent name", b) == nil {
			return b
		}
	}
	return "unknown"
}

// validPriorities is the request priority vocabulary.
var validPriorities = map[string]bool{"P1": true, "P2": true, "P3": true}

// reRelatedRef bounds a `related:` entry: a ticket/ADR/risk key or a request id,
// optionally agent-qualified (<agent>:<id>). One line, no YAML punctuation.
var reRelatedRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/#-]{0,127}$`)

// yamlQuote renders s as a double-quoted YAML scalar, so a value can never be
// read as structure (a colon, a hash, a leading dash) or span lines.
func yamlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", "")
	return `"` + s + `"`
}

// validateRequestFields checks the caller-supplied header values before any of
// them reaches a filename or a frontmatter line.
func validateRequestFields(reqType, from, priority string, related []string) error {
	if err := ValidName("request type", reqType); err != nil {
		return err
	}
	if err := ValidName("requester name", from); err != nil {
		return err
	}
	if !validPriorities[priority] {
		return fmt.Errorf("invalid priority %q: use P1, P2 or P3", priority)
	}
	for _, x := range related {
		if !reRelatedRef.MatchString(x) {
			return fmt.Errorf("invalid related ref %q: one token per ref (letters, digits, . _ : / # -), no spaces or newlines", x)
		}
	}
	return nil
}

// WriteRequest validates target+type and writes a new intake file into the
// target's inbox. It is the shared step behind both `send` and `ask`. Returns
// the created request (with path + id populated).
//
// `now` is injected for deterministic tests.
func WriteRequest(target *Agent, reqType, prompt, from, priority string, related []string, now time.Time) (*Request, error) {
	return writeRequest(target, reqType, prompt, from, priority, related, now, "")
}

// writeRequest is WriteRequest with an optional id suffix (a redirect uses one
// so its id never equals the original's, keeping `sent ack <id>` unambiguous).
func writeRequest(target *Agent, reqType, prompt, from, priority string, related []string, now time.Time, idSuffix string) (*Request, error) {
	if !target.AcceptsType(reqType) {
		var types []string
		for _, a := range target.Accepts {
			types = append(types, a.Type)
		}
		return nil, fmt.Errorf("agent %q does not accept type %q (accepts: %s)", target.Name, reqType, strings.Join(types, ", "))
	}
	if priority == "" {
		priority = "P2"
	}
	if err := validateRequestFields(reqType, from, priority, related); err != nil {
		return nil, err
	}
	if idSuffix != "" {
		if err := ValidName("id suffix", idSuffix); err != nil {
			return nil, err
		}
	}

	inbox := target.InboxPath()
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		return nil, err
	}

	date := now.Format("2006-01-02")
	base := fmt.Sprintf("%s-%s-%s", date, from, Slug(prompt))
	if idSuffix != "" {
		base += "-" + idSuffix
	}

	relatedLines := "related: []"
	if len(related) > 0 {
		relatedLines = "related:\n"
		for _, x := range related {
			relatedLines += "  - " + yamlQuote(x) + "\n"
		}
		relatedLines = strings.TrimRight(relatedLines, "\n")
	}

	// Never clobber an existing request. The file is created O_EXCL so two
	// writers racing on the same id cannot both "see it absent" and overwrite
	// each other; a clash falls back to a time suffix, then a counter.
	var r *Request
	for attempt := 0; ; attempt++ {
		id := base
		switch {
		case attempt == 1:
			id = fmt.Sprintf("%s-%s", base, now.Format("150405"))
		case attempt > 1:
			id = fmt.Sprintf("%s-%s-%d", base, now.Format("150405"), attempt)
		}
		full := filepath.Join(inbox, id+".md")
		r = &Request{
			ID: id, From: from, To: target.Name, Type: reqType, Created: date,
			Status: "pending", Priority: priority, Related: related, path: full,
		}
		content := fmt.Sprintf(`---
id: %s
from: %s
to: %s
type: %s
created: %s
status: pending
priority: %s
%s
response:
---

## Ask

%s

## Context

_(none supplied)_
`, yamlQuote(r.ID), yamlQuote(r.From), yamlQuote(r.To), yamlQuote(r.Type), r.Created, r.Priority, relatedLines, prompt)

		f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) && attempt < 50 {
				continue
			}
			return nil, err
		}
		_, werr := f.WriteString(content)
		cerr := f.Close()
		if werr != nil {
			return nil, werr
		}
		if cerr != nil {
			return nil, cerr
		}
		return r, nil
	}
}

// ReadRequest parses an intake file's frontmatter + body.
func ReadRequest(path string) (*Request, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, body := splitFrontmatter(string(b))
	var r Request
	if err := decodeYAML(fm, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	r.path = path
	r.Body = body
	return &r, nil
}

// ListRequests returns every *.md request file in an inbox (skips README.md and
// non-request files), sorted by filename.
func ListRequests(inbox string) ([]*Request, error) {
	entries, err := os.ReadDir(inbox)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Request
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".md") || strings.EqualFold(n, "README.md") {
			continue
		}
		r, err := ReadRequest(filepath.Join(inbox, n))
		if err != nil {
			continue // skip unparseable files; doctor reports separately
		}
		out = append(out, r)
	}
	return out, nil
}
