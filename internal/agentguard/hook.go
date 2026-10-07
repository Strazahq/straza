package agentguard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/trace"
	"github.com/strazahq/straza/internal/policy"
)

// HookIO carries the hook invocation's streams and environment. Environ is a
// func for testability (defaults to os.Environ at the call site).
type HookIO struct {
	Harness string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Environ func() []string
}

// Decider evaluates a normalized event and returns a decision. allowAll
// allows everything; the live decider wires the local snapshot PDP.
type Decider interface {
	Decide(n Normalized) policy.Decision
}

// allowAll is the observe-only decider: normalize and observe, no enforcement.
type allowAll struct{}

func (allowAll) Decide(Normalized) policy.Decision {
	return policy.Decision{Effect: policy.EffectAllow, Default: true}
}

// RunHook is the hook entrypoint: read stdin → detect dialect → normalize →
// (session.start: check in and inject packs) → (tool.pre: local PDP decide) →
// encode a dialect-appropriate response. A deny is expressed in the response
// encoding (exit code / JSON). Fail-closed on infrastructure errors.
func RunHook(hio HookIO) error {
	store, err := OpenStore()
	if err != nil {
		// Detect the dialect from the explicit override / env hints so this
		// pre-parse failure still fails closed in the right encoding (gemini
		// ignores the exit code and reads a stdout block; see failClosedDialect).
		harness := DetectHarness(hio.Harness, environMap(hio.Environ), nil, nil)
		return failClosedDialect(hio, harness, "Straza: state unavailable (%v)", err)
	}
	return runHook(hio, store, liveDecider{store: store})
}

// runHookWith is the test entrypoint with an injected decider and no
// session-start side effects.
func runHookWith(hio HookIO, decider Decider) error {
	return runHook(hio, nil, decider)
}

func runHook(hio HookIO, store *Store, decider Decider) error {
	env := environMap(hio.Environ)
	// Determine the dialect as early as possible (from the explicit --harness
	// override and env markers), so a failure BEFORE the payload can be parsed
	// still fails closed in the correct encoding. Gemini blocks via a strict
	// JSON document on stdout and IGNORES the process exit code, so a fail path
	// that emits only exit 2 fails OPEN on gemini. Refined
	// by the payload-heuristic detection once the payload parses.
	harness := DetectHarness(hio.Harness, env, nil, nil)

	adapters, err := LoadAdapters()
	if err != nil {
		return failClosedHook(hio, store, harness, "", "Straza: adapter load failed (%v)", err)
	}
	raw, err := io.ReadAll(hio.Stdin)
	if err != nil {
		return failClosedHook(hio, store, harness, "", "Straza: could not read hook payload (%v)", err)
	}
	payload, err := ParsePayload(raw)
	if err != nil {
		return failClosedHook(hio, store, harness, "", "Straza: %v", err)
	}
	// Payload parsed: refine detection with the payload heuristics (an explicit
	// override / env hint still wins inside DetectHarness).
	harness = DetectHarness(hio.Harness, env, payload, adapters)
	if harness == "" {
		return failClosedHook(hio, store, harness, eventName(payload), "Straza: could not detect harness (pass --harness)")
	}
	adapter := adapters[harness]
	if adapter == nil {
		return failClosedHook(hio, store, harness, eventName(payload), "Straza: no adapter for harness %q", harness)
	}
	// Debug-window breadcrumbs (trace package; a nil store yields a nil
	// logger and every call below is a no-op): which adapter ran and what
	// the normalizer made of the payload, names and kinds only, never the
	// payload itself.
	tl := store.Trace()
	if tl.DebugOn() {
		tl.Debug("adapter", slog.String("harness", harness))
	}
	n, err := Normalize(adapter, payload)
	if err != nil {
		return failClosedHook(hio, store, harness, eventName(payload), "Straza: %v", err)
	}
	if tl.DebugOn() {
		tl.Debug("normalized",
			slog.String("harness", harness),
			slog.String("native_event", trace.Short(n.NativeEvent, 64)),
			slog.String("event", n.Event.Kind),
			slog.String("tool", trace.Short(n.Event.Tool, 128)),
			slog.Bool("gateway_proxied", n.GatewayProxied),
			slog.Bool("delegate", n.AgentID != "" || n.AgentType != ""))
	}

	// session.start: check in, then inject packs as additional context.
	if n.Event.Kind == policy.EventSessionStart && store != nil {
		return handleSessionStart(hio, store, harness, n)
	}

	decision := decider.Decide(n)
	return encodeDecision(hio, harness, n, decision)
}

