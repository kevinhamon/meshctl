package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// outbound_batch.go — the outbound gate, ask side. A peer that declares the surface is
// asked once per summary: the batch is the sha256 of its summary file. The ask
// text is the signature of the confirmation round (Intent line, up to six
// `## Changes` bullets, `Outbound: <surface> via <tool>`, the call's input cut
// at 300 characters). After the human signs it, the PostToolUse hook records
// the batch in the session's ack file; later calls under the same hash pass
// silently into a per-session audit log, so a task of ten writes is one prompt
// and every write stays visible after the fact.
//
// Acks are per gate: the ack line is keyed by the gate's check name
// (GateAckKey), so a promotion signature at `kb finish` never acknowledges an
// outbound batch built from the same summary file, nor the reverse. A denied
// ask leaves no ack: the gate marks the asked call pending, and the hook acks
// only a call it finds pending (a call the gate denied never ran, and was
// never marked).

// OutboundDir is the per-peer outbound state directory under <peer>/.memory/:
// the audit log and the pending-ask markers, one pair per session.
const OutboundDir = "outbound"

// outboundAskBullets and outboundAskInput bound the ask's signature (about
// 600 characters render whole).
const (
	outboundAskBullets = 6
	outboundAskInput   = 300
)

// GateAckKey is the session-ack line for one gate's batch: the gate's check
// name, a space, the summary hash. One summary file feeds two gates (promotion
// and outbound); keying by gate keeps one signature from covering the other.
func GateAckKey(check, hash string) string { return check + " " + hash }

// OutboundBatch is one outbound call's batch: who asks, under which session,
// with which summary, and the batch key (the summary hash).
type OutboundBatch struct {
	Agent       *Agent
	Match       OutboundMatch
	Session     string
	SummaryPath string
	Summary     Summary
	Hash        string // HashBytes of the summary file's bytes
}

// AckKey is the batch's line in the session ack file.
func (b OutboundBatch) AckKey() string { return GateAckKey(GateCheckOutbound, b.Hash) }

// Acked reports whether the human already signed this batch in this session.
func (b OutboundBatch) Acked() bool {
	return HasSessionAck(b.Agent.Dir(), b.Session, b.AckKey())
}

// outboundBatchFor reads the session's summary for an outbound call. why is
// non-empty ("no file", "empty", "no Intent line") when the summary is unusable.
func outboundBatchFor(a *Agent, p HookPayload, mt OutboundMatch) (OutboundBatch, string) {
	session := p.SessionID
	if session == "" {
		session = ResolveAgentSessionID(a, os.Getpid(), time.Now())
	}
	b := OutboundBatch{Agent: a, Match: mt, Session: session, SummaryPath: SummaryPath(a.Dir(), session)}
	raw, err := os.ReadFile(b.SummaryPath)
	switch {
	case err != nil:
		return b, "no file"
	case strings.TrimSpace(string(raw)) == "":
		return b, "empty"
	}
	b.Summary = ParseSummary(string(raw))
	if !b.Summary.HasIntent() {
		return b, "no Intent line"
	}
	b.Hash = HashBytes(raw)
	return b, ""
}

// gateOutboundAsk is the ask for a peer that declares the surface: deny with
// "summary required" when the summary is unusable, allow silently (and audit)
// when the batch is acked, else ask with the signature and mark the call
// pending for the PostToolUse ack.
func gateOutboundAsk(agentsDir string, a *Agent, p HookPayload, mt OutboundMatch) GateDecision {
	_ = agentsDir
	b, why := outboundBatchFor(a, p, mt)
	if why != "" {
		return outboundDeny("mesh: `" + p.ToolName + "` writes the outbound surface `" + mt.Surface + "` — summary required (" + why +
			" at " + b.SummaryPath + "). Write it with `## Intent` (one line, first), `## Grounding`, `## Precedent` and " +
			"`## Changes` (the writes you plan, as `+`/`-` bullets), read the confirmation round out in chat, then re-issue " +
			"this call: the gate asks the human once for the whole batch.")
	}
	now := time.Now()
	if b.Acked() {
		_ = AppendOutboundAudit(b, p, now)
		return GateDecision{Check: GateCheckOutbound}
	}
	_ = markOutboundPending(b, p.ToolUseID)
	return GateDecision{Ask: true, Check: GateCheckOutbound, Reason: RenderAsk(a.Dir(), AskText{
		Check:  GateCheckOutbound,
		Intent: b.Summary.Intent,
		Body:   outboundAskBody(b, p),
		Footer: "Signing acknowledges this summary as one batch: later `" + mt.Surface + "` calls from `" + a.Name +
			"` under the same summary pass without asking, into " + OutboundAuditPath(a.Dir(), b.Session) +
			". Deny if this was not read to you as a confirmation round.",
	})}
}

// outboundAskBody is the signature after the Intent line: up to six Changes
// bullets, the `Outbound:` line, the flattened input.
func outboundAskBody(b OutboundBatch, p HookPayload) []string {
	var lines []string
	for _, c := range b.Summary.Changes {
		for _, x := range c.Added {
			lines = append(lines, "+ "+x)
		}
		for _, x := range c.Removed {
			lines = append(lines, "- "+x)
		}
	}
	if len(lines) > outboundAskBullets {
		lines = lines[:outboundAskBullets]
	}
	lines = append(lines, "", "Outbound: "+b.Match.Surface+" via "+b.Match.Tool)
	if in := FlattenToolInput(p.ToolInput, outboundAskInput); in != "" {
		lines = append(lines, "Input: "+in)
	}
	return lines
}

