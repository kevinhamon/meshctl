package mesh

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// dispatch_grant_tools.go — what a peer dispatched by `ask` may use for the
// grants it holds. The child's harness permissions are fixed (reads plus the
// meshctl answer verbs); a grant that applies at dispatch time (held, not
// paused, under its cap) adds the tools its action needs, and the prompt says
// which actions those are. The gate and the commands still check and log
// every action, so the added tools only make a granted action reachable.
// With no grant that applies, argv, config and prompt are what they were.

// dispatchToolsEnv carries the extra Claude `--allowedTools` entries from Ask
// to the real runner, one per line.
const dispatchToolsEnv = "MESHCTL_DISPATCH_TOOLS"

// grantedDispatch is the extra permissions and prompt lines for one dispatch.
type grantedDispatch struct {
	Grants   []string          // grant names that apply, in order
	Claude   []string          // extra --allowedTools entries
	Bash     map[string]string // opencode bash pattern → "allow"
	Edit     []string          // opencode edit patterns, relative to the peer's workspace
	Opencode []map[string]any  // opencode permission keys from outbound surfaces' allow
	Prompt   []string          // one line per granted action
}

func (g grantedDispatch) empty() bool { return len(g.Grants) == 0 }

// peerGrantOrder is the order meshctl-owned grants are listed in.
var peerGrantOrder = []string{GrantKBPromote, GrantStateSet, GrantKBConfirm, GrantTriageApply}

// dispatchGrants works out a's granted tools for a dispatch running as
// session, from the pool config at poolRoot, now.
func dispatchGrants(poolRoot string, a *Agent, session string, now time.Time) grantedDispatch {
	g := grantedDispatch{Bash: map[string]string{}}
	if poolRoot == "" || a == nil || !IsPool(poolRoot) {
		return g
	}
	cfg := LoadPoolConfig(poolRoot)
	if cfg.ConfigErr != nil {
		return g
	}
	held := cfg.Settings.Grants.Peers[a.Name]
	applies := func(name string) bool {
		if _, ok := held[name]; !ok {
			return false
		}
		return grantCheck(poolRoot, cfg, a.Name, name, now).Allowed
	}
	dir := a.Dir()
	summary := SummaryPath(dir, session)
	files := func(paths ...string) {
		for _, p := range paths {
			// Claude Code matches file paths on Edit rules only; they cover Write too.
			g.Claude = append(g.Claude, "Edit(/"+p+")")
			if rel, err := filepath.Rel(dir, p); err == nil {
				g.Edit = append(g.Edit, filepath.ToSlash(rel))
			}
		}
	}
	cmds := func(verbs ...string) {
		for _, v := range verbs {
			g.Claude = append(g.Claude, "Bash(meshctl "+v+":*)")
			g.Bash["meshctl "+v+"*"] = "allow"
		}
	}
	for _, name := range peerGrantOrder {
		if !applies(name) {
			continue
		}
		g.Grants = append(g.Grants, name)
		switch name {
		case GrantKBPromote:
			cmds("kb begin", "kb finish")
			files(filepath.Join(worktreePath(poolRoot, a.Name, session), "**"), summary)
			g.Prompt = append(g.Prompt, "- Promote knowledge (`kb-promote`): `meshctl kb begin`, edit only in that worktree, write your summary at "+summary+", then `meshctl kb finish --summary-file "+summary+"`.")
		case GrantStateSet:
			cmds("state set")
			files(summary)
			g.Prompt = append(g.Prompt, "- Rewrite the current-state digest (`state-set`): write your summary at "+summary+", then `meshctl state set --summary-file "+summary+"`.")
		case GrantKBConfirm:
			cmds("kb reconcile", "kb confirm")
			files(filepath.Join(ReconcileDir(dir), "**"))
			g.Prompt = append(g.Prompt, "- Confirm reviewed entries (`kb-confirm`): `meshctl kb reconcile`, then `meshctl kb confirm <path>... --report <report>`; never `--accept-ungrounded`.")
		case GrantTriageApply:
			cmds("inbox triage")
			files(filepath.Join(TriageDirPath(dir), "**"))
			g.Prompt = append(g.Prompt, "- Close triaged requests (`triage-apply`): `meshctl inbox triage`, then `meshctl inbox triage apply --report <report> <id>...`.")
		}
	}
	surfaces := make([]string, 0, len(a.Mutates))
	surfaces = append(surfaces, a.Mutates...)
	sort.Strings(surfaces)
	for _, surface := range surfaces {
		name := OutboundGrant(surface)
		if !applies(name) {
			continue
		}
		allow := cfg.Settings.Outbound[surface].Allow
		if len(allow.Claude) == 0 && len(allow.Opencode) == 0 {
			continue // nothing the child could use; doctor warns
		}
		g.Grants = append(g.Grants, name)
		g.Claude = append(g.Claude, allow.Claude...)
		if len(allow.Opencode) > 0 {
			g.Opencode = append(g.Opencode, allow.Opencode)
		}
		g.Prompt = append(g.Prompt, "- Write the `"+surface+"` surface (`"+name+"`): only what this request asks for.")
	}
	return g
}

// grantedSystemPrompt is the headless prompt for a dispatch with grants:
// the external-systems line names the granted actions instead of forbidding
// everything. With no grants it is the prompt unchanged.
func grantedSystemPrompt(base string, g grantedDispatch) string {
	if g.empty() {
		return base
	}
	const line = "Do not commit, push, deploy, or touch external systems."
	granted := "Touch nothing outside the granted actions below. The pool config grants you these for this run:\n" +
		strings.Join(g.Prompt, "\n") +
		"\nEach action is checked and logged. If one is refused, stop and respond `needs-human`, naming it.\n"
	return strings.Replace(base, line, granted, 1)
}

// opencodeDispatchConfigFor is the child's inline opencode config: the fixed
// dispatch config, plus the granted commands, edit paths and surface
// permissions. With no grants it is opencodeDispatchConfig byte for byte.
func opencodeDispatchConfigFor(g grantedDispatch) string {
	if g.empty() {
		return opencodeDispatchConfig
	}
	var cfg map[string]map[string]any
	if err := json.Unmarshal([]byte(opencodeDispatchConfig), &cfg); err != nil {
		return opencodeDispatchConfig
	}
	perm := cfg["permission"]
	bash, _ := perm["bash"].(map[string]any)
	for k, v := range g.Bash {
		bash[k] = v
	}
	if len(g.Edit) > 0 {
		edit := map[string]any{"*": "deny"}
		for _, p := range g.Edit {
			edit[p] = "allow"
		}
		perm["edit"] = edit
	}
	for _, extra := range g.Opencode {
		for k, v := range extra {
			cur, curMap := perm[k].(map[string]any)
			add, addMap := v.(map[string]any)
			if curMap && addMap {
				for kk, vv := range add {
					cur[kk] = vv
				}
				continue
			}
			perm[k] = v
		}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return opencodeDispatchConfig
	}
	return string(b)
}

// dispatchToolsFromEnv reads the extra Claude tools Ask put in env.
func dispatchToolsFromEnv(env []string) []string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], dispatchToolsEnv+"="); ok {
			if v == "" {
				return nil
			}
			return strings.Split(v, "\n")
		}
	}
	return nil
}