// handleSessionStart checks in and emits the governance banner plus the
// role-bound knowledge packs as harness additional-context. A checkin failure
// fails the session closed with an actionable reason (the model relays it).
func handleSessionStart(hio HookIO, store *Store, harness string, n Normalized) error {
	start := time.Now()
	tl := store.Trace()
	// The check-in's answer (status + X-Request-Id) is observed by Client.do
	// through this Call, so the session.start journal line names the server
	// record it depended on.
	call := &trace.Call{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = trace.WithCall(ctx, call)
	packs, info, err := SessionStart(ctx, store, n.HarnessName, n.HarnessVersion)
	status, corr := call.Get()
	if err != nil {
		journalDecision(tl, n, policy.Decision{Effect: policy.EffectDeny},
			decisionFacts{start: start, status: status, correlation: corr, failClosed: true})
		return failClosedHook(hio, store, harness, n.NativeEvent, "Straza: %v", err)
	}
	journalDecision(tl, n, policy.Decision{Effect: policy.EffectAllow},
		decisionFacts{start: start, snapshot: info.SnapshotID, status: status, correlation: corr})
	context := GovernanceBanner(info)
	if pc := PackContext(packs); pc != "" {
		context += "\n\n" + pc
	}
	return encodeSessionStart(hio, harness, n.NativeEvent, context, GovernanceUserLine(info))
}

// GovernanceUserLine is the human-visible one-liner (systemMessage) shown in
// the harness transcript at session start.
func GovernanceUserLine(info SessionInfo) string {
	snap := info.SnapshotID
	if len(snap) > 12 {
		snap = snap[:12]
	}
	line := fmt.Sprintf("🛡 Straza governance active: %s (%s) @ %s · policy %s · attestation %s",
		info.User, strings.Join(info.Roles, ","), info.ServerURL, snap, info.Attestation)
	if info.Capture.Conversations {
		line += " · conversations recorded " + recordingWords(info.Capture.Mode)
	}
	return line
}

// recordingWords names a recording mode the way the console does: word for
// word, or with secrets masked.
func recordingWords(mode string) string {
	if mode == policy.CaptureModeRedact {
		return "with secrets masked"
	}
	return "word for word"
}

// GovernanceBanner tells the agent, in its own context, that the session is
// governed: who it operates as, under which policy snapshot, and how to treat
// denials. Agents should KNOW Straza is live, not discover it by being
// denied.
func GovernanceBanner(info SessionInfo) string {
	snap := info.SnapshotID
	if len(snap) > 12 {
		snap = snap[:12]
	}
	banner := fmt.Sprintf(
		"Straza governance is active for this session. You are operating as %q (roles: %s) "+
			"against %s; policy snapshot %s, attestation %s. Tool use is checked locally against "+
			"signed policy and every decision is audited. A denied tool call always carries its "+
			"reason: relay it to the user and do not retry or work around the denial.",
		info.User, strings.Join(info.Roles, ", "), info.ServerURL, snap, info.Attestation)
	if info.Capture.Conversations {
		banner += fmt.Sprintf(" Recording is on: by policy, the prompts and responses of this "+
			"session are recorded %s to the audit system.", recordingWords(info.Capture.Mode))
	}
	return banner
}

// encodeDecision writes the decision in the harness's response dialect.
// Only tool.pre / permission.request are enforceable; other events are
// observational and always succeed.
func encodeDecision(hio HookIO, harness string, n Normalized, d policy.Decision) error {
	enforceable := n.Event.Kind == policy.EventToolPre || n.Event.Kind == policy.EventPermissionRequest
	if !enforceable || d.Effect == policy.EffectAllow {
		return encodeAllow(hio, harness, n.NativeEvent, enforceable)
	}
	return encodeDeny(hio, harness, n.NativeEvent, d)
}

// claudeResponse is the Claude Code hookSpecificOutput shape, emitted on the
// ENFORCEABLE events only (tool.pre / permission.request, the ones that carry
// a permissionDecision) and by encodeSessionStart's context document. Every
// other event acks with silence; see encodeAllow.
type claudeResponse struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision,omitempty"`
		PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	} `json:"hookSpecificOutput"`
}

