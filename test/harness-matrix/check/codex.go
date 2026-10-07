// codex.go holds the codex lane's gates. The harness's own telemetry surface is
// RUST_LOG=codex_hooks=trace on stderr: `hook: <Event>` / `hook: <Event>
// Failed` per hook (verified against codex-cli 0.146.0).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// keyFreeEvents are the codex events a fake-key `codex exec` turn fires,
// verified on codex 0.146.0: the session opens, the
// prompt submits, the model call dies on auth, the session closes. Stop,
// PreToolUse and the Subagent pair need a completing model turn and belong to
// the live-model gates (g3-live / g4-e2e).
var keyFreeEvents = []string{"SessionStart", "UserPromptSubmit", "SessionEnd"}

// liveEvents are what a COMPLETING codex turn fires and the live-model gates
// require: the key-free three plus Stop, which only exists once the model
// actually answers. Verified with codex 0.146.0, ollama 0.32.5 and
// qwen3:1.7b via `codex exec --oss`.
var liveEvents = []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"}

// silentAckEvents must produce EMPTY hook stdout. Codex's allow ack is
// SILENCE on every event (internal/agentguard/hook.go encodeAllow), because
// codex rejects a document with "invalid stop hook JSON output" and with
// "PreToolUse hook returned unsupported permissionDecision:allow". These
// gates pin it against the real binary so a future encoder change that
// reintroduces a claude-shaped ack cannot ship green. SessionStart is
// excluded on purpose:
// an ENROLLED session legitimately prints its context doc there (and g4-e2e
// asserts that it does).
var silentAckEvents = map[string]bool{
	"UserPromptSubmit": true, "Stop": true, "SessionEnd": true, "PreToolUse": true,
}

// requireToolEvent hardens the tool-event ATTEMPT into a hard assertion.
// Off by default because codex --oss with small local models cannot be
// relied on to dispatch a tool call at all: a vendor defect sends the call to
// codex's router with an empty function name. Set
// HARNESS_MATRIX_REQUIRE_TOOL_EVENT=1 once the vendor stack is fixed or CI
// gets a model that dispatches (gpt-oss).
func requireToolEvent() bool { return os.Getenv("HARNESS_MATRIX_REQUIRE_TOOL_EVENT") == "1" }

func init() {
	gates["floor"] = func(o *opts) []string { return checkFloor(o.version) }
	gates["g1-static"] = func(o *opts) []string { return checkStatic(o.req, o.hooksJSON) }
	gates["g2-spawn"] = func(o *opts) []string { return checkSpawn(o.records, o.harnessErr, o.hookBin, true) }
	// The quoted-exe command form KILLED hook spawn on Windows codex 0.146
	// but is tolerated by the Linux spawn path on 0.144 and 0.146, verified
	// both ways. This gate PINS that tolerance: if a codex release starts
	// rejecting quotes on Linux too, the latest lane reports it as vendor
	// drift, which is exactly the signal the matrix exists to catch early.
	gates["g2-quoted"] = func(o *opts) []string { return checkSpawn(o.records, o.harnessErr, o.hookBin, false) }
	gates["g3-output"] = func(o *opts) []string { return checkOutput(o.records, o.harnessErr) }
	// Live-model gates. -records / -harness-stderr are the UNION across the
	// attempt ladder's turns (the lane appends), so one slow turn that the
	// timeout cut short cannot fail a set another turn completed.
	gates["g3-live"] = func(o *opts) []string { return checkLive(o.records, o.harnessErr) }
	// -req carries g4boot's server-side evidence JSON for this gate (main.go
	// keeps the flags generic: "the primary evidence file under test").
	gates["g4-e2e"] = func(o *opts) []string { return checkE2E(o.records, o.harnessErr, o.req) }
	// Observation, never a verdict: -req and -hooks-json are the two codex
	// output captures (default renderer / --color always). Always exits 0.
	gates["ansi-observe"] = func(o *opts) []string { return observeANSI(o.req, o.hooksJSON) }
}

