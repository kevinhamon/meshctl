package mesh

import (
	"fmt"
	"sort"
	"strings"
)

// knowledge_kind.go — a peer's knowledge kind.
// Canonical knowledge is part of the pool's one shared truth; perspective
// knowledge is a viewpoint, free to contradict. The kind is declared with one
// key at two levels: `knowledge:` in the pool config file (the default) and in
// a peer's manifest (the override). Absent at both levels means canonical.
// There is no per-entry metadata.

const (
	KnowledgeCanonical   = "canonical"
	KnowledgePerspective = "perspective"
)

// ValidKnowledgeKind reports whether s may appear as a `knowledge:` value.
// Empty (absent) is valid at both levels.
func ValidKnowledgeKind(s string) bool {
	return s == "" || s == KnowledgeCanonical || s == KnowledgePerspective
}

// EffectiveKnowledge is the kind a peer's knowledge resolves to: the
// manifest's value, else the pool config's, else canonical. An invalid value
// at either level is skipped as if absent (doctor errors on it), so a typo
// never silently makes a peer perspective.
func EffectiveKnowledge(cfg PoolConfig, a *Agent) string {
	for _, v := range []string{a.Knowledge, cfg.Settings.Knowledge} {
		if v != "" && ValidKnowledgeKind(v) {
			return v
		}
	}
	return KnowledgeCanonical
}

// IsCanonical reports whether the peer takes part in the pool's one shared
// truth (EffectiveKnowledge == canonical).
func IsCanonical(cfg PoolConfig, a *Agent) bool {
	return EffectiveKnowledge(cfg, a) == KnowledgeCanonical
}

// knowledgeKindProblem is the one message for an invalid `knowledge:` value,
// shared by manifest validation and the pool-config doctor finding.
func knowledgeKindProblem(v string) string {
	return fmt.Sprintf("knowledge %q is not a knowledge kind — use %s or %s (absent ⇒ %s)", v, KnowledgeCanonical, KnowledgePerspective, KnowledgeCanonical)
}

// canonicalOwnsFindings warns when two or more canonical peers declare the same
// `owns` tag (one fact owner per canonical fact). Perspective peers
// are exempt. Tags compare case-insensitively; one finding per shared tag,
// naming the peers in sorted order.
func canonicalOwnsFindings(cfg PoolConfig, agents []*Agent) []DoctorFinding {
	byTag := map[string][]string{}
	display := map[string]string{}
	for _, a := range agents {
		if !IsCanonical(cfg, a) {
			continue
		}
		seen := map[string]bool{}
		for _, tag := range a.Owns {
			k := strings.ToLower(strings.TrimSpace(tag))
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			if _, ok := display[k]; !ok {
				display[k] = strings.TrimSpace(tag)
			}
			byTag[k] = append(byTag[k], a.Name)
		}
	}
	tags := make([]string, 0, len(byTag))
	for k, names := range byTag {
		if len(names) > 1 {
			tags = append(tags, k)
		}
	}
	sort.Strings(tags)
	var out []DoctorFinding
	for _, k := range tags {
		names := byTag[k]
		sort.Strings(names)
		out = append(out, DoctorFinding{Agent: strings.Join(names, ","), Level: "warn", Message: fmt.Sprintf(
			"owns `%s` is declared by canonical peers %s — one fact owner per canonical fact: keep it on one peer, or declare the others `knowledge: perspective`",
			display[k], strings.Join(names, ", "))})
	}
	return out
}
