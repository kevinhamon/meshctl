package mesh

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Episodic memory (ADR-0033) is the owned, extractive-capture tier: one
// session-record per session, parsed structurally from the harness transcript at
// session end — NO LLM. It records facts (opening ask, files touched, commands,
// tools, commits, handoff, refs), not generated prose, so there is little to get
// wrong. Records live under <agent>/.memory/sessions/<id>.json (the .memory tree
// is git-ignored; episodic recall is pool-local). The KB stays authoritative;
// records are recall material that routes back to the KB/handoff/commits.
const SessionsSubdir = "sessions"

// SessionRecord is one captured session (ADR-0033 schema).
type SessionRecord struct {
	ID           string   `json:"id"`
	Agent        string   `json:"agent"`
	Started      string   `json:"started,omitempty"`
	Ended        string   `json:"ended"`
	FirstPrompt  string   `json:"first_prompt,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	FilesTouched []string `json:"files_touched,omitempty"`
	Commands     []string `json:"commands,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	Commits      []string `json:"commits,omitempty"`
	Handoff      string   `json:"handoff_ref,omitempty"`
	Refs         []string `json:"refs,omitempty"`
}

func sessionsDirPath(agentDir string) string {
	return filepath.Join(agentDir, MemoryDir, SessionsSubdir)
}

// safeID sanitizes a session id for use as a filename.
var reIDSanitize = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func sessionRecordPath(agentDir, id string) string {
	return filepath.Join(sessionsDirPath(agentDir), reIDSanitize.ReplaceAllString(id, "_")+".json")
}

// CaptureSession writes an extractive session-record for the session, mining the
// transcript at transcriptPath (may be empty) plus git context from the agent
// workspace. Fail-open by field: any part that can't be read is simply omitted;
// the function only errors if it cannot write the record at all. Returns nil (no
// error) when there is nothing worth recording (no transcript and no id).
func CaptureSession(agent *Agent, transcriptPath, id, started string, now time.Time) (*SessionRecord, error) {
	if id == "" {
		return nil, nil
	}
	rec := &SessionRecord{
		ID:      id,
		Agent:   agent.Name,
		Started: started,
		Ended:   now.UTC().Format(time.RFC3339),
	}

	if transcriptPath != "" {
		mineTranscript(transcriptPath, rec)
	}
	gitContext(agent.Dir(), started, rec)

	// Extractive summary: opening ask, trimmed. Cheap, no generation.
	rec.Summary = extractiveSummary(rec)
	// refs: scan the opening ask + commit subjects for CD-1234 / R-12 / ADR-0033.
	rec.Refs = extractRefs(rec)

	if err := writeSessionRecord(agent.Dir(), rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// mineTranscript parses a Claude Code transcript (JSONL) extractively: the first
// real user prompt, tool names, files touched, and shell commands. Defensive
// against schema drift — unknown shapes are skipped, never fatal.
func mineTranscript(path string, rec *SessionRecord) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	toolSet := map[string]bool{}
	fileSet := map[string]bool{}
	var commands []string

	dec := json.NewDecoder(f)
	dec.UseNumber()
	for {
		var line map[string]any
		if err := dec.Decode(&line); err != nil {
			break // EOF or a malformed tail line — stop, keep what we have
		}
		typ, _ := line["type"].(string)
		msg, _ := line["message"].(map[string]any)
		if msg == nil {
			continue
		}
		switch typ {
		case "user":
			if rec.FirstPrompt == "" {
				if p := firstUserText(msg["content"]); p != "" {
					rec.FirstPrompt = truncate(p, 500)
				}
			}
		case "assistant":
			blocks, _ := msg["content"].([]any)
			for _, b := range blocks {
				bm, ok := b.(map[string]any)
				if !ok {
					continue
				}
				if bm["type"] != "tool_use" {
					continue
				}
				name, _ := bm["name"].(string)
				if name != "" {
					toolSet[name] = true
				}
				input, _ := bm["input"].(map[string]any)
				if input == nil {
					continue
				}
				for _, k := range []string{"file_path", "path", "notebook_path"} {
					if v, ok := input[k].(string); ok && v != "" {
						fileSet[v] = true
					}
				}
				if c, ok := input["command"].(string); ok && c != "" {
					commands = append(commands, truncate(strings.TrimSpace(c), 200))
				}
			}
		}
	}
	rec.Tools = sortedKeys(toolSet)
	rec.FilesTouched = capSlice(sortedKeys(fileSet), 50)
	rec.Commands = capSlice(dedupeOrdered(commands), 30)
}

// firstUserText pulls the opening ask from a user message's content, which may be
// a plain string or a block list. Skips tool_result-only turns.
func firstUserText(content any) string {
	switch c := content.(type) {
	case string:
		return strings.TrimSpace(c)
	case []any:
		for _, b := range c {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if bm["type"] == "text" {
				if t, ok := bm["text"].(string); ok && strings.TrimSpace(t) != "" {
					return strings.TrimSpace(t)
				}
			}
		}
	}
	return ""
}

// gitContext adds commits made during the session and the handoff commit ref.
// All best-effort: a non-repo or git failure just leaves the fields empty.
func gitContext(dir, started string, rec *SessionRecord) {
	if !dirExists(filepath.Join(dir, ".git")) {
		return
	}
	if started != "" {
		out, err := exec.Command("git", "-C", dir, "log", "--since="+started,
			"--format=%h %s", "--", ".").Output()
		if err == nil {
			for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if ln = strings.TrimSpace(ln); ln != "" {
					rec.Commits = append(rec.Commits, ln)
				}
			}
			rec.Commits = capSlice(rec.Commits, 20)
		}
	}
	hp := filepath.Join(KnowledgeDir, HandoffFile)
	if out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%h", "--", hp).Output(); err == nil {
		if h := strings.TrimSpace(string(out)); h != "" {
			rec.Handoff = hp + "@" + h
		}
	}
}

func extractiveSummary(rec *SessionRecord) string {
	if rec.FirstPrompt != "" {
		return truncate(rec.FirstPrompt, 240)
	}
	if len(rec.Commits) > 0 {
		return "commits: " + strings.Join(rec.Commits, "; ")
	}
	return ""
}

var reRef = regexp.MustCompile(`\b([A-Z]{2,}-\d+|R-\d+|ADR-\d+)\b`)

func extractRefs(rec *SessionRecord) []string {
	hay := rec.FirstPrompt + " " + strings.Join(rec.Commits, " ")
	set := map[string]bool{}
	for _, m := range reRef.FindAllString(hay, -1) {
		set[m] = true
	}
	return sortedKeys(set)
}

// --- store ---

func writeSessionRecord(agentDir string, rec *SessionRecord) error {
	dir := sessionsDirPath(agentDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(sessionRecordPath(agentDir, rec.ID), append(b, '\n'), 0o644)
}

// ListSessionRecords returns all captured records for an agent, newest-ended first.
func ListSessionRecords(agent *Agent) []*SessionRecord {
	dir := sessionsDirPath(agent.Dir())
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*SessionRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r SessionRecord
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		out = append(out, &r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ended > out[j].Ended })
	return out
}

// --- small helpers ---

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupeOrdered(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func capSlice(in []string, n int) []string {
	if len(in) > n {
		return in[:n]
	}
	return in
}
