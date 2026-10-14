package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Wiring checks for defects that are invisible at runtime.
//
// Each of these shipped silently: the mesh looked configured, agents ran, and
// the only symptom was work quietly not happening. A missing distiller binding
// is indistinguishable from a model that chose not to distil; a role file that
// never loads is indistinguishable from a model ignoring it. Those cost far
// more to diagnose than to detect, so detect them.

// gateWiringFindings verifies the fail-closed enforcement gate is installed and
// current. Without it the mesh is advisory only — the exact defect that keeps
// looking "fixed" (hooks wired, doctrine written) while the model routes around
// it. An unwired gate is an error, not a warning: it means doctrine is optional.
// opencode: see opencodeGateFindings.
func gateWiringFindings(a *Agent) []DoctorFinding {
	if a.HarnessName() == HarnessOpencode {
		return opencodeGateFindings(a)
	}
	path := filepath.Join(a.Dir(), ".claude", "settings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return []DoctorFinding{{Agent: a.Name, Level: "error",
			Message: "no .claude/settings.json — mesh enforcement gate not installed; run `meshctl agent onboard " + a.Name + "`"}}
	}
	var root map[string]any
	if json.Unmarshal(b, &root) != nil {
		return []DoctorFinding{{Agent: a.Name, Level: "error",
			Message: ".claude/settings.json is not valid JSON — enforcement gate cannot be verified"}}
	}
	var out []DoctorFinding
	hooks, _ := root["hooks"].(map[string]any)
	entries, _ := hooks["PreToolUse"].([]any)
	if !hookCommandPresent(entries, "meshctl hook pre-tool-use") {
		out = append(out, DoctorFinding{Agent: a.Name, Level: "error",
			Message: "PreToolUse enforcement gate not wired — doctrine is advisory only (a model can write the " +
				"claude-mem/Claude memory store or hand-write intake). Run `meshctl agent onboard " + a.Name + "`"})
	}
	if v := WiringVersion(a.Dir()); v < MeshWiringVersion {
		out = append(out, DoctorFinding{Agent: a.Name, Level: "warn",
			Message: fmt.Sprintf("mesh wiring stale (v%d < v%d) — run `meshctl agent onboard %s` to pick up gate/policy changes",
				v, MeshWiringVersion, a.Name)})
	}
	out = append(out, peerWiringFindings(a)...)
	return out
}

// peerWiringFindings verifies the in-session peer definitions match the
// roster. A stale set means `msg ask --in-session` refuses (missing peer) or
// delegates to an outdated contract; a missing pool-root grant means the peer
// cannot read its own KB without a prompt.
func peerWiringFindings(a *Agent) []DoctorFinding {
	poolRoot := filepath.Dir(a.Dir())
	var out []DoctorFinding
	if plan := PlanPeerAgents(poolRoot, a.Dir()); !plan.Empty() {
		n := len(plan.Write) + len(plan.Update) + len(plan.Remove)
		out = append(out, DoctorFinding{Agent: a.Name, Level: "warn",
			Message: fmt.Sprintf("in-session peer subagents out of date (%d to write/refresh/remove) — run `meshctl pool upgrade`", n)})
	}
	if IsPool(poolRoot) && !PoolAccessWired(poolRoot, a.Dir()) {
		out = append(out, DoctorFinding{Agent: a.Name, Level: "warn",
			Message: "pool root not in .claude/settings.local.json additionalDirectories — in-session peers cannot read their own KB without prompts; run `meshctl agent onboard " + a.Name + "`"})
	}
	return out
}

