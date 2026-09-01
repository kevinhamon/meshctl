package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The pool registry (ADR-0037) is meshctl's ONE piece of global state: a
// discovery-only index of the pools on this machine, so `meshctl pool list` can
// enumerate them from anywhere. It is NOT authoritative and NOT a comms channel —
// AgentsDir() still scopes every roster/comms/KB operation to the cwd pool, so the
// ADR-0029 "pools can't see each other" guarantee is untouched. Losing the registry
// loses no pool data (re-register with `pool register`).
//
// Location is the single platform-specific point (registryPath); *nix uses XDG.
// Windows support is deferred — only registryPath changes.

// PoolEntry is one registered pool.
type PoolEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Created string `json:"created,omitempty"`
}

type poolRegistry struct {
	Pools []PoolEntry `json:"pools"`
}

// registryPath returns the registry file location: $XDG_CONFIG_HOME/meshctl/pools.json,
// else ~/.config/meshctl/pools.json. (Windows: deferred — only this changes.)
func registryPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "meshctl", "pools.json"), nil
}

func loadRegistry() (poolRegistry, error) {
	var reg poolRegistry
	p, err := registryPath()
	if err != nil {
		return reg, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return reg, nil // empty registry
	}
	if err != nil {
		return reg, err
	}
	_ = json.Unmarshal(b, &reg) // tolerant: a corrupt registry reads as empty
	return reg, nil
}

func saveRegistry(reg poolRegistry) error {
	p, err := registryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".pools-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	tmp.Close()
	return os.Rename(tmpName, p)
}

// RegisterPool adds (or updates, keyed by absolute path) a pool in the registry.
// Idempotent. Returns the resolved absolute path.
func RegisterPool(path, name string, now time.Time) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = filepath.Base(abs)
	}
	reg, err := loadRegistry()
	if err != nil {
		return abs, err
	}
	for i, e := range reg.Pools {
		if e.Path == abs {
			reg.Pools[i].Name = name // refresh name; keep original Created
			return abs, saveRegistry(reg)
		}
	}
	reg.Pools = append(reg.Pools, PoolEntry{Name: name, Path: abs, Created: now.UTC().Format(time.RFC3339)})
	return abs, saveRegistry(reg)
}

// ForgetPool removes the registry entry matching an absolute path or a name.
// Does NOT touch the pool on disk. Returns true if an entry was removed.
func ForgetPool(nameOrPath string) (bool, error) {
	abs, _ := filepath.Abs(nameOrPath)
	reg, err := loadRegistry()
	if err != nil {
		return false, err
	}
	var kept []PoolEntry
	removed := false
	for _, e := range reg.Pools {
		if e.Path == abs || e.Name == nameOrPath {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		return false, nil
	}
	reg.Pools = kept
	return true, saveRegistry(reg)
}

// RegisteredPools returns the registry entries (order preserved).
func RegisteredPools() ([]PoolEntry, error) {
	reg, err := loadRegistry()
	return reg.Pools, err
}

// PoolIsLive reports whether a registered pool's path still holds the marker
// (used to flag/prune stale entries at list time).
func PoolIsLive(e PoolEntry) bool { return IsPool(e.Path) }
