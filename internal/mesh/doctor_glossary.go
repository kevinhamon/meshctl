package mesh

import (
	"os"
	"path/filepath"
	"strings"
)

// doctor_glossary.go — peer glossaries retire. The pool glossary is the one
// vocabulary; a peer's legacy knowledge/glossary.md stays indexed as a
// current-tier state doc until it is migrated by hand, and doctor keeps both
// conditions in view. No config, no migrate command.

// PeerGlossaryFile is a peer's legacy glossary, relative to its knowledge/.
// The KB template no longer scaffolds it.
const PeerGlossaryFile = "glossary.md"

// poolGlossaryTermSlugs is the set of kebab slugs of the pool glossary's
// `**Term**:` lines, and whether the glossary file exists at all. An existing
// glossary with no term lines (the onboard template) yields an empty set.
func poolGlossaryTermSlugs(poolRoot string) (map[string]bool, bool) {
	b, err := os.ReadFile(PoolGlossaryPath(poolRoot))
	if err != nil {
		return nil, false
	}
	slugs := map[string]bool{}
	for _, ln := range strings.Split(string(b), "\n") {
		if m := reTermLine.FindStringSubmatch(ln); m != nil {
			if s := KebabSlug(m[1]); s != "" {
				slugs[s] = true
			}
		}
	}
	return slugs, true
}

// glossaryFindings is the per-peer glossary check. terms and poolExists come
// from poolGlossaryTermSlugs, read once per doctor run.
//   - Leftover peer glossary while the pool glossary exists → warn, naming
//     the by-hand migration.
//   - An `owns` value whose kebab slug matches no pool term → soft warn, only
//     when the pool glossary has terms and no legacy peer glossary remains
//     (until then the term may still be waiting in the peer's own file).
func glossaryFindings(a *Agent, terms map[string]bool, poolExists bool) []DoctorFinding {
	if !poolExists {
		return nil
	}
	if fileExists(filepath.Join(a.Dir(), KnowledgeDir, PeerGlossaryFile)) {
		return []DoctorFinding{{Agent: a.Name, Level: "warn", Message: "still has " + KnowledgeDir + "/" + PeerGlossaryFile +
			" — the pool glossary (mesh://" + ReservedOwner + "/" + PoolGlossaryFile + ") is the one vocabulary now; migrate its terms by hand" +
			" (consistent sections, batches of five, one confirmation round, pool edit then `meshctl kb finish` deleting them)"}}
	}
	if len(terms) == 0 {
		return nil
	}
	var missing []string
	for _, o := range a.Owns {
		if s := KebabSlug(o); s != "" && !terms[s] {
			missing = append(missing, "`"+o+"`")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []DoctorFinding{{Agent: a.Name, Level: "warn", Message: "owns " + strings.Join(missing, ", ") +
		" matches no pool glossary term — consider adding the term to the pool glossary or renaming the owns value"}}
}