// cognitionTriggerFindings verifies that something will actually cause
// distillation to run. An agent keeping a knowledge/ tree with no trigger
// accumulates nothing, and looks exactly like a model that won't cooperate.
func cognitionTriggerFindings(a *Agent) []DoctorFinding {
	var out []DoctorFinding

	auto := harnessAutoDistills(a)

	// Harnesses that spawn a background run need their CLI present; without it
	// SpawnDistill fails the PATH lookup and returns silently.
	if argv := HarnessFor(a).DistillCmd(""); len(argv) > 0 && !ToolOnPath(argv[0]) {
		out = append(out, DoctorFinding{Agent: a.Name, Level: "warn",
			Message: "harness=" + a.HarnessName() + " distils by running `" + argv[0] +
				"`, which is not on $PATH — session-end distillation will no-op silently"})
		auto = false
	}

	// No background primitive leaves the boundary nudge as the only trigger;
	// it is on for every agent unless $AGENT_MESH_DISTILL_NUDGE_AGENTS narrows it.
	if !auto && !distillNudgeEnabled(a.Name) {
		out = append(out, DoctorFinding{Agent: a.Name, Level: "warn",
			Message: "distillation has no trigger: this harness has no background primitive and " +
				distillNudgeAgentsEnv + " excludes this agent — the KB fills only if the model volunteers"})
	}
	// The nudge tells the model to spawn the `distiller` subagent; on Claude
	// Code that is a definition file meshctl scaffolds. Without it the nudge
	// asks for something that does not exist.
	if !auto && distillNudgeEnabled(a.Name) && a.HarnessName() == HarnessClaude &&
		!fileExists(filepath.Join(a.Dir(), ".claude", "agents", "distiller.md")) {
		out = append(out, DoctorFinding{Agent: a.Name, Level: "warn",
			Message: "distillation nudge is on but .claude/agents/distiller.md is missing — " +
				"run `meshctl agent onboard " + a.Name + "` to scaffold the distiller subagent"})
	}
	return out
}

// sessionSignalFindings checks that session records will contain something to
// mine. CaptureSession has exactly two sources — the harness transcript and the
// git spine. The opencode plugin sends no transcript path, so in a non-git
// workspace every record is an empty shell and distillation can only ever
// report "nothing to distil", however capable the model is.
func sessionSignalFindings(a *Agent) []DoctorFinding {
	if a.HarnessName() != HarnessOpencode {
		return nil // claude supplies a transcript path, so records have content
	}
	if AgentRepoHygiene(a.Dir()).IsRepo {
		return nil
	}
	return []DoctorFinding{{Agent: a.Name, Level: "warn",
		Message: "session records will be empty: the opencode plugin supplies no transcript path and this " +
			"workspace is not a git repo, leaving distillation nothing to mine"}}
}

