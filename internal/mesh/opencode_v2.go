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

// opencodePluginV2JS bridges opencode V2's plugin hooks to the harness-neutral
// `meshctl hook` subcommands, which emit raw text under --harness opencode.
//
//   - context hook: injects the agent's CLAUDE.md (V2 never loads it), then the
//     handoff (first request of a session) + intuition (every request).
//   - prompt hook: read-only capture of the latest prompt, per session — the V2
//     background service runs many sessions through one plugin instance.
//   - tool execute.before: the fail-closed gate (`meshctl hook pre-tool-use`);
//     a non-empty reason is thrown, which blocks the call.
//   - event stream: a finished turn captures a session-record (session-end).
//   - subagent child sessions: looked up once (parentID, agent).
//     Their calls reach the hooks in the shape of Claude Code's subagent
//     hooks — session_id the caller's, agent_id the child's, agent_type its
//     agent — so meshctl maps a mesh-<peer> child to that peer's gate; any
//     other child runs under the caller's full gate (no agent_id, so it does
//     not step aside as a Claude Code hub hook does for its subagents). A child
//     has no lifecycle of its own; a peer child's finished run is captured
//     as the peer's record (Claude Code's SubagentStop). A child whose agent
//     cannot be told is refused.
//
// In a headless run (dispatch or distill, flagged by the env meshctl sets) only
// the doctrine and the gate are registered: the lifecycle hooks would recurse
// into another distill, and V2 has no --pure to switch plugins off.
const opencodePluginV2JS = `// meshctl mesh integration for opencode V2 (generated by ` + "`meshctl agent new/onboard`" + `).
// V2 plugin API: default export { id, setup }. Do not edit — re-run onboard to upgrade.
import { spawn, spawnSync } from "node:child_process"
import { readFileSync, realpathSync } from "node:fs"
import { join } from "node:path"

// exec runs a meshctl hook subcommand. It resolves {out, err}: err is set when
// meshctl could not be run or did not finish in time, so the caller decides
// whether that fails open (context hooks) or closed (the gate).
const exec = (cwd, sub, payload, timeoutMs) => new Promise((resolve) => {
  try {
    const p = spawn("meshctl", ["--harness", "opencode", "hook", sub], { cwd, stdio: ["pipe", "pipe", "ignore"] })
    let out = ""
    const timer = setTimeout(() => { try { p.kill() } catch {} ; resolve({ out: "", err: "meshctl hook " + sub + " timed out" }) }, timeoutMs)
    p.stdout.on("data", (d) => { out += d })
    p.on("error", (e) => { clearTimeout(timer); resolve({ out: "", err: "could not run meshctl (" + (e && e.message || "spawn failed") + ")" }) })
    p.on("close", () => { clearTimeout(timer); resolve({ out: out.trim(), err: "" }) })
    p.stdin.on("error", () => {})
    p.stdin.end(JSON.stringify(payload))
  } catch (e) { resolve({ out: "", err: "could not run meshctl (" + (e && e.message || "spawn failed") + ")" }) }
})
// run: fail-open form for the advisory hooks.
const run = async (cwd, sub, payload) => (await exec(cwd, sub, payload, 30000)).out
// Idle window after which a quiet session is closed out (session-end):
// AGENT_MESH_OPENCODE_IDLE_SEC, default 10 minutes.
const idleMs = (Number(process.env.AGENT_MESH_OPENCODE_IDLE_SEC) || 600) * 1000

const doctrine = (cwd) => {
  try {
    const claude = join(cwd, "CLAUDE.md")
    try { if (realpathSync(join(cwd, "AGENTS.md")) === realpathSync(claude)) return "" } catch {}
    return readFileSync(claude, "utf8").trim()
  } catch { return "" }
}

export default {
  id: "meshctl",
  async setup(ctx) {
    const cwd = (ctx.location && ctx.location.directory) || process.cwd()
    const headless = process.env.AGENT_MESH_DISTILLING === "1" || (Number(process.env.AGENT_DISPATCH_DEPTH) || 0) > 0
    const started = new Set()
    const prompts = new Map()
    const idle = new Map()

    // Session lookup, cached: {parentID, agent}; null when it failed (not
    // cached, so the next call retries). A parentID marks a subagent's child.
    const sessions = new Map()
    const sessionInfo = async (sid) => {
      if (!sid) return null
      if (sessions.has(sid)) return sessions.get(sid)
      try {
        const s = await ctx.session.get({ sessionID: sid })
        const info = { parentID: (s && s.parentID) || "", agent: (s && s.agent) || "" }
        sessions.set(sid, info)
        return info
      } catch { return null }
    }
    const isPeer = (agent) => typeof agent === "string" && agent.startsWith("mesh-")

    // The gate is the mesh's one fail-CLOSED hook: if meshctl cannot be run
    // or does not answer, the call is refused with the reason, not waved through.
    await ctx.tool.hook("execute.before", async (e) => {
      const info = await sessionInfo(e.sessionID)
      const agent = e.agent || (info && info.agent) || ""
      if (!agent && (!info || info.parentID)) throw new Error("mesh gate cannot tell which agent runs this session and refuses rather than run a peer under the wrong rules; retry, or run meshctl doctor")
      const payload = { cwd, session_id: (info && info.parentID) || e.sessionID || "", tool_name: e.tool, tool_input: e.input }
      // Only a peer is flagged: any other subagent (general, explore) runs
      // under this agent's full gate — the plugin is the only hook it has.
      if (isPeer(agent)) {
        payload.agent_type = agent
        if (info && info.parentID) payload.agent_id = e.sessionID
      }
      const r = await exec(cwd, "pre-tool-use", payload, 30000)
      if (r.err) throw new Error("mesh gate could not evaluate this tool call (" + r.err + ") and refuses rather than fail open; install meshctl on PATH or remove .opencode/plugins/meshctl.js")
      if (r.out) throw new Error(r.out)
    })

    // A session ends when it is deleted, when the plugin is torn down, or after
    // an idle window with no new prompt. A finished TURN is only a heartbeat:
    // closing the session on every turn wrote one-turn session records and ran
    // distillation once, after the first exchange, then never again.
    const endSession = async (sid) => {
      if (!sid || !started.has(sid)) return
      started.delete(sid)
      const t = idle.get(sid); if (t) { clearTimeout(t); idle.delete(sid) }
      await run(cwd, "session-end", { cwd, session_id: sid, prompt: "" })
    }
    const armIdle = (sid) => {
      if (!sid) return
      const t = idle.get(sid); if (t) clearTimeout(t)
      const h = setTimeout(() => { void endSession(sid) }, idleMs)
      if (h && typeof h.unref === "function") h.unref()
      idle.set(sid, h)
    }

    if (headless) {
      await ctx.session.hook("context", (e) => {
        const role = doctrine(cwd)
        if (role) e.system.push({ type: "text", text: role })
      })
      return
    }

    await ctx.session.hook("prompt", (e) => {
      try {
        prompts.set(e.sessionID, (e.prompt && e.prompt.text) || "")
        const t = idle.get(e.sessionID); if (t) { clearTimeout(t); idle.delete(e.sessionID) }
      } catch {}
    })
    await ctx.session.hook("context", async (e) => {
      const sid = e.sessionID || ""
      const info = await sessionInfo(sid)
      if (info && info.parentID) {
        // A subagent: no session of its own to start or recall for. A mesh
        // peer takes its role from its definition and "agent brief", not
        // this agent's CLAUDE.md; any other subagent keeps the doctrine.
        const role = isPeer(e.agent || info.agent) ? "" : doctrine(cwd)
        if (role) e.system.push({ type: "text", text: role })
        return
      }
      const parts = []
      const role = doctrine(cwd)
      if (role) parts.push(role)
      if (sid && !started.has(sid)) {
        started.add(sid)
        const handoff = await run(cwd, "session-start", { cwd, session_id: sid, prompt: "" })
        if (handoff) parts.push(handoff)
      }
      const intu = await run(cwd, "user-prompt", { cwd, session_id: sid, prompt: prompts.get(sid) || "" })
      if (intu) parts.push(intu)
      for (const text of parts) e.system.push({ type: "text", text })
    })

    const controller = new AbortController()
    void (async () => {
      try {
        for await (const ev of ctx.event.subscribe({ signal: controller.signal })) {
          if (ev.location && ev.location.directory && ev.location.directory !== cwd) continue
          const data = ev.data || ev.properties || {}
          const sid = data.sessionID || (ev.durable && ev.durable.aggregateID) || ""
          if (ev.type === "session.deleted" || ev.type === "session.removed") { await endSession(sid); continue }
          const turnDone = ev.type === "session.execution.succeeded" || ev.type === "session.idle" ||
            (ev.type === "session.status" && data.status && data.status.type === "idle")
          if (!turnDone || !sid) continue
          const info = await sessionInfo(sid)
          if (info && info.parentID) {
            if (isPeer(info.agent)) await run(cwd, "session-end", { cwd, session_id: info.parentID, agent_id: sid, agent_type: info.agent, prompt: "" })
            continue
          }
          await run(cwd, "session-beat", { cwd, session_id: sid, prompt: "" })
          armIdle(sid)
        }
      } catch {}
    })()
    return () => {
      controller.abort()
      // Teardown: close out every open session so its record is captured.
      for (const sid of Array.from(started)) {
        started.delete(sid)
        const t = idle.get(sid); if (t) clearTimeout(t)
        try { spawnSync("meshctl", ["--harness", "opencode", "hook", "session-end"], { cwd, input: JSON.stringify({ cwd, session_id: sid, prompt: "" }), stdio: ["pipe", "ignore", "ignore"], timeout: 30000 }) } catch {}
      }
    }
  },
}
`
