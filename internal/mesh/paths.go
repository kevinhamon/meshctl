// Package mesh implements the meshctl CLI's core: manifest parsing, on-demand
// directory, file-based intake, headless dispatch with anti-cascade guards, and
// the concurrency layer (session presence, atomic claims, KB worktrees).
package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Runtime artifact + layout constants. Coordination/ephemeral state lives on the
// shared filesystem outside any repo; the mesh kit itself is one repo.
const (
	MeshRepoName = "agent-mesh" // legacy doctrine dir name (detection + doctor skip only)
	CommsFile    = "agent-comms.md"

	// CurrentPoolDir is the pool directory: the pool's single infra
	// footprint at the pool root — a visible git repo holding the marker, the
	// doctrine, the pool glossary and (ignored) runtime state, with agents as
	// first-class sibling repos beside it.
	CurrentPoolDir = "pool"
	// LegacyPoolDir is the hidden pool directory of older pools, recognised
	// until `pool upgrade` renames it. Both names live here and nowhere else —
	// PoolDir resolves them.
	LegacyPoolDir = ".agentmesh"

	// Names inside the pool directory. Build paths with the *Path helpers below;
	// nothing joins these onto a pool root directly.
	PoolMarkerFile    = "pool.yaml"
	PoolConfigFile    = "config.yaml"
	PoolGlossaryFile  = "glossary.md"
	PoolIgnoreFile    = ".gitignore"
	DispatchLogFile   = "dispatch.log"
	DispatchCountFile = "dispatch.count"
	DispatchLockFile  = "dispatch.lock"
	WorktreesDirName  = "worktrees"
	DoctrineDirName   = "doctrine"

	SessionsDir = ".sessions" // per-agent, inside the agent workspace

	ManifestName = "agent.yaml"
	IntakeDir    = "intake"

	// LegacyDoctrineDir is the older top-level doctrine dir; detected for
	// migration + back-compat.
	LegacyDoctrineDir = MeshRepoName
)

// PoolDir resolves the pool directory for a pool root: the one directory that
// holds the marker, the doctrine and the runtime state. Every runtime path is
// built from its result, so a change of layout is a change here alone.
// Resolution: `pool/pool.yaml` when present; else the legacy hidden directory
// when it exists (marker or not — the oldest pools had none); else `pool/`,
// the layout every new pool gets.
func PoolDir(poolRoot string) string {
	cur := filepath.Join(poolRoot, CurrentPoolDir)
	if fileExists(filepath.Join(cur, PoolMarkerFile)) {
		return cur
	}
	if legacy := filepath.Join(poolRoot, LegacyPoolDir); dirExists(legacy) {
		return legacy
	}
	return cur
}

// PoolDirIsLegacy reports whether the pool still lives in the hidden legacy
// directory, i.e. `pool upgrade` has not migrated it yet.
func PoolDirIsLegacy(poolRoot string) bool {
	return filepath.Base(PoolDir(poolRoot)) == LegacyPoolDir
}

// PoolDirName is the resolved pool directory's name relative to the pool root,
// for messages and rendered pointers.
func PoolDirName(poolRoot string) string {
	return filepath.Base(PoolDir(poolRoot))
}

// DispatchLogPath is the dispatch audit log.
func DispatchLogPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), DispatchLogFile)
}

// DispatchCountPath is the rolling-hour dispatch counter.
func DispatchCountPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), DispatchCountFile)
}

// DispatchLockPath is the single-flight dispatch lock (a mkdir lock).
func DispatchLockPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), DispatchLockFile)
}

// WorktreesPath holds the KB and delegated-work worktrees of every peer.
func WorktreesPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), WorktreesDirName)
}

// DoctrinePath is where the doctrine docs are emitted. Legacy pools
// used the top-level `agent-mesh/` dir — see `pool migrate`.
func DoctrinePath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), DoctrineDirName)
}

// PoolGlossaryPath is the pool glossary: the one vocabulary every
// peer uses, tracked in the pool repo.
func PoolGlossaryPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), PoolGlossaryFile)
}

