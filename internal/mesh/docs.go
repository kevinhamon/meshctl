package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// docs.go — documents an agent writes for the person (designs, reports,
// briefs, mockups). They live in the agent's own workspace, in DocsDir or the
// folders agent.yaml lists under `docs:`, never in a repo the agent works on.
// The console lists and renders them.

const (
	// DocsDir is the default documents folder, scaffolded add-only.
	DocsDir = "docs"
	// DocsLocalDir holds documents that must never leave the machine; the
	// meshctl-managed ignore block covers it.
	DocsLocalDir = DocsDir + "/local"
)

// DocFolders returns the agent's document folders, workspace-relative and
// cleaned: the `docs:` list, or DocsDir when it is empty. An entry that is
// absolute or leaves the workspace is dropped (Validate reports it).
func (a *Agent) DocFolders() []string {
	if len(a.Docs) == 0 {
		return []string{DocsDir}
	}
	var out []string
	seen := map[string]bool{}
	for _, d := range a.Docs {
		if c, ok := cleanDocFolder(d); ok && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// cleanDocFolder cleans one `docs:` entry; false when it is empty, absolute
// or escapes the workspace.
func cleanDocFolder(d string) (string, bool) {
	d = strings.TrimSpace(d)
	if d == "" || filepath.IsAbs(d) || strings.HasPrefix(d, "~") {
		return "", false
	}
	c := filepath.Clean(filepath.FromSlash(d))
	if c == "." || c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(c), true
}

// docFolderProblems are the manifest problems with `docs:`.
func (a *Agent) docFolderProblems() []string {
	var out []string
	for _, d := range a.Docs {
		if _, ok := cleanDocFolder(d); !ok {
			out = append(out, fmt.Sprintf("docs: %q must be a folder inside the workspace (relative, no ..)", d))
		}
	}
	return out
}

// docFolderFindings warns about declared document folders that do not exist
// or that a symlink leads out of the workspace. The default folder is only
// reported when the agent declared it.
func docFolderFindings(a *Agent) []DoctorFinding {
	if len(a.Docs) == 0 {
		return nil
	}
	var out []DoctorFinding
	root, err := filepath.EvalSymlinks(a.Dir())
	if err != nil {
		return nil
	}
	for _, d := range a.DocFolders() {
		p := filepath.Join(a.Dir(), filepath.FromSlash(d))
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			out = append(out, DoctorFinding{Agent: a.Name, Level: "warn",
				Message: fmt.Sprintf("agent.yaml docs: lists %s/, which does not exist — create it or drop the entry", d)})
			continue
		}
		if !within(root, real) {
			out = append(out, DoctorFinding{Agent: a.Name, Level: "warn",
				Message: fmt.Sprintf("agent.yaml docs: %s/ leads outside the workspace (%s) — documents belong in the workspace", d, real)})
		}
	}
	return out
}

// within reports whether p is root or below it (both already resolved).
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// scaffoldDocs creates the documents folder with its README. Add-only: an
// existing README is never touched, and a second run is a no-op.
func scaffoldDocs(dest string) (bool, error) {
	readme := filepath.Join(dest, DocsDir, "README.md")
	if fileExists(readme) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(readme), 0o755); err != nil {
		return false, err
	}
	return true, copyTemplate("templates/docs/README.md", readme)
}
