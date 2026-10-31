package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// opencode_ask.go — the opencode V2 ask path for gate-asked promotions.
//
// A V2 plugin cannot put a gate ask to the human: its pre-tool hook can only
// let a call through or throw. opencode does ask the human itself through a
// permission rule, {"action":"shell","resource":<glob>,"effect":"ask"}, and a
// non-interactive run refuses such a call. So meshctl wires one ask rule per
// gate-asked command into the agent's opencode.json, and the gate lets those
// calls through to opencode's ask instead of refusing them. Validated against
// opencode 2.0.28 with a scripted model:
//   - the plugin's execute.before runs BEFORE the permission ask, so a gate
//     refusal means the human is never asked;
//   - the last matching rule wins; the project opencode.json beats the user's
//     global config, and OPENCODE_CONFIG_CONTENT beats the project;
//   - a compound command is split and each simple command matched on its own;
//     `*` matches across spaces and flags;
//   - with no matching rule a shell call is allowed; `--auto` approves an ask.

// opencodeAskRule is one command meshctl wires as an opencode ask.
type opencodeAskRule struct {
	Sub      string // the meshctl subcommand the gate asks on
	Resource string // the opencode resource glob wired as "ask"
	Sample   string // a plain invocation the rule must ask on
}

// opencodeAskRules: the gate-asked commands. A `kb finish` or `state set`
// promotes only with --summary-file (the gate refuses one without), so a
// candidates-only finish is not asked, as on Claude Code.
var opencodeAskRules = []opencodeAskRule{
	{"meshctl kb finish", "meshctl kb finish *--summary-file*", "meshctl kb finish --summary-file s.md"},
	{"meshctl state set", "meshctl state set *--summary-file*", "meshctl state set --summary-file s.md"},
	{"meshctl kb confirm", "meshctl kb confirm*", "meshctl kb confirm p.md --report r.md"},
	{"meshctl inbox triage apply", "meshctl inbox triage apply*", "meshctl inbox triage apply"},
}

// ocPermRule is one V2 permission rule.
type ocPermRule struct{ Action, Resource, Effect string }

// ocPerms is what opencode applies to a project on top of the user's global
// config: the ordered V2 rules (project, then OPENCODE_CONFIG_CONTENT) and any
// V1-form `permission` entries, whose order against the V2 rules is unknown.
type ocPerms struct {
	rules []ocPermRule
	v1    []ocPermRule
}

// readOcPerms reads dir/opencode.json and, when withEnv, OPENCODE_CONFIG_CONTENT.
// An unreadable or invalid source contributes nothing.
func readOcPerms(dir string, withEnv bool) ocPerms {
	var ps ocPerms
	add := func(b []byte) {
		var root map[string]any
		if json.Unmarshal(b, &root) != nil {
			return
		}
		r, v := ocPermsOf(root)
		ps.rules = append(ps.rules, r...)
		ps.v1 = append(ps.v1, v...)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "opencode.json")); err == nil {
		add(b)
	}
	if withEnv {
		if s := os.Getenv("OPENCODE_CONFIG_CONTENT"); s != "" {
			add([]byte(s))
		}
	}
	return ps
}

// ocPermsOf extracts the V2 `permissions` array and the V1 `permission` form
// (a bare effect, or per tool a bare effect or a pattern→effect map; V1's
// `bash` is V2's `shell`).
func ocPermsOf(root map[string]any) (rules, v1 []ocPermRule) {
	arr, _ := root["permissions"].([]any)
	for _, x := range arr {
		m, _ := x.(map[string]any)
		a, _ := m["action"].(string)
		r, _ := m["resource"].(string)
		e, _ := m["effect"].(string)
		if a != "" && e != "" {
			if r == "" {
				r = "*"
			}
			rules = append(rules, ocPermRule{a, r, e})
		}
	}
	switch p := root["permission"].(type) {
	case string:
		v1 = append(v1, ocPermRule{"*", "*", p})
	case map[string]any:
		for tool, v := range p {
			if tool == "bash" {
				tool = "shell"
			}
			switch e := v.(type) {
			case string:
				v1 = append(v1, ocPermRule{tool, "*", e})
			case map[string]any:
				for pat, eff := range e {
					if s, ok := eff.(string); ok {
						v1 = append(v1, ocPermRule{tool, pat, s})
					}
				}
			}
		}
	}
	return rules, v1
}

