package mesh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The V2 plugin's session lifecycle, run under node against a fake opencode
// context and a stub `meshctl` that logs which hook it was asked for:
//   - a finished turn is a heartbeat (session-beat), not a session end;
//   - one session-end fires after the idle window;
//   - the gate throws when meshctl cannot run (fail-closed), and relays a
//     deny reason when it can;
//   - subagent children (ADR-0076): a mesh peer's calls carry the caller's
//     session, the child id and the agent; a peer child gets no session
//     lifecycle or caller doctrine, and its finished run is one session-end;
//     a general child runs as the caller; a child whose agent cannot be told
//     is refused.
func TestOpencodeV2Plugin_SessionLifecycleAndGate(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	dir := t.TempDir()
	plugin := filepath.Join(dir, "meshctl.mjs")
	if err := os.WriteFile(plugin, []byte(opencodePluginV2JS), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stub meshctl: appends the hook name to a log; denies tool "Forbidden".
	bin := filepath.Join(dir, "bin")
	_ = os.MkdirAll(bin, 0o755)
	logPath := filepath.Join(dir, "hooks.log")
	payloadLog := filepath.Join(dir, "payloads.log")
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("CALLER-DOCTRINE"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\n" +
		"sub=\"$4\"\n" +
		"payload=$(cat)\n" +
		"echo \"$sub\" >> " + logPath + "\n" +
		"echo \"$sub $payload\" >> " + payloadLog + "\n" +
		"if [ \"$sub\" = pre-tool-use ]; then case \"$payload\" in *Forbidden*) echo 'mesh: denied by stub';; esac; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "meshctl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	driver := filepath.Join(dir, "drive.mjs")
	script := `
import plugin from "` + plugin + `"
const hooks = {}
const events = []
let resolveEvents
const ctx = {
  location: { directory: "` + dir + `" },
  tool: { hook: async (name, fn) => { hooks["tool." + name] = fn } },
  session: {
    hook: async (name, fn) => { hooks["session." + name] = fn },
    get: async ({ sessionID }) => {
      const s = { S1: { agent: "build" }, C1: { parentID: "S1", agent: "mesh-arch" }, C2: { parentID: "S1", agent: "general" } }[sessionID]
      if (!s) throw new Error("no session " + sessionID)
      return { id: sessionID, ...s }
    },
  },
  event: { subscribe: async function* ({ signal }) {
    while (!signal.aborted) {
      if (events.length) { yield events.shift(); continue }
      await new Promise(r => { resolveEvents = r; setTimeout(r, 50) })
    }
  } },
}
const emit = (ev) => { events.push(ev); if (resolveEvents) resolveEvents() }
const sleep = (ms) => new Promise(r => setTimeout(r, ms))
const cleanup = await plugin.setup(ctx)

// Session starts on first context request.
await hooks["session.prompt"]({ sessionID: "S1", prompt: { text: "hello" } })
await hooks["session.context"]({ sessionID: "S1", system: [] })
// Two finished turns: heartbeats, not ends.
emit({ type: "session.execution.succeeded", data: { sessionID: "S1" } })
await sleep(300)
emit({ type: "session.execution.succeeded", data: { sessionID: "S1" } })
await sleep(300)
// Idle window (AGENT_MESH_OPENCODE_IDLE_SEC=1) elapses ⇒ exactly one session-end.
await sleep(1500)

// Gate: allowed tool passes; denied reason is thrown.
let threw = ""
try { await hooks["tool.execute.before"]({ sessionID: "S1", tool: "Forbidden", input: {} }) } catch (e) { threw = String(e.message) }
if (!threw.includes("denied by stub")) { console.log("FAIL gate reason not relayed: " + threw); process.exit(1) }
await hooks["tool.execute.before"]({ sessionID: "S1", tool: "read", input: {} })

// Children. A peer child: no session-start, no caller doctrine.
const sysPeer = []
await hooks["session.context"]({ sessionID: "C1", agent: "mesh-arch", system: sysPeer })
if (sysPeer.length) { console.log("FAIL peer child got injected context: " + JSON.stringify(sysPeer)); process.exit(1) }
await hooks["tool.execute.before"]({ sessionID: "C1", agent: "mesh-arch", tool: "read", input: { path: "x" } })
// A general child keeps the doctrine and runs as the caller.
const sysGen = []
await hooks["session.context"]({ sessionID: "C2", agent: "general", system: sysGen })
if (!JSON.stringify(sysGen).includes("CALLER-DOCTRINE")) { console.log("FAIL general child lost doctrine"); process.exit(1) }
await hooks["tool.execute.before"]({ sessionID: "C2", agent: "general", tool: "glob", input: {} })
// Unknown child: no agent, lookup fails ⇒ refused.
threw = ""
try { await hooks["tool.execute.before"]({ sessionID: "C9", tool: "read", input: {} }) } catch (e) { threw = String(e.message) }
if (!threw.includes("cannot tell which agent")) { console.log("FAIL unknown child not refused: " + threw); process.exit(1) }
// Finished runs: the peer child closes its dispatch; the general child does nothing.
emit({ type: "session.execution.succeeded", data: { sessionID: "C1" } })
emit({ type: "session.execution.succeeded", data: { sessionID: "C2" } })
await sleep(400)

// Fail-closed: meshctl missing from PATH ⇒ the gate throws.
const savedPath = process.env.PATH
process.env.PATH = "/nonexistent"
threw = ""
try { await hooks["tool.execute.before"]({ sessionID: "S1", tool: "read", input: {} }) } catch (e) { threw = String(e.message) }
if (!threw.includes("refuses rather than fail open")) { console.log("FAIL gate did not fail closed: " + threw); process.exit(1) }
process.env.PATH = savedPath

cleanup()
console.log("OK")
process.exit(0)
`
	if err := os.WriteFile(driver, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, driver)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AGENT_MESH_OPENCODE_IDLE_SEC=1",
		"AGENT_DISPATCH_DEPTH=", "AGENT_MESH_DISTILLING=",
	)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("driver failed: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(logPath)
	calls := strings.Fields(string(b))
	count := func(sub string) int {
		n := 0
		for _, c := range calls {
			if c == sub {
				n++
			}
		}
		return n
	}
	if count("session-start") != 1 {
		t.Errorf("want 1 session-start, got %d: %v", count("session-start"), calls)
	}
	if count("session-beat") != 2 {
		t.Errorf("want 2 session-beat (one per finished turn), got %d: %v", count("session-beat"), calls)
	}
	if count("session-end") != 2 {
		t.Errorf("want 2 session-end (the idle window, the peer child's run), got %d: %v", count("session-end"), calls)
	}
	pl, _ := os.ReadFile(payloadLog)
	payloads := string(pl)
	for _, want := range []string{
		`pre-tool-use {"cwd":"` + dir + `","session_id":"S1","tool_name":"read","tool_input":{"path":"x"},"agent_type":"mesh-arch","agent_id":"C1"}`,
		`pre-tool-use {"cwd":"` + dir + `","session_id":"S1","tool_name":"glob","tool_input":{}}`,
		`session-end {"cwd":"` + dir + `","session_id":"S1","agent_id":"C1","agent_type":"mesh-arch","prompt":""}`,
	} {
		if !strings.Contains(payloads, want) {
			t.Errorf("missing payload %s in:\n%s", want, payloads)
		}
	}
}