// checkStatic asserts the rendered configs are the file shapes the vendor
// parser accepts: BOM-less LF requirements.toml carrying straza's managed
// block with every rostered event, and a hooks.json that is a JSON object
// under a top-level "hooks" key. Parse ACCEPTANCE by the real binary is what
// g2-spawn proves; this catches the cheap breakages without spawning codex.
func checkStatic(reqPath, hooksPath string) (errs []string) {
	raw, err := os.ReadFile(reqPath) // #nosec G304 -- test-lane file
	switch {
	case err != nil:
		errs = append(errs, fmt.Sprintf("requirements.toml unreadable: %v", err))
	case len(raw) == 0:
		errs = append(errs, "requirements.toml is empty")
	default:
		if strings.HasPrefix(string(raw), "\xef\xbb\xbf") {
			errs = append(errs, "requirements.toml carries a UTF-8 BOM; codex requires BOM-less")
		}
		if strings.Contains(string(raw), "\r") {
			errs = append(errs, "requirements.toml carries CR bytes on a Unix render")
		}
		for _, marker := range []string{"# BEGIN straza-managed", "# END straza-managed", "[features]", "[hooks]"} {
			if !strings.Contains(string(raw), marker) {
				errs = append(errs, fmt.Sprintf("requirements.toml missing %q", marker))
			}
		}
		for _, ev := range []string{"SessionStart", "PreToolUse", "UserPromptSubmit", "Stop", "SessionEnd", "SubagentStart", "SubagentStop"} {
			if !strings.Contains(string(raw), "[[hooks."+ev+"]]") {
				errs = append(errs, fmt.Sprintf("requirements.toml missing event %s", ev))
			}
		}
		// The 0.146 spawn-kill class: codex hands the command string to the
		// session's shell, so the managed form must be the live-proven one:
		// a single-quoted TOML literal whose head is UNQUOTED (Windows gets
		// the single-token shim instead; unit tests byte-pin that branch).
		// A regression back to a double-quoted exe head would re-break
		// Windows fleets at the next release; pin it here on the Linux
		// render too.
		for _, line := range strings.Split(string(raw), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "command = ") {
				val := strings.TrimPrefix(t, "command = ")
				if strings.HasPrefix(val, `'"`) || strings.HasPrefix(val, `"\"`) {
					errs = append(errs, fmt.Sprintf("managed command regressed to a quoted exe head: %s", t))
				}
			}
		}
	}
	rawJSON, err := os.ReadFile(hooksPath) // #nosec G304 -- test-lane file
	if err != nil {
		errs = append(errs, fmt.Sprintf("hooks.json unreadable: %v", err))
		return errs
	}
	var doc map[string]any
	if err := json.Unmarshal(rawJSON, &doc); err != nil {
		errs = append(errs, fmt.Sprintf("hooks.json is not valid JSON: %v", err))
	} else if hooks, ok := doc["hooks"].(map[string]any); !ok || len(hooks) == 0 {
		errs = append(errs, `hooks.json has no populated top-level "hooks" object`)
	}
	return errs
}

// checkSpawn asserts the harness executed the hook with intact argv and a
// parseable payload. full=true additionally requires every key-free event and
// a Failed-free harness stderr; full=false (the quoted tolerance pin) requires
// only that at least one spawn arrived intact.
func checkSpawn(recordsPath, stderrPath, hookBin string, full bool) (errs []string) {
	recs, err := readRecords(recordsPath)
	if err != nil {
		return []string{err.Error()}
	}
	seen := map[string]bool{}
	for i, r := range recs {
		if len(r.Argv) != 4 || r.Argv[0] != hookBin || r.Argv[1] != "hook" || r.Argv[2] != "--harness" || r.Argv[3] != "codex" {
			errs = append(errs, fmt.Sprintf("record %d: argv not intact: %q", i, r.Argv))
			continue
		}
		ev := eventOf(r)
		if ev == "" {
			errs = append(errs, fmt.Sprintf("record %d: stdin is not JSON or has no hook_event_name", i))
			continue
		}
		seen[ev] = true
	}
	if !full {
		if len(recs) == 0 {
			errs = append(errs, "no spawn recorded: the quoted command form stopped spawning on this platform (vendor drift vs 0.144/0.146 behavior)")
		}
		return errs
	}
	for _, ev := range keyFreeEvents {
		if !seen[ev] {
			errs = append(errs, fmt.Sprintf("event %s never reached the hook", ev))
		}
	}
	errs = append(errs, failedHookLines(stderrPath)...)
	return errs
}