// FlattenToolInput is a tool call's input as one line: every string value
// (keys sorted), whitespace collapsed, cut at max characters with "…".
func FlattenToolInput(raw json.RawMessage, max int) string {
	s := strings.Join(strings.Fields(toolInputText(raw)), " ")
	r := []rune(s)
	if max > 0 && len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// --- PostToolUse ack ---

// hookOutboundAck records the batch ack after an asked outbound call ran (the
// human signed it), and audits that call. A call the gate allowed silently
// was audited by the gate; a call the gate denied was never marked pending
// and acks nothing. Fail-open: never errors.
func hookOutboundAck(agentsDir string, p HookPayload, now time.Time) {
	if p.StepAside() {
		return
	}
	a, mt, _ := resolveOutbound(agentsDir, p)
	if a == nil {
		return
	}
	b, why := outboundBatchFor(a, p, mt)
	if why != "" || b.Acked() {
		return
	}
	if !takeOutboundPending(b, p.ToolUseID) {
		return
	}
	if RecordSessionAck(a.Dir(), b.Session, b.AckKey()) == nil {
		_ = AppendOutboundAudit(b, p, now)
	}
}

// outboundPendingPath marks asked-but-not-yet-acked calls: one line per ask,
// `<summary hash> <tool_use_id|->`.
func outboundPendingPath(agentDir, session string) string {
	if session == "" {
		session = "no-session"
	}
	return filepath.Join(agentDir, MemoryDir, OutboundDir, sanitizeID(session)+".pending")
}

func markOutboundPending(b OutboundBatch, toolUseID string) error {
	p := outboundPendingPath(b.Agent.Dir(), b.Session)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(b.Hash + " " + orDash(toolUseID) + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// takeOutboundPending consumes the pending mark for this batch and call: same
// hash, and the same tool_use_id when both sides carry one.
func takeOutboundPending(b OutboundBatch, toolUseID string) bool {
	p := outboundPendingPath(b.Agent.Dir(), b.Session)
	lines := readLines(p)
	id := orDash(toolUseID)
	found := false
	var keep []string
	for _, ln := range lines {
		f := strings.Fields(ln)
		if len(f) == 0 {
			continue
		}
		pid := "-"
		if len(f) > 1 {
			pid = f[1]
		}
		if !found && f[0] == b.Hash && (pid == id || pid == "-" || id == "-") {
			found = true
			continue
		}
		keep = append(keep, ln)
	}
	if !found {
		return false
	}
	if len(keep) == 0 {
		_ = os.Remove(p)
	} else {
		_ = os.WriteFile(p, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
	}
	return true
}

// --- audit ---

// OutboundAuditEntry is one outbound call allowed under a batch.
type OutboundAuditEntry struct {
	Time        string `json:"time"`
	Surface     string `json:"surface"`
	Tool        string `json:"tool"`
	ArgsDigest  string `json:"args_digest"`
	SummaryHash string `json:"summary_hash"`
}

// OutboundAuditPath is the session's outbound audit log:
// <peer>/.memory/outbound/<session>.jsonl, one JSON line per allowed call.
func OutboundAuditPath(agentDir, session string) string {
	if session == "" {
		session = "no-session"
	}
	return filepath.Join(agentDir, MemoryDir, OutboundDir, sanitizeID(session)+".jsonl")
}

// AppendOutboundAudit appends one call to the batch's session audit log.
func AppendOutboundAudit(b OutboundBatch, p HookPayload, now time.Time) error {
	e := OutboundAuditEntry{
		Time:        now.UTC().Format(time.RFC3339),
		Surface:     b.Match.Surface,
		Tool:        b.Match.Tool,
		ArgsDigest:  HashBytes(p.ToolInput),
		SummaryHash: b.Hash,
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	path := OutboundAuditPath(b.Agent.Dir(), b.Session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// ReadOutboundAudit returns the session's audited outbound calls in order;
// unreadable lines are skipped.
func ReadOutboundAudit(agentDir, session string) []OutboundAuditEntry {
	var out []OutboundAuditEntry
	for _, ln := range readLines(OutboundAuditPath(agentDir, session)) {
		var e OutboundAuditEntry
		if json.Unmarshal([]byte(ln), &e) == nil && e.Tool != "" {
			out = append(out, e)
		}
	}
	return out
}

// OutboundAuditLines renders audited calls for a receipt or a session record:
// `<time> <surface> via <tool> (args <digest>, batch <hash>)`, digests cut to
// 12 hex characters.
func OutboundAuditLines(entries []OutboundAuditEntry) []string {
	short := func(h string) string {
		h = strings.TrimPrefix(h, "sha256:")
		if len(h) > 12 {
			h = h[:12]
		}
		return h
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%s %s via %s (args %s, batch %s)", e.Time, e.Surface, e.Tool, short(e.ArgsDigest), short(e.SummaryHash)))
	}
	return out
}
