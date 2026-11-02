package mesh

import (
	"fmt"
	"time"
)

// grants_lift.go — where a grant lifts a "no human at the prompt" deny.
// Each ask point checks its grant in two places: the
// gate, for a peer's tool call, and the command itself, for a run that never
// meets the gate. A grant changes only the no-human case: with a human at the
// prompt the gate asks as before, and with no grant every message is
// byte-for-byte what it was.
//
// A grant lifts only a headless run the command can see as well
// (HeadlessNow: dispatch depth set and non-zero, or a distil run; `kb finish
// --headless` too, in that command). An in-session consult or delegated work
// keeps its deny: the command run under it cannot tell it has no human, so it
// could not record the action as a policy confirmation.
//
// Each action a grant lets through is recorded once, as one policy-log line
// (RecordPolicyAction), by the command that takes it — or by the gate, for an
// outbound write only the gate sees.

// grantLift decides whether peer's grant lets a no-human action through now.
// lifted is the answer. note is what to append to today's deny when the peer
// holds the grant but it does not apply (paused, cap reached); "" when the
// grant is not set, so the deny reads exactly as it did.
func grantLift(poolRoot, peer, grant string, now time.Time) (lifted bool, note string) {
	if poolRoot == "" || peer == "" || !IsPool(poolRoot) {
		return false, ""
	}
	cfg := LoadPoolConfig(poolRoot)
	if cfg.ConfigErr != nil {
		return false, ""
	}
	if _, ok := cfg.Settings.Grants.PeerGrant(peer, grant); !ok {
		return false, ""
	}
	d := grantCheck(poolRoot, cfg, peer, grant, now)
	if d.Allowed {
		return true, ""
	}
	return false, grantNote(d.Reason)
}

// grantNote renders a GrantDecision.Reason as the tail of a deny.
func grantNote(reason string) string {
	if reason == "" {
		return ""
	}
	return " The grant does not apply: " + reason + "."
}

// gateGrantLift is grantLift for a gated tool call: only for a headless run
// (HeadlessNow), never for an in-session consult or delegated work.
func gateGrantLift(agentsDir string, p HookPayload, peer, grant string) (bool, string) {
	if !HeadlessNow() || p.InSession {
		return false, ""
	}
	return grantLift(agentsDir, peer, grant, time.Now())
}

// gateGrantAllow is the gate's allow for a call a grant let through. It is
// not an ask: nothing is shown, and the call falls through to the remaining
// gate steps like any allow.
func gateGrantAllow(check, grant string) GateDecision {
	return GateDecision{Check: check, Reason: "mesh: let through with no human at the prompt by the `" + grant + "` grant"}
}

// recordGrantAction writes the one policy-log line for an action a grant let
// through, dated now (so the per-day cap counts it on the day it happened).
func recordGrantAction(poolRoot, peer, grant, detail, session string, now time.Time) error {
	if err := RecordPolicyAction(poolRoot, PolicyAction{
		Time: now.UTC().Format(time.RFC3339), Peer: peer, Grant: grant, Detail: detail, Session: session,
	}); err != nil {
		return fmt.Errorf("the policy log %s could not be written: %w", PolicyLogPath(poolRoot), err)
	}
	return nil
}

// policyEntry marks a ledger entry as confirmed by policy under grant;
// grant "" leaves it a human confirmation.
func policyEntry(e LedgerEntry, grant string) LedgerEntry {
	if grant != "" {
		e.By, e.Grant = LedgerByPolicy, grant
	} else {
		e.By, e.Grant = "", ""
	}
	return e
}

// policyCommitLine is the Confirm: commit body line naming the grant, "" for
// a human confirmation.
func policyCommitLine(grant string) string {
	if grant == "" {
		return ""
	}
	return "Confirmed-by: " + LedgerByPolicy + " (grant " + grant + ")\n"
}
