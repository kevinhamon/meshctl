package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// outbound.go — the outbound gate, deny side (ADR-0068 and its amendment; spec
// decisions 7, 34, 35, 39–42). A peer that writes a tracker item, a wiki page
// or a pull request makes other people treat its claim as true; the KB gates
// cannot see that, so this one does, at the tool call.
//
// Core names no tracker. The pool config's `outbound:` block maps a surface
// name to the tool-name regexes (`tools`, case-insensitive, unanchored, so a
// server prefix never matters) and the shell-command regexes (`commands`,
// matched against each simple command of the call's own arguments, so a
// heredoc body or a `cat` of a script never matches; anchor with `^` to skip
// mentions in another command's arguments) that reach it. File-edit tools are
// never outbound. The surface name is the `mutates` tag: a peer without the
// tag is denied, a peer with it is asked (ticket 41 fills the ask).
//
// This is the one exception to the gate's fail-open rule: a config that
// cannot load or a matcher that cannot compile denies every outbound
// candidate with "outbound gate misconfigured". Outside any pool it is inert.

// OutboundMatchers is the compiled `outbound:` block. The zero value matches
// nothing.
type OutboundMatchers struct {
	surfaces []outboundMatcher // sorted by name
}

type outboundMatcher struct {
	name     string
	tools    []*regexp.Regexp
	commands []*regexp.Regexp
}

// OutboundMatch is one call matched to one surface: what the deny names and
// what the ask (ticket 41) renders as `Outbound: <surface> via <tool>`.
type OutboundMatch struct {
	Surface string
	Tool    string
	Command string // the simple command that matched, shell calls only
}

// CompileOutbound compiles a config's `outbound:` block. Any regex that fails
// to compile is an error naming the surface and the pattern: the caller must
// treat the whole gate as misconfigured, never as "no match".
func CompileOutbound(surfaces map[string]OutboundSurface) (OutboundMatchers, error) {
	names := make([]string, 0, len(surfaces))
	for n := range surfaces {
		names = append(names, n)
	}
	sort.Strings(names)
	var m OutboundMatchers
	for _, n := range names {
		s := surfaces[n]
		om := outboundMatcher{name: n}
		for _, pat := range s.Tools {
			re, err := regexp.Compile("(?i)" + pat)
			if err != nil {
				return OutboundMatchers{}, fmt.Errorf("outbound.%s.tools %q: %v", n, pat, err)
			}
			om.tools = append(om.tools, re)
		}
		for _, pat := range s.Commands {
			re, err := regexp.Compile(pat)
			if err != nil {
				return OutboundMatchers{}, fmt.Errorf("outbound.%s.commands %q: %v", n, pat, err)
			}
			om.commands = append(om.commands, re)
		}
		m.surfaces = append(m.surfaces, om)
	}
	return m, nil
}

// LoadOutbound reads and compiles the pool's outbound config. A config file
// that cannot be read or parsed (PoolConfig.ConfigErr) is an error, as is a
// regex that cannot compile.
func LoadOutbound(poolRoot string) (OutboundMatchers, error) {
	cfg := LoadPoolConfig(poolRoot)
	if cfg.ConfigErr != nil {
		return OutboundMatchers{}, cfg.ConfigErr
	}
	return CompileOutbound(cfg.Settings.Outbound)
}

// Empty reports whether no surface is declared.
func (m OutboundMatchers) Empty() bool { return len(m.surfaces) == 0 }

// OutboundCandidate reports whether a tool call could reach an outbound
// surface at all: anything but a file-edit or patch tool. It is the set a
// misconfigured gate denies.
func OutboundCandidate(toolName string) bool {
	return !fileEditTools[toolName] && !patchTools[toolName]
}