// checkOutput asserts the real hook ran behind the relay with exit 0 on every
// key-free event AND the harness logged no hook failure, i.e. codex ACCEPTED
// what the current encoder printed. This gate catches the 0.146 "invalid
// stop hook JSON output" class of break, exercised on SessionEnd (the same
// session.end encode path as Stop) without a model key.
func checkOutput(recordsPath, stderrPath string) (errs []string) {
	recs, err := readRecords(recordsPath)
	if err != nil {
		return []string{err.Error()}
	}
	seen := map[string]bool{}
	for i, r := range recs {
		ev := eventOf(r)
		if r.ExecExit == nil {
			errs = append(errs, fmt.Sprintf("record %d (%s): relay did not run the real hook", i, ev))
			continue
		}
		if *r.ExecExit != 0 {
			errs = append(errs, fmt.Sprintf("record %d (%s): straza hook exited %d", i, ev, *r.ExecExit))
			continue
		}
		seen[ev] = true
	}
	for _, ev := range keyFreeEvents {
		if !seen[ev] {
			errs = append(errs, fmt.Sprintf("event %s has no clean hook run", ev))
		}
	}
	errs = append(errs, failedHookLines(stderrPath)...)
	return errs
}

// checkFloor asserts the codex under test is at least the pinned hook floor,
// codex 0.124.0 (requirements.toml hook support arrived with hooks GA; an older
// codex silently ignores the managed file, which would green every gate on a
// harness running nothing).
func checkFloor(version string) []string {
	m := regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`).FindStringSubmatch(version)
	if m == nil {
		return []string{fmt.Sprintf("cannot parse codex version from %q", version)}
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major == 0 && minor < 124 {
		return []string{fmt.Sprintf("codex %s is below the 0.124 hook floor; gates would be vacuous", m[0])}
	}
	fmt.Printf("codex version %s ≥ 0.124 floor\n", m[0])
	return nil
}

// liveAcks is the shared body of both live-model gates: walk the relayed
// records of one or more real turns and assert what the REAL `straza hook`
// returned to the REAL codex. Returns the set of events that reached the
// hook cleanly.
func liveAcks(recs []record) (map[string]bool, []string) {
	var errs []string
	seen := map[string]bool{}
	for i, r := range recs {
		ev := eventOf(r)
		if ev == "" {
			errs = append(errs, fmt.Sprintf("record %d: stdin is not JSON or has no hook_event_name", i))
			continue
		}
		if r.ExecExit == nil {
			errs = append(errs, fmt.Sprintf("record %d (%s): relay did not run the real hook", i, ev))
			continue
		}
		if *r.ExecExit != 0 {
			errs = append(errs, fmt.Sprintf("record %d (%s): straza hook exited %d (stderr: %s)",
				i, ev, *r.ExecExit, strings.TrimSpace(r.ExecStderr)))
			continue
		}
		if silentAckEvents[ev] && r.ExecStdout != "" {
			errs = append(errs, fmt.Sprintf(
				"record %d (%s): codex allow ack must be EMPTY stdout, got %q (the 0.146 strict-parse class)",
				i, ev, r.ExecStdout))
			continue
		}
		seen[ev] = true
	}
	for _, ev := range liveEvents {
		if !seen[ev] {
			errs = append(errs, fmt.Sprintf("event %s has no clean hook run in any attempt: no turn completed", ev))
		}
	}
	return seen, errs
}

// toolEventGap is the attempt ladder's honest outcome when the model never
// dispatched a tool call: a hard error only under
// HARNESS_MATRIX_REQUIRE_TOOL_EVENT=1, otherwise nothing (the lane prints a
// loud observation row instead; the gate passes on its hard set).
func toolEventGap(gate string) []string {
	if !requireToolEvent() {
		return nil
	}
	return []string{fmt.Sprintf(
		"%s: HARNESS_MATRIX_REQUIRE_TOOL_EVENT=1 but no PreToolUse record: the model never dispatched an MCP tool call",
		gate)}
}

// checkLive is g3-live: one real completing `codex exec --oss` turn through
// the managed-hook overlay, with the real `straza hook --conformance-policy`
// behind the relay. HARD: SessionStart / UserPromptSubmit / Stop / SessionEnd
// each reached the hook with exit 0, every non-enforceable ack is EMPTY
// stdout, and codex's own telemetry logged no hook failure. ATTEMPT (never
// gate-failing by default): a PreToolUse record from the MCP echo server.
// If one arrives its ack is hard-asserted too, since the tier-1 policy
// ALLOWs mcp.call get_* for app github and codex's allow ack is silence.
func checkLive(recordsPath, stderrPath string) (errs []string) {
	recs, err := readRecords(recordsPath)
	if err != nil {
		return []string{err.Error()}
	}
	seen, errs := liveAcks(recs)
	errs = append(errs, failedHookLines(stderrPath)...)
	if !seen["PreToolUse"] {
		errs = append(errs, toolEventGap("g3-live")...)
	}
	return errs
}

// checkE2E is g4-e2e: the same live turn against the full product path (a real
// strazad, a real enrolled NHI, the real non-conformance hook), with the
// server's own read-back as the second half of the evidence.
//
// HARD, client side: the four turn events acked clean and silent, and
// SessionStart printed the ENROLLED context doc (which is what distinguishes
// this from the conformance lane: `straza hook` prints nothing at all there).
// HARD, server side (g4boot's evidence JSON, read over the admin API):
// a session for the NHI on the codex harness, and at least one hash-chained
// audit row for it, proof the whole async path landed and not just that the
// hook exited 0.
//
// ATTEMPT, same ladder as g3-live: a PreToolUse for an mcp__straza__* tool
// must be allowed with exit 0 AND leave a tool.pre audit row whose reason
// carries the gateway deferral (LocalPDP.Decide, normalize.go GatewayProxied).
func checkE2E(recordsPath, stderrPath, evidencePath string) (errs []string) {
	recs, err := readRecords(recordsPath)
	if err != nil {
		return []string{err.Error()}
	}
	seen, errs := liveAcks(recs)
	errs = append(errs, failedHookLines(stderrPath)...)

	// The enrolled SessionStart ack is a real document, not silence: it
	// carries the governance context the model is told to obey.
	for i, r := range recs {
		if eventOf(r) != "SessionStart" {
			continue
		}
		for _, want := range []string{`"hookEventName":"SessionStart"`, "Straza governance is active"} {
			if !strings.Contains(r.ExecStdout, want) {
				errs = append(errs, fmt.Sprintf(
					"record %d (SessionStart): enrolled ack is missing %q; the hook did not run against the enrolled session", i, want))
			}
		}
	}

	raw, err := os.ReadFile(evidencePath) // #nosec G304 -- test-lane file
	if err != nil {
		return append(errs, fmt.Sprintf("server evidence unreadable: %v", err))
	}
	var ev struct {
		User     string `json:"user"`
		Sessions []struct {
			Username string `json:"username"`
			Harness  string `json:"harness"`
			Status   string `json:"status"`
		} `json:"sessions"`
		AuditCounts   map[string]int `json:"auditCounts"`
		ToolDecisions []struct {
			Event, Tool, App, ToolName, Effect, Reason string
		} `json:"toolDecisions"`
		Waited string `json:"waited"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return append(errs, fmt.Sprintf("server evidence is not JSON: %v", err))
	}
	codexSession := false
	for _, s := range ev.Sessions {
		if strings.HasPrefix(s.Harness, "codex") {
			codexSession = true
		}
	}
	if !codexSession {
		errs = append(errs, fmt.Sprintf(
			"no server-side codex session for %s (sessions=%v): SessionStart never checked in", ev.User, ev.Sessions))
	}
	// session.end has no audit CE type of its own (it drains the client
	// spool and returns), so "the turn's audit reached the server" is
	// asserted on the rows a capture-active completing turn MUST produce:
	// the captured prompt AND the captured reply. The reply row is the live
	// pin for codex root reply capture (transcriptDelta over the rollout:
	// codex flushes the assistant line before firing Stop, probed live).
	// Its absence after the poll window is a capture regression or a drain
	// failure, not noise.
	for _, want := range []string{"straza.audit.prompt", "straza.audit.reply"} {
		if ev.AuditCounts[want] == 0 {
			errs = append(errs, fmt.Sprintf(
				"no %s audit row for %s after %s; a capture-active codex turn must land it", want, ev.User, ev.Waited))
		}
	}

	if seen["PreToolUse"] {
		deferred := false
		for _, d := range ev.ToolDecisions {
			if d.Event == "tool.pre" && strings.Contains(d.Reason, "deferred to the gateway") {
				deferred = true
			}
		}
		if !deferred {
			errs = append(errs, fmt.Sprintf(
				"a PreToolUse for a gateway-proxied tool was allowed but no tool.pre audit row carries the gateway deferral (decisions=%v)",
				ev.ToolDecisions))
		}
	} else {
		errs = append(errs, toolEventGap("g4-e2e")...)
	}
	return errs
}

