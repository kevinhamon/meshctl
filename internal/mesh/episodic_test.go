package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleTranscript = `{"type":"last-prompt","sessionId":"x"}
{"type":"user","message":{"role":"user","content":"please fix CD-1234, the telemetry drain bug in ingest"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"on it"},{"type":"tool_use","name":"Read","input":{"file_path":"/repo/ingest.go"}},{"type":"tool_use","name":"Bash","input":{"command":"go test ./ingest/..."}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/repo/drain.go","old_string":"a","new_string":"b"}}]}}
malformed tail line that is not json
`

func writeTranscript(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(p, []byte(sampleTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCapture_ExtractiveFromTranscript(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	tp := writeTranscript(t, t.TempDir())

	rec, err := CaptureSession(a, tp, "sess1", "2026-07-24T10:00:00Z", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("expected a record")
	}
	if !strings.Contains(rec.FirstPrompt, "telemetry drain") {
		t.Fatalf("first prompt: %q", rec.FirstPrompt)
	}
	if !hasStr(rec.FilesTouched, "/repo/ingest.go") || !hasStr(rec.FilesTouched, "/repo/drain.go") {
		t.Fatalf("files: %v", rec.FilesTouched)
	}
	if !hasStr(rec.Commands, "go test ./ingest/...") {
		t.Fatalf("commands: %v", rec.Commands)
	}
	if !hasStr(rec.Tools, "Read") || !hasStr(rec.Tools, "Edit") || !hasStr(rec.Tools, "Bash") {
		t.Fatalf("tools: %v", rec.Tools)
	}
	if !hasStr(rec.Refs, "CD-1234") {
		t.Fatalf("refs: %v", rec.Refs)
	}
	// Persisted + reloadable.
	if got := ListSessionRecords(a); len(got) != 1 || got[0].ID != "sess1" {
		t.Fatalf("expected 1 persisted record, got %v", got)
	}
}

func TestCapture_NoTranscriptStillWrites(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	rec, err := CaptureSession(a, "", "sess-empty", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.ID != "sess-empty" {
		t.Fatal("expected a (mostly empty) record with an id")
	}
}

func TestCapture_EmptyIDNoOp(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	rec, err := CaptureSession(a, "", "", "", time.Now())
	if err != nil || rec != nil {
		t.Fatalf("empty id should no-op, got rec=%v err=%v", rec, err)
	}
}

func TestSessions_NewestFirst(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	writeSessionRecord(a.Dir(), &SessionRecord{ID: "old", Agent: a.Name, Ended: "2026-07-01T00:00:00Z", Summary: "older"})
	writeSessionRecord(a.Dir(), &SessionRecord{ID: "new", Agent: a.Name, Ended: "2026-07-24T00:00:00Z", Summary: "newer"})
	recs := ListSessionRecords(a)
	if len(recs) != 2 || recs[0].ID != "new" {
		t.Fatalf("expected newest-first, got %v", recs)
	}
}

func TestRecall_UnifiesKBAndSessionLive(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)

	// A KB decision on telemetry.
	dec := filepath.Join(a.Dir(), KnowledgeDir, "decisions")
	os.MkdirAll(dec, 0o755)
	os.WriteFile(filepath.Join(dec, "0009-telemetry-drain.md"),
		[]byte("---\ntags: [telemetry, drain]\n---\n# Telemetry drain\nbody\n"), 0o644)

	// A captured session also about telemetry.
	CaptureSession(a, writeTranscript(t, t.TempDir()), "sessT", "2026-07-24T10:00:00Z", time.Now())

	// Recall must work WITHOUT a persisted index (live compute).
	os.RemoveAll(filepath.Join(a.Dir(), MemoryDir, MemoryIndexFile))

	hits, err := Recall(a, "telemetry", 20)
	if err != nil {
		t.Fatal(err)
	}
	var kb, sess bool
	for _, h := range hits {
		if h.Kind == "kb" && strings.Contains(h.Title, "telemetry") {
			kb = true
		}
		if h.Kind == "session" {
			sess = true
		}
	}
	if !kb {
		t.Fatalf("recall missed the KB topic; hits=%+v", hits)
	}
	if !sess {
		t.Fatalf("recall missed the session-record; hits=%+v", hits)
	}
}

func TestRecall_EmptyQueryReturnsRecent(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	writeSessionRecord(a.Dir(), &SessionRecord{ID: "s", Agent: a.Name, Ended: "2026-07-24T00:00:00Z", FirstPrompt: "did a thing", Summary: "did a thing"})
	hits, err := Recall(a, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("empty query should return recent activity")
	}
}

func hasStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
