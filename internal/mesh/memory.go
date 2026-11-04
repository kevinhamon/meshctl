package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Memory is a per-agent, generated,
// disposable index computed purely from the agent's knowledge/ tree + agent.yaml.
// It is advisory; the KB is authoritative. Deleting it and rebuilding fully
// reconstructs it — no durable state lives only here. No DB, no embeddings.
const (
	MemoryDir       = ".memory"
	MemoryIndexFile = "index.json"
	MemoryGenerated = "meshctl memory rebuild; do not edit"
	KnowledgeDir    = "knowledge"
)

// MemoryTopic is one routing entry: a normalized topic → the KB files it came
// from, plus the familiarity facts (freshness, refs). No learned "strength".
type MemoryTopic struct {
	Topic     string   `json:"topic"`
	Sources   []string `json:"sources"`
	Tags      []string `json:"tags,omitempty"`
	Freshness string   `json:"freshness"`
	Refs      int      `json:"refs"`
}

// MemoryIndex is the whole generated file. Pure function of knowledge/ + manifest.
type MemoryIndex struct {
	Generated      string        `json:"_generated"`
	Agent          string        `json:"agent"`
	BuiltFromMtime string        `json:"built_from_mtime"`
	ManifestTags   []string      `json:"manifest_tags"`
	Topics         []MemoryTopic `json:"topics"`
}

var (
	reHeading   = regexp.MustCompile(`(?m)^#{1,2}\s+(.+?)\s*$`)
	reNonAlnum  = regexp.MustCompile(`[^a-z0-9]+`)
	reNumPrefix = regexp.MustCompile(`^\d{2,}[-_]`)
)

// normTopic lowercases + kebab-cases; drops a leading numeric prefix (0009-...).
func normTopic(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = reNumPrefix.ReplaceAllString(s, "")
	s = reNonAlnum.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// frontMatterTags returns normalized `tags:` from a leading --- block (list or
// inline [a, b]); empty if there is no front matter.
func frontMatterTags(body string) []string {
	if !strings.HasPrefix(body, "---") {
		return nil
	}
	end := strings.Index(body[3:], "\n---")
	if end < 0 {
		return nil
	}
	fm := body[3 : 3+end]
	var out []string
	lines := strings.Split(fm, "\n")
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, "tags:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(t, "tags:"))
		if strings.HasPrefix(rest, "[") { // inline list
			rest = strings.Trim(rest, "[]")
			for _, p := range strings.Split(rest, ",") {
				if v := normTopic(p); v != "" {
					out = append(out, v)
				}
			}
		} else { // block list on following "- x" lines
			for _, nl := range lines[i+1:] {
				s := strings.TrimSpace(nl)
				if !strings.HasPrefix(s, "- ") {
					break
				}
				if v := normTopic(strings.TrimPrefix(s, "- ")); v != "" {
					out = append(out, v)
				}
			}
		}
		break
	}
	return out
}