// observeANSI answers whether ANSI colour survives codex's hook
// `systemMessage` rendering, and NEVER votes on the gate. Each input
// is one combined capture (the lane concatenates codex's stdout and stderr
// with `== stdout ==` / `== stderr ==` delimiters) of a turn whose
// SessionStart ack carried ESC[31m…ESC[0m around a marker.
//
// The finding is reported, not asserted: `codex exec` is the HEADLESS
// renderer, and the interactive TUI may treat the same bytes differently,
// which only a manual run in a terminal covers. Cross-harness context for
// whoever reads the row: gemini 0.53.0 sanitizes ANSI by default and ships
// --raw-output precisely to turn that off.
func observeANSI(defaultCapture, forcedCapture string) []string {
	fmt.Println("- ANSI observation (codex hook systemMessage; NEVER pass/fail):")
	for _, c := range []struct{ label, path string }{
		{"codex exec, default renderer (stdout piped ⇒ --color auto)", defaultCapture},
		{"codex exec --color always", forcedCapture},
	} {
		raw, err := os.ReadFile(c.path) // #nosec G304 -- test-lane file
		if err != nil {
			fmt.Printf("    - %s: capture unreadable (%v)\n", c.label, err)
			continue
		}
		// Whether codex ACCEPTED the probe document is a separate fact from
		// whether it displayed it, and both belong in the row: a rejected
		// ack would show up as `hook: SessionStart Failed`.
		verdict := "codex accepted the ack (SessionStart Completed, no Failed)"
		switch {
		case strings.Contains(string(raw), "hook: SessionStart Failed"):
			verdict = "codex REJECTED the ack (hook: SessionStart Failed)"
		case !strings.Contains(string(raw), "SessionStart"):
			verdict = "no SessionStart hook telemetry in this capture"
		}
		fmt.Printf("    - %s: %s [%s]\n", c.label, classifyANSI(string(raw)), verdict)
	}
	fmt.Println("    - caveat: `codex exec` is the HEADLESS renderer, and the interactive TUI may differ, which only a manual terminal run covers.")
	fmt.Println("    - cross-harness: gemini 0.53.0 sanitizes ANSI by default, and its --raw-output flag exists to disable that.")
	return nil
}