// opencodeInstructionsFindings verifies the agent's role file is actually
// loaded. opencode discovers AGENTS.md; a mesh agent's doctrine lives in
// CLAUDE.md, which is only read when opencode.json lists it under
// `instructions`. Without that the agent runs on global config alone and
// behaves as though it has no role and no peers — because it hasn't been told
// it has either.
//
// V2 accepts `instructions` but does not load it; there the mesh plugin injects
// CLAUDE.md, and its presence is checked by opencodeGateFindings. Only the JSON
// validity check applies.
func opencodeInstructionsFindings(a *Agent) []DoctorFinding {
	if a.HarnessName() != HarnessOpencode {
		return nil
	}
	v2 := opencodeExpectedMajor(a.Dir()) >= 2
	path := filepath.Join(a.Dir(), "opencode.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil // absence of opencode.json is already reported elsewhere
	}
	var cfg struct {
		Instructions []string `json:"instructions"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return []DoctorFinding{{Agent: a.Name, Level: "error",
			Message: "opencode.json is not valid JSON: " + err.Error()}}
	}
	if v2 {
		return nil
	}
	for _, i := range cfg.Instructions {
		if strings.Contains(i, "CLAUDE.md") {
			return nil
		}
	}
	return []DoctorFinding{{Agent: a.Name, Level: "warn",
		Message: "opencode.json does not list CLAUDE.md under `instructions` — opencode discovers AGENTS.md " +
			"only, so the agent's role and comms doctrine never load; run `meshctl agent onboard " + a.Name + "`"}}
}

// pluginState classifies the meshctl plugin file at one path.
type pluginState int

const (
	pluginAbsent  pluginState = iota
	pluginForeign             // a file meshctl did not write
	pluginStale               // meshctl's, but not the current body
	pluginCurrent
)

func opencodePluginState(dir, rel, want string) pluginState {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	switch {
	case err != nil:
		return pluginAbsent
	case !strings.Contains(string(b), opencodePluginMarker):
		return pluginForeign
	case string(b) != want:
		return pluginStale
	}
	return pluginCurrent
}

func managed(s pluginState) bool { return s == pluginStale || s == pluginCurrent }

// opencodeExpectedMajor is the opencode major an agent's wiring must match: the
// installed binary's, else (opencode not on this machine) whichever variant the
// workspace is wired for, defaulting to the current line.
func opencodeExpectedMajor(dir string) int {
	if m := InstalledOpencodeMajor(); m > 0 {
		return m
	}
	if managed(opencodePluginState(dir, opencodePluginV1Rel, opencodePluginJS)) &&
		!managed(opencodePluginState(dir, opencodePluginV2Rel, opencodePluginV2JS)) {
		return 1
	}
	return 2
}

// opencodeGateFindings verifies an opencode agent's mesh plugin matches the
// installed opencode major. A mismatch is silent at runtime — V2 skips a V1
// plugin with a log warning, so doctrine, context and the gate all just stop —
// which is why it is an error here. On V2 the plugin is also the enforcement
// gate (tool.execute.before) and the only thing that loads CLAUDE.md. V1 has no
// pre-tool hook, so a V1 agent is advisory-only; say so rather than imply it is
// enforced.
func opencodeGateFindings(a *Agent) []DoctorFinding {
	v1 := opencodePluginState(a.Dir(), opencodePluginV1Rel, opencodePluginJS)
	v2 := opencodePluginState(a.Dir(), opencodePluginV2Rel, opencodePluginV2JS)
	onboard := "run `meshctl agent onboard " + a.Name + "`"
	f := func(level, msg string) DoctorFinding { return DoctorFinding{Agent: a.Name, Level: level, Message: msg} }
	var out []DoctorFinding

	if opencodeExpectedMajor(a.Dir()) >= 2 {
		switch v2 {
		case pluginAbsent:
			if managed(v1) {
				return []DoctorFinding{f("error", "wired for opencode V1 but opencode 2.x is installed — V2 ignores V1 plugins, "+
					"so CLAUDE.md doctrine, mesh context and the enforcement gate are all off; "+onboard)}
			}
			return []DoctorFinding{f("error", "mesh plugin not installed ("+opencodePluginV2Rel+") — on opencode V2 it loads "+
				"CLAUDE.md and is the enforcement gate; "+onboard)}
		case pluginForeign:
			out = append(out, f("warn", opencodePluginV2Rel+" is not meshctl's plugin — mesh gate and doctrine loading cannot be verified"))
		case pluginStale:
			out = append(out, f("warn", "mesh plugin stale ("+opencodePluginV2Rel+") — "+onboard+" to pick up gate/context changes"))
		}
		if managed(v1) {
			out = append(out, f("warn", "leftover opencode V1 plugin ("+opencodePluginV1Rel+") — V2 warns on it every start; "+onboard))
		}
		return out
	}

	if managed(v2) {
		return []DoctorFinding{f("error", "wired for opencode V2 but opencode 1.x is installed — V1 cannot run the V2 plugin; "+
			onboard+" (or upgrade opencode)")}
	}
	switch v1 {
	case pluginAbsent:
		out = append(out, f("warn", "mesh plugin not installed ("+opencodePluginV1Rel+") — no handoff/intuition/session capture; "+onboard))
	case pluginStale:
		out = append(out, f("warn", "mesh plugin stale ("+opencodePluginV1Rel+") — "+onboard))
	}
	return append(out, f("warn", "opencode V1 has no pre-tool hook — the mesh enforcement gate is unavailable and doctrine "+
		"is advisory only; upgrade to opencode V2 and "+onboard))
}

// doctrineFlagFindings catches doctrine instructing agents to pass flags that
// do not exist. Doctrine is executed by models verbatim, so a wrong flag is not
// a typo — it is an instruction that fails every time it is followed, and the
// model reports the capability as unavailable rather than the flag as wrong.
func doctrineFlagFindings(poolRoot string) []DoctorFinding {
	dir := filepath.Join(poolRoot, DoctrineDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []DoctorFinding
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if m := reBadDoctrineFlag.FindString(string(b)); m != "" {
			out = append(out, DoctorFinding{Agent: "pool", Level: "warn",
				Message: "doctrine/" + e.Name() + " tells agents to pass `--project`, which no meshctl " +
					"command accepts; calls following it fail with \"unknown flag\""})
		}
	}
	return out
}

// reBadDoctrineFlag matches --project only where it is being *applied* to a
// value (`--project <agent>`, `--project $NAME`, `--project foo`). Doctrine
// that names the flag in order to warn against it -- "there is no `--project`
// flag" -- must not trip the check, or fixing the doctrine leaves the warning
// in place forever and the check trains you to ignore it.
var reBadDoctrineFlag = regexp.MustCompile(`--project\s+[<$"'\w]`)
