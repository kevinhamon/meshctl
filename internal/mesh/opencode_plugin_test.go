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
//     deny reason when it can.
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
	stub := "#!/bin/sh\n" +
		"sub=\"$4\"\n" +
		"payload=$(cat)\n" +
		"echo \"$sub\" >> " + logPath + "\n" +
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
  session: { hook: async (name, fn) => { hooks["session." + name] = fn } },
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

// Fail-closed: meshctl missing from PATH ⇒ the gate throws.
process.env.PATH = "/nonexistent"
threw = ""
try { await hooks["tool.execute.before"]({ sessionID: "S1", tool: "read", input: {} }) } catch (e) { threw = String(e.message) }
if (!threw.includes("refuses rather than fail open")) { console.log("FAIL gate did not fail closed: " + threw); process.exit(1) }

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
	if count("session-end") != 1 {
		t.Errorf("want exactly 1 session-end after the idle window, got %d: %v", count("session-end"), calls)
	}
}
