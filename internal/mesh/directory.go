package mesh

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The marker wrapping any --write snapshot so humans + tooling know it is
// generated and never authoritative (the live command is the source of truth).
const (
	GeneratedBegin = "<!-- BEGIN meshctl agent list (generated — do not edit) -->"
	GeneratedEnd   = "<!-- END meshctl agent list -->"
)

// DirectoryMarkdown renders the roster as the table format used in
// agent-comms.md, computed live from manifests.
func DirectoryMarkdown(agents []*Agent) string {
	var b strings.Builder
	fmt.Fprintln(&b, "| Agent | Inbox | Owns | Accepts | Dispatch |")
	fmt.Fprintln(&b, "|-------|-------|------|---------|----------|")
	for _, a := range agents {
		var types []string
		for _, x := range a.Accepts {
			types = append(types, x.Type)
		}
		dispatch := "send-only"
		if a.IsDispatchable() {
			dispatch = "ask+send"
		}
		if len(a.Mutates) > 0 {
			dispatch = fmt.Sprintf("send-only (mutates: %s)", strings.Join(a.Mutates, ","))
		}
		fmt.Fprintf(&b, "| **%s** | `%s/` | %s | %s | %s |\n",
			a.Name,
			strings.TrimRight(firstNonEmpty(a.Inbox, IntakeDir), "/"),
			strings.Join(a.Owns, ", "),
			strings.Join(types, ", "),
			dispatch,
		)
	}
	return b.String()
}

// dirEntry is the machine (JSON) form for routing.
type dirEntry struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Role         string   `json:"role,omitempty"`
	Inbox        string   `json:"inbox"`
	Owns         []string `json:"owns"`
	Domains      []string `json:"domains"`
	Accepts      []Accept `json:"accepts"`
	Dispatchable bool     `json:"dispatchable"`
	Mutates      []string `json:"mutates"`
	Model        string   `json:"model,omitempty"`
	Repo         string   `json:"repo,omitempty"`
}

// DirectoryJSON renders the roster as machine-readable JSON for routing.
func DirectoryJSON(agents []*Agent) (string, error) {
	out := make([]dirEntry, 0, len(agents))
	for _, a := range agents {
		out = append(out, dirEntry{
			Name:         a.Name,
			Title:        a.Title,
			Role:         strings.TrimSpace(a.Role),
			Inbox:        firstNonEmpty(a.Inbox, IntakeDir),
			Owns:         a.Owns,
			Domains:      a.Domains,
			Accepts:      a.Accepts,
			Dispatchable: a.IsDispatchable(),
			Mutates:      a.Mutates,
			Model:        a.Model,
			Repo:         a.Repo,
		})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