// PoolConfigPath is the human-owned pool settings file: knowledge
// kind, outbound surfaces, reconcile order, tool reference. Never rewritten by
// meshctl; see pool_config.go.
func PoolConfigPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), PoolConfigFile)
}

// PoolIgnorePath is the pool repo's ignore file, which keeps runtime state
// (dispatch files, worktrees) out of the pool's history.
func PoolIgnorePath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), PoolIgnoreFile)
}

// poolRel is a path under the pool directory, relative to the pool root, for
// messages that report what was written.
func poolRel(poolRoot string, elem ...string) string {
	return filepath.Join(append([]string{PoolDirName(poolRoot)}, elem...)...)
}

// AgentsDir resolves the agents container, in priority order:
//  1. $AGENTS_DIR if set.
//  2. The nearest ancestor of the current working directory that contains a
//     `agent-mesh/agent-comms.md` (i.e. we are inside the mesh or a sibling
//     agent workspace) — that ancestor is the container.
//
// There is deliberately no default pool. A machine can hold several pools, and
// silently falling back to one (formerly ~/agents) made a command run from the
// wrong directory act on another pool — an agent would see a foreign roster
// and lose track of which pool it belongs to. Outside a pool this errors.
func AgentsDir() (string, error) {
	if v := os.Getenv("AGENTS_DIR"); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", err
		}
		return abs, nil
	}

	if cwd, err := os.Getwd(); err == nil {
		for dir := cwd; ; {
			// Preferred marker: the resolved pool dir.
			if IsPool(dir) {
				return dir, nil
			}
			// Back-compat: the doctrine clone marker.
			if fileExists(filepath.Join(dir, MeshRepoName, CommsFile)) {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	cwd, _ := os.Getwd()
	return "", fmt.Errorf("not inside a mesh pool: no %s/ in %s or any parent — run meshctl from a pool "+
		"or agent workspace, or set $AGENTS_DIR (`meshctl pool list` shows this machine's pools)", PoolDirName(cwd), cwd)
}

// AgentDir returns the workspace directory for a named agent.
func AgentDir(agentsDir, name string) string { return filepath.Join(agentsDir, name) }

// ManifestPath returns the agent.yaml path for a named agent.
func ManifestPath(agentsDir, name string) string {
	return filepath.Join(agentsDir, name, ManifestName)
}

// PoolMarkerPath is the binary-managed pool identity file inside PoolDir.
func PoolMarkerPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), PoolMarkerFile)
}

// IsPool reports whether poolRoot is a pool: it carries `pool/pool.yaml`, or the
// legacy hidden directory (whose marker is optional — the oldest pools
// gained it on their first repair). A bare `pool/` without a marker is not a
// pool: the visible name can collide with a user's own directory.
func IsPool(poolRoot string) bool {
	if dirExists(filepath.Join(poolRoot, LegacyPoolDir)) {
		return true
	}
	return fileExists(filepath.Join(poolRoot, CurrentPoolDir, PoolMarkerFile))
}

// WritePoolMarker writes the pool marker (PoolMarkerPath) with pool identity.
// Idempotent by caller: pass overwrite=false to leave an existing marker intact.
func WritePoolMarker(poolRoot, name, doctrineVersion string, created time.Time, overwrite bool) error {
	p := PoolMarkerPath(poolRoot)
	if fileExists(p) && !overwrite {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if name == "" {
		name = filepath.Base(poolRoot)
	}
	return os.WriteFile(p, []byte(poolMarkerBody(name, created.UTC().Format(time.RFC3339), doctrineVersion)), 0o644)
}

// poolMarkerBody is the identity file's whole content: identity only. Human
// settings (tool reference included) live in PoolConfigPath.
func poolMarkerBody(name, created, doctrineVersion string) string {
	return fmt.Sprintf("# meshctl pool marker — binary-managed; identity for this pool.\n"+
		"# Pool settings (knowledge, outbound, reconcile, tools) go in %s beside this file.\n"+
		"name: %s\ncreated: %s\ndoctrine_version: %s\n", PoolConfigFile, name, created, doctrineVersion)
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
