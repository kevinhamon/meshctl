package console

import "github.com/kevinhamon/meshctl/internal/mesh"

// attention.go — how the console counts an agent for the person: the pool
// summaries behind Home and the switcher (console layout spec, C18).

type attention int

const (
	attentionNone     attention = iota
	attentionWaiting            // a phrase to say, or answers to read back
	attentionNeedsYou           // KB branches to confirm or open conflicts
)

// agentAttention is the agent's state for the person, as the page colours
// it: red "Needs you", amber "Waiting".
func agentAttention(a mesh.PendingAgentReport) attention {
	at := attentionNone
	for _, l := range a.Lines {
		switch {
		case l.Kind == "kb-branches" || l.Kind == "conflicts":
			return attentionNeedsYou
		case len(l.Say) > 0 || l.Kind == "read-back":
			at = attentionWaiting
		}
	}
	return at
}
