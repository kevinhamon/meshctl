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
// Claude Code only — opencode has no PreToolUse envelope.
func gateWiringFindings(a *Agent) []DoctorFinding {
	if a.HarnessName() == HarnessOpencode {
		return nil
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

	// No background primitive leaves the boundary nudge as the only trigger,
	// and the nudge is allowlist-gated (default: pm only).
	if !auto && !distillNudgeAllowlist()[a.Name] {
		out = append(out, DoctorFinding{Agent: a.Name, Level: "warn",
			Message: "distillation has no trigger: this harness has no background primitive and the agent " +
				"is not in AGENT_MESH_DISTILL_NUDGE_AGENTS — the KB fills only if the model volunteers"})
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
func opencodeInstructionsFindings(a *Agent) []DoctorFinding {
	if a.HarnessName() != HarnessOpencode {
		return nil
	}
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
	for _, i := range cfg.Instructions {
		if strings.Contains(i, "CLAUDE.md") {
			return nil
		}
	}
	return []DoctorFinding{{Agent: a.Name, Level: "warn",
		Message: "opencode.json does not list CLAUDE.md under `instructions` — opencode discovers AGENTS.md " +
			"only, so the agent's role and comms doctrine never load; run `meshctl agent onboard " + a.Name + "`"}}
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
