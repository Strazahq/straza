// gemini.go holds the gemini lane's gates. Gemini has NO per-hook telemetry
// surface (no codex-style `hook: <Event> Failed` line, no headless doctor
// row), so every verdict is read from the sentinel records and from gemini's
// own user-visible stderr. Two stderr tokens are the lane's positive controls:
//
//	"Hook system message:"      hook stdout was NOT the structured dialect and
//	                            got surfaced verbatim instead of parsed.
//	"Agent execution blocked:"  a hook returned {"decision":"deny",...}, proof
//	                            gemini PARSES and HONORS the encoder's dialect,
//	                            asserted by gemini-control so that
//	                            gemini-output's silence counts as evidence.
//
// Unlike codex, gemini does not strict-parse hook stdout: unknown output
// degrades to a system message, not a rejection, so the honest claim is that
// gemini parsed our ack and ran on, not that gemini validated our schema.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// geminiKeyFreeEvents are the gemini events a fake-key `gemini -p` turn fires:
// the session opens and the prompt is submitted, then the model call dies on
// auth. BeforeTool and AfterAgent need a completing turn (verified on
// gemini-cli 0.53.0), unreachable on the auth-death cases, so the spawn gate
// reports them as not-fired rows; the gemini-live-* gates reach them key-free
// by serving the model traffic themselves (geministub) and DO assert them.
var geminiKeyFreeEvents = []string{"SessionStart", "BeforeAgent"}

// geminiRosterEvents is the installer's gemini roster (install.go: session,
// pre-tool, prompt, end). Kept here as the SHAPE the static gate asserts; the
// live gates never assume an event fires.
var geminiRosterEvents = []string{"SessionStart", "BeforeTool", "BeforeAgent", "AfterAgent"}

// geminiPinnedKeys are the stdin payload keys adapters/gemini.yaml pins from
// the v0.50.0 source read. Missing one is drift that breaks
// Normalize and fails the gate; EXTRA keys are reported as vendor drift rows,
// never failed.
var geminiPinnedKeys = map[string][]string{
	"":            {"session_id", "transcript_path", "cwd", "hook_event_name", "timestamp"},
	"BeforeAgent": {"prompt"},
	// Live-pinned by the gemini-live-* gates on a completing 0.53.0 turn:
	// the keys Normalize reads (adapters/gemini.yaml
	// promptResponse / toolName / toolInput). Extra live keys (prompt +
	// stop_hook_active on AfterAgent) stay drift rows, not pins.
	"AfterAgent": {"prompt_response"},
	"BeforeTool": {"tool_name", "tool_input"},
}

func init() {
	gates["gemini-floor"] = func(o *opts) []string { return checkGeminiFloor(o.version) }
	gates["gemini-static"] = func(o *opts) []string { return checkGeminiStatic(o.req, o.hooksJSON) }
	gates["gemini-spawn"] = func(o *opts) []string { return checkGeminiSpawn(o.records, o.hookBin) }
	gates["gemini-output"] = func(o *opts) []string { return checkGeminiOutput(o.records, o.harnessErr) }
	// The positive control for gemini-output: a hook that DENIES must visibly
	// stop the turn. Without it, "gemini printed no complaint" would be
	// indistinguishable from "gemini never read hook stdout at all".
	gates["gemini-control"] = func(o *opts) []string { return checkGeminiControl(o.harnessErr) }
	// Trust battery. Each is one marker-sensed run; the lane names the claim.
	gates["gemini-nofire"] = func(o *opts) []string { return checkGeminiNoFire(o.records) }
	gates["gemini-fire"] = func(o *opts) []string { return checkGeminiFire(o.records, o.hookBin) }
	// Completing-turn gates (geministub behind GOOGLE_GEMINI_BASE_URL): the
	// events every fake-key case is blind to, asserted instead of observed.
	gates["gemini-live-reply"] = func(o *opts) []string { return checkGeminiLive(o.records, o.hookBin, o.want, false) }
	gates["gemini-live-tool"] = func(o *opts) []string { return checkGeminiLive(o.records, o.hookBin, o.want, true) }
	gates["gemini-sysfire"] = func(o *opts) []string { return checkGeminiFire(o.records, o.hookBin) }
	gates["gemini-untrusted"] = func(o *opts) []string { return checkGeminiUntrusted(o.records, o.harnessErr) }
}