// Match returns every surface the call reaches, in surface-name order. err is
// set when a shell call's input cannot be read while shell patterns are
// declared: an unevaluable matcher, which the gate denies.
func (m OutboundMatchers) Match(p HookPayload) ([]OutboundMatch, error) {
	if m.Empty() || !OutboundCandidate(p.ToolName) {
		return nil, nil
	}
	var cmds [][]string
	if shellTools[p.ToolName] && m.hasCommands() {
		var in struct {
			Command string `json:"command"`
		}
		if len(p.ToolInput) == 0 || json.Unmarshal(p.ToolInput, &in) != nil {
			return nil, fmt.Errorf("%s input has no readable command", p.ToolName)
		}
		cmds = ShellSimpleCommands(in.Command)
	}
	var out []OutboundMatch
	for _, s := range m.surfaces {
		if mt, ok := s.match(p.ToolName, cmds); ok {
			out = append(out, mt)
		}
	}
	return out, nil
}

func (m OutboundMatchers) hasCommands() bool {
	for _, s := range m.surfaces {
		if len(s.commands) > 0 {
			return true
		}
	}
	return false
}

func (s outboundMatcher) match(tool string, cmds [][]string) (OutboundMatch, bool) {
	for _, re := range s.tools {
		if re.MatchString(tool) {
			return OutboundMatch{Surface: s.name, Tool: tool}, true
		}
	}
	for _, words := range cmds {
		line := strings.Join(words, " ")
		for _, re := range s.commands {
			if re.MatchString(line) {
				return OutboundMatch{Surface: s.name, Tool: tool, Command: line}, true
			}
		}
	}
	return OutboundMatch{}, false
}

// PeersMutating names the pool's peers whose `mutates` carries surface, sorted.
func PeersMutating(agentsDir, surface string) []string {
	agents, _, _ := ScanAgents(agentsDir)
	var out []string
	for _, a := range agents {
		if hasTag(a.Mutates, surface) {
			out = append(out, a.Name)
		}
	}
	return out
}

func hasTag(tags []string, t string) bool {
	for _, x := range tags {
		if x == t {
			return true
		}
	}
	return false
}

// harnessHasAskForm reports whether a harness can put a gate ask to the human.
// Only Claude Code's PreToolUse envelope can; opencode turns an ask into a deny.
func harnessHasAskForm(name string) bool {
	return name == "" || name == HarnessClaude
}

