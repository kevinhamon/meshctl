package mesh

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// opencode_v2.go — the opencode V2 side of the opencode adapter. V2 (`@opencode/cli`,
// 2.x) is a separate release line from V1 (`opencode-ai`, 1.x); both are maintained,
// and their plugin APIs are mutually incompatible: V2 does not run a V1 plugin (it
// logs a warning and carries on, so the mesh would go silently dark) and V1 does not
// understand a V2 one. One harness id, `opencode`, covers both: the installed
// binary's major version picks the variant at wire, dispatch and distill time.
//
// Validated against opencode 2.0.28 (fake OpenAI-compatible provider, real binary):
//   - a plain-object default export `{ id, setup(ctx) }` in .opencode/plugins/*.js
//     loads with no @opencode/plugin import (Plugin.define is the identity);
//   - the plugin runtime is Bun; node:child_process spawn with stdin works;
//   - session.hook("context") fires once per primary model request (not for the
//     title request) with sessionID, and a pushed {type:"text"} system part
//     reaches the model;
//   - tool.hook("execute.before") sees every call, including tools invoked from
//     inside Code Mode `execute` (named <namespace>_<tool>); throwing blocks the
//     call and the thrown message is the tool error the model sees;
//   - write/edit take `path` (often relative), not V1's `filePath`;
//   - events arrive as { type, data: { sessionID } }; session.execution.succeeded
//     marks the end of a turn (session.idle is deprecated);
//   - `opencode run` has --standalone/--auto/--model but no --pure; a standalone
//     run's server inherits the client env, a background-service run's does not;
//   - `instructions` in opencode.json is accepted but NOT loaded in V2, and V2 reads
//     AGENTS.md only — so the plugin injects CLAUDE.md itself.

// opencodeMajorEnv overrides detection (tests, CI, or a box with both lines).
const opencodeMajorEnv = "MESHCTL_OPENCODE_MAJOR"

var (
	opencodeMajorOnce sync.Once
	opencodeMajorSeen int
)

var reOpencodeVersion = regexp.MustCompile(`v?(\d+)\.\d+`)

// parseOpencodeMajor pulls the major version out of `opencode --version` output:
// "1.18.35" (V1) or "opencode v2.0.28" (V2). 0 ⇒ unparseable.
func parseOpencodeMajor(s string) int {
	m := reOpencodeVersion.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// InstalledOpencodeMajor reports the major version of the opencode on $PATH:
// the env override if set, else `opencode --version` (probed once per process).
// 0 ⇒ not installed or unrecognised.
func InstalledOpencodeMajor() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(opencodeMajorEnv))); err == nil && v > 0 {
		return v
	}
	opencodeMajorOnce.Do(func() {
		out, err := exec.Command("opencode", "--version").Output()
		if err == nil {
			opencodeMajorSeen = parseOpencodeMajor(string(out))
		}
	})
	return opencodeMajorSeen
}

const (
	opencodePluginV1Rel = ".opencode/plugin/meshctl.js"
	opencodePluginV2Rel = ".opencode/plugins/meshctl.js"
)

// writeManagedPlugin writes body to dest/rel when absent, and overwrites an
// existing meshctl-managed copy whose content differs. A non-meshctl file at
// that path is left untouched.
func writeManagedPlugin(dest, rel, body string) ([]string, error) {
	out := filepath.Join(dest, filepath.FromSlash(rel))
	if b, err := os.ReadFile(out); err == nil {
		cur := string(b)
		if !strings.Contains(cur, opencodePluginMarker) {
			return nil, nil // someone else's plugin at this path — don't clobber
		}
		if cur == body {
			return nil, nil // already current
		}
		if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
			return nil, err
		}
		return []string{"updated " + rel + " (plugin upgraded)"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
		return nil, err
	}
	return []string{"wrote " + rel}, nil
}

