package mesh

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveRequest(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	req, err := WriteRequest(a, "guidance", "advise me", "tester", "P2", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a claim sidecar.
	os.WriteFile(req.path+".claim", []byte("{}"), 0o644)

	out, err := ArchiveRequest(a, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !fileExists(out) {
		t.Fatalf("archived file missing at %s", out)
	}
	if fileExists(req.path) {
		t.Fatal("request should be moved out of the inbox")
	}
	if fileExists(req.path + ".claim") {
		t.Fatal("claim sidecar should be removed, not archived")
	}
	if filepath.Base(filepath.Dir(out)) != IntakeArchiveDir {
		t.Fatalf("archived to wrong dir: %s", out)
	}
	// Inbox now empty of requests.
	reqs, _ := ListRequests(a.InboxPath())
	if len(reqs) != 0 {
		t.Fatalf("inbox should be empty after archive, got %d", len(reqs))
	}
}

func TestArchiveAnswered(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	r1, _ := WriteRequest(a, "guidance", "one", "u", "P2", nil, time.Now())
	time.Sleep(1100 * time.Millisecond)
	r2, _ := WriteRequest(a, "guidance", "two", "u", "P2", nil, time.Now())
	// Mark r1 answered by rewriting its status line.
	b, _ := os.ReadFile(r1.path)
	os.WriteFile(r1.path, []byte(replaceOnce(string(b), "status: pending", "status: answered")), 0o644)

	ids, err := ArchiveAnswered(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != r1.ID {
		t.Fatalf("only the answered request should archive, got %v", ids)
	}
	if !fileExists(r2.path) {
		t.Fatal("pending request must stay in the inbox")
	}
}

func replaceOnce(s, old, new string) string {
	i := indexOf(s, old)
	if i < 0 {
		return s
	}
	return s[:i] + new + s[i+len(old):]
}