// checkGeminiFloor asserts the gemini under test is at least 0.26.0, the
// first release whose settings schema carries `hooksConfig` (derived
// from the vendor's own schemas/settings.schema.json at release
// tags v0.15.0…v0.26.0: `hooks`/`BeforeTool` exist from ≤0.15, `hooksConfig`
// appears first at v0.26.0). Below that floor the whole kill-switch battery
// (C5/C6, and with it the managed install's `hooksConfig.enabled = true`
// system pin) is vacuous: gemini has no such setting to honor or ignore.
func checkGeminiFloor(version string) []string {
	m := regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`).FindStringSubmatch(version)
	if m == nil {
		return []string{fmt.Sprintf("cannot parse gemini version from %q", version)}
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major == 0 && minor < 26 {
		return []string{fmt.Sprintf("gemini %s is below the 0.26 hooksConfig floor; the kill-switch/system-pin gates would be vacuous", m[0])}
	}
	fmt.Printf("gemini version %s ≥ 0.26 hooksConfig floor\n", m[0])
	return nil
}

// checkGeminiStatic asserts the two settings.json files the REAL installer
// wrote are the shapes gemini's loader accepts, and that the two scopes differ
// in exactly the one documented way. userPath is written by `straza install
// gemini` (hooks + MCP registration); managedPath by InstallManagedHooks
// (hooks + the hooksConfig.enabled pin). Parse ACCEPTANCE by the real binary
// is what gemini-spawn proves; this catches the cheap breakages.
func checkGeminiStatic(userPath, managedPath string) (errs []string) {
	user, uerrs := readGeminiHooks(userPath, "user settings.json")
	errs = append(errs, uerrs...)
	managed, merrs := readGeminiHooks(managedPath, "managed settings.json")
	errs = append(errs, merrs...)

	// The MCP registration lives in the SAME settings.json as the hooks for
	// gemini (installmcp.go: MCPConfigPath → SettingsPath), user scope only,
	// gemini has no vendor-documented managed MCP layer.
	if user != nil {
		servers, ok := user["mcpServers"].(map[string]any)
		if !ok {
			errs = append(errs, "user settings.json has no mcpServers object; `straza install gemini` must register the MCP server beside the hooks")
		} else if entry, ok := servers["straza"].(map[string]any); !ok {
			errs = append(errs, `user settings.json mcpServers has no "straza" entry`)
		} else if _, ok := entry["command"].(string); !ok {
			errs = append(errs, "user settings.json mcpServers.straza has no command")
		}
		// The hooksConfig pin is a MANAGED-scope decision (install.go:262):
		// writing it user-scope would pin a value the operator owns and would
		// not survive gemini's own precedence anyway.
		if _, ok := user["hooksConfig"]; ok {
			errs = append(errs, "user settings.json carries hooksConfig; the enabled pin is managed-scope only (install.go)")
		}
	}
	if managed != nil {
		hc, ok := managed["hooksConfig"].(map[string]any)
		if !ok {
			errs = append(errs, "managed settings.json has no hooksConfig object; the system-scope kill-switch pin is missing")
		} else if enabled, _ := hc["enabled"].(bool); !enabled {
			errs = append(errs, "managed settings.json does not pin hooksConfig.enabled = true (the system pin)")
		}
	}
	return errs
}

// readGeminiHooks loads one settings.json and asserts the installer's hook
// shape: every rostered event present, each entry a {hooks:[{type:"command",
// command:"<bin> hook --harness gemini"}]} group, and matcher "*" on
// BeforeTool ONLY (install.go writes it there and nowhere else). Returns the
// parsed document so the caller can assert scope-specific keys.
func readGeminiHooks(path, label string) (map[string]any, []string) {
	var errs []string
	raw, err := os.ReadFile(path) // #nosec G304 -- test-lane file
	if err != nil {
		return nil, []string{fmt.Sprintf("%s unreadable: %v", label, err)}
	}
	if strings.HasPrefix(string(raw), "\xef\xbb\xbf") {
		errs = append(errs, label+" carries a UTF-8 BOM")
	}
	if strings.Contains(string(raw), "\r") {
		errs = append(errs, label+" carries CR bytes on a Unix render")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, append(errs, fmt.Sprintf("%s is not valid JSON: %v", label, err))
	}
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok || len(hooks) == 0 {
		return doc, append(errs, label+` has no populated top-level "hooks" object`)
	}
	for _, ev := range geminiRosterEvents {
		groups, ok := hooks[ev].([]any)
		if !ok || len(groups) == 0 {
			errs = append(errs, fmt.Sprintf("%s missing event %s", label, ev))
			continue
		}
		found := false
		for _, g := range groups {
			group, ok := g.(map[string]any)
			if !ok {
				errs = append(errs, fmt.Sprintf("%s %s: hook group is not an object", label, ev))
				continue
			}
			matcher, _ := group["matcher"].(string)
			switch {
			case ev == "BeforeTool" && matcher != "*":
				errs = append(errs, fmt.Sprintf("%s BeforeTool matcher is %q, want \"*\"", label, matcher))
			case ev != "BeforeTool" && matcher != "":
				errs = append(errs, fmt.Sprintf("%s %s carries matcher %q; install.go writes a matcher on BeforeTool only", label, ev, matcher))
			}
			inner, ok := group["hooks"].([]any)
			if !ok {
				errs = append(errs, fmt.Sprintf("%s %s: group has no hooks array", label, ev))
				continue
			}
			for _, h := range inner {
				hm, ok := h.(map[string]any)
				if !ok {
					continue
				}
				typ, _ := hm["type"].(string)
				cmd, _ := hm["command"].(string)
				if typ != "command" {
					errs = append(errs, fmt.Sprintf("%s %s: hook type is %q, want \"command\"", label, ev, typ))
				}
				if !strings.HasSuffix(cmd, "hook --harness gemini") {
					errs = append(errs, fmt.Sprintf("%s %s: command does not end in the straza gemini hook marker: %q", label, ev, cmd))
					continue
				}
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s %s: no straza hook command", label, ev))
		}
	}
	for ev := range hooks {
		known := false
		for _, r := range geminiRosterEvents {
			if r == ev {
				known = true
			}
		}
		if !known {
			errs = append(errs, fmt.Sprintf("%s registers %s, which is not in the installer roster", label, ev))
		}
	}
	return doc, errs
}

// checkGeminiSpawn asserts gemini EXECUTED the installer's hook command with
// intact argv and a payload straza can normalize, and reports the payload key
// inventory per event so a vendor key rename shows up as a drift row before it
// shows up as a silently ungoverned field.
func checkGeminiSpawn(recordsPath, hookBin string) (errs []string) {
	recs, err := readRecords(recordsPath)
	if err != nil {
		return []string{err.Error()}
	}
	seen := map[string]bool{}
	for i, r := range recs {
		if len(r.Argv) != 4 || r.Argv[0] != hookBin || r.Argv[1] != "hook" || r.Argv[2] != "--harness" || r.Argv[3] != "gemini" {
			errs = append(errs, fmt.Sprintf("record %d: argv not intact: %q", i, r.Argv))
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(r.Stdin), &payload); err != nil {
			errs = append(errs, fmt.Sprintf("record %d: stdin is not JSON: %v", i, err))
			continue
		}
		ev, _ := payload["hook_event_name"].(string)
		if ev == "" {
			errs = append(errs, fmt.Sprintf("record %d: payload has no hook_event_name", i))
			continue
		}
		seen[ev] = true
		errs = append(errs, geminiPayloadRows(ev, payload)...)
	}
	for _, ev := range geminiKeyFreeEvents {
		if !seen[ev] {
			errs = append(errs, fmt.Sprintf("event %s never reached the hook", ev))
		}
	}
	for _, ev := range geminiRosterEvents {
		if !seen[ev] {
			fmt.Printf("not fired (key-gated, needs a completing turn): %s\n", ev)
		}
	}
	return errs
}

// checkGeminiLive asserts a COMPLETING key-free turn (geministub serving the
// model traffic behind GOOGLE_GEMINI_BASE_URL) fired the events a fake-key
// auth-death run never can: SessionStart, BeforeAgent, and AfterAgent,
// gemini's ONLY reply lane, whose prompt_response must equal the stub's
// scripted reply byte-for-byte (the standing live pin for
// adapters/gemini.yaml's promptResponse mapping). With wantTool the turn was
// scripted as a functionCall round-trip and BeforeTool must fire too: a
// wrong tool param name dies in the CLI's own validator BEFORE the hook, so
// this assert is what notices that silent loss. Payloads go through the same
// pinned-key/drift inventory as the spawn gate.
func checkGeminiLive(recordsPath, hookBin, want string, wantTool bool) (errs []string) {
	recs, err := readRecords(recordsPath)
	if err != nil {
		return []string{err.Error()}
	}
	seen := map[string]map[string]any{}
	for i, r := range recs {
		if len(r.Argv) != 4 || r.Argv[0] != hookBin || r.Argv[1] != "hook" || r.Argv[2] != "--harness" || r.Argv[3] != "gemini" {
			errs = append(errs, fmt.Sprintf("record %d: argv not intact: %q", i, r.Argv))
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(r.Stdin), &payload); err != nil {
			errs = append(errs, fmt.Sprintf("record %d: stdin is not JSON: %v", i, err))
			continue
		}
		ev, _ := payload["hook_event_name"].(string)
		if ev == "" {
			errs = append(errs, fmt.Sprintf("record %d: payload has no hook_event_name", i))
			continue
		}
		seen[ev] = payload
		errs = append(errs, geminiPayloadRows(ev, payload)...)
	}

	required := []string{"SessionStart", "BeforeAgent", "AfterAgent"}
	if wantTool {
		required = append(required, "BeforeTool")
	}
	for _, ev := range required {
		if seen[ev] == nil {
			errs = append(errs, fmt.Sprintf("event %s never fired on a COMPLETING turn: the live claim this gate exists for", ev))
		}
	}
	if p := seen["AfterAgent"]; p != nil && want != "" {
		if got, _ := p["prompt_response"].(string); got != want {
			errs = append(errs, fmt.Sprintf("AfterAgent prompt_response = %q, want the scripted reply %q; the reply lane is not carrying the model's text", got, want))
		}
	}
	return errs
}

// geminiPayloadRows checks one payload against the adapter's pinned keys and
// prints the drift inventory. A MISSING pinned key is an error (Normalize
// reads it); an EXTRA key is a printed drift row, since the adapter is never
// "fixed" to an unverified shape from inside a gate.
func geminiPayloadRows(event string, payload map[string]any) (errs []string) {
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("payload %s: keys=%s\n", event, strings.Join(keys, ","))

	pinned := map[string]bool{}
	for _, k := range append(append([]string{}, geminiPinnedKeys[""]...), geminiPinnedKeys[event]...) {
		pinned[k] = true
		if _, ok := payload[k]; !ok {
			errs = append(errs, fmt.Sprintf("payload %s: adapter-pinned key %q is MISSING (adapters/gemini.yaml drift)", event, k))
		}
	}
	var extra []string
	for _, k := range keys {
		if !pinned[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		fmt.Printf("VENDOR DRIFT %s: keys not pinned by adapters/gemini.yaml (v0.50 read): %s\n", event, strings.Join(extra, ","))
	}
	return errs
}

// checkGeminiOutput asserts the real `straza hook` ran behind the relay with
// exit 0 on every fired event, printed EXACTLY the gemini-dialect ack the
// encoder promises ({"decision":"allow"}, hook.go encodeAllow/encodeSessionStart),
// and that gemini neither surfaced it as raw text nor blocked the turn over
// it. The "no complaint" half is only evidence because gemini-control proves
// gemini reads this channel at all.
func checkGeminiOutput(recordsPath, stderrPath string) (errs []string) {
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
			errs = append(errs, fmt.Sprintf("record %d (%s): straza hook exited %d (stderr: %s)", i, ev, *r.ExecExit, strings.TrimSpace(r.ExecStderr)))
			continue
		}
		var ack map[string]any
		if err := json.Unmarshal([]byte(r.ExecStdout), &ack); err != nil {
			errs = append(errs, fmt.Sprintf("record %d (%s): hook stdout is not JSON (%v): %q", i, ev, err, r.ExecStdout))
			continue
		}
		if d, _ := ack["decision"].(string); d != "allow" {
			errs = append(errs, fmt.Sprintf("record %d (%s): ack is not the gemini allow shape: %q", i, ev, r.ExecStdout))
			continue
		}
		fmt.Printf("ack %s: %s\n", ev, strings.TrimSpace(r.ExecStdout))
		seen[ev] = true
	}
	for _, ev := range geminiKeyFreeEvents {
		if !seen[ev] {
			errs = append(errs, fmt.Sprintf("event %s has no clean hook run", ev))
		}
	}
	return append(errs, geminiStderrComplaints(stderrPath)...)
}

// geminiStderrComplaints surfaces the two live-established rejection tokens.
// "Hook system message:" means gemini did NOT recognize the hook's stdout as
// the structured dialect and relayed it as text; "Agent execution blocked:"
// means a hook decision stopped the turn. Neither is acceptable from a
// straza allow ack.
func geminiStderrComplaints(stderrPath string) (errs []string) {
	raw, err := os.ReadFile(stderrPath) // #nosec G304 -- test-lane file
	if err != nil {
		return []string{fmt.Sprintf("harness stderr unreadable: %v", err)}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "Hook system message:") || strings.Contains(line, "Agent execution blocked:") {
			errs = append(errs, "gemini reported: "+strings.TrimSpace(line))
		}
	}
	return errs
}

