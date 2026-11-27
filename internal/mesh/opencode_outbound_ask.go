package mesh

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// opencode_outbound_ask.go — the opencode V2 ask path for outbound writes and
// for writes to pool knowledge.
//
// Same mechanism as the promotion asks (opencode_ask.go): the plugin's hook
// cannot ask, opencode's own permission rules can. meshctl wires an ask rule
// for each call the gate would ask on, and the gate lets a call through only
// when opencode will ask the human on it; any other form is refused, naming
// what to change in this harness. Validated against opencode 2.4.1 with a
// scripted model:
//   - {"action":"edit","resource":<glob>,"effect":"ask"} covers the write and
//     the edit tool; the resource is the target path relative to the project
//     directory ("../pool/config.yaml"), whether the tool was given a relative
//     or an absolute path; `*` crosses `/`;
//   - a later, narrower allow rule carves a subtree out of an earlier ask.
// A tool-name rule ({"action":<tool glob>,"resource":"*"}) is opencode's
// documented V2 form; it was not run here.
//
// Shell rules are globs and the pool's `commands` are regexes. A regex that is
// a plain literal ("^ssh ", "git push") is wired as its glob ("ssh *",
// "git push*"); anything else needs the surface's `ask.opencode.shell`
// patterns in the pool config, and so does every `tools` regex
// (`ask.opencode.tools`).

// reLiteralCommand matches a command regex with no regex syntax in it but an
// optional leading `^`.
var reLiteralCommand = regexp.MustCompile(`^\^?[A-Za-z0-9 _/=:@,-]+$`)

// literalCommandGlob is the opencode glob equal to a literal command regex.
func literalCommandGlob(pattern string) (string, bool) {
	if !reLiteralCommand.MatchString(pattern) {
		return "", false
	}
	lit := strings.TrimPrefix(pattern, "^")
	if strings.TrimSpace(lit) == "" {
		return "", false
	}
	return lit + "*", true
}

// surfaceAskGlobs are the opencode patterns for one surface: the declared
// ones, else those of its literal command regexes. undeclared lists the
// regexes no pattern covers.
func surfaceAskGlobs(s OutboundSurface) (shell, tools, undeclared []string) {
	shell = append(shell, s.Ask.Opencode.Shell...)
	tools = append(tools, s.Ask.Opencode.Tools...)
	if len(shell) == 0 {
		for _, c := range s.Commands {
			if g, ok := literalCommandGlob(c); ok {
				shell = append(shell, g)
			} else {
				undeclared = append(undeclared, "commands "+c)
			}
		}
	}
	if len(tools) == 0 {
		for _, t := range s.Tools {
			undeclared = append(undeclared, "tools "+t)
		}
	}
	return shell, tools, undeclared
}

// globSample is a string the glob matches: what the wiring check asks about.
func globSample(glob string) string {
	return strings.NewReplacer("*", "x", "?", "x").Replace(glob)
}

// poolEditGlobs are the edit patterns for the pool directory as opencode sees
// it from an agent's workspace, and for the KB worktrees inside it, which
// agents edit freely.
func poolEditGlobs(agentDir string) (pool, worktrees string) {
	rel, err := filepath.Rel(agentDir, PoolDir(filepath.Dir(agentDir)))
	if err != nil {
		rel = filepath.Join("..", CurrentPoolDir)
	}
	rel = filepath.ToSlash(rel)
	return rel + "/*", rel + "/" + WorktreesDirName + "/*"
}

