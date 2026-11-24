package mesh

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The opencode plugins are thin bridges to `meshctl hook event`: these tests
// run them under node against a fake opencode context and a stub `meshctl`
// that logs every event it is sent, and check only the bridge's own job —
// which hook becomes which event, with which host facts, in which order, and
// how the reply is applied. What meshctl does with an event is tested in
// hook_event_test.go.

// pluginStub writes a stub meshctl into dir/bin that appends each event to
// dir/events.log (one JSON per line), replies CTX-REPLY to a context event
// and denies a tool.before for the tool "Forbidden". Returns the bin dir.
func pluginStub(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\n" +
		"[ \"$1 $2 $3 $4\" = \"--harness opencode hook event\" ] || { echo \"bad argv: $*\" >> " + filepath.Join(dir, "argv.log") + "; exit 0; }\n" +
		"payload=$(cat)\n" +
		"echo \"$payload\" >> " + filepath.Join(dir, "events.log") + "\n" +
		"case \"$payload\" in\n" +
		"  *'\"event\":\"context\"'*) echo CTX-REPLY;;\n" +
		"  *'\"event\":\"tool.before\"'*'Forbidden'*) echo 'mesh: denied by stub';;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "meshctl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// pluginEvents reads the events the stub received.
func pluginEvents(t *testing.T, dir string) []map[string]any {
	t.Helper()
	if b, _ := os.ReadFile(filepath.Join(dir, "argv.log")); len(b) > 0 {
		t.Fatalf("meshctl called with the wrong argv:\n%s", b)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "events.log"))
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("event is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

// eventsOf filters events by kind and, if sid is set, session.
func eventsOf(evs []map[string]any, kind, sid string) []map[string]any {
	var out []map[string]any
	for _, e := range evs {
		if e["event"] == kind && (sid == "" || e["session_id"] == sid) {
			out = append(out, e)
		}
	}
	return out
}

func runNode(t *testing.T, dir, bin, script string, env ...string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	runner := filepath.Join(dir, "run.mjs")
	if err := os.WriteFile(runner, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, runner)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}, env...)...)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("plugin run failed: %v\n%s", err, out)
	}
}

// v2Ctx is a fake opencode V2 plugin context: hooks are captured in `hooks`,
// sessions come from `table`, and `emit` feeds the event stream.
const v2Ctx = `
const hooks = {}
const events = []
let wakeEvents
const ctx = {
  location: { directory: DIR },
  tool: { hook: async (name, fn) => { hooks["tool." + name] = fn } },
  session: {
    hook: async (name, fn) => { hooks["session." + name] = fn },
    get: async ({ sessionID }) => {
      const s = table[sessionID]
      if (!s) throw new Error("no session " + sessionID)
      return { id: sessionID, ...s }
    },
  },
  event: { subscribe: async function* ({ signal }) {
    while (!signal.aborted) {
      if (events.length) { yield events.shift(); continue }
      await new Promise(r => { wakeEvents = r; setTimeout(r, 50) })
    }
  } },
}
const emit = (ev) => { events.push(ev); if (wakeEvents) wakeEvents() }
const sleep = (ms) => new Promise(r => setTimeout(r, ms))
const fail = (msg) => { console.log("FAIL " + msg); process.exit(1) }
`