// checkGeminiControl is the positive control: the relayed hook denied, so
// gemini MUST have blocked the turn. If this passes, gemini demonstrably
// parses hook stdout in the dialect straza writes, which is what makes
// gemini-output's silence meaningful.
func checkGeminiControl(stderrPath string) []string {
	raw, err := os.ReadFile(stderrPath) // #nosec G304 -- test-lane file
	if err != nil {
		return []string{fmt.Sprintf("harness stderr unreadable: %v", err)}
	}
	if !strings.Contains(string(raw), "Agent execution blocked:") {
		return []string{`positive control FAILED: a hook returning {"decision":"deny"} did not block the turn; gemini is not honoring hook stdout on this build, so gemini-output's "no complaint" proves nothing (vendor drift)`}
	}
	fmt.Println("control: gemini honored a hook deny (Agent execution blocked); the hook-output channel is live")
	return nil
}

// checkGeminiNoFire asserts NOTHING reached the hook, the shape of every
// "governance is off" claim in the trust battery (kill switch, system-settings
// redirect). An absent records file is the expected evidence.
func checkGeminiNoFire(recordsPath string) []string {
	if _, err := os.Stat(recordsPath); os.IsNotExist(err) {
		fmt.Println("no hook ran (no sentinel records file), as claimed")
		return nil
	}
	recs, err := readRecords(recordsPath)
	if err != nil {
		return []string{err.Error()}
	}
	if len(recs) == 0 {
		fmt.Println("no hook ran (empty sentinel records), as claimed")
		return nil
	}
	var got []string
	for _, r := range recs {
		got = append(got, eventOf(r))
	}
	return []string{fmt.Sprintf("expected NO hook to run, but %d did: %s", len(recs), strings.Join(got, ","))}
}