// opencodeGateAskRules are the permission rules an agent's opencode.json
// needs so opencode asks on pool edits and on the outbound surfaces the agent
// declares. undeclared names the matchers no rule could be written for.
func opencodeGateAskRules(agentDir string) (rules []ocPermRule, undeclared []string) {
	pool, wt := poolEditGlobs(agentDir)
	rules = append(rules, ocPermRule{"edit", pool, "ask"}, ocPermRule{"edit", wt, "allow"})
	agentsDir := filepath.Dir(agentDir)
	a, err := FindAgent(agentsDir, filepath.Base(agentDir))
	if err != nil || a == nil {
		return rules, nil
	}
	cfg := LoadPoolConfig(agentsDir)
	if cfg.ConfigErr != nil {
		return rules, nil
	}
	names := make([]string, 0, len(cfg.Settings.Outbound))
	for n := range cfg.Settings.Outbound {
		if hasTag(a.Mutates, n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		shell, tools, missing := surfaceAskGlobs(cfg.Settings.Outbound[n])
		for _, g := range shell {
			rules = append(rules, ocPermRule{"shell", g, "ask"})
		}
		for _, g := range tools {
			rules = append(rules, ocPermRule{g, "*", "ask"})
		}
		for _, m := range missing {
			undeclared = append(undeclared, "outbound."+n+"."+m)
		}
	}
	return rules, undeclared
}

// ruleHolds reports whether ps already gives the rule's effect on a call the
// rule matches.
func (ps ocPerms) ruleHolds(r ocPermRule) bool {
	return ps.effect(globSample(r.Action), globSample(r.Resource)) == r.Effect
}

// missingGateAskRules lists the rules of opencodeGateAskRules the project
// config does not already satisfy, in order.
func missingGateAskRules(agentDir string, ps ocPerms) []ocPermRule {
	want, _ := opencodeGateAskRules(agentDir)
	var out []ocPermRule
	for _, r := range want {
		if !ps.ruleHolds(r) {
			out = append(out, r)
			ps.rules = append(ps.rules, r) // later rules are judged with this one in place
		}
	}
	return out
}

// opencodeGateAskWired reports whether dir's opencode.json asks on pool edits
// and on every outbound pattern the agent's surfaces have.
func opencodeGateAskWired(dir string) bool {
	return len(missingGateAskRules(dir, readOcPerms(dir, false))) == 0
}

// ensureOpencodeGateAskRules appends the missing pool and outbound ask rules
// to a parsed opencode.json. Appended last, since the last matching rule wins.
func ensureOpencodeGateAskRules(root map[string]any, agentDir string) []string {
	rules, v1 := ocPermsOf(root)
	missing := missingGateAskRules(agentDir, ocPerms{rules: rules, v1: v1})
	if len(missing) == 0 {
		return nil
	}
	arr, _ := root["permissions"].([]any)
	var added []string
	for _, r := range missing {
		arr = append(arr, map[string]any{"action": r.Action, "resource": r.Resource, "effect": r.Effect})
		added = append(added, r.Action+" "+r.Resource+" ("+r.Effect+")")
	}
	root["permissions"] = arr
	return added
}

// opencodeCannotAsk is the refusal for a call opencode will not ask on. It
// says what to do in this harness and names no other.
func opencodeCannotAsk(check, what, fix string) GateDecision {
	return GateDecision{Deny: true, Check: check, Reason: "mesh: " + what + " needs your confirmation, and opencode will not ask " +
		"you before it runs. " + fix}
}

// opencodePoolEditAsk hands the gate's ask on a pool write to opencode's own
// ask: through when the call is a file edit opencode asks on, else refused.
func opencodePoolEditAsk(p HookPayload, d GateDecision) GateDecision {
	const what = "this write to pool knowledge"
	if !isFileEditTool(p.ToolName) {
		return opencodeCannotAsk(d.Check, what, "Make the change with the file-edit tool on the pool file itself: opencode asks "+
			"you on that. A shell command or a patch that writes under the pool directory cannot be asked.")
	}
	paths := gateEditPaths(p)
	ps := readOcPerms(p.Cwd, true)
	for _, abs := range paths {
		rel, err := filepath.Rel(p.Cwd, abs)
		if err != nil || ps.effect("edit", filepath.ToSlash(rel)) != "ask" {
			return opencodeCannotAsk(d.Check, what, "Its edit ask rule is missing from opencode.json or overridden. Run "+
				"`meshctl agent onboard` in this workspace, restart opencode, then retry.")
		}
	}
	if len(paths) == 0 {
		return opencodeCannotAsk(d.Check, what, "The gate could not read the target path of this call.")
	}
	return GateDecision{Check: d.Check}
}

// opencodeOutboundAsk hands the gate's ask on an outbound write to opencode's
// own ask. A tool call passes when opencode asks on that tool. A shell call
// passes when every simple command that reaches the surface appears verbatim
// as a top-level command and opencode asks on it (opencode matches its rules
// against the raw text of each simple command). A call let through is written
// to the outbound audit log: opencode has no after-call hook, so the gate
// cannot record whether the human approved it.
func opencodeOutboundAsk(agentsDir string, a *Agent, p HookPayload, mt OutboundMatch, d GateDecision) GateDecision {
	if !d.Ask || !runsUnderOpencode(p) {
		return d
	}
	what := "`" + p.ToolName + "` writes the outbound surface `" + mt.Surface + "`, which"
	fix := "Declare the opencode patterns that reach this surface in " + PoolConfigPath(agentsDir) + " (`outbound." + mt.Surface +
		".ask.opencode`, `shell:` and `tools:` lists), run `meshctl agent onboard` in this workspace, restart opencode, then retry."
	ps := readOcPerms(p.Cwd, true)
	through := func() GateDecision {
		if b, why := outboundBatchFor(a, p, mt); why == "" {
			_ = AppendOutboundAudit(b, p, time.Now())
		}
		return GateDecision{Check: d.Check}
	}
	if !isShellTool(p.ToolName) {
		if ps.effect(p.ToolName, "*") == "ask" {
			return through()
		}
		return opencodeCannotAsk(d.Check, what, fix)
	}
	m, err := LoadOutbound(agentsDir)
	if err != nil {
		return outboundDeny("mesh: outbound gate misconfigured (" + err.Error() + ")")
	}
	command := shellCommandInput(p)
	reaches := func(words []string) bool {
		_, ok := m.surface(mt.Surface).match("", [][]string{words})
		return ok
	}
	asked := 0
	for _, words := range shellSplit(ShellOwnArguments(command)) {
		text := strings.Join(words, " ")
		if reaches(words) && strings.Contains(command, text) && ps.shellEffect(text) == "ask" {
			asked++
		}
	}
	invoked, first := 0, ""
	for _, words := range ShellSimpleCommands(command) {
		if reaches(words) {
			invoked++
			if first == "" {
				first = strings.Join(words, " ")
			}
		}
	}
	if invoked > 0 && asked >= invoked {
		return through()
	}
	return opencodeCannotAsk(d.Check, what, "No ask rule in opencode.json matches `"+first+"` as written. Run it on its own, in its plain "+
		"form (no `env`/`sudo` wrapper, no `sh -c` string). If it is already plain: "+strings.ToLower(fix[:1])+fix[1:])
}

// surface returns the named surface's matcher (the zero matcher when absent).
func (m OutboundMatchers) surface(name string) outboundMatcher {
	for _, s := range m.surfaces {
		if s.name == name {
			return s
		}
	}
	return outboundMatcher{}
}