// ocGlob reports whether s matches an opencode glob: `*` any run, `?` one character.
func ocGlob(pattern, s string) bool {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(s)
}

// shellEffect is the effect opencode applies to one simple shell command.
func (ps ocPerms) shellEffect(command string) string { return ps.effect("shell", command) }

// effect is the effect opencode applies to one action on one resource: the
// last matching V2 rule, else allow. A matching V1-form allow wins whatever
// the order, since its place among the V2 rules is unknown.
func (ps ocPerms) effect(action, resource string) string {
	eff := "allow"
	for _, r := range ps.rules {
		if ocGlob(r.Action, action) && ocGlob(r.Resource, resource) {
			eff = r.Effect
		}
	}
	for _, r := range ps.v1 {
		if r.Effect == "allow" && ocGlob(r.Action, action) && ocGlob(r.Resource, resource) {
			return "allow"
		}
	}
	return eff
}

// mutatingWorkPeers are the in-session work definitions a caller holds for
// state-mutating peers: each delegation to one is the human's call,
// which on opencode V2 is opencode's own ask on the subagent call.
func mutatingWorkPeers(callerDir string) []string {
	var out []string
	agents, _, err := ScanAgents(filepath.Dir(callerDir))
	if err != nil {
		return nil
	}
	for stem, d := range wantedPeerDefs(agents, callerDir) {
		if d.work && len(d.peer.Mutates) > 0 {
			out = append(out, PeerAgentPrefix+stem)
		}
	}
	sort.Strings(out)
	return out
}

// missingSubagentAsk lists the mutating work definitions opencode would launch
// without asking: the project rules and, when withEnv, OPENCODE_CONFIG_CONTENT.
func missingSubagentAsk(callerDir string, withEnv bool) []string {
	ps := readOcPerms(callerDir, withEnv)
	var out []string
	for _, name := range mutatingWorkPeers(callerDir) {
		if ps.effect("subagent", name) != "ask" {
			out = append(out, name)
		}
	}
	return out
}

// ensureOpencodeSubagentAsk appends a subagent ask rule to the caller's
// opencode.json for each mutating work definition opencode would not ask on.
// Add-only; appended last, since the last matching rule wins.
func ensureOpencodeSubagentAsk(callerDir string) ([]string, error) {
	missing := missingSubagentAsk(callerDir, false)
	if len(missing) == 0 {
		return nil, nil
	}
	path := filepath.Join(callerDir, "opencode.json")
	root := map[string]any{"$schema": "https://opencode.ai/config.json"}
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if json.Unmarshal(b, &root) != nil {
			return []string{"opencode.json is not valid JSON — add subagent ask rules for " + strings.Join(missing, ", ") + " by hand"}, nil
		}
	}
	arr, _ := root["permissions"].([]any)
	for _, name := range missing {
		arr = append(arr, map[string]any{"action": "subagent", "resource": name, "effect": "ask"})
	}
	root["permissions"] = arr
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	return []string{"opencode.json: subagent ask rules for " + strings.Join(missing, ", ")}, nil
}

// opencodeDelegationAsk hands the gate's ask on a delegation to a mutating
// peer to opencode's own ask: through when opencode will ask on that
// subagent, refused otherwise.
func opencodeDelegationAsk(p HookPayload, d GateDecision, subagent string) GateDecision {
	if !d.Ask || p.Harness != HarnessOpencode {
		return d
	}
	if readOcPerms(p.Cwd, true).effect("subagent", subagent) == "ask" {
		return GateDecision{Check: d.Check}
	}
	return GateDecision{Deny: true, Check: d.Check, Reason: "mesh: delegating to `" + subagent + "` needs your approval, and opencode " +
		"will not ask you before it runs: its subagent ask rule is missing from opencode.json or overridden. Run `meshctl agent onboard` " +
		"in this workspace, then retry."}
}

