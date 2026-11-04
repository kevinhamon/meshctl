package mesh

import (
	"os"
	"path/filepath"
)

// skills_link.go — the mesh skills (mesh-cognition, mesh-steward) are rendered
// once per pool into pool/skills/<name>/, and each agent's harness skill
// directory is a gitignored relative symlink to that copy (issue #12). An
// upgrade then writes one copy, not one per agent; each pool keeps its own
// version. Claude Code and opencode both follow a symlinked skill directory.

// PoolSkillPath is the pool's rendered copy of a mesh skill.
func PoolSkillPath(poolRoot, name string) string {
	return filepath.Join(PoolDir(poolRoot), PoolSkillsDirName, name)
}

// renderPoolSkill writes the pool's copy of skill name when it differs.
func renderPoolSkill(poolRoot, name string) (bool, error) {
	body, err := renderedSkill(poolRoot, name)
	if err != nil {
		return false, err
	}
	return writeSkillFile(filepath.Join(PoolSkillPath(poolRoot, name), "SKILL.md"), body)
}

// skillLinkTarget is the relative link from the agent's skill directory to the
// pool's copy.
func skillLinkTarget(poolRoot, dest, name string, h Harness) (string, error) {
	return filepath.Rel(filepath.Dir(h.SkillDir(dest, name)), PoolSkillPath(poolRoot, name))
}

// skillLinked reports whether the agent's skill directory already links to the
// pool's copy.
func skillLinked(poolRoot, dest, name string, h Harness) bool {
	want, err := skillLinkTarget(poolRoot, dest, name, h)
	if err != nil {
		return false
	}
	got, err := os.Readlink(h.SkillDir(dest, name))
	return err == nil && got == want
}

// linkSkill renders the pool's copy and points the agent's skill directory at
// it, replacing a rendered copy or a stale link. When the platform cannot make
// the link, the agent gets a rendered copy instead. changed is true when the
// agent's skill was created or replaced.
func linkSkill(poolRoot, dest, name string, h Harness) (changed bool, err error) {
	if _, err := renderPoolSkill(poolRoot, name); err != nil {
		return false, err
	}
	if skillLinked(poolRoot, dest, name, h) {
		return false, nil
	}
	target, err := skillLinkTarget(poolRoot, dest, name, h)
	if err != nil {
		return false, err
	}
	dir := h.SkillDir(dest, name)
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return false, err
	}
	if err := os.Symlink(target, dir); err != nil {
		body, rerr := renderedSkill(poolRoot, name)
		if rerr != nil {
			return false, rerr
		}
		return h.RenderSkill(name, body, dest)
	}
	return true, nil
}
