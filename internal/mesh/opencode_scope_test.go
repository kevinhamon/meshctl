package mesh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// opencode's background service runs every loaded plugin's hooks for every
// session it hosts. A session whose location is another workspace belongs to
// that workspace's agent: this agent's plugin injects no doctrine, starts no
// session and runs no gate for it. Its own session is unchanged.
func TestOpencodeV2Plugin_IgnoresOtherWorkspacesSessions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	dir := t.TempDir()
	plugin := filepath.Join(dir, "meshctl.mjs")
	if err := os.WriteFile(plugin, []byte(opencodePluginV2JS), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("OWN-DOCTRINE"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	_ = os.MkdirAll(bin, 0o755)
	logPath := filepath.Join(dir, "hooks.log")
	stub := "#!/bin/sh\npayload=$(cat)\necho \"$4 $payload\" >> " + logPath + "\n" +
		"if [ \"$4\" = pre-tool-use ]; then case \"$payload\" in *Forbidden*) echo 'mesh: denied by stub';; esac; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "meshctl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `
import plugin from "` + plugin + `"
const hooks = {}
const ctx = {
  location: { directory: "` + dir + `" },
  tool: { hook: async (name, fn) => { hooks["tool." + name] = fn } },
  session: {
    hook: async (name, fn) => { hooks["session." + name] = fn },
    get: async ({ sessionID }) => {
      const s = { OWN: { agent: "build", location: { directory: "` + dir + `" } },
                  OTHER: { agent: "build", location: { directory: "/elsewhere/other-agent" } } }[sessionID]
      if (!s) throw new Error("no session " + sessionID)
      return { id: sessionID, ...s }
    },
  },
  event: { subscribe: async function* ({ signal }) { while (!signal.aborted) await new Promise(r => setTimeout(r, 50)) } },
}
const cleanup = await plugin.setup(ctx)
const sysOther = []
await hooks["session.context"]({ sessionID: "OTHER", system: sysOther })
if (sysOther.length) { console.log("FAIL other workspace's session got doctrine: " + JSON.stringify(sysOther)); process.exit(1) }
await hooks["tool.execute.before"]({ sessionID: "OTHER", tool: "Forbidden", input: {} })
const sysOwn = []
await hooks["session.context"]({ sessionID: "OWN", system: sysOwn })
if (!JSON.stringify(sysOwn).includes("OWN-DOCTRINE")) { console.log("FAIL own session lost doctrine"); process.exit(1) }
let threw = ""
try { await hooks["tool.execute.before"]({ sessionID: "OWN", tool: "Forbidden", input: {} }) } catch (e) { threw = String(e.message) }
if (!threw.includes("denied by stub")) { console.log("FAIL own session not gated: " + threw); process.exit(1) }
// A failed lookup is treated as ours: the gate stays fail-closed.
threw = ""
try { await hooks["tool.execute.before"]({ sessionID: "UNKNOWN", tool: "read", input: {} }) } catch (e) { threw = String(e.message) }
if (!threw.includes("cannot tell which agent")) { console.log("FAIL unknown session not refused: " + threw); process.exit(1) }
if (typeof cleanup === "function") await cleanup()
console.log("OK")
process.exit(0)
`
	runner := filepath.Join(dir, "run.mjs")
	if err := os.WriteFile(runner, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, runner)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("plugin run: %v\n%s", err, out)
	}
	log, _ := os.ReadFile(logPath)
	if strings.Contains(string(log), "OTHER") {
		t.Fatalf("meshctl ran for another workspace's session:\n%s", log)
	}
	if !strings.Contains(string(log), "session-start") {
		t.Fatalf("own session did not start:\n%s", log)
	}
}
