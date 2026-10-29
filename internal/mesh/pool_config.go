package mesh

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// pool_config.go — the two pool files and the one loader that reads them
// (ADR-0066, spec decision 10). `pool.yaml` is binary-managed identity: name,
// created, doctrine version. `config.yaml` is human-owned: knowledge kind,
// outbound surfaces (ADR-0068), reconcile order and the mesh-wide tool
// reference. Keeping them apart is what makes "an upgrade can never clobber
// what the human wrote" true by construction: meshctl rewrites the identity
// file and only ever appends to the config file (the one-time tools split).
//
// Transition: tool references still found in the identity file are honoured
// (merged after the config file's own) and doctor warns until `pool upgrade`
// moves them.

// OutboundSurface is one entry of the config's `outbound:` map (ADR-0068): the
// tool-name regexes and shell-command regexes that mutate the surface. The
// surface name must equal a `mutates` tag on the peers that write it.
type OutboundSurface struct {
	Tools    []string `yaml:"tools"`
	Commands []string `yaml:"commands"`
}

// ReconcileSettings is the config's `reconcile:` block (ADR-0070).
type ReconcileSettings struct {
	Order []string `yaml:"order"` // overrides the computed peer walk order for reconciliation; empty ⇒ computed
}

// PoolSettings is the parsed human-owned config file (PoolConfigPath).
type PoolSettings struct {
	Knowledge string                     `yaml:"knowledge"` // canonical | perspective; "" ⇒ canonical
	Outbound  map[string]OutboundSurface `yaml:"outbound"`
	Reconcile ReconcileSettings          `yaml:"reconcile"`
	Tools     []ToolRef                  `yaml:"tools"`
	Extras    []PoolExtra                `yaml:"extras"` // non-agent folders `pool clone` also clones (pool-clone C4)
}

// poolIdentity is the parsed identity file (PoolMarkerPath). `tools` is read
// only for the transition; new pools never write it here.
type poolIdentity struct {
	Name            string    `yaml:"name"`
	Created         string    `yaml:"created"`
	DoctrineVersion string    `yaml:"doctrine_version"`
	Tools           []ToolRef `yaml:"tools"`
}

// PoolConfig is what callers see of a pool: its identity, its human-owned
// settings and the effective tool reference. Tools is the merged list every
// caller should use (config file first, then any still in the identity file,
// deduplicated by cmd); IdentityTools is the transition leftover doctor warns
// about. ConfigErr is set when the config file exists but cannot be parsed, so
// a gate reading Settings can fail closed instead of seeing an empty map.
type PoolConfig struct {
	Name            string
	Created         string
	DoctrineVersion string
	Settings        PoolSettings
	Tools           []ToolRef
	IdentityTools   []ToolRef
	ConfigErr       error
}

// LoadPoolConfig reads the identity file and the config file. A missing file
// is an empty one; an unreadable identity file yields a zero identity, an
// unparseable config file is reported in ConfigErr with empty settings.
func LoadPoolConfig(poolRoot string) PoolConfig {
	var id poolIdentity
	if b, err := os.ReadFile(PoolMarkerPath(poolRoot)); err == nil {
		_ = yaml.Unmarshal(b, &id)
	}
	c := PoolConfig{Name: id.Name, Created: id.Created, DoctrineVersion: id.DoctrineVersion, IdentityTools: id.Tools}
	if b, err := os.ReadFile(PoolConfigPath(poolRoot)); err == nil {
		if uerr := yaml.Unmarshal(b, &c.Settings); uerr != nil {
			c.Settings = PoolSettings{}
			c.ConfigErr = fmt.Errorf("%s: %v", poolRel(poolRoot, PoolConfigFile), uerr)
		}
	} else if !os.IsNotExist(err) {
		// Present but unreadable: as unusable as unparseable (the outbound
		// gate fails closed on it).
		c.ConfigErr = fmt.Errorf("%s: %v", poolRel(poolRoot, PoolConfigFile), err)
	}
	c.Tools = mergeToolRefs(c.Settings.Tools, id.Tools)
	return c
}

// mergeToolRefs keeps first-seen order and lets the first list win on a cmd.
func mergeToolRefs(lists ...[]ToolRef) []ToolRef {
	seen := map[string]bool{}
	var out []ToolRef
	for _, l := range lists {
		for _, t := range l {
			if t.Cmd == "" || seen[t.Cmd] {
				continue
			}
			seen[t.Cmd] = true
			out = append(out, t)
		}
	}
	return out
}