// opencodeAskWired reports whether opencode asks the human on every
// gate-asked command in dir: the project rules and, when withEnv, the
// OPENCODE_CONFIG_CONTENT this process sees.
func opencodeAskWired(dir string, withEnv bool) bool {
	ps := readOcPerms(dir, withEnv)
	for _, r := range opencodeAskRules {
		if ps.shellEffect(r.Sample) != "ask" {
			return false
		}
	}
	return true
}

// ensureOpencodeAskRules appends the ask rule for each gate-asked command the
// project config does not already ask on. Appended last, since the last
// matching rule wins. Returns the resources it added.
func ensureOpencodeAskRules(root map[string]any) []string {
	rules, v1 := ocPermsOf(root)
	ps := ocPerms{rules: rules, v1: v1}
	arr, _ := root["permissions"].([]any)
	var added []string
	for _, r := range opencodeAskRules {
		if ps.shellEffect(r.Sample) == "ask" {
			continue
		}
		arr = append(arr, map[string]any{"action": "shell", "resource": r.Resource, "effect": "ask"})
		ps.rules = append(ps.rules, ocPermRule{"shell", r.Resource, "ask"})
		added = append(added, r.Resource)
	}
	if len(added) > 0 {
		root["permissions"] = arr
	}
	return added
}

// gatedRule returns the ask rule whose subcommand words invokes, or nil.
func gatedRule(words []string) *opencodeAskRule {
	for i, r := range opencodeAskRules {
		if shellWordsMatch(words, strings.Fields(r.Sub)) {
			return &opencodeAskRules[i]
		}
	}
	return nil
}

// opencodeAskPath hands a gate ask on a promotion command to opencode's own
// ask. It applies only under the opencode harness, and lets the
// call through only when opencode will ask the human on every invocation of a
// gate-asked subcommand in it. opencode matches its rules against the raw
// text of each simple command, so an invocation counts only as a top-level
// command that appears verbatim in the line (no quoting, no `env`/`sudo`
// wrapper, no `bash -c` string, `meshctl` by name); any other form is
// refused, naming the plain form. Other decisions are returned unchanged.
func opencodeAskPath(p HookPayload, d GateDecision) GateDecision {
	if !d.Ask || p.Harness != HarnessOpencode {
		return d
	}
	command := shellCommandInput(p)
	ps := readOcPerms(p.Cwd, true)
	asked := 0
	for _, words := range shellSplit(ShellOwnArguments(command)) {
		text := strings.Join(words, " ")
		if gatedRule(words) != nil && strings.Contains(command, text) && ps.shellEffect(text) == "ask" {
			asked++
		}
	}
	invoked := 0
	var first *opencodeAskRule
	for _, words := range ShellSimpleCommands(command) {
		if r := gatedRule(words); r != nil {
			invoked++
			if first == nil {
				first = r
			}
		}
	}
	if invoked > 0 && asked >= invoked {
		return GateDecision{Check: d.Check}
	}
	if first == nil {
		return GateDecision{Deny: true, Check: d.Check, Reason: "mesh: this call needs your confirmation and opencode has no ask " +
			"path for it; run it yourself, or from a Claude Code session."}
	}
	return GateDecision{Deny: true, Check: d.Check, Reason: "mesh: this `" + first.Sub + "` needs your confirmation, and opencode " +
		"will not ask you before it runs: its ask rule (`" + first.Resource + "`) is missing from opencode.json, overridden, or " +
		"does not match this form of the command. Run `meshctl agent onboard` in this workspace if the rule is missing, then " +
		"run it on its own in the plain form `" + first.Sample + "` so opencode asks you."}
}
