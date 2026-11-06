package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

func runHookTest(t *testing.T, decider Decider, env []string, payload string) (stdout, stderr string, denyCode int) {
	t.Helper()
	var out, errb bytes.Buffer
	err := runHookWith(HookIO{
		Stdin:   strings.NewReader(payload),
		Stdout:  &out,
		Stderr:  &errb,
		Environ: func() []string { return env },
	}, decider)
	if err != nil {
		if c, ok := err.(interface{ Code() int }); ok {
			denyCode = c.Code()
		} else {
			t.Fatalf("runHook: %v", err)
		}
	}
	return out.String(), errb.String(), denyCode
}

// denyShell denies any shell.exec whose command contains "rm -rf".
type denyShell struct{}

func (denyShell) Decide(n Normalized) policy.Decision {
	if n.Event.Tool == policy.ToolShellExec && strings.Contains(n.Event.Command, "rm -rf") {
		return policy.Decision{Effect: policy.EffectDeny, Reason: "Straza: destructive delete blocked"}
	}
	return policy.Decision{Effect: policy.EffectAllow}
}

func TestHookAllowClaude(t *testing.T) {
	stdout, _, code := runHookTest(t, allowAll{},
		[]string{"CLAUDECODE=1"},
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`)
	if code != 0 {
		t.Fatalf("allow should not set deny code, got %d", code)
	}
	var resp claudeResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout not JSON: %v: %s", err, stdout)
	}
	if resp.HookSpecificOutput.PermissionDecision != "allow" {
		t.Errorf("decision = %q", resp.HookSpecificOutput.PermissionDecision)
	}
}

func TestHookDenyClaudeExit2(t *testing.T) {
	stdout, stderr, code := runHookTest(t, denyShell{},
		[]string{"CLAUDECODE=1"},
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`)
	if code != 2 {
		t.Fatalf("deny should exit 2, got %d", code)
	}
	var resp claudeResponse
	_ = json.Unmarshal([]byte(stdout), &resp)
	if resp.HookSpecificOutput.PermissionDecision != "deny" ||
		!strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "destructive") {
		t.Errorf("deny response = %+v", resp)
	}
	if !strings.Contains(stderr, "destructive") {
		t.Errorf("reason should also go to stderr: %q", stderr)
	}
}

func TestHookGeminiStrictJSON(t *testing.T) {
	// Gemini: strict JSON on stdout, deny does NOT use exit 2.
	stdout, _, code := runHookTest(t, denyShell{},
		[]string{"GEMINI_CLI=1"},
		`{"event":"BeforeTool","toolName":"run_shell_command","args":{"command":"rm -rf /"}}`)
	if code != 0 {
		t.Errorf("gemini deny should not use exit code, got %d", code)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("gemini stdout must be strict JSON: %v: %q", err, stdout)
	}
	if resp["decision"] != "deny" {
		t.Errorf("gemini decision = %v", resp["decision"])
	}
}

func TestHookNonEnforceableAlwaysAllows(t *testing.T) {
	// A session.start event can't block even if the decider denies.
	_, _, code := runHookTest(t, denyShell{},
		[]string{"CLAUDECODE=1"},
		`{"hook_event_name":"SessionStart","cwd":"/w"}`)
	if code != 0 {
		t.Errorf("non-enforceable event blocked with code %d", code)
	}
}