// checkGeminiFire asserts hooks DID reach the sentinel, and that at least one
// record came from hookBin specifically. The C6 gate passes the SYSTEM-scope
// sentinel copy as hookBin, which is how it proves the system settings file
// (not the user one) is what put governance back on.
func checkGeminiFire(recordsPath, hookBin string) (errs []string) {
	recs, err := readRecords(recordsPath)
	if err != nil {
		return []string{err.Error()}
	}
	if len(recs) == 0 {
		return []string{"expected hooks to run, but the sentinel recorded nothing"}
	}
	scopes := map[string]int{}
	fromBin := 0
	for i, r := range recs {
		if len(r.Argv) == 0 {
			errs = append(errs, fmt.Sprintf("record %d has no argv", i))
			continue
		}
		if eventOf(r) == "" {
			errs = append(errs, fmt.Sprintf("record %d: stdin is not JSON or has no hook_event_name", i))
		}
		scopes[r.Argv[0]]++
		if r.Argv[0] == hookBin {
			fromBin++
		}
	}
	bins := make([]string, 0, len(scopes))
	for b := range scopes {
		bins = append(bins, fmt.Sprintf("%s×%d", b[strings.LastIndex(b, "/")+1:], scopes[b]))
	}
	sort.Strings(bins)
	fmt.Printf("hooks ran: %d record(s) from %s\n", len(recs), strings.Join(bins, " "))
	if fromBin == 0 {
		errs = append(errs, fmt.Sprintf("no record came from the expected hook binary %s (got %s)", hookBin, strings.Join(bins, " ")))
	}
	return errs
}

// checkGeminiUntrusted pins what gemini ACTUALLY does headless in an untrusted
// folder with folder-trust enabled: it refuses to start at all, loudly, naming
// the two bypasses. It does NOT run a hook-less "safe mode" session. The gate
// asserts both halves (nothing ran AND gemini said why), because the failure
// mode straza cares about is the silent one: if a future release starts
// running the session with hooks quietly skipped, this gate goes red and the
// refutation gets re-examined instead of rotting in a comment.
func checkGeminiUntrusted(recordsPath, stderrPath string) []string {
	if errs := checkGeminiNoFire(recordsPath); len(errs) > 0 {
		return errs
	}
	raw, err := os.ReadFile(stderrPath) // #nosec G304 -- test-lane file
	if err != nil {
		return []string{fmt.Sprintf("harness stderr unreadable: %v", err)}
	}
	if !strings.Contains(string(raw), "not running in a trusted directory") {
		return []string{"gemini ran (or died) without the untrusted-directory refusal on stderr: the folder-trust behavior this lane pins has changed; re-read the live verdict in geminitrust.go"}
	}
	fmt.Println("gemini refused to start: \"not running in a trusted directory\" (LOUD, not a silent hook skip)")
	return nil
}