// MemoryRebuild computes the KB topic index and writes it to index.json via
// temp+atomic-rename. Idempotent. The persisted index is the cache every
// recall path reads through topicIndex; a rebuild only forces it fresh.
func MemoryRebuild(agent *Agent) (*MemoryIndex, error) {
	idx, err := computeTopics(agent)
	if err != nil {
		return nil, err
	}
	if err := writeMemoryIndex(agent.Dir(), idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// topicIndex is the topic index every recall reads: the persisted index.json
// when it was built from the knowledge tree as it is now, else a fresh
// computeTopics, persisted for the next caller. The per-prompt hook, the gate
// and cross-peer recall all route through here, so the cost of a recall with
// an unchanged KB is one stat walk and one JSON read instead of reading every
// note and the O(n²) cross-reference pass computeTopics runs.
//
// Freshness is exact, not "newer than": the index names the newest
// modification time (nanoseconds) across the knowledge tree, its directories
// and the manifest, and any difference rebuilds. Directory times cover
// deletions and renames, which touch no file; the manifest covers the tags.
func topicIndex(agent *Agent) (*MemoryIndex, error) {
	stamp := kbNewestMtime(agent)
	if idx, err := LoadMemoryIndex(agent); err == nil && idx.Agent == agent.Name && idx.BuiltFromMtime != "" && idx.BuiltFromMtime == stamp {
		return idx, nil
	}
	idx, err := computeTopics(agent)
	if err != nil {
		return nil, err
	}
	_ = writeMemoryIndex(agent.Dir(), idx) // best-effort cache; the result is good either way
	return idx, nil
}

// kbNewestMtime is the freshness stamp topicIndex compares: the newest
// modification time over knowledge/ (files and directories) and agent.yaml,
// RFC 3339 with nanoseconds; "" when there is no knowledge tree.
func kbNewestMtime(agent *Agent) string {
	var newest time.Time
	bump := func(info os.FileInfo) {
		if info != nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	kb := filepath.Join(agent.Dir(), KnowledgeDir)
	_ = filepath.WalkDir(kb, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || strings.HasSuffix(p, ".md") {
			info, _ := d.Info()
			bump(info)
		}
		return nil
	})
	if newest.IsZero() {
		return ""
	}
	if info, err := os.Stat(filepath.Join(agent.Dir(), ManifestName)); err == nil {
		bump(info)
	}
	return newest.UTC().Format(time.RFC3339Nano)
}

// computeTopics scans <agent>/knowledge/**/*.md + the manifest and returns the
// topic index WITHOUT writing it — the live-recall path and the
// persisted rebuild share this pure computation.
func computeTopics(agent *Agent) (*MemoryIndex, error) {
	root := agent.Dir()
	kb := filepath.Join(root, KnowledgeDir)
	if !dirExists(kb) {
		return nil, fmt.Errorf("no %s/ dir for agent %s", KnowledgeDir, agent.Name)
	}

	type fileInfo struct {
		rel    string
		stem   string
		mtime  time.Time
		body   string
		topics []string
	}
	var files []fileInfo
	// Stamped before the walk: a note written while we read is newer than the
	// stamp, so the next topicIndex rebuilds rather than trusting this pass.
	stamp := kbNewestMtime(agent)

	// Superseded/deprecated/rejected decisions are history, not knowledge:
	// they never enter the topic index, so recall cannot route to them.
	inactive := inactiveKBFiles(kb)

	err := filepath.WalkDir(kb, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		if kbRel, _ := filepath.Rel(kb, p); inactive[filepath.ToSlash(kbRel)] || kbRel == HandoffFile {
			// The handoff is injected whole at session start; its headings
			// are session-local and would pollute the topic space.
			return nil
		}
		info, _ := d.Info()
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		stem := strings.TrimSuffix(filepath.Base(p), ".md")
		body := string(b)
		ts := entryTopics(stem, body)
		mt := time.Time{}
		if info != nil {
			mt = info.ModTime()
		}
		files = append(files, fileInfo{rel: rel, stem: stem, mtime: mt, body: body, topics: ts})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// refs: for each file stem, how many OTHER KB files mention it.
	mentions := map[string]int{}
	for _, f := range files {
		for _, g := range files {
			if g.rel == f.rel {
				continue
			}
			if strings.Contains(g.body, f.stem) {
				mentions[f.stem]++
			}
		}
	}

	manifestTags := normSet(append(append([]string{}, agent.Owns...), agent.Domains...))

	// merge topics across files
	type agg struct {
		sources map[string]bool
		fresh   time.Time
		refs    int
	}
	byTopic := map[string]*agg{}
	for _, f := range files {
		for _, t := range f.topics {
			a := byTopic[t]
			if a == nil {
				a = &agg{sources: map[string]bool{}}
				byTopic[t] = a
			}
			a.sources[f.rel] = true
			if f.mtime.After(a.fresh) {
				a.fresh = f.mtime
			}
			if mentions[f.stem] > a.refs {
				a.refs = mentions[f.stem]
			}
		}
	}

	idx := &MemoryIndex{
		Generated:      MemoryGenerated,
		Agent:          agent.Name,
		BuiltFromMtime: stamp,
		ManifestTags:   manifestTags,
	}
	for topic, a := range byTopic {
		var srcs []string
		for s := range a.sources {
			srcs = append(srcs, s)
		}
		sort.Strings(srcs)
		var tags []string
		for _, mt := range manifestTags {
			if strings.Contains(topic, mt) || mentionsInSources(mt, srcs) {
				tags = append(tags, mt)
			}
		}
		fr := ""
		if !a.fresh.IsZero() {
			fr = a.fresh.UTC().Format(time.RFC3339)
		}
		idx.Topics = append(idx.Topics, MemoryTopic{Topic: topic, Sources: srcs, Tags: tags, Freshness: fr, Refs: a.refs})
	}
	sort.Slice(idx.Topics, func(i, j int) bool { return idx.Topics[i].Topic < idx.Topics[j].Topic })
	return idx, nil
}

// entryTopics is one KB file's topic set: front-matter tags ∪ H1/H2
// headings ∪ filename stem, normalized (normTopic), sorted.
func entryTopics(stem, body string) []string {
	set := map[string]bool{}
	for _, t := range frontMatterTags(body) {
		set[t] = true
	}
	for _, m := range reHeading.FindAllStringSubmatch(body, -1) {
		if t := normTopic(m[1]); t != "" {
			set[t] = true
		}
	}
	if t := normTopic(stem); t != "" {
		set[t] = true
	}
	ts := make([]string, 0, len(set))
	for t := range set {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	return ts
}

func mentionsInSources(tag string, sources []string) bool {
	for _, s := range sources {
		if strings.Contains(s, tag) {
			return true
		}
	}
	return false
}

func normSet(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if v := normTopic(s); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// writeMemoryIndex writes index.json atomically (temp + rename), so a lost
// concurrent-rebuild race self-heals rather than corrupting the file.
func writeMemoryIndex(root string, idx *MemoryIndex) error {
	dir := filepath.Join(root, MemoryDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	tmp.Close()
	return os.Rename(tmpName, filepath.Join(dir, MemoryIndexFile))
}

// LoadMemoryIndex reads a prebuilt index (recall path).
func LoadMemoryIndex(agent *Agent) (*MemoryIndex, error) {
	p := filepath.Join(agent.Dir(), MemoryDir, MemoryIndexFile)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("no memory index (%s) — run `meshctl memory rebuild`", p)
	}
	var idx MemoryIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("corrupt memory index %s: %w", p, err)
	}
	return &idx, nil
}

// MemoryRecall matches a query against topics+tags and returns locations +
// familiarity facts, ranked by refs then freshness — WITHOUT reading source
// bodies (recall routes, the KB read that follows answers).
func MemoryRecall(agent *Agent, query string) ([]MemoryTopic, error) {
	idx, err := topicIndex(agent)
	if err != nil {
		return nil, err
	}
	// Tokenize the query; a topic matches if any token appears in its topic or a
	// tag (substring both ways). Score = distinct tokens matched → rank by score,
	// then refs, then freshness.
	var tokens []string
	for _, tok := range strings.Split(normTopic(query), "-") {
		if len(tok) >= 3 {
			tokens = append(tokens, tok)
		}
	}
	type scored struct {
		t     MemoryTopic
		score int
	}
	var out []scored
	for _, t := range idx.Topics {
		if len(tokens) == 0 { // empty query → everything
			out = append(out, scored{t, 0})
			continue
		}
		hitset := map[string]bool{}
		hay := append([]string{t.Topic}, t.Tags...)
		for _, tok := range tokens {
			for _, h := range hay {
				if strings.Contains(h, tok) || strings.Contains(tok, h) {
					hitset[tok] = true
					break
				}
			}
		}
		if len(hitset) > 0 {
			out = append(out, scored{t, len(hitset)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].t.Refs != out[j].t.Refs {
			return out[i].t.Refs > out[j].t.Refs
		}
		return out[i].t.Freshness > out[j].t.Freshness
	})
	hits := make([]MemoryTopic, len(out))
	for i, s := range out {
		hits[i] = s.t
	}
	return hits, nil
}

// MemoryStale reports whether the index is missing or was not built from the
// knowledge tree as it is now (doctor staleness check; warn-only, and
// self-healing, since every recall rebuilds through topicIndex).
func MemoryStale(agent *Agent) (stale bool, reason string) {
	idx, err := LoadMemoryIndex(agent)
	if err != nil {
		return true, "no memory index"
	}
	if idx.BuiltFromMtime == "" {
		return true, "index has no build mtime"
	}
	if idx.BuiltFromMtime != kbNewestMtime(agent) {
		return true, "knowledge changed since the index was built"
	}
	return false, ""
}
