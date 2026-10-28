package mesh

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// console_settings.go — how the mesh console looks for this pool (console
// layout spec C26–C29): saved in the pool repo so a pool cloned on another
// machine opens the same way. A file of its own beside config.yaml, which
// meshctl never rewrites (ADR-0066); the console writes and commits this one,
// and never pushes.

// ConsoleSettingsFile is <pool dir>/console.yaml.
const ConsoleSettingsFile = "console.yaml"

// ConsoleSettings are the console's per-pool settings.
type ConsoleSettings struct {
	Layout        string `yaml:"layout" json:"layout"`           // classic | workspace
	AgentOrder    string `yaml:"agent_order" json:"agent_order"` // activity | name
	AttentionOnly bool   `yaml:"attention_only" json:"attention_only"`
}

// DefaultConsoleSettings is what a pool without the file gets.
func DefaultConsoleSettings() ConsoleSettings {
	return ConsoleSettings{Layout: "classic", AgentOrder: "activity"}
}

// Valid reports the first unknown value, if any.
func (s ConsoleSettings) Valid() error {
	if s.Layout != "classic" && s.Layout != "workspace" {
		return fmt.Errorf("layout %q: want classic or workspace", s.Layout)
	}
	if s.AgentOrder != "activity" && s.AgentOrder != "name" {
		return fmt.Errorf("agent_order %q: want activity or name", s.AgentOrder)
	}
	return nil
}

// ConsoleSettingsPath is the file's path for the pool at poolRoot.
func ConsoleSettingsPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), ConsoleSettingsFile)
}

// ReadConsoleSettings reads the pool's console settings; a missing file, or
// an unknown value, falls back to the defaults field by field.
func ReadConsoleSettings(poolRoot string) ConsoleSettings {
	s := DefaultConsoleSettings()
	b, err := os.ReadFile(ConsoleSettingsPath(poolRoot))
	if err != nil {
		return s
	}
	var got ConsoleSettings
	if yaml.Unmarshal(b, &got) != nil {
		return s
	}
	if got.Layout == "classic" || got.Layout == "workspace" {
		s.Layout = got.Layout
	}
	if got.AgentOrder == "activity" || got.AgentOrder == "name" {
		s.AgentOrder = got.AgentOrder
	}
	s.AttentionOnly = got.AttentionOnly
	return s
}

const consoleSettingsHeader = "# meshctl console settings for this pool — written by the console's Settings page and\n" +
	"# committed here; push the pool repo to carry them to another machine.\n"

// WriteConsoleSettings saves s for the pool and commits the file in the pool
// repo (only that file; anything else uncommitted is left alone). committed
// is false when nothing changed or the pool directory is not a repo yet.
func WriteConsoleSettings(poolRoot string, s ConsoleSettings) (committed bool, err error) {
	if err := s.Valid(); err != nil {
		return false, err
	}
	b, err := yaml.Marshal(s)
	if err != nil {
		return false, err
	}
	p := ConsoleSettingsPath(poolRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(p, append([]byte(consoleSettingsHeader), b...), 0o644); err != nil {
		return false, err
	}
	pool := PoolDir(poolRoot)
	if !poolRepoPresent(pool) {
		return false, nil
	}
	ok, err := commitPoolPaths(pool, []string{ConsoleSettingsFile}, "console: settings", "Written by `meshctl console`.")
	if err != nil {
		return false, fmt.Errorf("settings saved but the commit in %s/ failed (%v) — set git user.name and user.email, then commit it by hand", PoolDirName(poolRoot), err)
	}
	return ok, nil
}