// classifyANSI reports which of the outcomes a capture shows, per stream,
// verbatim enough to be actionable, and carries its own positive control.
// "no marker anywhere" is ambiguous on its own: it could mean codex dropped
// the systemMessage, or that the capture is colour-blind. Codex writes its
// own SGR sequences on the same streams, so whether ANY escape survived the
// capture is what separates the two readings, and it is reported alongside.
func classifyANSI(capture string) string {
	const marker = "STRAZA-ANSI-RED"
	var parts []string
	for _, s := range splitCapture(capture) {
		control := "no ESC bytes at all in this stream"
		if strings.Contains(s.body, "\x1b[") {
			control = "control: codex's OWN output on this stream DOES carry SGR escapes"
		}
		switch {
		case strings.Contains(s.body, "\x1b[31m"+marker):
			parts = append(parts, s.name+": RAW ESC SURVIVED (\\x1b[31m"+marker+"\\x1b[0m byte-for-byte)")
		case strings.Contains(s.body, marker) && strings.Contains(s.body, "plain-tail"):
			parts = append(parts, s.name+": STRIPPED ("+marker+" plain-tail present, escapes removed; "+control+")")
		case strings.Contains(s.body, marker):
			parts = append(parts, s.name+": PARTIAL (marker without the plain tail)")
		case strings.TrimSpace(s.body) == "":
			parts = append(parts, s.name+": EMPTY (nothing written to this stream)")
		default:
			parts = append(parts, s.name+": systemMessage NOT SURFACED (no marker anywhere; "+control+")")
		}
	}
	if len(parts) == 0 {
		return "capture empty"
	}
	return strings.Join(parts, "; ")
}

type captureSection struct{ name, body string }

// splitCapture splits the lane's combined capture back into its streams. An
// undelimited file is reported whole, so the observation degrades to "one
// stream" instead of failing.
func splitCapture(capture string) []captureSection {
	const so, se = "== stdout ==\n", "== stderr ==\n"
	i, j := strings.Index(capture, so), strings.Index(capture, se)
	if i < 0 || j < 0 || j < i {
		return []captureSection{{"capture", capture}}
	}
	return []captureSection{
		{"stdout", capture[i+len(so) : j]},
		{"stderr", capture[j+len(se):]},
	}
}

// failedHookLines surfaces codex's own per-hook failure telemetry. The
// harness deciding a hook failed is the ground truth this lane exists to
// read; any `hook: <Event> Failed` line is a red gate.
func failedHookLines(stderrPath string) (errs []string) {
	raw, err := os.ReadFile(stderrPath) // #nosec G304 -- test-lane file
	if err != nil {
		return []string{fmt.Sprintf("harness stderr unreadable: %v", err)}
	}
	for _, m := range regexp.MustCompile(`hook: \S+ Failed`).FindAllString(string(raw), -1) {
		errs = append(errs, "codex reported: "+m)
	}
	return errs
}
