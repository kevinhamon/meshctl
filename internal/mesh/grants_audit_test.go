package mesh

import (
	"strings"
	"testing"
	"time"
)

// grants_audit_test.go — session-start audit and paused notice, status-line
// paused marker.

const auditHeading = "# Grant audit"

func recordAt(t *testing.T, root, peer, grant, detail string, at time.Time) {
	t.Helper()
	if err := RecordPolicyAction(root, PolicyAction{Time: at.UTC().Format(time.RFC3339), Peer: peer, Grant: grant, Detail: detail}); err != nil {
		t.Fatal(err)
	}
}

func TestGrantAudit_NoLogNoSection(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "alpha", true, nil)
	ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s"}, 1, time.Now())
	if strings.Contains(ctx, auditHeading) || strings.Contains(ctx, "Grants are paused") {
		t.Fatalf("no policy log, no grants block: %q", ctx)
	}
	if fileExists(policySeenPath(dir, "alpha")) {
		t.Fatal("no marker for a pool without grant actions")
	}
}

func TestGrantAudit_ListsActionsSinceLastInteractiveStart(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "alpha", true, nil)
	newTestAgent(t, dir, "beta", true, nil)
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	recordAt(t, dir, "alpha", GrantKBPromote, "knowledge/old.md", t0)
	// First interactive start: everything so far is new.
	ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s1"}, 1, t0.Add(time.Hour))
	if !strings.Contains(ctx, auditHeading) || !strings.Contains(ctx, "knowledge/old.md") {
		t.Fatalf("first start must list the action: %q", ctx)
	}

	recordAt(t, dir, "alpha", GrantStateSet, "state.md", t0.Add(2*time.Hour))
	recordAt(t, dir, "beta", GrantKBPromote, "knowledge/other-peer.md", t0.Add(2*time.Hour))
	recordAt(t, dir, PoolGrantPeer, GrantPoolUpgrade, "v9.9.9", t0.Add(3*time.Hour))
	ctx = HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s2"}, 1, t0.Add(4*time.Hour))
	for _, want := range []string{auditHeading, "`state-set` — state.md", "`pool-upgrade` (pool-wide) — v9.9.9", "since your last session (2026-11-01T10:00:00Z)"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("missing %q in %q", want, ctx)
		}
	}
	for _, not := range []string{"knowledge/old.md", "other-peer"} {
		if strings.Contains(ctx, not) {
			t.Fatalf("%q must not be listed: %q", not, ctx)
		}
	}

	// Nothing new since: no section.
	ctx = HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s3"}, 1, t0.Add(5*time.Hour))
	if strings.Contains(ctx, auditHeading) {
		t.Fatalf("nothing new, no section: %q", ctx)
	}
}

func TestGrantAudit_HeadlessStartDoesNotSeeOrReset(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "alpha", true, nil)
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	recordAt(t, dir, "alpha", GrantKBPromote, "knowledge/x.md", t0)

	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "h"}, 1, t0.Add(time.Hour))
	if strings.Contains(ctx, auditHeading) {
		t.Fatalf("a headless run must not see the audit: %q", ctx)
	}
	if fileExists(policySeenPath(dir, "alpha")) {
		t.Fatal("a headless run must not move the marker")
	}
	t.Setenv("AGENT_DISPATCH_DEPTH", "0")

	ctx = HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "i"}, 1, t0.Add(2*time.Hour))
	if !strings.Contains(ctx, "knowledge/x.md") {
		t.Fatalf("the next interactive start still lists it: %q", ctx)
	}
}

func TestGrantAudit_CapsRows(t *testing.T) {
	dir := meshRoot(t)
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < policyAuditMax+3; i++ {
		recordAt(t, dir, "alpha", GrantKBPromote, "", t0.Add(time.Duration(i)*time.Minute))
	}
	md := PolicyAuditMarkdown(dir, "alpha", time.Time{})
	if got := strings.Count(md, "\n- 2026"); got != policyAuditMax {
		t.Fatalf("rows: %d\n%s", got, md)
	}
	if !strings.Contains(md, "and 3 earlier") {
		t.Fatalf("missing overflow line: %s", md)
	}
}

func TestGrantsPaused_SessionStartNotice(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "alpha", true, nil)
	if ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s"}, 1, time.Now()); strings.Contains(ctx, "Grants are paused") {
		t.Fatalf("not paused, no notice: %q", ctx)
	}
	writeGrantsConfig(t, dir, "grants:\n  paused: true\n")
	if ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s2"}, 1, time.Now()); !strings.Contains(ctx, "Grants are paused") {
		t.Fatalf("paused notice missing: %q", ctx)
	}
}

func TestStatusLine_GrantsPausedMarker(t *testing.T) {
	dir, _, arch, _ := pendingPool(t)
	before := plainStatusLine(dir, cwdPayload(arch.Dir()))
	if strings.Contains(before, "paused") {
		t.Fatalf("no marker without a pause: %q", before)
	}
	writeGrantsConfig(t, dir, "grants:\n  paused: false\n")
	if got := plainStatusLine(dir, cwdPayload(arch.Dir())); got != before {
		t.Fatalf("paused: false must leave the line unchanged:\n got %q\nwant %q", got, before)
	}
	writeGrantsConfig(t, dir, "grants:\n  paused: true\n")
	if got := plainStatusLine(dir, cwdPayload(arch.Dir())); got != before+" * [grants: paused]" {
		t.Fatalf("paused marker:\n got %q", got)
	}
}
