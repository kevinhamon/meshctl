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

// ArchiveAnswered archives every request whose status is "answered" (the requester
// has, by convention, already read it back). Returns the archived request ids.
func ArchiveAnswered(agent *Agent) ([]string, error) {
	reqs, err := ListRequests(agent.InboxPath())
	if err != nil {
		return nil, err
	}
	var done []string
	for _, r := range reqs {
		if strings.EqualFold(r.Status, "answered") {
			if _, err := ArchiveRequest(agent, r.ID); err != nil {
				return done, err
			}
			done = append(done, r.ID)
		}
	}
	return done, nil
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
// working directory's basename.
func CallerName() string {
	if v := os.Getenv("AGENT_NAME"); v != "" {
		return v
	}
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Base(cwd)
	}
	return "unknown"
}

// WriteRequest validates target+type and writes a new intake file into the
// target's inbox. It is the shared step behind both `send` and `ask`. Returns
// the created request (with path + id populated).
//
// `now` is injected for deterministic tests.
func WriteRequest(target *Agent, reqType, prompt, from, priority string, related []string, now time.Time) (*Request, error) {
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

	inbox := target.InboxPath()
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		return nil, err
	}

	date := now.Format("2006-01-02")
	id := fmt.Sprintf("%s-%s-%s", date, from, Slug(prompt))
	fname := id + ".md"

	// Never clobber an existing request; disambiguate with a time suffix.
	full := filepath.Join(inbox, fname)
	if fileExists(full) {
		id = fmt.Sprintf("%s-%s", id, now.Format("150405"))
		fname = id + ".md"
		full = filepath.Join(inbox, fname)
	}

	r := &Request{
		ID:       id,
		From:     from,
		To:       target.Name,
		Type:     reqType,
		Created:  date,
		Status:   "pending",
		Priority: priority,
		Related:  related,
		path:     full,
	}

	relatedLines := "related: []"
	if len(related) > 0 {
		relatedLines = "related:\n"
		for _, x := range related {
			relatedLines += fmt.Sprintf("  - %s\n", x)
		}
		relatedLines = strings.TrimRight(relatedLines, "\n")
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
`, r.ID, r.From, r.To, r.Type, r.Created, r.Priority, relatedLines, prompt)

	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return r, nil
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
