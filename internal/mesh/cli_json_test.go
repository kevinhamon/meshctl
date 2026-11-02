package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// keysOf returns the sorted top-level keys of v marshalled to a JSON object.
// The shape tests pin field names: a rename or a removal breaks a reader, so it
// must change ReportSchema, and this test makes that a deliberate edit.
func keysOf(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func wantKeys(t *testing.T, name string, v any, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := keysOf(t, v); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s keys\n got  %v\n want %v", name, got, want)
	}
}

func jsonAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, IntakeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Agent{Name: "peer", dir: dir}
}

func writeTestRequest(t *testing.T, a *Agent, id, status string) string {
	t.Helper()
	p := filepath.Join(a.InboxPath(), id+".md")
	body := "---\nid: " + id + "\nfrom: asker\nto: peer\ntype: question\ncreated: 2026-10-31\nstatus: " + status + "\npriority: P1\nrelated: [x]\n---\n\nbody\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInboxListJSON_Shape(t *testing.T) {
	a := jsonAgent(t)
	now := time.Now()
	writeTestRequest(t, a, "r1", "pending")
	p := writeTestRequest(t, a, "r2", "in-progress")
	if err := os.WriteFile(p+".claim", []byte(`{"owner_session":"s9","pid":1,"heartbeat":`+itoa(now.Unix())+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	q, err := InboxQueue(a, false)
	if err != nil {
		t.Fatal(err)
	}
	r := NewInboxListJSON(a, q, now)
	wantKeys(t, "inbox list", r, "schema", "generated", "agent", "requests")
	if r.Schema != ReportSchema || len(r.Requests) != 2 {
		t.Fatalf("%+v", r)
	}
	wantKeys(t, "request", r.Requests[0], "id", "path", "from", "to", "type", "status", "priority", "created", "related", "claim")
	var claimed *RequestJSON
	for i := range r.Requests {
		if r.Requests[i].ID == "r2" {
			claimed = &r.Requests[i]
		}
	}
	if claimed == nil || claimed.Claim == nil || claimed.Claim.Session != "s9" {
		t.Fatalf("claim not reported: %+v", claimed)
	}
	wantKeys(t, "claim", claimed.Claim, "session", "heartbeat")
}

func TestInboxListJSON_EmptyIsAnArray(t *testing.T) {
	a := jsonAgent(t)
	b, _ := json.Marshal(NewInboxListJSON(a, nil, time.Now()))
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	if string(m["requests"]) != "[]" {
		t.Fatalf("requests = %s, want []", m["requests"])
	}
}

func TestInboxNextJSON_Shape(t *testing.T) {
	a := jsonAgent(t)
	now := time.Now()
	none := NewInboxNextJSON(a, nil, false, now)
	wantKeys(t, "inbox next", none, "schema", "generated", "agent", "request", "claimed")
	if b, _ := json.Marshal(none); !json.Valid(b) || none.Request != nil {
		t.Fatal("an empty queue reports request: null")
	}
	writeTestRequest(t, a, "r1", "pending")
	r, err := InboxNext(a, "s1", os.Getpid(), true, now)
	if err != nil || r == nil {
		t.Fatalf("%v %v", r, err)
	}
	got := NewInboxNextJSON(a, r, true, now)
	if !got.Claimed || got.Request == nil || got.Request.Claim == nil || got.Request.Claim.Session != "s1" {
		t.Fatalf("%+v", got)
	}
}

func TestDocJSON_Shape(t *testing.T) {
	a := jsonAgent(t)
	now := time.Now()
	missing := NewDocJSON(a, filepath.Join(a.Dir(), "nope.md"), now)
	wantKeys(t, "doc", missing, "schema", "generated", "agent", "exists", "updated", "path", "body")
	if missing.Exists || missing.Body != "" || missing.Updated != "" {
		t.Fatalf("%+v", missing)
	}
	p := filepath.Join(a.Dir(), "h.md")
	_ = os.WriteFile(p, []byte("focus\n"), 0o644)
	got := NewDocJSON(a, p, now)
	if !got.Exists || got.Body != "focus\n" || got.Updated == "" {
		t.Fatalf("%+v", got)
	}
}

func TestUpgradeCheckJSON_Shape(t *testing.T) {
	plan := PoolUpgradePlan{
		PoolRoot:  "/pool",
		Pool:      []string{"make pool/ a git repo"},
		Doctrine:  []string{"core.md"},
		Conflicts: []string{"local.md"},
		Warnings:  []string{"global config"},
		Agents:    []AgentUpgradePlan{{Name: "peer", Harness: "claude", Actions: []string{"re-wire hooks"}, Warnings: []string{"uncommitted changes"}}},
	}
	r := NewUpgradeCheckJSON(plan, false, time.Now())
	wantKeys(t, "upgrade check", r, "schema", "generated", "pool_root", "pending", "steps", "warnings")
	if !r.Pending || len(r.Steps) != 4 || len(r.Warnings) < 2 {
		t.Fatalf("%+v", r)
	}
	wantKeys(t, "step", r.Steps[0], "area", "agent", "action", "conflict")
	wantKeys(t, "warning", r.Warnings[0], "agent", "message")
	if only := NewUpgradeCheckJSON(plan, true, time.Now()); len(only.Steps) != 3 {
		t.Fatalf("--doctrine-only leaves agent steps out: %+v", only.Steps)
	}
}

func TestSessionListJSON_Shape(t *testing.T) {
	foreignScope(t)
	a := jsonAgent(t)
	now := time.Now()
	writeTestSession(t, a.Dir(), &Session{ID: "here", Started: now.Unix(), LastBeat: now.Unix(), HarnessPID: os.Getpid(), ProcScope: "here/pid:[1]"})
	writeTestSession(t, a.Dir(), &Session{ID: "far", Started: now.Unix() + 1, LastBeat: now.Unix(), HarnessPID: 7, ProcScope: elsewhere})
	r := NewSessionListJSON(a, SessionList(a, now), now)
	wantKeys(t, "session list", r, "schema", "generated", "agent", "sessions")
	if len(r.Sessions) != 2 {
		t.Fatalf("%+v", r)
	}
	wantKeys(t, "session", r.Sessions[0], "id", "started", "last_beat", "current_claim", "proc_scope", "pid_checkable")
	if !r.Sessions[0].PIDCheckable || r.Sessions[1].PIDCheckable {
		t.Fatalf("pid_checkable: %+v", r.Sessions)
	}
}

func TestDirectoryJSON_ReportsLiveSessions(t *testing.T) {
	a := jsonAgent(t)
	now := time.Now()
	writeTestSession(t, a.Dir(), &Session{ID: "s1", LastBeat: now.Unix()})
	out, err := DirectoryJSON([]*Agent{a}, PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("agent list --json stays a bare array: %v", err)
	}
	if string(entries[0]["live"]) != "1" {
		t.Fatalf("live = %s", entries[0]["live"])
	}
}