// The V2 bridge: prompt before context, the context reply pushed on every
// call, finished turns and deletions forwarded with the host facts, a tick
// after the idle window, the gate's deny relayed and fail-closed, children
// carrying their parent and agent, a shutdown on teardown.
func TestOpencodeV2Plugin_ForwardsEvents(t *testing.T) {
	dir := t.TempDir()
	plugin := filepath.Join(dir, "meshctl.mjs")
	if err := os.WriteFile(plugin, []byte(opencodePluginV2JS), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := pluginStub(t, dir)
	script := `import plugin from "` + plugin + `"
const DIR = "` + dir + `"
const table = { S1: { agent: "build" }, C1: { parentID: "S1", agent: "mesh-arch" }, C2: { parentID: "S1", agent: "general" } }
` + v2Ctx + `
const cleanup = await plugin.setup(ctx)

await hooks["session.prompt"]({ sessionID: "S1", prompt: { text: "hello" } })
for (let i = 1; i <= 2; i++) {
  const e = { sessionID: "S1", system: [] }
  await hooks["session.context"](e)
  if (!e.system.some(p => p.type === "text" && p.text === "CTX-REPLY")) fail("context call " + i + " did not push the reply")
}
emit({ type: "session.execution.succeeded", data: { sessionID: "S1" } })
await sleep(300)

// Gate: a deny is thrown, an allow passes.
let threw = ""
try { await hooks["tool.execute.before"]({ sessionID: "S1", tool: "Forbidden", input: {} }) } catch (e) { threw = String(e.message) }
if (!threw.includes("denied by stub")) fail("gate reason not relayed: " + threw)
await hooks["tool.execute.before"]({ sessionID: "S1", tool: "read", input: { path: "x" } })
// Children and an unknown session: forwarded with their facts.
await hooks["tool.execute.before"]({ sessionID: "C1", agent: "mesh-arch", tool: "read", input: {} })
await hooks["tool.execute.before"]({ sessionID: "C9", tool: "read", input: {} })
const sysPeer = []
await hooks["session.context"]({ sessionID: "C1", agent: "mesh-arch", system: sysPeer })
emit({ type: "session.execution.succeeded", data: { sessionID: "C1" } })
emit({ type: "session.deleted", data: { sessionID: "C2" } })
// Wake-up: AGENT_MESH_OPENCODE_IDLE_SEC=1 ⇒ a tick ~3s after S1's turn.
await sleep(3500)

// Fail-closed: meshctl missing from PATH ⇒ the gate throws.
const savedPath = process.env.PATH
process.env.PATH = "/nonexistent"
threw = ""
try { await hooks["tool.execute.before"]({ sessionID: "S1", tool: "read", input: {} }) } catch (e) { threw = String(e.message) }
if (!threw.includes("refuses rather than fail open")) fail("gate did not fail closed: " + threw)
process.env.PATH = savedPath

cleanup()
console.log("OK")
process.exit(0)
`
	runNode(t, dir, bin, script, "AGENT_MESH_OPENCODE_IDLE_SEC=1")
	evs := pluginEvents(t, dir)

	// Every event names the harness, its variant, the instance and the workspace.
	inst := evs[0]["instance"]
	for _, e := range evs {
		if e["harness"] != "opencode" || e["variant"] != float64(2) || e["cwd"] != dir || e["instance"] != inst || inst == "" {
			t.Fatalf("event lacks the bridge facts: %v", e)
		}
	}
	// Order: the prompt is recorded before the first context request.
	if evs[0]["event"] != "prompt" || evs[0]["prompt"] != "hello" || evs[1]["event"] != "context" {
		t.Fatalf("prompt must precede context: %v", evs[:2])
	}
	if c := eventsOf(evs, "context", "S1"); len(c) != 2 || c[0]["doctrine"] != true || c[0]["agent"] != "build" {
		t.Fatalf("context events: %v", c)
	}
	if n := len(eventsOf(evs, "turn.end", "S1")); n != 1 {
		t.Fatalf("want 1 turn.end for S1, got %d", n)
	}
	if n := len(eventsOf(evs, "tick", "S1")); n != 1 {
		t.Fatalf("want 1 wake-up tick for S1, got %d", n)
	}
	if n := len(eventsOf(evs, "tick", "C1")); n != 0 {
		t.Fatal("a child's finished run armed a wake-up")
	}
	gate := eventsOf(evs, "tool.before", "")
	if len(gate) != 4 { // the fail-closed call never reached meshctl
		t.Fatalf("want 4 tool.before, got %d: %v", len(gate), gate)
	}
	if tool := gate[1]["tool"].(map[string]any); tool["name"] != "read" || tool["input"].(map[string]any)["path"] != "x" {
		t.Fatalf("tool not forwarded: %v", gate[1])
	}
	if g := gate[2]; g["session_id"] != "C1" || g["parent_session_id"] != "S1" || g["agent"] != "mesh-arch" || g["session_unknown"] != false {
		t.Fatalf("peer child facts: %v", g)
	}
	if g := gate[3]; g["session_id"] != "C9" || g["session_unknown"] != true || g["agent"] != "" {
		t.Fatalf("unknown session facts: %v", g)
	}
	if e := eventsOf(evs, "turn.end", "C1"); len(e) != 1 || e[0]["parent_session_id"] != "S1" || e[0]["agent"] != "mesh-arch" {
		t.Fatalf("peer child's finished run: %v", e)
	}
	if e := eventsOf(evs, "session.end", "C2"); len(e) != 1 {
		t.Fatalf("deletion not forwarded: %v", e)
	}
	if e := eventsOf(evs, "shutdown", ""); len(e) != 1 {
		t.Fatalf("teardown did not send shutdown: %v", e)
	}
}

// opencode's background service runs every loaded plugin's hooks for every
// session it hosts. A session whose location is another workspace belongs to
// that workspace's agent: this agent's plugin forwards nothing for it. Its
// own session, and one whose lookup failed (so the gate stays fail-closed),
// are forwarded.
func TestOpencodeV2Plugin_IgnoresOtherWorkspacesSessions(t *testing.T) {
	dir := t.TempDir()
	plugin := filepath.Join(dir, "meshctl.mjs")
	if err := os.WriteFile(plugin, []byte(opencodePluginV2JS), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := pluginStub(t, dir)
	script := `import plugin from "` + plugin + `"
const DIR = "` + dir + `"
const table = { OWN: { agent: "build", location: { directory: DIR } }, OTHER: { agent: "build", location: { directory: "/elsewhere/other-agent" } } }
` + v2Ctx + `
const cleanup = await plugin.setup(ctx)
for (const sid of ["OTHER", "OWN"]) {
  await hooks["session.prompt"]({ sessionID: sid, prompt: { text: "p" } })
  const sys = []
  await hooks["session.context"]({ sessionID: sid, system: sys })
  if (sid === "OTHER" && sys.length) fail("other workspace's session got context")
  let threw = ""
  try { await hooks["tool.execute.before"]({ sessionID: sid, tool: "Forbidden", input: {} }) } catch (e) { threw = String(e.message) }
  if (sid === "OWN" && !threw.includes("denied by stub")) fail("own session not gated: " + threw)
  emit({ type: "session.execution.succeeded", data: { sessionID: sid } })
}
emit({ type: "session.execution.succeeded", location: { directory: "/elsewhere/other-agent" }, data: { sessionID: "OWN" } })
await hooks["tool.execute.before"]({ sessionID: "UNKNOWN", tool: "read", input: {} })
await sleep(300)
cleanup()
console.log("OK")
process.exit(0)
`
	runNode(t, dir, bin, script)
	evs := pluginEvents(t, dir)
	for _, e := range evs {
		if e["session_id"] == "OTHER" {
			t.Fatalf("forwarded another workspace's session: %v", e)
		}
	}
	for _, k := range []string{"prompt", "context", "tool.before", "turn.end"} {
		if n := len(eventsOf(evs, k, "OWN")); n != 1 {
			t.Errorf("own session: want 1 %s, got %d", k, n)
		}
	}
	if n := len(eventsOf(evs, "tool.before", "UNKNOWN")); n != 1 {
		t.Error("a session whose lookup failed must still reach the gate")
	}
}

// The V1 bridge: chat.message forwards the prompt, the system transform
// pushes the context reply, session.idle is a finished turn (plus a wake-up
// tick), session.deleted ends the session.
func TestOpencodeV1Plugin_ForwardsEvents(t *testing.T) {
	dir := t.TempDir()
	plugin := filepath.Join(dir, "meshctl.mjs")
	if err := os.WriteFile(plugin, []byte(opencodePluginJS), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := pluginStub(t, dir)
	// A minimal stand-in for Bun's $: run the template as a shell line with
	// each interpolation single-quoted, and expose .quiet() → {stdout}.
	script := `import { MeshctlPlugin } from "` + plugin + `"
import { execSync } from "node:child_process"
const q = (v) => "'" + String(v).replace(/'/g, "'\\''") + "'"
const $ = (strings, ...vals) => ({ quiet: async () => ({ stdout: execSync(strings.reduce((a, s, i) => a + s + (i < vals.length ? q(vals[i]) : ""), "")) }) })
const fail = (msg) => { console.log("FAIL " + msg); process.exit(1) }
const sleep = (ms) => new Promise(r => setTimeout(r, ms))
const hooks = await MeshctlPlugin({ $, directory: "` + dir + `" })
await hooks["chat.message"]({ sessionID: "V1" }, { parts: [{ type: "text", text: "it's \"quoted\"" }] })
const out = { system: [] }
await hooks["experimental.chat.system.transform"]({ sessionID: "V1" }, out)
if (!out.system.includes("CTX-REPLY")) fail("context reply not pushed: " + JSON.stringify(out.system))
await hooks.event({ event: { type: "session.idle", properties: { sessionID: "V1" } } })
await sleep(3500)
await hooks.event({ event: { type: "session.deleted", properties: { sessionID: "V1" } } })
console.log("OK")
process.exit(0)
`
	runNode(t, dir, bin, script, "AGENT_MESH_OPENCODE_IDLE_SEC=1")
	evs := pluginEvents(t, dir)
	var kinds []string
	for _, e := range evs {
		if e["harness"] != "opencode" || e["variant"] != float64(1) || e["cwd"] != dir || e["session_id"] != "V1" {
			t.Fatalf("event lacks the bridge facts: %v", e)
		}
		kinds = append(kinds, e["event"].(string))
	}
	if got := strings.Join(kinds, " "); got != "prompt context turn.end tick session.end" {
		t.Fatalf("V1 event sequence: %s", got)
	}
	if evs[0]["prompt"] != `it's "quoted"` {
		t.Fatalf("prompt mangled in transit: %q", evs[0]["prompt"])
	}
}
