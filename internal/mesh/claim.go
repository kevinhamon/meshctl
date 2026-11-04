package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Claim is the atomic intake-claim sidecar (the claim race). It is the
// mutual-exclusion authority; the request `status` field is only a human
// display and must never be used for exclusion (TOCTOU).
type Claim struct {
	OwnerSession string `json:"owner_session"`
	PID          int    `json:"pid"`
	Heartbeat    int64  `json:"heartbeat"`
	path         string `json:"-"`
}

// claimPath is the sidecar path for a request file.
func claimPath(requestPath string) string { return requestPath + ".claim" }

// claimReclaimPath is the mutex directory a stale-claim reclaim is done under
// (the .dispatch.lock pattern, see reclaimLocked).
func claimReclaimPath(cp string) string { return cp + ".reclaim" }

// ClaimRequest attempts an atomic O_EXCL claim of a request. Returns the winning
// claim and whether this caller now owns it. A loser gets ok=false and the
// existing claim. A stale claim from a dead session is reclaimed.
//
// Reclaim runs under a per-claim mutex and re-reads the claim inside it. A bare
// remove-then-create let two reclaimers that both judged the old claim stale
// end up owning the request: the second one's remove deleted the first one's
// fresh claim, and its create then succeeded.
func ClaimRequest(requestPath, session string, pid int, now time.Time) (*Claim, bool) {
	cp := claimPath(requestPath)
	c := &Claim{OwnerSession: session, PID: pid, Heartbeat: now.Unix(), path: cp}

	write := func() bool {
		f, err := os.OpenFile(cp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return false
		}
		defer f.Close()
		b, _ := json.Marshal(c)
		_, _ = f.Write(b)
		return true
	}

	if write() {
		return c, true
	}

	// Claim exists — inspect for staleness.
	existing, err := readClaim(cp)
	if err != nil {
		// Unreadable claim: treat as held to be safe.
		return &Claim{path: cp}, false
	}
	if existing.OwnerSession == session {
		// Already ours — refresh heartbeat, report owned.
		existing.Heartbeat = now.Unix()
		_ = writeClaim(existing)
		return existing, true
	}
	if reclaimable(requestPath, existing, now) {
		if removeAbandonedClaim(requestPath, cp, now) && write() {
			return c, true
		}
		// Lost the reclaim mutex or the create to a concurrent caller: report
		// whatever claim is there now.
		if cur, err := readClaim(cp); err == nil {
			return cur, false
		}
	}
	return existing, false
}

// removeAbandonedClaim removes the claim at cp if, re-read under the reclaim
// mutex, it is still abandoned, and reports whether the caller may now race for
// the create. The mutex is released on return, so the O_EXCL create that follows
// stays the only arbiter: the mutex only guarantees that what gets removed is
// the abandoned claim, never a fresh one. False means another reclaimer holds
// the mutex, or the claim turned out to be live.
func removeAbandonedClaim(requestPath, cp string, now time.Time) bool {
	reclaim := claimReclaimPath(cp)
	if !reclaimLocked(reclaim) {
		return false
	}
	defer os.Remove(reclaim)
	cur, err := readClaim(cp)
	switch {
	case os.IsNotExist(err):
		return true // already removed by the previous reclaimer; race for the create
	case err != nil:
		return false // unreadable: treat as held
	case !reclaimable(requestPath, cur, now):
		return false // a concurrent reclaimer already replaced it with a live claim
	}
	return os.Remove(cp) == nil || !fileExists(cp)
}

// ReleaseClaim removes a claim, but only if held by the given session (never
// release someone else's live claim). An abandoned claim — stale heartbeat and
// owner session no longer alive — may be released by anyone, since it would be
// reclaimed on the next claim anyway.
func ReleaseClaim(requestPath, session string, now time.Time) error {
	cp := claimPath(requestPath)
	existing, err := readClaim(cp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if existing.OwnerSession == session {
		return os.Remove(cp)
	}
	if !reclaimable(requestPath, existing, now) {
		return fmt.Errorf("claim on %s is held by live session %q, not %q", filepath.Base(requestPath), existing.OwnerSession, session)
	}
	// Abandoned by someone else: release under the reclaim mutex so a claim a
	// concurrent reclaimer just created in its place is not the one removed.
	removeAbandonedClaim(requestPath, cp, now)
	return nil
}

// reclaimable reports whether a claim may be stolen: its own heartbeat is stale
// AND the owning session is not still heartbeating in the presence registry.
func reclaimable(requestPath string, c *Claim, now time.Time) bool {
	if int(now.Unix()-c.Heartbeat) <= staleSec() {
		return false
	}
	// Cross-check the owner's session registry. The agent dir is two levels up
	// from the request file (…/<agent>/intake/<req>).
	agentDir := filepath.Dir(filepath.Dir(requestPath))
	return !sessionAlive(agentDir, c.OwnerSession, now)
}

func readClaim(cp string) (*Claim, error) {
	b, err := os.ReadFile(cp)
	if err != nil {
		return nil, err
	}
	var c Claim
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	c.path = cp
	return &c, nil
}

func writeClaim(c *Claim) error {
	b, _ := json.Marshal(c)
	return writeFileAtomic(c.path, b, 0o644)
}

// InboxNext atomically claims and returns the next pending request in an agent's
// inbox (oldest first). Skips requests already claimed by a live session.
// Returns nil if none available. Two concurrent callers can never both win the
// same request (the O_EXCL claim is the arbiter).
func InboxNext(agent *Agent, session string, pid int, doClaim bool, now time.Time) (*Request, error) {
	reqs, err := ListRequests(agent.InboxPath())
	if err != nil {
		return nil, err
	}
	for _, r := range reqs {
		if st, _ := CanonicalStatus(r.Status); st != StatusPending && st != StatusNeedsInfo {
			continue
		}
		cp := claimPath(r.path)
		if existing, err := readClaim(cp); err == nil {
			// Claimed — skip unless stale + reclaimable.
			if !reclaimable(r.path, existing, now) {
				continue
			}
		}
		if !doClaim {
			return r, nil
		}
		if _, ok := ClaimRequest(r.path, session, pid, now); ok {
			return r, nil
		}
		// Lost the race to a concurrent caller — try the next one.
	}
	return nil, nil
}
