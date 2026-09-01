package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Claim is the atomic intake-claim sidecar (ADR-0011 Race A). It is the
// mutual-exclusion authority; the request `status` field is only a human
// display and must never be used for exclusion (TOCTOU).
type Claim struct {
	OwnerSession string `json:"owner_session"`
	PID          int    `json:"pid"`
	Heartbeat    int64  `json:"heartbeat"`
	agent        string `json:"-"`
	path         string `json:"-"`
}

// claimPath is the sidecar path for a request file.
func claimPath(requestPath string) string { return requestPath + ".claim" }

// ClaimRequest attempts an atomic O_EXCL claim of a request. Returns the winning
// claim and whether this caller now owns it. A loser gets ok=false and the
// existing claim. A stale claim from a dead session is reclaimed.
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
		_ = os.Remove(cp)
		if write() {
			return c, true
		}
	}
	return existing, false
}

// ReleaseClaim removes a claim, but only if held by the given session (never
// release someone else's claim).
func ReleaseClaim(requestPath, session string) error {
	cp := claimPath(requestPath)
	existing, err := readClaim(cp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if existing.OwnerSession != session {
		return fmt.Errorf("claim on %s is held by %q, not %q", filepath.Base(requestPath), existing.OwnerSession, session)
	}
	return os.Remove(cp)
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
	if sessionAlive(agentDir, c.OwnerSession, now) {
		return false
	}
	return true
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
	return os.WriteFile(c.path, b, 0o644)
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
		if r.Status != "pending" && r.Status != "needs-info" {
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
