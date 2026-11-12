package agentguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// serviceDecider asks the Straza background service to decide each hook
// event. The service holds the session, the snapshot and the audit spool, so
// the hook keeps only what lives in the user's own files: the transcript
// watermarks, the decision journal and the error log.
type serviceDecider struct {
	store *Store
	svc   *localapi.Client
}

// Decide sends one normalized event to the service and returns its decision.
// A service it cannot reach, or one that refuses, is a deny with the sentence
// that says why (ServiceSentence). At prompt.submit, session.end and
// subagent.stop it then hands the conversation text the capture directive
// asks for to the service, oldest first. Observational events always allow
// at the encoder.
func (d serviceDecider) Decide(n Normalized) policy.Decision {
	start := time.Now()
	tl := d.store.Trace()
	req := localapi.DecideRequest{
		Harness:     windowOf(n),
		NativeEvent: n.NativeEvent, Event: n.Event, NativeTool: n.NativeTool, ToolInput: n.ToolInput,
		AgentType: n.AgentType, AgentID: n.AgentID,
		GatewayProxied: n.GatewayProxied,
	}
	ans, err := d.svc.Decide(context.Background(), req)
	if err != nil {
		dec := failClosed(serviceSentence(err, outcomeDenied))
		journalDecision(tl, n, dec, decisionFacts{start: start, failClosed: true})
		return dec
	}
	j := ans.Journal
	if Unresolved(ans.Decision) {
		// The service resolves every escalation before it answers, so an
		// allow that still needs one is a fault there, and fails closed.
		ans.Decision, j.FailClosed = policy.Decision{Effect: policy.EffectDeny,
			RuleID: ans.Decision.RuleID, SetName: ans.Decision.SetName, Reason: denyLine(unresolvedReason)}, true
	}
	journalDecision(tl, n, ans.Decision, decisionFacts{
		start: start, snapshot: j.Snapshot, escalation: j.Escalation,
		status: j.Status, correlation: j.Correlation,
		spooled: j.Spooled, spoolErr: j.SpoolError, failClosed: j.FailClosed,
	})
	if tl.DebugOn() && j.Escalation != "" {
		tl.Debug("escalate", slog.String("branch", j.Escalation), slog.Int("status", j.Status),
			slog.String("correlation", j.Correlation))
	}
	switch n.Event.Kind {
	case policy.EventPromptSubmit:
		// Pin the reply watermark to "now", before the model answers and
		// whatever the capture directive says: a later capture must never
		// reach back past this prompt, including when capture is flipped ON
		// mid-session. If the PREVIOUS governed turn's session.end never
		// fired (killed terminal), its orphaned reply comes back here and is
		// handed over before the prompt, so the audit order stays
		// chronological.
		if late := pinTranscriptWatermark(d.store, n.TranscriptPath, ans.Capture.Conversations); late != "" {
			d.capture(n, localapi.CaptureReply, late)
		}
		if ans.Capture.Conversations && n.Prompt != "" {
			d.capture(n, localapi.CapturePrompt, n.Prompt)
		}
	case policy.EventSessionEnd, policy.EventSubagentStop:
		// Capture points are a SET, not a single event: the root turn ends at
		// session.end, a delegate's at subagent.stop (its reply lives in its own
		// transcript, which no other event ever names). Both are capture/audit
		// boundaries, not PEPs: enforcement for delegated work stays at tool.pre
		// on the spawn tool and inside the delegate's own tool calls.
		d.captureReply(n, ans.Capture)
	}
	return ans.Decision
}

// unresolvedReason is the deny of an allow the service answered with an
// escalation still open.
const unresolvedReason = "the background service answered an allow it had not resolved (a server check, a classifier or an approval was still open), " +
	"so Straza denied this call. Run `straza doctor`, and tell your Straza administrator if it happens again."

// Unresolved reports whether d is an allow that still carries an escalation
// only the service can resolve, as conformanceDecider refuses one. The
// service must never answer one, and the hook denies one fail-closed.
func Unresolved(d policy.Decision) bool {
	return d.Effect == policy.EffectAllow && (d.ServerCheck || d.Classify || d.Approve != nil)
}

// windowOf names the harness window of a hook event for the service.
func windowOf(n Normalized) localapi.Harness {
	return localapi.Harness{Name: n.HarnessName, Version: n.HarnessVersion, Session: n.SessionID}
}

