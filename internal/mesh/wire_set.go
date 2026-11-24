package mesh

import (
	"fmt"
	"strings"
)

// wire_set.go — which harnesses an agent workspace is wired for. Every
// harness installed on this host is wired into every agent, so a workspace
// works under whichever harness opens it; the pool may narrow that with the
// `harnesses:` allowlist in its config file. The agent's `harness:` key is
// its preferred harness: always wired when installed, even if the allowlist
// leaves it out. A harness that is not installed is skipped (its files are
// never written) and reported. Scaffold, onboard, `pool upgrade` and doctor
// all compute the same set here, so the plan and the apply agree.

// installedHarnessesFn is the host probe the wire set uses. Tests stub it so
// they never depend on what this machine has installed.
var installedHarnessesFn = InstalledHarnesses

// PoolHarnessAllowlist is the pool config's `harnesses:` allowlist,
// normalized and in the order written; nil ⇒ every registered harness. Ids
// that are not registered are dropped here and reported by doctor.
func PoolHarnessAllowlist(poolRoot string) []string {
	var out []string
	for _, id := range LoadPoolConfig(poolRoot).Settings.Harnesses {
		if s, ok := LookupHarness(id); ok {
			out = append(out, s.ID)
		}
	}
	return out
}

// poolAllowsAll reports whether the pool sets no usable allowlist.
func poolAllowsAll(poolRoot string) bool {
	return len(LoadPoolConfig(poolRoot).Settings.Harnesses) == 0
}

// WireSet is the harnesses every agent in the pool is wired for: registered ∩
// installed on this host ∩ the pool's `harnesses:` allowlist (empty ⇒ all),
// in registration order. It probes the installed binaries: call it at wire,
// upgrade and doctor time, never from a hook.
func WireSet(poolRoot string) []InstalledHarness {
	return wireSetFrom(poolRoot, installedHarnessesFn())
}

func wireSetFrom(poolRoot string, installed []InstalledHarness) []InstalledHarness {
	allowAll := poolAllowsAll(poolRoot)
	allowed := map[string]bool{}
	for _, id := range PoolHarnessAllowlist(poolRoot) {
		allowed[id] = true
	}
	var out []InstalledHarness
	for _, h := range installed {
		if _, ok := LookupHarness(h.ID); !ok {
			continue
		}
		if allowAll || allowed[h.ID] {
			out = append(out, h)
		}
	}
	return out
}

// AgentWireSet is WireSet plus the agent's preferred harness when it is
// installed (the allowlist never unwires the harness an agent prefers), in
// registration order.
func AgentWireSet(poolRoot, preferred string) []InstalledHarness {
	return agentWireSetFrom(poolRoot, preferred, installedHarnessesFn())
}

func agentWireSetFrom(poolRoot, preferred string, installed []InstalledHarness) []InstalledHarness {
	set := wireSetFrom(poolRoot, installed)
	pref := normHarnessID(preferred)
	if pref == "" {
		pref = DefaultHarness
	}
	if installedHas(set, pref) {
		return set
	}
	h, ok := installedFind(installed, pref)
	if !ok {
		return set
	}
	// Re-insert in registration order.
	var out []InstalledHarness
	for _, id := range RegisteredHarnessIDs() {
		if id == pref {
			out = append(out, h)
			continue
		}
		if x, ok := installedFind(set, id); ok {
			out = append(out, x)
		}
	}
	return out
}

func installedHas(set []InstalledHarness, id string) bool {
	_, ok := installedFind(set, id)
	return ok
}

func installedFind(set []InstalledHarness, id string) (InstalledHarness, bool) {
	id = normHarnessID(id)
	for _, h := range set {
		if h.ID == id {
			return h, true
		}
	}
	return InstalledHarness{}, false
}

// wireTarget is one harness adapter to wire into a workspace.
type wireTarget struct {
	ID      string
	Harness Harness
}

// wireTargets resolves the adapters to wire into an agent workspace and the
// notes to show for the harnesses skipped. preferred is the agent's
// preference; pin pins its variant (0 ⇒ the installed one). When no
// registered harness is installed at all, the preferred harness is wired
// anyway — an agent is never left with no gate — and the note says so.
func wireTargets(poolRoot, preferred string, pin int) ([]wireTarget, []string) {
	return wireTargetsFrom(poolRoot, preferred, pin, installedHarnessesFn())
}

func wireTargetsFrom(poolRoot, preferred string, pin int, installed []InstalledHarness) ([]wireTarget, []string) {
	pref := normHarnessID(preferred)
	if _, ok := LookupHarness(pref); !ok {
		pref = DefaultHarness
	}
	set := agentWireSetFrom(poolRoot, pref, installed)
	var notes []string
	if !installedHas(installed, pref) {
		notes = append(notes, "preferred harness "+harnessIDLabel(pref)+" is not installed on this host")
	}
	for _, s := range RegisteredHarnesses() {
		if !installedHas(installed, s.ID) && s.ID != pref {
			notes = append(notes, "skipped "+s.Label+": not installed on this host")
		}
	}
	if len(set) == 0 {
		notes = append(notes, "no registered harness is installed on this host — wired the preferred harness "+harnessIDLabel(pref)+" only")
		return []wireTarget{{ID: pref, Harness: HarnessByNameVersion(pref, pin)}}, notes
	}
	var out []wireTarget
	for _, h := range set {
		v := h.Variant
		if h.ID == pref && pin > 0 {
			v = pin
		}
		out = append(out, wireTarget{ID: h.ID, Harness: HarnessByNameVersion(h.ID, v)})
	}
	return out, notes
}

// harnessIDLabel is a harness id's human name, or its id when unregistered.
func harnessIDLabel(id string) string {
	if s, ok := LookupHarness(id); ok && s.Label != "" {
		return s.Label
	}
	return id
}

// harnessAllowlistFindings are doctor's findings on the pool's `harnesses:`
// allowlist: an id that names no registered harness is an error (a typo would
// otherwise silently unwire every harness it was meant to keep).
func harnessAllowlistFindings(poolRoot string, cfg PoolConfig) []DoctorFinding {
	var bad []string
	for _, id := range cfg.Settings.Harnesses {
		if _, err := ResolveHarness(id, 0); err != nil || strings.TrimSpace(id) == "" {
			bad = append(bad, fmt.Sprintf("%q", id))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return []DoctorFinding{{Agent: "pool", Level: "error", Message: poolRel(poolRoot, PoolConfigFile) +
		": `harnesses` names unknown harness(es) " + strings.Join(bad, ", ") +
		" (registered: " + strings.Join(RegisteredHarnessIDs(), ", ") + ") — fix the list; unknown ids are ignored"}}
}
