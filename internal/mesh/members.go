package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// members.go — the pool's member list: each agent's
// folder, git remote and branch, kept in the pool repo so `meshctl pool
// clone` can rebuild the pool on another machine. Membership itself is still
// what ScanAgents finds on disk; this file is a record of it, binary-managed
// like pool.yaml and console.yaml, so config.yaml stays the human's. meshctl
// writes and commits it (only that file) and never pushes.

// MembersFile is <pool dir>/members.yaml.
const MembersFile = "members.yaml"

// membersSubject is the subject of the commit that refreshes the list.
const membersSubject = "pool: members"

// WriterVersion is the meshctl version recorded in files the binary writes
// for another machine to read (members.yaml); main sets it at start-up.
var WriterVersion = "devel"

// Member is one agent of the pool as another machine needs it.
type Member struct {
	Name   string `yaml:"name"`
	Path   string `yaml:"path"`   // relative to the pool root
	Remote string `yaml:"remote"` // "" ⇒ cannot be rebuilt elsewhere
	Branch string `yaml:"branch"`
}

// MemberList is the parsed members.yaml.
type MemberList struct {
	Meshctl string   `yaml:"meshctl"` // the meshctl that last wrote the file
	Members []Member `yaml:"members"`
}

// PoolExtra is a folder beside the agents that is not an agent (a shared
// tools repo, a service stack), listed by hand in config.yaml `extras:` (C4).
// `pool clone` clones it too; meshctl only reads the list.
type PoolExtra struct {
	Path   string `yaml:"path"`
	Remote string `yaml:"remote"`
	Branch string `yaml:"branch,omitempty"`
}

// MembersPath is the file's path for the pool at poolRoot.
func MembersPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), MembersFile)
}

// ReadMembers reads the pool's member list; false when there is none or it
// cannot be parsed.
func ReadMembers(poolRoot string) (MemberList, bool) {
	b, err := os.ReadFile(MembersPath(poolRoot))
	if err != nil {
		return MemberList{}, false
	}
	var l MemberList
	if yaml.Unmarshal(b, &l) != nil {
		return MemberList{}, false
	}
	return l, true
}