// removeManagedPlugin deletes the other major's meshctl plugin, if meshctl wrote
// it: V2 still scans .opencode/plugin/ and would warn on the V1 file every start,
// and V1 must not trip over a V2 file. A non-meshctl file is left alone.
func removeManagedPlugin(dest, rel string) ([]string, error) {
	out := filepath.Join(dest, filepath.FromSlash(rel))
	b, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(b), opencodePluginMarker) {
		return nil, nil
	}
	if err := os.Remove(out); err != nil {
		return nil, err
	}
	return []string{"removed " + rel + " (wrong opencode major)"}, nil
}

// opencodePluginV2JS is a thin bridge from opencode V2's plugin hooks to
// `meshctl --harness opencode hook event` (hook_event.go): each hook or event
// becomes one normalized event, and the reply is applied — context text pushed
// as a system part, a tool.before reason thrown. Session state, doctrine
// loading, the headless rules and the idle window are meshctl's.
//
// What stays here is what only the host can answer or must do itself:
//   - the session lookup (parentID, agent, directory), cached. opencode's
//     background service runs every loaded plugin's hooks for every session it
//     hosts; a session that runs in another workspace belongs to that
//     workspace's agent, so this plugin does not forward it (forwarding would
//     cost one meshctl run per loaded agent per event). An unknown directory
//     (a failed lookup) is treated as ours, so the gate stays fail-closed.
//   - the gate's failure mode: tool execute.before is the mesh's one
//     fail-CLOSED hook; if meshctl cannot be run or does not answer, the call
//     is refused, not waved through.
//   - ordering: a session's advisory events are sent one at a time, so a
//     prompt is recorded before the context request that follows it.
//   - wake-up timers: after a finished turn, a tick once the idle window has
//     passed, so meshctl can close a quiet session with no further event.
//   - teardown: a shutdown event closes the sessions this instance saw.
const opencodePluginV2JS = `// meshctl mesh integration for opencode V2 (generated by ` + "`meshctl agent new/onboard`" + `).
// V2 plugin API: default export { id, setup }. Do not edit — re-run onboard to upgrade.
// A thin bridge: each hook becomes one event for ` + "`meshctl hook event`" + `, which keeps
// the session state and makes every mesh decision.
import { spawn, spawnSync } from "node:child_process"

const ARGV = ["--harness", "opencode", "hook", "event"]

// exec sends one event to meshctl. It resolves {out, err}: err is set when
// meshctl could not be run or did not finish in time, so the caller decides
// whether that fails open (advisory events) or closed (the gate).
const exec = (cwd, payload, timeoutMs) => new Promise((resolve) => {
  try {
    const p = spawn("meshctl", ARGV, { cwd, stdio: ["pipe", "pipe", "ignore"] })
    let out = ""
    const timer = setTimeout(() => { try { p.kill() } catch {} ; resolve({ out: "", err: "meshctl hook event timed out" }) }, timeoutMs)
    p.stdout.on("data", (d) => { out += d })
    p.on("error", (e) => { clearTimeout(timer); resolve({ out: "", err: "could not run meshctl (" + (e && e.message || "spawn failed") + ")" }) })
    p.on("close", () => { clearTimeout(timer); resolve({ out: out.trim(), err: "" }) })
    p.stdin.on("error", () => {})
    p.stdin.end(JSON.stringify(payload))
  } catch (e) { resolve({ out: "", err: "could not run meshctl (" + (e && e.message || "spawn failed") + ")" }) }
})
// Idle window (AGENT_MESH_OPENCODE_IDLE_SEC, default 600s): only when to wake
// meshctl up; meshctl decides whether the session is still quiet.
const idleMs = (Number(process.env.AGENT_MESH_OPENCODE_IDLE_SEC) || 600) * 1000

export default {
  id: "meshctl",
  async setup(ctx) {
    const cwd = (ctx.location && ctx.location.directory) || process.cwd()
    const instance = "oc2-" + process.pid + "-" + Date.now().toString(36)
    const base = { harness: "opencode", variant: 2, instance, cwd }

    // Session lookup, cached: {parentID, agent, directory}; null when it
    // failed (not cached, so the next call retries).
    const sessions = new Map()
    const sessionInfo = async (sid) => {
      if (!sid) return null
      if (sessions.has(sid)) return sessions.get(sid)
      try {
        const s = await ctx.session.get({ sessionID: sid })
        const info = { parentID: (s && s.parentID) || "", agent: (s && s.agent) || "",
          directory: (s && s.location && s.location.directory) || "" }
        sessions.set(sid, info)
        return info
      } catch { return null }
    }
    const elsewhere = (info) => !!(info && info.directory && info.directory !== cwd)
    // The host facts every session event carries.
    const facts = (sid, info, agent) => ({ session_id: sid || "", parent_session_id: (info && info.parentID) || "",
      agent: agent || (info && info.agent) || "", session_unknown: !info })

    // Advisory events, one at a time per session; fail open.
    const chains = new Map()
    const send = (sid, ev) => {
      const next = (chains.get(sid) || Promise.resolve()).then(() => exec(cwd, { ...base, ...ev }, 30000)).then((r) => r.out)
      const tail = next.catch(() => {})
      chains.set(sid, tail)
      void tail.then(() => { if (chains.get(sid) === tail) chains.delete(sid) })
      return next
    }
    const wake = new Map()
    const armWake = (sid) => {
      const t = wake.get(sid); if (t) clearTimeout(t)
      const h = setTimeout(() => { wake.delete(sid); void send(sid, { event: "tick", session_id: sid }) }, idleMs + 2000)
      if (h && typeof h.unref === "function") h.unref()
      wake.set(sid, h)
    }

    await ctx.tool.hook("execute.before", async (e) => {
      const info = await sessionInfo(e.sessionID)
      if (elsewhere(info)) return
      const r = await exec(cwd, { ...base, event: "tool.before", ...facts(e.sessionID, info, e.agent),
        tool: { name: e.tool || "", input: e.input === undefined ? {} : e.input } }, 30000)
      if (r.err) throw new Error("mesh gate could not evaluate this tool call (" + r.err + ") and refuses rather than fail open; install meshctl on PATH or remove .opencode/plugins/meshctl.js")
      if (r.out) throw new Error(r.out)
    })

    await ctx.session.hook("prompt", async (e) => {
      try {
        const info = await sessionInfo(e.sessionID)
        if (elsewhere(info)) return
        await send(e.sessionID, { event: "prompt", ...facts(e.sessionID, info, e.agent), prompt: (e.prompt && e.prompt.text) || "" })
      } catch {}
    })
    await ctx.session.hook("context", async (e) => {
      const info = await sessionInfo(e.sessionID)
      if (elsewhere(info)) return
      const text = await send(e.sessionID, { event: "context", ...facts(e.sessionID, info, e.agent), doctrine: true })
      if (text) e.system.push({ type: "text", text })
    })

    const controller = new AbortController()
    void (async () => {
      try {
        for await (const ev of ctx.event.subscribe({ signal: controller.signal })) {
          if (ev.location && ev.location.directory && ev.location.directory !== cwd) continue
          const data = ev.data || ev.properties || {}
          const sid = data.sessionID || (ev.durable && ev.durable.aggregateID) || ""
          if (!sid) continue
          const ended = ev.type === "session.deleted" || ev.type === "session.removed"
          const turnDone = ev.type === "session.execution.succeeded" || ev.type === "session.idle" ||
            (ev.type === "session.status" && data.status && data.status.type === "idle")
          if (!ended && !turnDone) continue
          const info = await sessionInfo(sid)
          if (elsewhere(info)) continue
          await send(sid, { event: ended ? "session.end" : "turn.end", ...facts(sid, info) })
          if (turnDone && !(info && info.parentID)) armWake(sid)
        }
      } catch {}
    })()
    return () => {
      controller.abort()
      for (const t of wake.values()) clearTimeout(t)
      wake.clear()
      // Teardown: close out every session this instance saw, synchronously.
      try { spawnSync("meshctl", ARGV, { cwd, input: JSON.stringify({ ...base, event: "shutdown" }), stdio: ["pipe", "ignore", "ignore"], timeout: 30000 }) } catch {}
    }
  },
}
`
