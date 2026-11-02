package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// grants_audit.go — what a peer's interactive session start shows about
// grants:
//
//   - the audit: every action a grant let through for this peer since its last
//     interactive session start, plus the pool-wide ones (pool-upgrade) in the
//     same window, so each peer's human sees a pool-wide action once;
//   - a one-line notice when grants are paused.
//
// "Since its last interactive session" is a per-peer marker under the pool's
// machine-local .memory/ directory. Session records cannot serve: they are
// removed at session end, and headless runs register them too. Only an
// interactive start (no dispatch depth, no distil run, not in-session) reads
// and advances the marker; a headless run neither sees the list nor resets
// it. With no policy log there is nothing to list and no section.

// policySeenDir holds one marker file per peer: the RFC3339 time of its last
// interactive session start.
const policySeenDir = "policy-seen"

// policyAuditMax caps the rows rendered; the rest are counted and the log
// path named.
const policyAuditMax = 20

func policySeenPath(poolRoot, peer string) string {
	return filepath.Join(PoolDir(poolRoot), MemoryDir, policySeenDir, peer)
}

// policySeen reads peer's marker; zero when absent or unreadable (everything
// in the log is then new to this peer).
func policySeen(poolRoot, peer string) time.Time {
	b, err := os.ReadFile(policySeenPath(poolRoot, peer))
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil {
		return time.Time{}
	}
	return t
}

// markPolicySeen advances peer's marker to now (second precision, matching
// the log).
func markPolicySeen(poolRoot, peer string, now time.Time) error {
	p := policySeenPath(poolRoot, peer)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// policyActionsForAudit returns peer's and the pool-wide actions logged at or
// after since (all when since is zero), oldest first. "At or after": the log
// has second precision, so an action in the same second as the last start may
// be listed twice, never missed.
func policyActionsForAudit(poolRoot, peer string, since time.Time) []PolicyAction {
	var out []PolicyAction
	for _, a := range ReadPolicyActions(poolRoot) {
		if a.Peer != peer && a.Peer != PoolGrantPeer {
			continue
		}
		t, err := time.Parse(time.RFC3339, a.Time)
		if err != nil || (!since.IsZero() && t.Before(since)) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// PolicyAuditMarkdown renders the audit section, "" when there is nothing to
// list. It does not move the marker.
func PolicyAuditMarkdown(poolRoot, peer string, since time.Time) string {
	acts := policyActionsForAudit(poolRoot, peer, since)
	if len(acts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Grant audit — actions a grant let through with no human at the prompt")
	if !since.IsZero() {
		fmt.Fprintf(&b, " since your last session (%s)", since.UTC().Format(time.RFC3339))
	}
	b.WriteString("\n\nTell the human about these. A grant is removed in the pool config's `grants:` block.\n\n")
	shown := acts
	if len(shown) > policyAuditMax {
		shown = shown[len(shown)-policyAuditMax:]
	}
	for _, a := range shown {
		grant := "`" + a.Grant + "`"
		if a.Peer == PoolGrantPeer {
			grant += " (pool-wide)"
		}
		line := "- " + a.Time + " · " + grant
		if a.Detail != "" {
			line += " — " + a.Detail
		}
		b.WriteString(line + "\n")
	}
	if n := len(acts) - len(shown); n > 0 {
		fmt.Fprintf(&b, "- … and %d earlier; the full log is `%s`\n", n, PolicyLogPath(poolRoot))
	}
	return strings.TrimRight(b.String(), "\n")
}

// GrantsPausedNotice is the one line session start shows when grants are
// paused, "" otherwise.
func GrantsPausedNotice(cfg PoolConfig) string {
	if !cfg.Settings.Grants.Paused {
		return ""
	}
	return "⚠ Grants are paused (`grants.paused` in " + PoolConfigFile + "): no grant lets an action through; headless runs wait for a human."
}

// sessionStartGrants is the session-start grants block: the paused notice
// for every start, and, for an interactive start only, the audit since the
// peer's marker, then the marker moved to now.
func sessionStartGrants(poolRoot string, cfg PoolConfig, a *Agent, interactive bool, now time.Time) string {
	var parts []string
	if n := GrantsPausedNotice(cfg); n != "" {
		parts = append(parts, n)
	}
	if interactive {
		if md := PolicyAuditMarkdown(poolRoot, a.Name, policySeen(poolRoot, a.Name)); md != "" {
			parts = append(parts, md)
		}
		// Only pools that have logged a grant action keep a marker.
		if fileExists(PolicyLogPath(poolRoot)) {
			_ = markPolicySeen(poolRoot, a.Name, now)
		}
	}
	return strings.Join(parts, "\n\n")
}