// ScanMembers builds the member list from what is on disk, ordered by path.
func ScanMembers(poolRoot string) ([]Member, error) {
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return nil, err
	}
	out := []Member{}
	for _, a := range agents {
		dir := a.Dir()
		out = append(out, Member{Name: a.Name, Path: filepath.Base(dir), Remote: repoRemoteURL(dir), Branch: repoBranch(dir)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// repoRemoteURL is the URL of the remote dir's current branch tracks, else
// origin's, else the first remote's; "" when dir is not its own repo or has
// no remote. Its own repo only: an agent folder inside some other work tree
// has no remote of its own to be rebuilt from.
func repoRemoteURL(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return ""
	}
	name := ""
	if b := repoBranch(dir); b != "" {
		name = gitConfig(dir, "branch."+b+".remote")
	}
	if name == "" || name == "." {
		out, _ := git(dir, "remote")
		remotes := strings.Fields(out)
		switch {
		case len(remotes) == 0:
			return ""
		case hasString(remotes, "origin"):
			name = "origin"
		default:
			name = remotes[0]
		}
	}
	return gitConfig(dir, "remote."+name+".url")
}

// repoBranch is dir's current branch; "" when detached or not its own repo.
func repoBranch(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return ""
	}
	out, err := git(dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func gitConfig(dir, key string) string {
	out, err := exec.Command("git", "-C", dir, "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func hasString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// cloneMissingFile (in the pool's machine-local .memory/) lists the members
// `pool clone` could not clone here. While such a path holds no agent the
// list keeps its entry instead of dropping it, so a refresh on the new
// machine never erases the record (the remote to retry from) of a peer it
// lacks.
const cloneMissingFile = "clone-missing.json"

func cloneMissingPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), MemoryDir, cloneMissingFile)
}

// writeCloneMissing records the members a clone left behind; none removes
// the file.
func writeCloneMissing(poolRoot string, ms []Member) error {
	p := cloneMissingPath(poolRoot)
	if len(ms) == 0 {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(ms, "", "  ")
	return writeFileAtomic(p, append(b, '\n'), 0o644)
}

// NotClonedHere is the members a clone left behind whose path still holds
// no agent, by path.
func NotClonedHere(poolRoot string) map[string]Member {
	b, err := os.ReadFile(cloneMissingPath(poolRoot))
	if err != nil {
		return nil
	}
	var ms []Member
	if json.Unmarshal(b, &ms) != nil {
		return nil
	}
	out := map[string]Member{}
	for _, m := range ms {
		if m.Path != "" && !fileExists(filepath.Join(poolRoot, filepath.FromSlash(m.Path), ManifestName)) {
			out[m.Path] = m
		}
	}
	return out
}

// wantedMembers is what members.yaml should list: the agents on disk, plus
// any listed member a clone left behind and that has not arrived.
func wantedMembers(poolRoot string) ([]Member, error) {
	disk, err := ScanMembers(poolRoot)
	if err != nil {
		return nil, err
	}
	kept := NotClonedHere(poolRoot)
	if len(kept) == 0 {
		return disk, nil
	}
	have, _ := ReadMembers(poolRoot)
	for _, m := range have.Members {
		if _, ok := kept[m.Path]; ok {
			disk = append(disk, m)
		}
	}
	sort.Slice(disk, func(i, j int) bool { return disk[i].Path < disk[j].Path })
	return disk, nil
}

// MembersDrift says how members.yaml differs from disk, one phrase per
// difference ("" pieces dropped); nil when it is current. A missing file is
// one difference. The recorded meshctl version is not compared: a newer
// binary alone never rewrites the list.
func MembersDrift(poolRoot string) ([]string, error) {
	disk, err := wantedMembers(poolRoot)
	if err != nil {
		return nil, err
	}
	have, ok := ReadMembers(poolRoot)
	if !ok {
		return []string{poolRel(poolRoot, MembersFile) + " is missing"}, nil
	}
	listed := map[string]Member{}
	for _, m := range have.Members {
		listed[m.Path] = m
	}
	var drift []string
	for _, m := range disk {
		l, ok := listed[m.Path]
		delete(listed, m.Path)
		switch {
		case !ok:
			drift = append(drift, m.Name+" not listed")
		case l.Remote != m.Remote:
			drift = append(drift, m.Name+"'s remote changed")
		case l.Name != m.Name || l.Branch != m.Branch:
			drift = append(drift, m.Name+" changed")
		}
	}
	var gone []string
	for p := range listed {
		gone = append(gone, p)
	}
	sort.Strings(gone)
	for _, p := range gone {
		drift = append(drift, p+" listed but gone")
	}
	return drift, nil
}

const membersHeader = "# meshctl pool members — binary-managed; rebuilt by `pool upgrade`, `agent new`, `agent onboard`.\n" +
	"# `meshctl pool clone <this repo's url>` uses it to rebuild the pool on another machine.\n"

// RefreshMembers rewrites members.yaml from disk when it differs and commits
// it in the pool repo (only that file; anything else uncommitted is left
// alone). changed is false when the list was already current. A pool whose
// directory is not a repo yet gets the file uncommitted; `pool upgrade`'s
// first commit carries it.
func RefreshMembers(poolRoot string) (changed bool, err error) {
	drift, err := MembersDrift(poolRoot)
	if err != nil || len(drift) == 0 {
		return false, err
	}
	disk, err := wantedMembers(poolRoot)
	if err != nil {
		return false, err
	}
	b, err := yaml.Marshal(MemberList{Meshctl: WriterVersion, Members: disk})
	if err != nil {
		return false, err
	}
	p := MembersPath(poolRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	if err := writeFileAtomic(p, append([]byte(membersHeader), b...), 0o644); err != nil {
		return false, err
	}
	pool := PoolDir(poolRoot)
	if !poolRepoPresent(pool) {
		return true, nil
	}
	if _, err := commitPoolPaths(pool, []string{MembersFile}, membersSubject, "Written by meshctl from the agents on disk."); err != nil {
		return true, fmt.Errorf("member list written but the commit in %s/ failed (%v) — set git user.name and user.email, then commit it by hand", PoolDirName(poolRoot), err)
	}
	return true, nil
}
