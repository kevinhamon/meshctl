package mesh

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestCanonicalStatus(t *testing.T) {
	for in, want := range map[string]string{
		"pending": StatusPending, "Rejected": StatusDeclined, "done": StatusAnswered,
		"resolved": StatusAnswered, " needs-human ": StatusNeedsHuman, "filed": StatusFiled,
	} {
		if got, ok := CanonicalStatus(in); !ok || got != want {
			t.Errorf("CanonicalStatus(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	if _, ok := CanonicalStatus("wontfix"); ok {
		t.Error("unknown status accepted")
	}
}

func TestRespond_WritesBackAndReleasesClaim(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	req, err := WriteRequest(a, "guidance", "which queue?", "developer", "P2", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ClaimRequest(req.path, "me", 1, now); !ok {
		t.Fatal("claim")
	}
	for _, bad := range []RespondOptions{
		{Status: "wontfix", Response: "x"},
		{Status: "pending", Response: "x"},
		{Status: "answered"}, // no response
	} {
		bad.Session, bad.Now = "me", now
		if _, err := RespondRequest(a, req.ID, bad); err == nil {
			t.Errorf("accepted invalid respond %+v", bad)
		}
	}
	r, err := RespondRequest(a, req.ID, RespondOptions{Status: "done", Response: "Use SQS.", Session: "me", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusAnswered || r.Responded == "" || !strings.Contains(r.Body, "## Response — architect") || !strings.Contains(r.Body, "Use SQS.") {
		t.Fatalf("write-back wrong: status=%q responded=%q body=%q", r.Status, r.Responded, r.Body)
	}
	if fileExists(claimPath(req.path)) {
		t.Fatal("claim not released on close")
	}
}

func TestRespond_RefusedWhileAnotherLiveSessionHoldsClaim(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	req, _ := WriteRequest(a, "guidance", "x", "developer", "P2", nil, now)
	_, _ = SessionStart(a, "holder", 1, now)
	ClaimRequest(req.path, "holder", 1, now)
	if _, err := RespondRequest(a, req.ID, RespondOptions{Status: "answered", Response: "x", Session: "other", Now: now}); err == nil {
		t.Fatal("responded over a live claim")
	}
}

func TestDecline_RedirectsToOwner(t *testing.T) {
	dir := meshRoot(t)
	arch := newTestAgent(t, dir, "architect", true, nil)
	newTestAgent(t, dir, "pm", false, nil)
	now := time.Now()
	req, _ := WriteRequest(arch, "guidance", "file three stories for the ETL", "developer", "P1", []string{"R-1"}, now)

	if _, _, err := DeclineRequest(dir, arch, req.ID, DeclineOptions{Now: now}); err == nil {
		t.Fatal("decline without reason accepted")
	}
	if _, _, err := DeclineRequest(dir, arch, req.ID, DeclineOptions{Reason: "x", RedirectTo: "architect", Now: now}); err == nil {
		t.Fatal("self-redirect accepted")
	}
	orig, fwd, err := DeclineRequest(dir, arch, req.ID, DeclineOptions{Reason: "tracker work is the PM's", RedirectTo: "pm", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if orig.Status != StatusDeclined || orig.RedirectedTo != "pm:"+fwd.ID || !strings.Contains(orig.Body, "## Declined") {
		t.Fatalf("original not declined+linked: %+v", orig)
	}
	got, err := ReadRequest(fwd.path)
	if err != nil {
		t.Fatal(err)
	}
	if fwd.ID == req.ID || !strings.HasSuffix(fwd.ID, "-redirect") {
		t.Fatalf("redirect id must differ from the original: %q", fwd.ID)
	}
	if got.From != "developer" || got.To != "pm" || got.Priority != "P1" || len(got.Related) < 1 || got.Related[0] != req.ID {
		t.Fatalf("redirect lost provenance: %+v", got)
	}
	if !strings.Contains(got.Body, "file three stories") || !strings.Contains(got.Body, "## Redirected — architect") {
		t.Fatalf("redirect body wrong: %q", got.Body)
	}
	if _, _, err := DeclineRequest(dir, arch, req.ID, DeclineOptions{Reason: "again", Now: now}); err == nil {
		t.Fatal("re-declined a closed request")
	}
}

func TestSent_ReadBackLoopAndArchive(t *testing.T) {
	dir := meshRoot(t)
	arch := newTestAgent(t, dir, "architect", true, nil)
	dev := newTestAgent(t, dir, "developer", false, nil)
	now := time.Now()
	a1, _ := WriteRequest(arch, "guidance", "one", "developer", "P2", nil, now)
	WriteRequest(arch, "guidance", "two", "developer", "P2", nil, now)
	if _, _, err := DeclineRequest(dir, arch, a1.ID, DeclineOptions{Reason: "not mine", Now: now}); err != nil {
		t.Fatal(err)
	}

	sent, err := ListSent(dir, "developer", true)
	if err != nil || len(sent) != 1 || sent[0].ID != a1.ID {
		t.Fatalf("want only the declined request awaiting read-back, got %v err=%v", sent, err)
	}
	if all, _ := ListSent(dir, "developer", false); len(all) != 2 {
		t.Fatalf("--all: want 2, got %d", len(all))
	}
	if ctx := HookSessionStart(dir, HookPayload{Cwd: dev.Dir(), SessionID: "d"}, 1, now); !strings.Contains(ctx, a1.ID) || !strings.Contains(ctx, "declined") {
		t.Fatalf("session start did not surface the decline: %q", ctx)
	}

	// Not read back yet ⇒ archive --closed keeps it.
	if ids, skipped, _ := ArchiveClosed(arch, false); len(ids) != 0 || len(skipped) != 1 {
		t.Fatalf("archived before read-back: ids=%v skipped=%v", ids, skipped)
	}
	if _, err := AckSent(dir, "developer", a1.ID, now); err != nil {
		t.Fatal(err)
	}
	if sent, _ := ListSent(dir, "developer", true); len(sent) != 0 {
		t.Fatalf("ack did not clear read-back: %v", sent)
	}
	if ids, _, _ := ArchiveClosed(arch, false); len(ids) != 1 {
		t.Fatalf("read-back request not archived: %v", ids)
	}
}

func TestInboxStatusFindings_FlagsUnknownOnly(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	r1, _ := WriteRequest(a, "guidance", "one", "x", "P2", nil, now)
	r2, _ := WriteRequest(a, "guidance", "two", "x", "P2", nil, now)
	_ = setFrontmatterFields(r1.path, map[string]string{"status": "rejected"})
	_ = setFrontmatterFields(r2.path, map[string]string{"status": "wontfix"})
	got := InboxStatusFindings(a)
	if len(got) != 1 || !strings.Contains(got[0], "wontfix") {
		t.Fatalf("want one finding for wontfix, got %v", got)
	}
}

func TestSetFrontmatterFields_PreservesOtherLines(t *testing.T) {
	p := t.TempDir() + "/r.md"
	in := "---\nid: x\nstatus: pending   # pending | declined\nresponse:\n---\n\nbody\n"
	if err := os.WriteFile(p, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setFrontmatterFields(p, map[string]string{"status": "declined", "read_back": "2026-01-01"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	want := "---\nid: x\nstatus: declined\nresponse:\nread_back: 2026-01-01\n---\n\nbody\n"
	if string(b) != want {
		t.Fatalf("got %q want %q", b, want)
	}
}

func TestSent_GrandfathersLegacyResponses(t *testing.T) {
	dir := meshRoot(t)
	arch := newTestAgent(t, dir, "architect", true, nil)
	r, _ := WriteRequest(arch, "guidance", "old", "developer", "P2", nil, time.Now())
	_ = setFrontmatterFields(r.path, map[string]string{"status": "answered"}) // hand-closed, no `responded`
	if sent, _ := ListSent(dir, "developer", true); len(sent) != 0 {
		t.Fatalf("legacy response surfaced for read-back: %v", sent)
	}
	if ids, skipped, _ := ArchiveClosed(arch, false); len(ids) != 1 || len(skipped) != 0 {
		t.Fatalf("legacy closed request not archivable: ids=%v skipped=%v", ids, skipped)
	}
}
