package mesh

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// meshref.go — knowledge references (ADR-0065, ADR-0066; spec decision 14).
// A `mesh://<owner>/<path>#<slug>` names one entry in an owner's knowledge.
// `pool` is the reserved owner: the pool's own knowledge (glossary, local
// doctrine) under the pool directory, so a peer can never carry that name.
// Peer owners (`mesh://<peer>/<kb-path>#<slug>`) resolve against the peer's
// knowledge base (ADR-0065); whether a reader may follow one (canonical
// targets only) is the caller's rule, not the resolver's.

// ReservedOwner is the owner name of pool knowledge in a `mesh://` reference.
// No peer may be called this (ValidAgentName, doctor).
const ReservedOwner = "pool"

// MeshRefScheme prefixes every knowledge reference.
const MeshRefScheme = "mesh://"

// MeshRef is a parsed knowledge reference: the owner whose knowledge holds the
// entry, the file path inside that knowledge, and the optional kebab slug of a
// `**Term**:` line or heading in it.
type MeshRef struct {
	Owner string
	Path  string // slash-separated, relative to the owner's knowledge root
	Slug  string // "" means the whole file
}

// String renders the reference back in its canonical form.
func (r MeshRef) String() string {
	s := MeshRefScheme + r.Owner + "/" + r.Path
	if r.Slug != "" {
		s += "#" + r.Slug
	}
	return s
}

// ParseMeshRef splits `mesh://<owner>/<path>#<slug>` and rejects anything that
// could leave the owner's knowledge root (absolute paths, `..`) or forge an
// owner name. The slug is kept as written; matching is case-insensitive.
func ParseMeshRef(ref string) (MeshRef, error) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, MeshRefScheme) {
		return MeshRef{}, fmt.Errorf("not a knowledge reference %q: expected %s<owner>/<path>#<slug>", ref, MeshRefScheme)
	}
	rest := strings.TrimPrefix(ref, MeshRefScheme)
	var slug string
	if i := strings.Index(rest, "#"); i >= 0 {
		rest, slug = rest[:i], rest[i+1:]
	}
	owner, p, found := strings.Cut(rest, "/")
	if err := ValidName("reference owner", owner); err != nil {
		return MeshRef{}, fmt.Errorf("knowledge reference %q: %v", ref, err)
	}
	if !found || p == "" {
		return MeshRef{}, fmt.Errorf("knowledge reference %q has no path: expected %s%s/<path>#<slug>", ref, MeshRefScheme, owner)
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return MeshRef{}, fmt.Errorf("knowledge reference %q: path must be relative to the owner's knowledge", ref)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return MeshRef{}, fmt.Errorf("knowledge reference %q: path must not contain empty, '.' or '..' segments", ref)
		}
	}
	return MeshRef{Owner: owner, Path: path.Clean(p), Slug: strings.TrimSpace(slug)}, nil
}

// meshRefBase is the directory an owner's reference paths are relative to.
// The reserved owner maps to the pool directory (glossary, doctrine/, config);
// a peer maps to its knowledge base (`<peer>/knowledge/`), so a peer
// reference can never reach its manifest, `.memory/` or session records. A
// name that is no peer of the pool is an error: the reference cannot resolve.
func meshRefBase(root, owner string) (string, error) {
	if owner == ReservedOwner {
		return PoolDir(root), nil
	}
	a, err := FindAgent(root, owner)
	if err != nil {
		return "", fmt.Errorf("knowledge reference owner %q is neither the reserved owner %q nor a peer of this pool", owner, ReservedOwner)
	}
	return filepath.Join(a.Dir(), KnowledgeDir), nil
}

