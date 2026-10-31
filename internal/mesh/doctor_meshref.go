package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// doctor_meshref.go — knowledge-reference hygiene. Every `mesh://` link in a
// peer's knowledge base and in the pool glossary must resolve; a canonical
// peer's entry must never link to a perspective peer's entry. Both are
// warnings, never auto-fixed: a dangling link is fixed by hand, a
// canonical-to-perspective link is moved or dropped by the human.

// meshRefFindings walks every peer's knowledge/**/*.md (all tiers) and the
// pool glossary, one finding per (file, reference).
func meshRefFindings(root string, cfg PoolConfig, agents []*Agent) []DoctorFinding {
	byName := map[string]*Agent{}
	for _, a := range agents {
		byName[a.Name] = a
	}
	var out []DoctorFinding
	check := func(who, where string, canonical bool, body string) {
		for _, ref := range MeshRefsIn(body) {
			_, ok, err := ResolveMeshRef(root, ref)
			if err != nil || !ok {
				why := "nothing is there"
				if err != nil {
					why = err.Error()
				}
				out = append(out, DoctorFinding{Agent: who, Level: "warn", Message: fmt.Sprintf(
					"dangling knowledge reference %s in %s — %s; fix the link by hand (doctor never auto-fixes it)", ref, where, why)})
				continue
			}
			if !canonical {
				continue
			}
			r, _ := ParseMeshRef(ref)
			if t := byName[r.Owner]; t != nil && !IsCanonical(cfg, t) {
				out = append(out, DoctorFinding{Agent: who, Level: "warn", Message: fmt.Sprintf(
					"canonical entry %s references perspective peer %s: %s — a canonical entry never references a perspective entry; move the fact to a canonical owner or drop the link",
					where, r.Owner, ref)})
			}
		}
	}
	for _, a := range agents {
		kb := filepath.Join(a.Dir(), KnowledgeDir)
		canonical := IsCanonical(cfg, a)
		var files []string
		_ = filepath.WalkDir(kb, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
				files = append(files, p)
			}
			return nil
		})
		sort.Strings(files)
		for _, p := range files {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			rel, _ := filepath.Rel(a.Dir(), p)
			check(a.Name, filepath.ToSlash(rel), canonical, string(b))
		}
	}
	if b, err := os.ReadFile(PoolGlossaryPath(root)); err == nil {
		check(ReservedOwner, PoolGlossaryFile, true, string(b))
	}
	return out
}
