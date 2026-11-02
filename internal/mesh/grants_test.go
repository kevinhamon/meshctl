package mesh

import (
	"os"
	"strings"
	"testing"
	"time"
)

func writeGrantsConfig(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(PoolConfigPath(root), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const grantsCfg = `outbound:
  tracker:
    tools: ['create.*']
grants:
  pool:
    pool-upgrade: {}
  peers:
    pm:
      kb-promote: { max_per_day: 2 }
      "outbound:tracker": {}
`

func TestGrants_NoBlockGrantsNothing(t *testing.T) {
	root := migratedRoot(t)
	d := GrantCheck(root, "pm", GrantKBPromote, time.Now())
	if d.Allowed || !strings.Contains(d.Reason, "no `kb-promote` grant") {
		t.Fatalf("got %+v", d)
	}
}

func TestGrants_GrantCapAndPause(t *testing.T) {
	root := migratedRoot(t)
	writeGrantsConfig(t, root, grantsCfg)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if d := GrantCheck(root, "pm", GrantKBPromote, now); !d.Allowed {
			t.Fatalf("action %d: %+v", i, d)
		}
		if err := RecordPolicyAction(root, PolicyAction{Time: now.Format(time.RFC3339), Peer: "pm", Grant: GrantKBPromote}); err != nil {
			t.Fatal(err)
		}
	}
	if d := GrantCheck(root, "pm", GrantKBPromote, now); d.Allowed || !strings.Contains(d.Reason, "grant cap reached") {
		t.Fatalf("cap: %+v", d)
	}
	if d := GrantCheck(root, "pm", GrantKBPromote, now.Add(24*time.Hour)); !d.Allowed {
		t.Fatalf("next day must reset: %+v", d)
	}
	if d := GrantCheck(root, PoolGrantPeer, GrantPoolUpgrade, now); !d.Allowed {
		t.Fatalf("pool grant: %+v", d)
	}
	if got := PolicyActionsSince(root, "pm", now.Add(-time.Minute)); len(got) != 2 {
		t.Fatalf("since: %d", len(got))
	}
	writeGrantsConfig(t, root, strings.Replace(grantsCfg, "grants:\n", "grants:\n  paused: true\n", 1))
	if d := GrantCheck(root, "pm", OutboundGrant("tracker"), now); d.Allowed || !strings.Contains(d.Reason, "paused") {
		t.Fatalf("paused: %+v", d)
	}
}

func TestGrants_UnparseableConfigGrantsNothing(t *testing.T) {
	root := migratedRoot(t)
	writeGrantsConfig(t, root, "grants: [unclosed\n")
	if d := GrantCheck(root, PoolGrantPeer, GrantPoolUpgrade, time.Now()); d.Allowed {
		t.Fatalf("got %+v", d)
	}
}

func TestGrantProblems(t *testing.T) {
	s := GrantSettings{
		Pool: map[string]Grant{"kb-promote": {}},
		Peers: map[string]map[string]Grant{
			"pm":    {"outbound:nowhere": {}, "fly": {}, "kb-confirm": {MaxPerDay: -1}},
			"ghost": {"state-set": {}},
		},
	}
	got := strings.Join(grantProblems(s, map[string]OutboundSurface{"tracker": {}}, map[string]bool{"pm": true}), "\n")
	for _, want := range []string{`unknown grant "kb-promote"`, `"outbound:nowhere" names no surface`, `unknown grant "fly"`, "max_per_day must be ≥ 0", `"ghost" is not a peer`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if p := grantProblems(GrantSettings{}, nil, nil); len(p) != 0 {
		t.Fatalf("empty block: %v", p)
	}
}

func TestGrants_MissingOutboundGrants(t *testing.T) {
	s := GrantSettings{Peers: map[string]map[string]Grant{"pm": {"outbound:tracker": {}}}}
	if got := s.MissingOutboundGrants("pm", []string{"tracker", "chat"}); len(got) != 1 || got[0] != "chat" {
		t.Fatalf("got %v", got)
	}
}