// poolConfigFindings are doctor's pool-level findings about the two config
// files: a config file that cannot be parsed (error — the outbound gate fails
// closed on it), an invalid `knowledge` value (error, ADR-0069) and tool
// references still in the identity file (warn — moved by `pool upgrade`).
func poolConfigFindings(poolRoot string, cfg PoolConfig) []DoctorFinding {
	if !IsPool(poolRoot) {
		return nil
	}
	var out []DoctorFinding
	if cfg.ConfigErr != nil {
		out = append(out, DoctorFinding{Agent: "pool", Level: "error", Message: poolRel(poolRoot, PoolConfigFile) + " cannot be parsed (" + cfg.ConfigErr.Error() + ") — fix it; the outbound gate denies every write until it loads"})
	}
	if !ValidKnowledgeKind(cfg.Settings.Knowledge) {
		out = append(out, DoctorFinding{Agent: "pool", Level: "error", Message: poolRel(poolRoot, PoolConfigFile) + ": " + knowledgeKindProblem(cfg.Settings.Knowledge)})
	}
	if len(cfg.IdentityTools) > 0 {
		cmds := make([]string, 0, len(cfg.IdentityTools))
		for _, t := range cfg.IdentityTools {
			cmds = append(cmds, t.Cmd)
		}
		out = append(out, DoctorFinding{Agent: "pool", Level: "warn", Message: poolRel(poolRoot, PoolMarkerFile) + " still carries `tools` (" + strings.Join(cmds, ", ") + ") — they belong in " + poolRel(poolRoot, PoolConfigFile) + "; run `meshctl pool upgrade` to move them"})
	}
	return out
}

// splitToolsState classifies the one-time tools split for the plan and the
// apply: nothing to do, automatic (config has no tools of its own), or manual
// (both files declare tools — a human must merge, since config.yaml is never
// rewritten).
type splitToolsState int

const (
	splitNone splitToolsState = iota
	splitAuto
	splitManual
)

func readSplitToolsState(poolRoot string) (splitToolsState, []string) {
	cfg := LoadPoolConfig(poolRoot)
	if len(cfg.IdentityTools) == 0 {
		return splitNone, nil
	}
	cmds := make([]string, 0, len(cfg.IdentityTools))
	for _, t := range cfg.IdentityTools {
		cmds = append(cmds, t.Cmd)
	}
	if len(cfg.Settings.Tools) > 0 || cfg.ConfigErr != nil || configDeclaresTools(poolRoot) {
		return splitManual, cmds
	}
	return splitAuto, cmds
}

// configDeclaresTools reports a top-level `tools` key in the config file, even
// an empty one: appending a second would make the file unparseable.
func configDeclaresTools(poolRoot string) bool {
	b, err := os.ReadFile(PoolConfigPath(poolRoot))
	if err != nil {
		return false
	}
	var keys map[string]yaml.Node
	if yaml.Unmarshal(b, &keys) != nil {
		return false
	}
	_, ok := keys["tools"]
	return ok
}

// splitToolsFromIdentity moves the identity file's `tools` into the config
// file: appends them as a `tools:` block (the config file is otherwise left
// byte for byte as the human wrote it) and rewrites the identity file with
// identity only. Callers commit.
func splitToolsFromIdentity(poolRoot string) error {
	var id poolIdentity
	b, err := os.ReadFile(PoolMarkerPath(poolRoot))
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, &id); err != nil {
		return fmt.Errorf("%s: %v", poolRel(poolRoot, PoolMarkerFile), err)
	}
	block, err := yaml.Marshal(struct {
		Tools []ToolRef `yaml:"tools"`
	}{Tools: id.Tools})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(PoolConfigPath(poolRoot), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.WriteString("\n# moved from " + PoolMarkerFile + " by `meshctl pool upgrade` (ADR-0066)\n" + string(block))
	f.Close()
	if werr != nil {
		return werr
	}
	return os.WriteFile(PoolMarkerPath(poolRoot), []byte(poolMarkerBody(id.Name, id.Created, id.DoctrineVersion)), 0o644)
}