// encodeSessionStart emits knowledge packs as harness additional context.
// Claude Code / Codex read hookSpecificOutput.additionalContext; Gemini reads
// a top-level additionalContext (strict JSON). userLine, when non-empty, is
// ALSO shown to the human via Claude Code's systemMessage: the agent knowing
// it is governed is additionalContext; the human seeing it is systemMessage.
// Gemini parses strict JSON with a fixed schema, so it gets context only.
//
// nativeEvent is the harness-native session-start event name (n.NativeEvent),
// echoed back as hookEventName so the harness accepts the response. It is NOT
// hardcoded to "SessionStart": a dialect that renames its session-start event
// would break silently otherwise. Every current dialect names it
// "SessionStart".
func encodeSessionStart(hio HookIO, harness, nativeEvent, context, userLine string) error {
	if context == "" && userLine == "" {
		return encodeAllow(hio, harness, nativeEvent, false)
	}
	switch harness {
	case "gemini":
		return writeJSON(hio.Stdout, map[string]any{"decision": "allow", "additionalContext": context})
	default:
		out := map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":     nativeEvent,
				"additionalContext": context,
			},
		}
		if userLine != "" {
			out["systemMessage"] = userLine
		}
		return writeJSON(hio.Stdout, out)
	}
}

// encodeAllow writes the allow ack. eventName is the harness-native event,
// echoed as hookEventName; permissionDecision only for enforceable events.
//
// Codex gets silence on every event. Codex strict-parses hook stdout per event
// (serde deny_unknown_fields): on exit 0, print schema-valid JSON or NOTHING.
// The echo document below is valid JSON of the wrong shape, and even a
// well-formed permissionDecision:"allow" is rejected on tool.pre unless the
// hook rewrites input. Empty stdout is the one ack valid for every codex
// event; denies are unaffected, since exit 2 plus stderr is their contract and
// codex ignores stdout on exit 2.
//
// The default lane gets silence on NON-ENFORCEABLE events for the same reason:
// it validates stdout against a per-event hookSpecificOutput schema whose union
// has no SessionEnd variant, so an echo is rejected in front of the user on
// every governed session close. Enforceable events keep the document.
func encodeAllow(hio HookIO, harness, eventName string, enforceable bool) error {
	switch harness {
	case "gemini":
		// Gemini requires strict JSON on stdout; all diagnostics to stderr.
		return writeJSON(hio.Stdout, map[string]any{"decision": "allow"})
	case "codex":
		return nil
	default:
		if !enforceable {
			return nil
		}
		resp := claudeResponse{}
		resp.HookSpecificOutput.HookEventName = eventName
		resp.HookSpecificOutput.PermissionDecision = "allow"
		return writeJSON(hio.Stdout, resp)
	}
}

// exitDenyError signals the caller to exit with the harness deny convention
// (Claude Code / Codex use exit code 2 to block).
type exitDenyError struct{ code int }