// TestHookEmitsInvokedEventName pins the claude-code hook response contract on
// both halves:
//
//   - ENFORCEABLE events (tool.pre / permission.request) carry a document, and
//     its hookEventName MUST be the lifecycle event the hook was invoked for.
//     A hardcoded "PreToolUse" gets UserPromptSubmit and SessionEnd rejected
//     with "expected 'UserPromptSubmit' but got 'PreToolUse'", which breaks
//     capture.
//   - NON-ENFORCEABLE events carry NOTHING. Claude validates hook stdout
//     against a per-event hookSpecificOutput schema whose union has no
//     SessionEnd variant, so an echo there is rejected on every governed
//     session close with "Hook JSON output validation failed" plus
//     "(root): Invalid input". This is verified against claude-code 2.1.220
//     in the harness-matrix claude-output gate. Empty stdout is accepted on
//     every event that lane can fire. Pinned here so neither half regresses.
func TestHookEmitsInvokedEventName(t *testing.T) {
	cases := []struct {
		name       string
		env        []string
		payload    string
		wantEvent  string // "" = no document at all (non-enforceable ack)
		wantDecide string
		wantCode   int
	}{
		{"session start", []string{"CLAUDECODE=1"}, `{"hook_event_name":"SessionStart","cwd":"/w"}`, "", "", 0},
		{"prompt submit", []string{"CLAUDECODE=1"}, `{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"hi"}`, "", "", 0},
		{"session end", []string{"CLAUDECODE=1"}, `{"hook_event_name":"SessionEnd","session_id":"s"}`, "", "", 0},
		{"pre tool allow", []string{"CLAUDECODE=1"}, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`, "PreToolUse", "allow", 0},
		{"pre tool deny", []string{"CLAUDECODE=1"}, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`, "PreToolUse", "deny", 2},
		{"permission request", []string{"CLAUDECODE=1"}, `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`, "PermissionRequest", "allow", 0},
		// codex is deliberately absent: it acks with SILENCE on every event,
		// enforceable included (hook_codex_encode_test.go pins that contract).
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, code := runHookTest(t, denyShell{}, tc.env, tc.payload)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (stdout %q)", code, tc.wantCode, stdout)
			}
			if tc.wantEvent == "" {
				if stdout != "" {
					t.Fatalf("non-enforceable %s ack must be empty (claude validates stdout per event and has no schema for this one), got %q", tc.name, stdout)
				}
				return
			}
			var resp claudeResponse
			if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
				t.Fatalf("stdout not claude JSON: %v: %s", err, stdout)
			}
			if got := resp.HookSpecificOutput.HookEventName; got != tc.wantEvent {
				t.Errorf("hookEventName = %q, want %q", got, tc.wantEvent)
			}
			if got := resp.HookSpecificOutput.PermissionDecision; got != tc.wantDecide {
				t.Errorf("permissionDecision = %q, want %q", got, tc.wantDecide)
			}
		})
	}
}

func TestHookFailClosedOnBadPayload(t *testing.T) {
	// Unparseable payload on a governed hook: fail closed (deny/exit 2).
	_, stderr, code := runHookTest(t, allowAll{}, []string{"CLAUDECODE=1"}, `not json`)
	if code != 2 {
		t.Errorf("bad payload should fail closed with exit 2, got %d", code)
	}
	if !strings.Contains(stderr, "Straza") {
		t.Errorf("expected actionable Straza error: %q", stderr)
	}
}

func TestHookUndetectedHarnessFailsClosed(t *testing.T) {
	_, _, code := runHookTest(t, allowAll{}, nil, `{"something":"unknown"}`)
	if code != 2 {
		t.Errorf("undetected harness should fail closed, got %d", code)
	}
}

