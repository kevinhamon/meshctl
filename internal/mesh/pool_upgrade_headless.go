package mesh

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// pool_upgrade_headless.go — `pool upgrade` with no human at the prompt.
// A run outside a harness never meets the gate, so the command
// enforces this itself: with no human it goes ahead only when the pool-wide
// `pool-upgrade` grant allows it (present, not paused, under its cap) AND the
// `--check` plan flags nothing for a human (no doctrine conflicts, no
// warnings). Otherwise it refuses and leaves everything as it was. A run that
// goes ahead is one line in the policy log. With a human present nothing here
// applies.

// ErrPoolUpgradeNeedsHuman: a no-human `pool upgrade` the grant or the plan
// does not let through.
var ErrPoolUpgradeNeedsHuman = errors.New("pool upgrade: no human at the prompt")

// CLINoHumanCause names why a meshctl command run has no human at the prompt,
// "" when one is: dispatch depth set and non-zero, a distil run, or the
// caller's --headless. A command run carries no hook payload, so these are
// the only signals (the in-session modes are the gate's to judge).
func CLINoHumanCause(headless bool) string { return kbNoHumanCause(headless) }

// UpgradeCheckWarnings is everything `pool upgrade --check` flags for a human,
// in --check order: doctrine conflicts, then the warnings of
// `--check --json` (agent warnings unless doctrineOnly, pool warnings, the
// pool-repo reminder).
func UpgradeCheckWarnings(p PoolUpgradePlan, doctrineOnly bool, now time.Time) []string {
	var out []string
	for _, c := range p.Conflicts {
		out = append(out, "doctrine conflict: "+c+" has local edits")
	}
	for _, w := range NewUpgradeCheckJSON(p, doctrineOnly, now).Warnings {
		if w.Agent != "" {
			out = append(out, w.Agent+": "+w.Message)
		} else {
			out = append(out, w.Message)
		}
	}
	return out
}

// HeadlessUpgradeOptions configures RunHeadlessPoolUpgrade.
type HeadlessUpgradeOptions struct {
	Cause        string // why no human is at the prompt (CLINoHumanCause); must be non-empty
	DoctrineOnly bool
	Version      string // the meshctl doing the upgrade, for the policy log
	Now          time.Time
}

// RunHeadlessPoolUpgrade decides a no-human `pool upgrade` and, when the
// grant and a clean plan allow it, runs apply and records one policy action
// (also when apply fails partway: the audit lists whatever a grant let
// start). A refusal wraps ErrPoolUpgradeNeedsHuman, names the cause and the
// missing grant / pause / cap reason or lists the warnings, and runs nothing.
func RunHeadlessPoolUpgrade(poolRoot string, o HeadlessUpgradeOptions, apply func() error) error {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	plan, err := PlanPoolUpgrade(poolRoot, o.Now)
	if err != nil {
		return err
	}
	warnings := UpgradeCheckWarnings(plan, o.DoctrineOnly, o.Now)
	d := GrantCheck(poolRoot, PoolGrantPeer, GrantPoolUpgrade, o.Now)
	if !d.Allowed {
		also := ""
		if len(warnings) > 0 {
			also = "\nthe plan also flags:\n  - " + strings.Join(warnings, "\n  - ")
		}
		return fmt.Errorf("%w (%s): %s — it waits for a human; run `meshctl pool upgrade` in an interactive session%s",
			ErrPoolUpgradeNeedsHuman, o.Cause, d.Reason, also)
	}
	if len(warnings) > 0 {
		return fmt.Errorf("%w (%s): the `%s` grant applies only to a clean `pool upgrade --check`, and it flags %d item(s) — it waits for a human:\n  - %s",
			ErrPoolUpgradeNeedsHuman, o.Cause, GrantPoolUpgrade, len(warnings), strings.Join(warnings, "\n  - "))
	}
	from := ""
	if s, ok := ReadUpgradeStamp(poolRoot); ok {
		from = s.Version
	}
	applyErr := apply()
	detail := PoolUpgradePolicyDetail(plan, o.DoctrineOnly, from, o.Version)
	if applyErr != nil {
		detail += "; ended with: " + applyErr.Error()
	}
	if err := RecordPolicyAction(poolRoot, PolicyAction{Time: o.Now.UTC().Format(time.RFC3339), Peer: PoolGrantPeer, Grant: GrantPoolUpgrade, Detail: detail}); err != nil && applyErr == nil {
		return fmt.Errorf("pool upgrade applied, but the policy log could not be written: %w", err)
	}
	return applyErr
}

// PoolUpgradePolicyDetail is the policy-log summary of an upgrade: the
// versions and the step counts by area.
func PoolUpgradePolicyDetail(p PoolUpgradePlan, doctrineOnly bool, from, to string) string {
	var b strings.Builder
	b.WriteString("pool upgrade")
	switch {
	case from != "" && to != "":
		fmt.Fprintf(&b, " %s → %s", from, to)
	case to != "":
		b.WriteString(" to " + to)
	}
	agentSteps := 0
	if !doctrineOnly {
		for _, a := range p.Agents {
			agentSteps += len(a.Actions)
		}
	}
	fmt.Fprintf(&b, ": %d step(s) — pool %d, doctrine %d, agents %d", len(p.Pool)+len(p.Doctrine)+agentSteps, len(p.Pool), len(p.Doctrine), agentSteps)
	if doctrineOnly {
		b.WriteString(" (doctrine only)")
	}
	return b.String()
}
