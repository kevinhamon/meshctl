package mesh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// recall_pool.go — the pool glossary as a recall source (ADR-0066, spec
// decision 15). Every peer sees the one shared vocabulary in recall as
// current-tier hits via the reserved owner, scored exactly like its own
// current tier (term name as the topic, no refs prior) so a term and the
// peer's own entry on the same subject rank side by side. Confirmed by
// construction (pool writes are gate-asked and committed by meshctl) unless
// the pool repo is dirty, which means something there has not been committed.

// poolGlossaryHits indexes `**Term**:` lines of the pool glossary against the
// query tokens. An empty query lists every term at score 0, as own topics do.
// A pool without a glossary yields nothing.
func poolGlossaryHits(root string, tokens []string) []RecallHit {
	p := PoolGlossaryPath(root)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	confirmed := len(gitDirtyPaths(PoolDir(root))) == 0
	date := ""
	if t, ok := poolFileDate(PoolDir(root), PoolGlossaryFile); ok {
		date = t.UTC().Format(time.RFC3339)
	}
	var hits []RecallHit
	for _, ln := range strings.Split(string(b), "\n") {
		m := reTermLine.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		slug := KebabSlug(m[1])
		if slug == "" {
			continue
		}
		score := 0
		if len(tokens) > 0 {
			score = tokenScore(slug, tokens)
			if score == 0 {
				continue
			}
			score = score*3 + rankBonus(TierCurrent, confirmed)
		}
		hits = append(hits, RecallHit{
			Kind: "kb", Title: slug,
			Where:     []string{MeshRef{Owner: ReservedOwner, Path: PoolGlossaryFile, Slug: slug}.String()},
			Date:      date,
			Score:     score,
			Tier:      TierCurrent,
			Via:       ReservedOwner,
			Confirmed: confirmed,
		})
	}
	return hits
}

// poolFileDate is a pool file's last-commit date, or its mtime when the pool
// is not a repo or the file has uncommitted changes (the edit is newer than
// the commit). The kbFileDate twin for files under the pool directory.
func poolFileDate(poolDir, file string) (time.Time, bool) {
	info, err := os.Stat(filepath.Join(poolDir, file))
	if err != nil {
		return time.Time{}, false
	}
	if poolRepoPresent(poolDir) && len(gitDirtyPaths(poolDir, file)) == 0 {
		out, err := exec.Command("git", "-C", poolDir, "log", "-1", "--format=%cI", "--", file).Output()
		if err == nil {
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(out))); err == nil {
				return t, true
			}
		}
	}
	return info.ModTime(), true
}
