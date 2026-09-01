// Package mesh implements the meshctl CLI's core: manifest parsing, on-demand
// directory, file-based intake, headless dispatch with anti-cascade guards, and
// the ADR-0011 concurrency layer (session presence, atomic claims, KB worktrees).
package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Runtime artifact + layout constants. Coordination/ephemeral state lives on the
// shared filesystem outside any repo (ADR-0011); the mesh kit itself is one repo.
const (
	MeshRepoName = "agent-mesh" // legacy doctrine dir name (detection + doctor skip only)
	CommsFile    = "agent-comms.md"

	// PoolMarkerDir (ADR-0031/0037/0038) is the pool's single infra footprint: a
	// `.agentmesh/` dir at the pool root holding the marker, doctrine, and all
	// runtime state. Agents are first-class sibling repos next to it (poly-repo
	// native, ADR-0038).
	PoolMarkerDir  = ".agentmesh"
	PoolMarkerFile = "pool.yaml"

	// All pool infra now lives UNDER .agentmesh/ (ADR-0038), so a fresh pool root
	// shows exactly one mesh entry (`.agentmesh/`) beside the agent repos.
	DispatchLog   = ".agentmesh/dispatch.log"
	DispatchCount = ".agentmesh/dispatch.count"
	DispatchLock  = ".agentmesh/dispatch.lock"

	SessionsDir  = ".sessions" // per-agent, inside the agent workspace
	WorktreesDir = ".agentmesh/worktrees"

	ManifestName = "agent.yaml"
	IntakeDir    = "intake"

	// DoctrineDir is the pool-relative dir the doctrine docs are emitted to
	// (ADR-0038: under .agentmesh/). Legacy pools used the top-level `agent-mesh/`
	// dir — see `pool migrate`.
	DoctrineDir = ".agentmesh/doctrine"

	// LegacyDoctrineDir is the pre-ADR-0038 top-level doctrine dir; detected for
	// migration + back-compat.
	LegacyDoctrineDir = MeshRepoName
)

// AgentsDir resolves the agents container, in priority order:
//  1. $AGENTS_DIR if set.
//  2. The nearest ancestor of the current working directory that contains a
//     `agent-mesh/agent-comms.md` (i.e. we are inside the mesh or a sibling
//     agent workspace) — that ancestor is the container.
//  3. ~/agents as the last-resort default.
//
// Relocatable behavior without hardcoding a path,
// and works for a $PATH install run from any agent workspace.
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
			// Preferred marker (ADR-0031): a .agentmesh/ pool dir.
			if dirExists(filepath.Join(dir, PoolMarkerDir)) {
				return dir, nil
			}
			// Back-compat (ADR-0029): the doctrine clone marker.
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

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve $AGENTS_DIR, mesh ancestor, or home dir: %w", err)
	}
	return filepath.Join(home, "agents"), nil
}

// AgentDir returns the workspace directory for a named agent.
func AgentDir(agentsDir, name string) string { return filepath.Join(agentsDir, name) }

// ManifestPath returns the agent.yaml path for a named agent.
func ManifestPath(agentsDir, name string) string {
	return filepath.Join(agentsDir, name, ManifestName)
}

// PoolMarkerPath is <poolRoot>/.agentmesh/pool.yaml.
func PoolMarkerPath(poolRoot string) string {
	return filepath.Join(poolRoot, PoolMarkerDir, PoolMarkerFile)
}

// IsPool reports whether poolRoot already carries the binary-managed marker.
func IsPool(poolRoot string) bool {
	return dirExists(filepath.Join(poolRoot, PoolMarkerDir))
}

// WritePoolMarker writes <poolRoot>/.agentmesh/pool.yaml with pool identity.
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
	body := fmt.Sprintf("# meshctl pool marker (ADR-0031) — binary-managed; identity for this pool.\nname: %s\ncreated: %s\ndoctrine_version: %s\n"+
		"# Mesh-wide tool reference (ADR-0042) — uncomment + edit; injected at session start,\n"+
		"# checked by `meshctl doctor`. Pointers only; agents self-serve `<cmd> --help`.\n"+
		"# tools:\n"+
		"#   - { cmd: tea, use: \"Gitea CLI — prefer over gh\", help: \"tea --help\" }\n"+
		"#   - { cmd: gh, avoid: \"this mesh uses tea\" }\n",
		name, created.UTC().Format(time.RFC3339), doctrineVersion)
	return os.WriteFile(p, []byte(body), 0o644)
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
