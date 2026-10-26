package console

import (
	"fmt"
	"strings"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// AgentInfo is what a peer's card shows about the peer itself.
type AgentInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Role    string `json:"role,omitempty"` // first sentence, trimmed
	Harness string `json:"harness"`
	Badge   string `json:"badge,omitempty"`
	Color   string `json:"color,omitempty"` // CSS rgb() from the manifest badge
}

// Roster lists the pool's peers for the cards, in roster order.
func Roster(dir string) []AgentInfo {
	out := []AgentInfo{}
	agents, _, err := mesh.ScanAgents(dir)
	if err != nil {
		return out
	}
	for _, a := range agents {
		info := AgentInfo{Name: a.Name, Title: a.Title, Role: firstSentence(a.Role, 160), Harness: a.HarnessName(), Badge: a.Badge.Label}
		if a.Badge.R+a.Badge.G+a.Badge.B > 0 {
			c := func(v float64) int { return int(v*255 + 0.5) }
			info.Color = fmt.Sprintf("rgb(%d, %d, %d)", c(a.Badge.R), c(a.Badge.G), c(a.Badge.B))
		}
		out = append(out, info)
	}
	return out
}

func firstSentence(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	if len(s) > max {
		s = strings.TrimSpace(s[:max-1]) + "…"
	}
	return s
}
