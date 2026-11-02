package mesh

import (
	"fmt"
	"strings"
)

// dispatch_grants.go — dispatch follows grants. A peer may be
// run headless when its manifest says `dispatchable: true` and, if it
// declares `mutates`, the pool config holds an `outbound:<surface>` grant for
// every one of those surfaces and grants are not paused. With no grants this
// is exactly the old rule: a state-mutating peer is never dispatched.

// DispatchRefusal returns why a may not be dispatched headless in the pool
// at poolRoot, or "" when it may. Only the dispatch decision lives here; the
// anti-cascade guards (depth, hourly cap, lock) still run afterwards.
func DispatchRefusal(poolRoot string, a *Agent) string {
	if !a.DeclaresDispatchable() {
		return "not dispatchable"
	}
	if len(a.Mutates) == 0 {
		return ""
	}
	return dispatchRefusal(LoadPoolConfig(poolRoot), a)
}

// dispatchRefusal is DispatchRefusal for a declared-dispatchable peer with
// mutates, against an already-loaded pool config.
func dispatchRefusal(cfg PoolConfig, a *Agent) string {
	head := fmt.Sprintf("mutates %v — state-mutating agents stay human-triggered", a.Mutates)
	if cfg.ConfigErr != nil {
		return head + ": the pool config cannot be parsed, so no grant applies"
	}
	s := cfg.Settings.Grants
	if missing := s.MissingOutboundGrants(a.Name, a.Mutates); len(missing) > 0 {
		names := make([]string, len(missing))
		for i, m := range missing {
			names[i] = "`" + OutboundGrant(m) + "`"
		}
		return fmt.Sprintf("%s: no %s grant in grants.peers.%s (pool config %s)", head, strings.Join(names, ", "), a.Name, PoolConfigFile)
	}
	if s.Paused {
		return head + ": grants are paused (grants.paused in pool config " + PoolConfigFile + ")"
	}
	return ""
}

// dispatchableUnder reports whether a may be dispatched headless under cfg —
// DispatchRefusal for callers that already hold the pool config.
func dispatchableUnder(cfg PoolConfig, a *Agent) bool {
	if !a.DeclaresDispatchable() {
		return false
	}
	return len(a.Mutates) == 0 || dispatchRefusal(cfg, a) == ""
}
