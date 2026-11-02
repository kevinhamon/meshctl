package mesh

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// grants.go — per-action grants. A grant lifts exactly one
// "no human at the prompt" deny for one peer (or, for pool-upgrade, the pool).
// Grants live in the pool config's `grants:` block, never in agent.yaml: a
// pool-config write is already human-asked, meshctl-committed and denied with
// no human at the prompt, so a peer cannot grant itself anything.
//
// This file is the shared core every enforcement point uses:
//   - GrantSettings / Grant: the config schema;
//   - grantProblems: validation for doctor;
//   - GrantCheck: may this action go through with no human (grant present,
//     not paused, under its per-day cap)?
//   - RecordPolicyAction / PolicyActionsSince: the policy log, which is both
//     the per-day counter (C7) and the session-start audit (C6c).
//
// With no `grants:` block nothing changes: every check reports "no grant".

// Grant names. An outbound grant is GrantOutboundPrefix+surface.
const (
	GrantKBPromote      = "kb-promote"
	GrantStateSet       = "state-set"
	GrantKBConfirm      = "kb-confirm"
	GrantTriageApply    = "triage-apply"
	GrantPoolUpgrade    = "pool-upgrade"
	GrantOutboundPrefix = "outbound:"
)

// PolicyLogFile is the policy log under the pool's machine-local .memory/
// directory (already ignored by the pool repo): one JSON line per action a
// grant let through.
const PolicyLogFile = "policy.log"

// Grant is one grant's options. `{}` is an uncapped grant.
type Grant struct {
	MaxPerDay int `yaml:"max_per_day,omitempty" json:"max_per_day,omitempty"`
}

// GrantSettings is the pool config's `grants:` block.
type GrantSettings struct {
	Paused bool                        `yaml:"paused"`
	Pool   map[string]Grant            `yaml:"pool"`
	Peers  map[string]map[string]Grant `yaml:"peers"`
}

// OutboundGrant is the grant name for writing an outbound surface.
func OutboundGrant(surface string) string { return GrantOutboundPrefix + surface }

// peerGrantNames are the per-peer grants other than outbound:<surface>.
var peerGrantNames = map[string]bool{GrantKBPromote: true, GrantStateSet: true, GrantKBConfirm: true, GrantTriageApply: true}

// poolGrantNames are the pool-wide grants.
var poolGrantNames = map[string]bool{GrantPoolUpgrade: true}

// PeerGrant returns peer's grant named name, if set (ignores pause and cap).
func (s GrantSettings) PeerGrant(peer, name string) (Grant, bool) {
	g, ok := s.Peers[peer][name]
	return g, ok
}

// PoolGrant returns the pool-wide grant named name, if set.
func (s GrantSettings) PoolGrant(name string) (Grant, bool) {
	g, ok := s.Pool[name]
	return g, ok
}

// MissingOutboundGrants lists, in order, the surfaces of mutates that peer has
// no outbound grant for (ignores pause and caps).
func (s GrantSettings) MissingOutboundGrants(peer string, mutates []string) []string {
	var out []string
	for _, m := range mutates {
		if _, ok := s.PeerGrant(peer, OutboundGrant(m)); !ok {
			out = append(out, m)
		}
	}
	return out
}

// grantProblems validates the block against the pool's outbound surfaces
// and peers: unknown grant names, an outbound grant for an undeclared surface,
// a negative cap, a grant for an unknown peer. Each string is one problem.
func grantProblems(s GrantSettings, surfaces map[string]OutboundSurface, peers map[string]bool) []string {
	var out []string
	for name, g := range s.Pool {
		if !poolGrantNames[name] {
			out = append(out, fmt.Sprintf("grants.pool: unknown grant %q (pool-wide grants: %s)", name, GrantPoolUpgrade))
		}
		if g.MaxPerDay < 0 {
			out = append(out, fmt.Sprintf("grants.pool.%s: max_per_day must be ≥ 0", name))
		}
	}
	for _, peer := range grantKeys(s.Peers) {
		if peers != nil && !peers[peer] {
			out = append(out, fmt.Sprintf("grants.peers: %q is not a peer in this pool", peer))
		}
		for _, name := range grantKeys(s.Peers[peer]) {
			g := s.Peers[peer][name]
			switch {
			case peerGrantNames[name]:
			case strings.HasPrefix(name, GrantOutboundPrefix):
				surface := strings.TrimPrefix(name, GrantOutboundPrefix)
				if _, ok := surfaces[surface]; !ok {
					out = append(out, fmt.Sprintf("grants.peers.%s: %q names no surface in `outbound:`", peer, name))
				}
			default:
				out = append(out, fmt.Sprintf("grants.peers.%s: unknown grant %q (grants: kb-promote, state-set, kb-confirm, triage-apply, outbound:<surface>)", peer, name))
			}
			if g.MaxPerDay < 0 {
				out = append(out, fmt.Sprintf("grants.peers.%s.%s: max_per_day must be ≥ 0", peer, name))
			}
		}
	}
	return out
}

func grantKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// grantFindings are doctor's findings for the `grants:` block: invalid
// entries (error) and, per peer, every `mutates` surface with no outbound
// grant on a dispatchable peer (warn — it cannot be dispatched headless).
func grantFindings(poolRoot string, cfg PoolConfig, agents []*Agent) []DoctorFinding {
	if !IsPool(poolRoot) {
		return nil
	}
	peers := map[string]bool{}
	for _, a := range agents {
		peers[a.Name] = true
	}
	var out []DoctorFinding
	for _, p := range grantProblems(cfg.Settings.Grants, cfg.Settings.Outbound, peers) {
		out = append(out, DoctorFinding{Agent: "pool", Level: "error", Message: poolRel(poolRoot, PoolConfigFile) + ": " + p})
	}
	for _, a := range agents {
		if a.Dispatchable == nil || !*a.Dispatchable || len(a.Mutates) == 0 {
			continue
		}
		for _, m := range cfg.Settings.Grants.MissingOutboundGrants(a.Name, a.Mutates) {
			out = append(out, DoctorFinding{Agent: a.Name, Level: "warn", Message: fmt.Sprintf("dispatchable with mutates %q but no `%s` grant in %s — it is not dispatched headless until the grant is added", m, OutboundGrant(m), poolRel(poolRoot, PoolConfigFile))})
		}
	}
	return out
}

// GrantDecision is the answer to "may this action go through with no human?".
type GrantDecision struct {
	Allowed bool
	Grant   string // the grant name checked
	Reason  string // when !Allowed: why ("no `kb-promote` grant", "grants are paused", "grant cap reached …")
}

// PoolGrantPeer is the peer name recorded for pool-wide grants.
const PoolGrantPeer = "pool"

// GrantCheck decides whether peer's grant name lets an action through with no
// human at the prompt, now. peer == PoolGrantPeer checks a pool-wide grant.
// A config that cannot be parsed allows nothing. It does not record anything:
// call RecordPolicyAction once the action is actually taken.
func GrantCheck(poolRoot, peer, name string, now time.Time) GrantDecision {
	cfg := LoadPoolConfig(poolRoot)
	return grantCheck(poolRoot, cfg, peer, name, now)
}

func grantCheck(poolRoot string, cfg PoolConfig, peer, name string, now time.Time) GrantDecision {
	d := GrantDecision{Grant: name}
	if cfg.ConfigErr != nil {
		d.Reason = "the pool config cannot be parsed, so no grant applies"
		return d
	}
	s := cfg.Settings.Grants
	var g Grant
	var ok bool
	if peer == PoolGrantPeer {
		g, ok = s.PoolGrant(name)
	} else {
		g, ok = s.PeerGrant(peer, name)
	}
	if !ok {
		where := "grants.peers." + peer
		if peer == PoolGrantPeer {
			where = "grants.pool"
		}
		d.Reason = fmt.Sprintf("no `%s` grant (%s in %s)", name, where, PoolConfigFile)
		return d
	}
	if s.Paused {
		d.Reason = "grants are paused (grants.paused in " + PoolConfigFile + ")"
		return d
	}
	if g.MaxPerDay > 0 {
		if n := policyActionsOn(poolRoot, peer, name, now); n >= g.MaxPerDay {
			d.Reason = fmt.Sprintf("grant cap reached: `%s` let %d of %d actions through today", name, n, g.MaxPerDay)
			return d
		}
	}
	d.Allowed = true
	return d
}

// PolicyAction is one line of the policy log: an action a grant let through.
type PolicyAction struct {
	Time    string `json:"time"` // RFC3339 UTC
	Peer    string `json:"peer"` // PoolGrantPeer for pool-wide grants
	Grant   string `json:"grant"`
	Detail  string `json:"detail,omitempty"` // what was done: paths, surface, summary file
	Session string `json:"session,omitempty"`
}

// PolicyLogPath is <pool dir>/.memory/policy.log.
func PolicyLogPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), MemoryDir, PolicyLogFile)
}

// RecordPolicyAction appends a to the policy log (Time defaults to now).
func RecordPolicyAction(poolRoot string, a PolicyAction) error {
	if a.Time == "" {
		a.Time = time.Now().UTC().Format(time.RFC3339)
	}
	p := PolicyLogPath(poolRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// ReadPolicyActions returns every parseable line of the policy log, oldest
// first. A missing log is empty.
func ReadPolicyActions(poolRoot string) []PolicyAction {
	f, err := os.Open(PolicyLogPath(poolRoot))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []PolicyAction
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var a PolicyAction
		if json.Unmarshal(sc.Bytes(), &a) == nil && a.Grant != "" {
			out = append(out, a)
		}
	}
	return out
}

// PolicyActionsSince returns peer's policy actions strictly after since (all
// when since is zero), oldest first.
func PolicyActionsSince(poolRoot, peer string, since time.Time) []PolicyAction {
	var out []PolicyAction
	for _, a := range ReadPolicyActions(poolRoot) {
		if a.Peer != peer {
			continue
		}
		t, err := time.Parse(time.RFC3339, a.Time)
		if err != nil || (!since.IsZero() && !t.After(since)) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// policyActionsOn counts peer's actions under grant on now's UTC day.
func policyActionsOn(poolRoot, peer, grant string, now time.Time) int {
	day := now.UTC().Format("2006-01-02")
	n := 0
	for _, a := range ReadPolicyActions(poolRoot) {
		if a.Peer == peer && a.Grant == grant && strings.HasPrefix(a.Time, day) {
			n++
		}
	}
	return n
}

// GrantsPaused reports the pool's kill switch.
func GrantsPaused(poolRoot string) bool {
	return LoadPoolConfig(poolRoot).Settings.Grants.Paused
}
