package mesh

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The intuition tier (ADR-0016): claude-mem is the swappable episodic *engine*;
// this is the owned, harness-neutral *contract* over it. It returns COMPACT,
// deduped pointers (title + date) from the engine's semantic endpoint — not the
// full-narrative dump claude-mem's own push emits. Fail-open: any error yields
// no hits (intuition is advisory; it must never break a caller).

// IntuitionHit is one compact pointer into episodic memory.
type IntuitionHit struct {
	Title string `json:"title"`
	Date  string `json:"date,omitempty"`
}

const intuitionMinQuery = 20 // engine ignores shorter queries

func claudeMemPort() int {
	if home, err := os.UserHomeDir(); err == nil {
		if b, err := os.ReadFile(filepath.Join(home, ".claude-mem", "worker.pid")); err == nil {
			var m struct {
				Port int `json:"port"`
			}
			if json.Unmarshal(b, &m) == nil && m.Port > 0 {
				return m.Port
			}
		}
	}
	return 37702
}

func isYear(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// IntuitionRecall queries the episodic engine and returns compact, deduped
// pointers. project defaults to $AGENT_NAME, else the cwd basename. Never errors —
// engine down / slow / short query / bad response all yield nil.
func IntuitionRecall(query, project string, limit int) []IntuitionHit {
	q := strings.TrimSpace(query)
	if len(q) < intuitionMinQuery {
		return nil
	}
	if project == "" {
		project = os.Getenv("AGENT_NAME")
	}
	if project == "" {
		if cwd, err := os.Getwd(); err == nil {
			project = filepath.Base(cwd)
		}
	}
	if limit < 1 {
		limit = 3
	} else if limit > 10 {
		limit = 10
	}

	body, _ := json.Marshal(map[string]any{"q": q, "project": project, "limit": limit})
	url := fmt.Sprintf("http://127.0.0.1:%d/api/context/semantic", claudeMemPort())
	cl := &http.Client{Timeout: 6 * time.Second}
	resp, err := cl.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out struct {
		Context string `json:"context"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil
	}

	var hits []IntuitionHit
	seen := map[string]bool{}
	for _, ln := range strings.Split(out.Context, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "### ") {
			continue
		}
		h := strings.TrimSpace(ln[4:])
		date := ""
		if strings.HasSuffix(h, ")") {
			if i := strings.LastIndex(h, " ("); i >= 0 {
				tail := h[i+2 : len(h)-1]
				if len(tail) >= 4 && isYear(tail[:4]) {
					date = tail
					h = strings.TrimSpace(h[:i])
				}
			}
		}
		k := strings.ToLower(h)
		if h == "" || seen[k] {
			continue
		}
		seen[k] = true
		hits = append(hits, IntuitionHit{Title: h, Date: date})
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// RecallIntuition returns compact past-work pointers for the injection surface
// (ADR-0033 stage-2). The DEFAULT backend is the OWNED engine — episodic
// session-records + the git time-spine, via Recall — so intuition works on any
// harness with no external daemon. Set MESH_INTUITION_ENGINE=claude-mem to use
// the legacy episodic engine instead (optional, opt-in; being retired).
func RecallIntuition(agent *Agent, query string, limit int) []IntuitionHit {
	if limit < 1 {
		limit = 3
	}
	if os.Getenv("MESH_INTUITION_ENGINE") == "claude-mem" {
		proj := ""
		if agent != nil {
			proj = agent.Name
		}
		return IntuitionRecall(query, proj, limit)
	}
	if agent == nil {
		return nil
	}
	hits, err := Recall(agent, query, limit*3)
	if err != nil {
		return nil
	}
	var out []IntuitionHit
	for _, h := range hits {
		if h.Kind == "kb" { // intuition = past *activity*; KB topics are `memory recall`'s job
			continue
		}
		out = append(out, IntuitionHit{Title: h.Title, Date: h.Date})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// IntuitionMarkdown renders hits as the compact injected block (empty if none).
func IntuitionMarkdown(hits []IntuitionHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Intuition (past work — pointers; read the KB/mem for detail)\n")
	for _, h := range hits {
		if h.Date != "" {
			fmt.Fprintf(&b, "- %s (%s)\n", h.Title, h.Date)
		} else {
			fmt.Fprintf(&b, "- %s\n", h.Title)
		}
	}
	return b.String()
}
