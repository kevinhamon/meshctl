package mesh

import (
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

// Tool reference (ADR-0042) is the capability-awareness layer — distinct from
// skills (procedures) and the KB (knowledge). It declares which CLI tools an agent
// should use and the house rules ("prefer tea over gh"), as POINTERS: name +
// one-line purpose + how to self-serve help. meshctl never fetches/embeds the help
// text; the agent runs `<cmd> --help` itself. Declared mesh-wide on the pool and/or
// per-agent; injected at session start; validated by doctor (on $PATH?).
type ToolRef struct {
	Cmd   string `yaml:"cmd" json:"cmd"`
	Use   string `yaml:"use,omitempty" json:"use,omitempty"`     // one-line purpose / house rule
	Avoid string `yaml:"avoid,omitempty" json:"avoid,omitempty"` // set to say "don't use this here (why)"
	Help  string `yaml:"help,omitempty" json:"help,omitempty"`   // self-serve help cmd (default: "<cmd> --help")
}

// PoolConfig is the parsed .agentmesh/pool.yaml (identity + mesh-wide policy).
type PoolConfig struct {
	Name            string    `yaml:"name"`
	Created         string    `yaml:"created"`
	DoctrineVersion string    `yaml:"doctrine_version"`
	Tools           []ToolRef `yaml:"tools"`
}

// LoadPoolConfig reads .agentmesh/pool.yaml. Missing/invalid ⇒ empty config.
func LoadPoolConfig(poolRoot string) PoolConfig {
	var c PoolConfig
	if b, err := os.ReadFile(PoolMarkerPath(poolRoot)); err == nil {
		_ = yaml.Unmarshal(b, &c)
	}
	return c
}

// EffectiveTools merges mesh-wide (pool) tool refs with an agent's own — agent
// entries override pool entries with the same cmd (agent wins). Order: pool refs
// first, then agent-only refs.
func EffectiveTools(pool PoolConfig, agent *Agent) []ToolRef {
	byCmd := map[string]ToolRef{}
	var order []string
	add := func(t ToolRef) {
		if t.Cmd == "" {
			return
		}
		if _, seen := byCmd[t.Cmd]; !seen {
			order = append(order, t.Cmd)
		}
		byCmd[t.Cmd] = t // later (agent) wins
	}
	for _, t := range pool.Tools {
		add(t)
	}
	if agent != nil {
		for _, t := range agent.Tools {
			add(t)
		}
	}
	out := make([]ToolRef, 0, len(order))
	for _, c := range order {
		out = append(out, byCmd[c])
	}
	return out
}

// HelpCmd returns the self-serve help invocation for a tool (default "<cmd> --help").
func (t ToolRef) HelpCmd() string {
	if strings.TrimSpace(t.Help) != "" {
		return t.Help
	}
	return t.Cmd + " --help"
}

// ToolsMarkdown renders the injected session-start block (empty if no tools).
func ToolsMarkdown(tools []ToolRef) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Tools for this mesh — self-serve `--help` for detail; don't guess flags\n")
	for _, t := range tools {
		if t.Avoid != "" {
			b.WriteString("- avoid `" + t.Cmd + "` — " + t.Avoid + "\n")
			continue
		}
		line := "- `" + t.Cmd + "`"
		if t.Use != "" {
			line += " — " + t.Use
		}
		line += " (`" + t.HelpCmd() + "`)\n"
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ToolOnPath reports whether cmd is resolvable on $PATH.
func ToolOnPath(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}