// TestGovernanceBannerAndUserLine pins the agent's session-start context
// word for word and the person's one line. The context names the user and
// no roles, server, snapshot or attestation, which only the person's line
// shows. It leaves the next step after a denial to the denial's reason and
// forbids no retry outright.
func TestGovernanceBannerAndUserLine(t *testing.T) {
	info := SessionInfo{
		User: "bob", Roles: []string{"dev"},
		SnapshotID:  "81e69834b9d78b8037ddc64150e79bad",
		Attestation: "none", ServerURL: "http://localhost:8420",
	}
	const rules = `Straza governs this session as "bob". ` +
		"Every tool call is checked against policy and recorded in the audit log. " +
		"When a call is denied, tell the user what the reason says, then do what it says next: " +
		"wait for a person's approval, change the call, or try once more later. " +
		"A denial that says none of these is a policy decision, and the same call will be denied again. " +
		"Do not try to reach the denied result another way."
	if got := StartText("bob", false); got != rules {
		t.Fatalf("StartText =\n%q\nwant\n%q", got, rules)
	}
	whoami := rules + " To see what this session may do, call the straza__whoami tool of the straza MCP server."
	if got := StartText("bob", true); got != whoami {
		t.Fatalf("StartText with whoami =\n%q\nwant\n%q", got, whoami)
	}
	banner := GovernanceBanner(info)
	line := GovernanceUserLine(info)
	for _, want := range []string{"bob", "dev", "http://localhost:8420", "81e69834b9d7", "attestation none", "Straza governance active"} {
		if !strings.Contains(line, want) {
			t.Errorf("user line missing %q: %s", want, line)
		}
	}

	// claude-code SessionStart response carries BOTH the model context and the
	// human-visible systemMessage.
	var out bytes.Buffer
	hio := HookIO{Stdout: &out, Stderr: io.Discard}
	if err := encodeSessionStart(hio, "claude-code", "SessionStart", banner, line); err != nil {
		t.Fatalf("encodeSessionStart: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if resp["systemMessage"] != line {
		t.Errorf("systemMessage = %v", resp["systemMessage"])
	}
	hso, _ := resp["hookSpecificOutput"].(map[string]any)
	if hso == nil || hso["additionalContext"] != banner {
		t.Errorf("additionalContext missing or wrong")
	}

	// Recording is named in the console's words, and only when policy
	// records the session.
	for _, tc := range []struct {
		name       string
		capture    policy.CaptureDirective
		wantBanner string
		wantLine   string
	}{
		{"off", policy.CaptureDirective{}, rules, ""},
		{"word for word", policy.CaptureDirective{Conversations: true, Mode: policy.CaptureModeVerbatim},
			rules + " By policy, the prompts and responses of this session are recorded word for word in the audit log.",
			" · conversations recorded word for word"},
		{"secrets masked", policy.CaptureDirective{Conversations: true, Mode: policy.CaptureModeRedact},
			rules + " By policy, the prompts and responses of this session are recorded with secrets masked in the audit log.",
			" · conversations recorded with secrets masked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := info
			rec.Capture = tc.capture
			banner, line := GovernanceBanner(rec), GovernanceUserLine(rec)
			if banner != tc.wantBanner {
				t.Errorf("banner =\n%q\nwant\n%q", banner, tc.wantBanner)
			}
			if !strings.HasSuffix(line, tc.wantLine) {
				t.Errorf("line %q does not end with %q", line, tc.wantLine)
			}
			if tc.wantLine == "" && strings.Contains(line, "recorded") {
				t.Errorf("line %q speaks of recording for a session policy does not record", line)
			}
			for _, retired := range []string{"capture", "ACTIVE", "verbatim", "redact"} {
				if strings.Contains(banner, retired) || strings.Contains(line, retired) {
					t.Errorf("banner %q or line %q still says %q", banner, line, retired)
				}
			}
		})
	}
}

// TestGovernanceBannerServerText pins whose start text the agent reads:
// the server's when it sent one, the client's own when it sent none, and no
// governance paragraph when it sent an empty one. The recording sentence
// follows policy in every case.
func TestGovernanceBannerServerText(t *testing.T) {
	recorded := "By policy, the prompts and responses of this session are recorded word for word in the audit log."
	on := policy.CaptureDirective{Conversations: true, Mode: policy.CaptureModeVerbatim}
	custom, empty := "Ask the platform team before you touch production.", ""
	for _, tc := range []struct {
		name    string
		server  *string
		capture policy.CaptureDirective
		want    string
	}{
		{"an older server sends none", nil, policy.CaptureDirective{}, StartText("dana", false)},
		{"an older server sends none, recorded", nil, on, StartText("dana", false) + " " + recorded},
		{"the server's text", &custom, policy.CaptureDirective{}, custom},
		{"the server's text, recorded", &custom, on, custom + " " + recorded},
		{"the server turned it off", &empty, policy.CaptureDirective{}, ""},
		{"the server turned it off, recorded", &empty, on, recorded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := GovernanceBanner(SessionInfo{User: "dana", StartText: tc.server, Capture: tc.capture})
			if got != tc.want {
				t.Errorf("banner =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestClientSentencesSayAIAgent pins the doctor's enrollment row for an
// agent to the product's words: an AI agent, and never NHI or IdP.
func TestClientSentencesSayAIAgent(t *testing.T) {
	svc := newFakeService(t)
	svc.status.Enrollment = &localapi.Enrollment{ServerURL: "http://127.0.0.1:1", Kind: localapi.KindAgent, Username: "ci-bot", KeyThumbprint: "thumb"}
	var row string
	for _, c := range Doctor(context.Background(), svc.store(t), NewServiceClient()) {
		if c.Name == "enrollment" {
			row = c.Detail
		}
	}
	if !strings.Contains(row, "ci-bot (AI agent, no device)") {
		t.Errorf("doctor enrollment row = %q, want it to say ci-bot (AI agent, no device)", row)
	}
	for _, retired := range []string{"NHI", "IdP"} {
		if strings.Contains(row, retired) {
			t.Errorf("doctor enrollment row = %q still says %q", row, retired)
		}
	}
}
