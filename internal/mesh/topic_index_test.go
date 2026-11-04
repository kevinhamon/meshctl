package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Recall reads the persisted topic index while the knowledge tree is unchanged
// and rebuilds it on any change, including a deletion (which touches no file,
// only its directory). Proof of the cache: a topic planted straight into
// index.json is recalled until the tree changes.
func TestTopicIndex_CachedUntilKnowledgeChanges(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "arch", false, nil)
	writeKB(t, a, "playbooks/release.md", "# Release checklist\n\nTag then push.\n")

	hits, err := RecallKnowledge(a, "release checklist", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("first recall: hits=%d err=%v", len(hits), err)
	}
	idxPath := filepath.Join(a.Dir(), MemoryDir, MemoryIndexFile)
	if !fileExists(idxPath) {
		t.Fatal("recall did not persist the topic index")
	}

	// Plant a topic the tree does not contain. Recall must return it: that
	// is only possible by reading the index rather than the notes.
	plant := func() {
		b, err := os.ReadFile(idxPath)
		if err != nil {
			t.Fatal(err)
		}
		var idx MemoryIndex
		if err := json.Unmarshal(b, &idx); err != nil {
			t.Fatal(err)
		}
		idx.Topics = append(idx.Topics, MemoryTopic{Topic: "planted-zebra-topic", Sources: []string{"playbooks/release.md"}})
		if err := writeMemoryIndex(a.Dir(), &idx); err != nil {
			t.Fatal(err)
		}
	}
	plant()
	if hits, _ := RecallKnowledge(a, "zebra", 5); len(hits) != 1 || hits[0].Title != "planted-zebra-topic" {
		t.Fatalf("an unchanged tree must be served from the index; got %+v", hits)
	}

	// A new note invalidates the index: the planted topic is gone, the new
	// note's topic is found.
	time.Sleep(10 * time.Millisecond) // a distinct mtime, whatever the filesystem's resolution
	p := writeKB(t, a, "playbooks/rollback.md", "# Rollback procedure\n\nRevert the tag.\n")
	if hits, _ := RecallKnowledge(a, "zebra", 5); len(hits) != 0 {
		t.Fatalf("a changed tree must rebuild the index; planted topic survived: %+v", hits)
	}
	if hits, _ := RecallKnowledge(a, "rollback procedure", 5); len(hits) == 0 {
		t.Fatal("the new note is not recalled after the rebuild")
	}

	// A deletion invalidates it too: the only change is the directory's mtime.
	plant()
	time.Sleep(10 * time.Millisecond)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if hits, _ := RecallKnowledge(a, "zebra", 5); len(hits) != 0 {
		t.Fatal("a deleted note must invalidate the index")
	}
	if hits, _ := RecallKnowledge(a, "rollback procedure", 5); len(hits) != 0 {
		t.Fatalf("a deleted note is still recalled: %+v", hits)
	}

	// The manifest's tags feed the index, so a manifest edit invalidates it.
	plant()
	time.Sleep(10 * time.Millisecond)
	mp := filepath.Join(a.Dir(), ManifestName)
	now := time.Now()
	if err := os.Chtimes(mp, now, now); err != nil {
		t.Fatal(err)
	}
	if hits, _ := RecallKnowledge(a, "zebra", 5); len(hits) != 0 {
		t.Fatal("a manifest change must invalidate the index")
	}
	if stale, why := MemoryStale(a); stale {
		t.Fatalf("index just rebuilt reports stale: %s", why)
	}
}

// The status line computes only the peer's own workspace: no read-back line
// (it never renders one) and no triage signals, the two kinds that read every
// peer's inbox and archive and run git in every peer repo.
func TestPending_OwnScopeSkipsPoolWideKinds(t *testing.T) {
	dir, _, arch, _ := pendingPool(t)
	now := time.Now()

	full := Pending(dir, arch, now)
	own := pendingScoped(dir, arch, now, scopeOwn)

	hasKind := func(b PendingBlock, k PendingKind) bool {
		for _, l := range b.Lines {
			if l.Kind == k {
				return true
			}
		}
		return false
	}
	if !hasKind(full, PendingReadBack) {
		t.Fatal("fixture: the pool-scoped block should carry a read-back line")
	}
	if hasKind(own, PendingReadBack) {
		t.Fatal("the own-workspace block must not compute read-back")
	}
	for _, l := range own.Lines {
		if l.Kind == PendingIntake && l.TriageSignals != 0 {
			t.Fatal("the own-workspace block must not compute triage signals")
		}
	}
	// Everything else is identical, so the status line loses nothing it shows.
	for _, k := range []PendingKind{PendingKBBranches, PendingIntake, PendingDelegated} {
		if hasKind(full, k) != hasKind(own, k) {
			t.Fatalf("kind %d differs between pool and own scope", k)
		}
	}
	want := `[agent: architect] * [needs-confirm: 1, say "confirm kb"] * [intake: 2, oldest: 3d, say "check intake"] * [dispatched: 1, oldest: 0d, say "review work"]`
	if got := plainStatusLine(dir, cwdPayload(arch.Dir())); got != want {
		t.Fatalf("status line:\n got %q\nwant %q", got, want)
	}
}