// captureReply captures a reply at a capture-point event. At session.end a
// payload-borne reply (gemini AfterAgent `prompt_response`) wins when the
// dialect pins it (exact, no file I/O); otherwise the asymmetric half is read
// as the parent-transcript delta since the last capture. At subagent.stop
// the lanes invert: the DELEGATE's own transcript delta wins (it holds the
// whole governed output) and the payload-borne last_assistant_message (only
// the final message) is the fallback for an absent child file (codex:
// nullable). Best-effort by design: any failure leaves the event untouched.
func (d serviceDecider) captureReply(n Normalized, dir policy.CaptureDirective) {
	path, payloadReply := n.TranscriptPath, n.Reply
	delegate := n.Event.Kind == policy.EventSubagentStop
	if delegate {
		// The delegate class tag is the entry ticket ("capture, tagged").
		// Harnesses run internal sidechains too: claude-code's input
		// autosuggest generator fires SubagentStop, and capturing it would
		// plant ghost suggestion text in the transcript. Since claude-code
		// 2.1.220 that sidechain carries an agent_id but still no
		// agent_type, so identity alone proves nothing. Real delegate lanes
		// always carry the type. No agent_type, no capture.
		if n.AgentType == "" {
			return
		}
		path, payloadReply = n.AgentTranscriptPath, n.AgentReply
	}
	if payloadReply == "" && path == "" {
		return
	}
	if !dir.Conversations {
		// The delegate's stop is its ONLY boundary (no prompt.submit ever
		// pins its file), so seal the watermark even when capture is off:
		// flipping capture on later must never reach back past this stop
		// (the subagent analog of pinTranscriptWatermark).
		if delegate && path != "" {
			sealTranscript(d.store, path)
		}
		return
	}
	var reply string
	if delegate {
		if path != "" {
			reply, _ = transcriptDelta(d.store, path)
		}
		if reply == "" {
			reply = payloadReply
		}
	} else {
		reply = payloadReply
		if reply == "" {
			var err error
			if reply, err = transcriptDelta(d.store, path); err != nil {
				return
			}
		}
	}
	if reply != "" {
		d.capture(n, localapi.CaptureReply, reply)
	}
}

// capture hands one turn of conversation text to the service, which applies
// the capture mode and records it. A failure goes to the error log and never
// to the harness.
func (d serviceDecider) capture(n Normalized, kind, text string) {
	err := d.svc.Capture(context.Background(), localapi.CaptureRequest{
		Harness: windowOf(n), Kind: kind, Text: text,
		AgentType: n.AgentType, AgentID: n.AgentID,
	})
	if err != nil {
		d.store.logClientError(ClientError{Kind: "capture", Harness: n.HarnessName, Event: n.NativeEvent,
			Err: serviceSentence(err, "the conversation text was not recorded")})
	}
}

// failClosed is the deny for a decision that could not be made. The reason
// carries the Straza marker once (denyLine).
func failClosed(reason string) policy.Decision {
	return policy.Decision{Effect: policy.EffectDeny, Reason: denyLine(reason)}
}

// FileSHA256 returns the "sha256:<hex>" digest of the file at path, the form
// the attestation hashes and the managed install's registry rows use.
func FileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 G703 -- hashing our own managed artifacts for attestation; paths come from the managed resolvers (%ProgramData% env on Windows), never from request input
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// SessionInfo describes the governed session established at checkin: who the
// agent is operating as and under which policy. Injected as session context so
// the agent knows it is governed (and can relay denials instead of fighting
// them).
type SessionInfo struct {
	User        string
	Roles       []string
	SnapshotID  string
	Attestation string
	ServerURL   string
	// Capture: whether this session's conversations are recorded (spec/
	// policyset §6: the principal is TOLD, never silently surveilled).
	Capture policy.CaptureDirective
	// StartText is the governance paragraph the server wrote at check-in.
	// Nil means the server sent none, and an empty text turns it off.
	StartText *string
}

// PackContext renders knowledge packs as a single additional-context string
// for injection at session start.
func PackContext(packs []localapi.Pack) string {
	if len(packs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Straza knowledge packs (delivered by role)\n\n")
	for _, p := range packs {
		fmt.Fprintf(&b, "## %s (v%s)\n\n%s\n\n", p.Name, p.Version, p.Content)
	}
	return b.String()
}