func (e exitDenyError) Error() string { return fmt.Sprintf("deny (exit %d)", e.code) }

// Code returns the process exit code a deny should produce.
func (e exitDenyError) Code() int { return e.code }

// encodeDeny writes the deny ack. eventName (the harness-native event, echoed
// as hookEventName) is only reached for enforceable events (tool.pre /
// permission.request), so a permissionDecision always applies.
func encodeDeny(hio HookIO, harness, eventName string, d policy.Decision) error {
	reason := d.Reason
	if reason == "" {
		reason = "Straza: blocked by policy"
	}
	switch harness {
	case "gemini":
		_ = writeJSON(hio.Stdout, map[string]any{"decision": "deny", "reason": reason})
		return nil
	default:
		resp := claudeResponse{}
		resp.HookSpecificOutput.HookEventName = eventName
		resp.HookSpecificOutput.PermissionDecision = "deny"
		resp.HookSpecificOutput.PermissionDecisionReason = reason
		_ = writeJSON(hio.Stdout, resp)
		// Claude Code / Codex also honor exit code 2 as a hard block.
		fmt.Fprintln(hio.Stderr, reason)
		return exitDenyError{code: 2}
	}
}

// failClosedDialect writes a dialect-correct fail-closed deny: on infra, parse
// or normalize errors a governed hook must BLOCK with an actionable reason,
// and the encoding differs by dialect, so it writes both.
//
//   - codex and the default lane honor exit 2 as a hard block, so the reason
//     always goes to stderr and exitDenyError{2} is returned. That alone is
//     their contract: hookSpecificOutput needs a harness-native event name an
//     infra error may lack.
//   - gemini IGNORES the exit code and blocks only on a strict-JSON
//     {"decision":"deny","reason":...} document on stdout, so that JSON is ALSO
//     written for gemini and for an UNKNOWN harness that could be gemini.
//
// The extra document is benign elsewhere: they block on exit 2 regardless of
// stdout and read hookSpecificOutput.permissionDecision, never a top-level
// "decision" (pinned by TestFailClosed*Unchanged).
func failClosedDialect(hio HookIO, harness, format string, args ...any) error {
	return failClosedMsg(hio, harness, fmt.Sprintf(format, args...))
}

// failClosedHook is failClosedDialect for the governed hook path: it FIRST
// records the failure to the client error log (event name + error string +
// exit, never payload content), then encodes the same fail-closed deny. The
// log write is best-effort and cannot alter the deny (isolation is
// regression-pinned by TestHookErrorLogBestEffortIsolation). A nil store (the
// injected-decider test entrypoint runHookWith, and the pre-store failure in
// RunHook) logs nothing: there is no home to log under.
func failClosedHook(hio HookIO, store *Store, harness, event, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if store != nil {
		store.logClientError(ClientError{Kind: "hook", Harness: harness, Event: event, Err: msg, Exit: 2})
	}
	return failClosedMsg(hio, harness, msg)
}

func failClosedMsg(hio HookIO, harness, msg string) error {
	if harness == "gemini" || harness == "" {
		// Gemini reads its block from stdout and ignores the exit code; when the
		// harness is unknown we cannot rule gemini out, so emit it universally.
		_ = writeJSON(hio.Stdout, map[string]any{"decision": "deny", "reason": msg})
	}
	fmt.Fprintln(hio.Stderr, msg)
	return exitDenyError{code: 2}
}

// fail is the harness-agnostic fail-closed deny used where the dialect is not
// yet known (conformance policy-load errors, RunHookConformance): it takes the
// universal path so a gemini conformance run still blocks on stdout.
func fail(hio HookIO, format string, args ...any) error {
	return failClosedDialect(hio, "", format, args...)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	return enc.Encode(v)
}

func environMap(environ func() []string) map[string]string {
	out := map[string]string{}
	if environ == nil {
		return out
	}
	for _, kv := range environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}
