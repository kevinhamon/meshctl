package mesh

import (
	"os"
)

// all_pools.go — the host-wide walk behind `--all-pools`: every registered
// pool, classified, and which harnesses each agent is wired for. The walk
// only reports: a registry entry whose path is gone is never forgotten here
// (`meshctl pool forget` is the human's or the steward's call).

// PoolState classifies one registry entry for a host-wide walk.
type PoolState string

const (
	PoolOK      PoolState = "ok"         // the path holds a pool
	PoolMissing PoolState = "missing"    // the path does not exist
	PoolNotPool PoolState = "not-a-pool" // the path exists but holds no pool marker
)

// WalkedPool is one registry entry and its state.
type WalkedPool struct {
	PoolEntry
	State PoolState
}

// Reason is a one-line explanation for a skipped pool ("" for PoolOK).
func (w WalkedPool) Reason() string {
	switch w.State {
	case PoolMissing:
		return "path does not exist — `meshctl pool forget " + w.Name + "` drops the entry"
	case PoolNotPool:
		return "path holds no pool — `meshctl pool forget " + w.Name + "` drops the entry, or `meshctl pool init` it again"
	}
	return ""
}

// ClassifyPool reports whether a registry entry's path holds a pool.
func ClassifyPool(e PoolEntry) PoolState {
	if _, err := os.Stat(e.Path); err != nil {
		if os.IsNotExist(err) {
			return PoolMissing
		}
		return PoolNotPool
	}
	if !IsPool(e.Path) {
		return PoolNotPool
	}
	return PoolOK
}

// AllPools walks the pool registry in its order and classifies every entry.
func AllPools() ([]WalkedPool, error) {
	pools, err := RegisteredPools()
	if err != nil {
		return nil, err
	}
	out := make([]WalkedPool, 0, len(pools))
	for _, e := range pools {
		out = append(out, WalkedPool{PoolEntry: e, State: ClassifyPool(e)})
	}
	return out, nil
}

// HarnessCell is the state of one harness for one agent.
type HarnessCell string

const (
	HarnessWired     HarnessCell = "wired"   // the workspace carries the harness's markers
	HarnessNotWired  HarnessCell = "missing" // installed on this host, not wired
	HarnessNotOnHost HarnessCell = "n/a"     // not installed on this host (and not wired)
)

// AgentHarnessRow is one agent's row of the pool × agent × harness matrix.
type AgentHarnessRow struct {
	Agent string
	// Cells is keyed by harness id, one per registered harness.
	Cells map[string]HarnessCell
}

// HarnessMatrix computes, for every agent in the pool, each registered
// harness's state: wired (markers present, whether or not the binary is
// here), missing (installed on this host but not wired) or n/a. Agents whose
// manifest does not load are left out (doctor reports them).
func HarnessMatrix(poolRoot string, installed []InstalledHarness) ([]AgentHarnessRow, error) {
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return nil, err
	}
	onHost := map[string]bool{}
	for _, h := range installed {
		onHost[h.ID] = true
	}
	ids := RegisteredHarnessIDs()
	rows := make([]AgentHarnessRow, 0, len(agents))
	for _, a := range agents {
		wired := map[string]bool{}
		for _, id := range WiredHarnesses(a.Dir()) {
			wired[id] = true
		}
		row := AgentHarnessRow{Agent: a.Name, Cells: map[string]HarnessCell{}}
		for _, id := range ids {
			switch {
			case wired[id]:
				row.Cells[id] = HarnessWired
			case onHost[id]:
				row.Cells[id] = HarnessNotWired
			default:
				row.Cells[id] = HarnessNotOnHost
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