// outboundAgent resolves the peer making the call: the --agent flag, else the
// workspace the cwd sits in (at any depth, or a KB worktree), nil when the
// call is in the pool but in no peer's workspace.
func outboundAgent(agentsDir string, p HookPayload) *Agent {
	if p.PeerAgent != "" {
		return payloadAgent(agentsDir, p)
	}
	if a := hookAgent(agentsDir, p.Cwd); a != nil {
		return a
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if name := AgentFromWorktreePath(agentsDir, cwd); name != "" {
		if a, err := FindAgent(agentsDir, name); err == nil {
			return a
		}
	}
	if !underDir(cwd, agentsDir) {
		return nil
	}
	rel, err := filepath.Rel(filepath.Clean(agentsDir), filepath.Clean(cwd))
	if err != nil || rel == "." {
		return nil
	}
	first := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
	if a, err := FindAgent(agentsDir, first); err == nil {
		return a
	}
	return nil
}

func outboundDeny(reason string) GateDecision {
	return GateDecision{Deny: true, Check: GateCheckOutbound, Reason: reason}
}

// gateOutbound is gate step 6 (gate.go): after the KB rules and the pool
// ask, never in step-aside mode.
func gateOutbound(agentsDir string, p HookPayload) GateDecision {
	a, mt, d := resolveOutbound(agentsDir, p)
	if a == nil {
		return d
	}
	return gateOutboundAsk(agentsDir, a, p, mt)
}

// resolveOutbound runs the deny side of the outbound gate. It returns the
// calling peer and the surface it may be asked for, or a nil agent and the
// decision to return (allow when the call is not outbound, else a deny).
func resolveOutbound(agentsDir string, p HookPayload) (*Agent, OutboundMatch, GateDecision) {
	if agentsDir == "" || !OutboundCandidate(p.ToolName) {
		return nil, OutboundMatch{}, GateDecision{}
	}
	m, err := LoadOutbound(agentsDir)
	if err != nil {
		if meshctlDiagnosticOnly(p) {
			return nil, OutboundMatch{}, GateDecision{}
		}
		return nil, OutboundMatch{}, outboundDeny("mesh: outbound gate misconfigured (" + err.Error() + ") — every call that could reach an outbound " +
			"surface is refused until it loads. Fix " + PoolConfigPath(agentsDir) + " (with a file-edit tool) and run `meshctl doctor`.")
	}
	matches, err := m.Match(p)
	if err != nil {
		if meshctlDiagnosticOnly(p) {
			return nil, OutboundMatch{}, GateDecision{}
		}
		return nil, OutboundMatch{}, outboundDeny("mesh: outbound gate misconfigured — cannot evaluate this call (" + err.Error() + "); refusing rather than " +
			"read it as not outbound.")
	}
	if len(matches) == 0 {
		return nil, OutboundMatch{}, GateDecision{}
	}
	a := outboundAgent(agentsDir, p)
	if a == nil {
		return nil, OutboundMatch{}, outboundDeny("mesh: `" + p.ToolName + "` writes the outbound surface `" + matches[0].Surface + "`, but this call " +
			"is in the pool and in no peer's workspace — run from a peer workspace (or with `--agent <you>`), so the gate knows " +
			"who writes and can show the human its summary.")
	}
	var mt *OutboundMatch
	for i := range matches {
		if hasTag(a.Mutates, matches[i].Surface) {
			mt = &matches[i]
			break
		}
	}
	if mt == nil {
		s := matches[0].Surface
		who := "no peer declares it yet"
		if peers := PeersMutating(agentsDir, s); len(peers) > 0 {
			who = "peers that declare it: " + strings.Join(peers, ", ")
		}
		return nil, OutboundMatch{}, outboundDeny("mesh: `" + p.ToolName + "` writes the outbound surface `" + s + "`, and `" + a.Name +
			"` does not declare `mutates: [" + s + "]` (" + who + "). Hand the write to one of them (`meshctl send`), " +
			"with your summary of what and why.")
	}
	if cause := noHumanCause(p); cause != "" {
		return nil, OutboundMatch{}, outboundDeny(outboundNoHumanReason(a, p, *mt, cause))
	}
	harness := p.Harness
	if harness == "" {
		harness = a.HarnessName()
	}
	if !harnessHasAskForm(harness) {
		return nil, OutboundMatch{}, outboundDeny("mesh: `" + p.ToolName + "` writes the outbound surface `" + mt.Surface + "`, which is gate-asked, " +
			"and " + harness + " has no ask form — no confirmation path; run from a gated session (Claude Code in `" + a.Name + "`'s workspace).")
	}
	return a, *mt, GateDecision{}
}

// outboundSessionSummary is the summary path the peer must write for this
// call's session.
func outboundSessionSummary(a *Agent, p HookPayload) string {
	session := p.SessionID
	if session == "" {
		session = ResolveAgentSessionID(a, os.Getpid(), time.Now())
	}
	return SummaryPath(a.Dir(), session)
}

func outboundNoHumanReason(a *Agent, p HookPayload, mt OutboundMatch, cause string) string {
	head := "mesh: `" + p.ToolName + "` writes the outbound surface `" + mt.Surface + "` with no human at the prompt (" + cause + "). "
	if p.InSession && !p.Work {
		return head + "A consulted peer never writes outbound surfaces: respond `needs-human` naming the write, and stop."
	}
	return head + "Delegated and headless peers never write outbound surfaces (ADR-0068): stop, write your summary at " +
		outboundSessionSummary(a, p) + ", and return `needs-confirm` carrying the confirmation round and that summary path, " +
		"so the human makes the write from `" + a.Name + "`'s own session."
}

// outboundFindings are doctor's outbound checks (spec decisions 41, 42): a
// config or matcher the gate cannot use (error; the unparseable file itself is
// reported by poolConfigFindings), a `mutates` tag with no surface, a surface
// no peer mutates, and a surface with `tools` but no `commands` (reachable
// ungated from the shell).
func outboundFindings(agentsDir string, cfg PoolConfig, agents []*Agent) []DoctorFinding {
	if cfg.ConfigErr != nil {
		return nil
	}
	var out []DoctorFinding
	if _, err := CompileOutbound(cfg.Settings.Outbound); err != nil {
		out = append(out, DoctorFinding{Agent: "pool", Level: "error", Message: "outbound gate misconfigured: " + err.Error() +
			" — fix " + poolRel(agentsDir, PoolConfigFile) + "; the gate denies every outbound candidate until it compiles"})
	}
	surfaces := cfg.Settings.Outbound
	for _, a := range agents {
		for _, tag := range a.Mutates {
			if _, ok := surfaces[tag]; !ok {
				out = append(out, DoctorFinding{Agent: a.Name, Level: "warn", Message: "mutates `" + tag + "` but no outbound surface `" +
					tag + "` is declared in " + poolRel(agentsDir, PoolConfigFile) + " — writes to it are not gate-asked; add it under `outbound:`"})
			}
		}
	}
	names := make([]string, 0, len(surfaces))
	for n := range surfaces {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		mutated := false
		for _, a := range agents {
			if hasTag(a.Mutates, n) {
				mutated = true
				break
			}
		}
		if !mutated {
			out = append(out, DoctorFinding{Agent: "pool", Level: "warn", Message: "outbound surface `" + n + "` is mutated by no peer — " +
				"every matched call is denied; tag the peers that write it (`mutates: [" + n + "]`) or drop the surface"})
		}
		if s := surfaces[n]; len(s.Tools) > 0 && len(s.Commands) == 0 {
			out = append(out, DoctorFinding{Agent: "pool", Level: "warn", Message: "outbound surface `" + n + "` lists `tools` but no `commands` — " +
				"it is reachable ungated from the shell (e.g. a REST call); add a `commands` pattern for its host"})
		}
	}
	return out
}

// OutboundWiringLines describe the declared outbound surfaces for doctor's
// wiring check: one line per surface with its patterns and its mutators, or a
// single line when none is declared or the config is unusable.
func OutboundWiringLines(agentsDir string) []string {
	cfg := LoadPoolConfig(agentsDir)
	if cfg.ConfigErr != nil {
		return []string{"outbound: misconfigured (" + cfg.ConfigErr.Error() + ") — gate denies every outbound candidate"}
	}
	if _, err := CompileOutbound(cfg.Settings.Outbound); err != nil {
		return []string{"outbound: misconfigured (" + err.Error() + ") — gate denies every outbound candidate"}
	}
	if len(cfg.Settings.Outbound) == 0 {
		return []string{"outbound: no surfaces declared — the outbound gate is inert"}
	}
	names := make([]string, 0, len(cfg.Settings.Outbound))
	for n := range cfg.Settings.Outbound {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		s := cfg.Settings.Outbound[n]
		who := strings.Join(PeersMutating(agentsDir, n), ", ")
		if who == "" {
			who = "none"
		}
		out = append(out, "outbound: "+n+" — tools ["+strings.Join(s.Tools, ", ")+"], commands ["+
			strings.Join(s.Commands, ", ")+"], mutated by "+who)
	}
	return out
}

// meshctlDiagnosticOnly reports whether a shell call runs nothing but
// read-only meshctl diagnostics (`meshctl doctor` without --fix, `meshctl
// pool upgrade --check`, `meshctl version`), optionally after `cd`, with no
// redirection. The misconfigured outbound deny tells the agent to run doctor,
// so fail-closed must let these through. The reading is cobra's
// (readMeshctlCall), exact, never the matcher's superset: this is an allow.
func meshctlDiagnosticOnly(p HookPayload) bool {
	cmd := shellCommandInput(p)
	if cmd == "" || shellRedirects(ShellOwnArguments(cmd)) {
		return false
	}
	cmds := ShellSimpleCommands(cmd)
	if len(cmds) == 0 {
		return false
	}
	for _, words := range cmds {
		if path.Base(words[0]) == "cd" {
			continue
		}
		if strings.HasSuffix(words[0], "/") || path.Base(words[0]) != "meshctl" {
			return false
		}
		c := readMeshctlCall(words)
		switch strings.Join(c.args, " ") {
		case "doctor":
			if c.boolFlag("fix") {
				return false
			}
		case "pool upgrade":
			if !c.boolFlag("check") {
				return false
			}
		case "version":
		default:
			return false
		}
	}
	return true
}