// ResolveMeshRef reads the entry a `mesh://` reference names under the pool at
// root: pool knowledge or a peer's knowledge base. With no slug the whole file is the text; with one, the matching
// `**Term**:` entry (through the next term or heading) or the heading's section
// (through the next heading of the same or a higher level), matched by kebab
// slug, case-insensitively. ok=false with err=nil means the reference is well
// formed but nothing is there (unknown slug, missing file): unresolved, which
// is a finding, not a failure. err is a malformed or unsupported reference.
func ResolveMeshRef(root, ref string) (text string, ok bool, err error) {
	r, err := ParseMeshRef(ref)
	if err != nil {
		return "", false, err
	}
	base, err := meshRefBase(root, r.Owner)
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(r.Path)))
	if err != nil {
		return "", false, nil
	}
	if r.Slug == "" {
		return string(b), true, nil
	}
	text, ok = sectionBySlug(string(b), r.Slug)
	return text, ok, nil
}

var (
	reTermLine    = regexp.MustCompile(`(?m)^\*\*([^*]+?)\*\*\s*:`) // multi-line: FindAll over a body too
	reAnyHeading  = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	reSlugNonWord = regexp.MustCompile(`[^a-z0-9]+`)
)

// KebabSlug is the anchor form of a term or heading: lowercase, every run of
// non-alphanumerics collapsed to one hyphen, no leading or trailing hyphen.
// Unlike normTopic it keeps a leading number ("2026 plan" → "2026-plan"), since
// a slug must round-trip the text it was made from.
func KebabSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = reSlugNonWord.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// sectionBySlug finds the `**Term**:` entry or heading whose kebab slug equals
// slug (case-insensitively) and returns its text. A term runs to the next term
// line or heading; a heading section runs to the next heading of the same or
// a higher level. Terms are tried first: a glossary group heading and a term
// never share a slug in practice, and the term is the finer unit.
func sectionBySlug(body, slug string) (string, bool) {
	want := KebabSlug(slug)
	if want == "" {
		return "", false
	}
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		if m := reTermLine.FindStringSubmatch(ln); m != nil && KebabSlug(m[1]) == want {
			end := len(lines)
			for j := i + 1; j < len(lines); j++ {
				if reTermLine.MatchString(lines[j]) || reAnyHeading.MatchString(lines[j]) {
					end = j
					break
				}
			}
			return strings.TrimSpace(strings.Join(lines[i:end], "\n")), true
		}
	}
	for i, ln := range lines {
		m := reAnyHeading.FindStringSubmatch(ln)
		if m == nil || KebabSlug(m[2]) != want {
			continue
		}
		level := len(m[1])
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if h := reAnyHeading.FindStringSubmatch(lines[j]); h != nil && len(h[1]) <= level {
				end = j
				break
			}
		}
		return strings.TrimSpace(strings.Join(lines[i:end], "\n")), true
	}
	return "", false
}

// reMeshRefLink finds a knowledge reference in prose: the scheme, an owner and
// a path, ending at whitespace or markdown/HTML delimiters. Trailing sentence
// punctuation is trimmed by MeshRefsIn.
var (
	reMeshRefLink   = regexp.MustCompile(`mesh://[^\s()\[\]<>"'` + "`" + `]+`)
	reFencedCode    = regexp.MustCompile("(?ms)^\\s*(```|~~~).*?^\\s*(```|~~~)[^\\n]*$")
	reInlineCodeRun = regexp.MustCompile("`[^`\\n]*`")
)

// MeshRefsIn lists the knowledge references a markdown body carries, in order
// of first appearance, deduplicated. Text in fenced code blocks and inline
// code spans is an example, not a link (doctrine and templates write
// `mesh://<peer>/<path>#<slug>` that way), so it is skipped.
func MeshRefsIn(body string) []string {
	body = reFencedCode.ReplaceAllString(body, "")
	body = reInlineCodeRun.ReplaceAllString(body, "")
	seen := map[string]bool{}
	var out []string
	for _, m := range reMeshRefLink.FindAllString(body, -1) {
		m = strings.TrimRight(m, ".,;:!?*_")
		if m == MeshRefScheme || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}
