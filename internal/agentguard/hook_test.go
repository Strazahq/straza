package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/oidcflow"
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

func TestGovernanceBannerAndUserLine(t *testing.T) {
	info := SessionInfo{
		User: "bob", Roles: []string{"dev"},
		SnapshotID:  "81e69834b9d78b8037ddc64150e79bad",
		Attestation: "none", ServerURL: "http://localhost:8420",
	}
	banner := GovernanceBanner(info)
	for _, want := range []string{`"bob"`, "roles: dev", "http://localhost:8420", "81e69834b9d7", "Straza governance is active", "do not retry"} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner missing %q:\n%s", want, banner)
		}
	}
	line := GovernanceUserLine(info)
	for _, want := range []string{"bob", "dev", "81e69834b9d7", "Straza governance active"} {
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
		{"off", policy.CaptureDirective{}, "", ""},
		{"word for word", policy.CaptureDirective{Conversations: true, Mode: policy.CaptureModeVerbatim},
			"Recording is on: by policy, the prompts and responses of this session are recorded word for word to the audit system.",
			" · conversations recorded word for word"},
		{"secrets masked", policy.CaptureDirective{Conversations: true, Mode: policy.CaptureModeRedact},
			"Recording is on: by policy, the prompts and responses of this session are recorded with secrets masked to the audit system.",
			" · conversations recorded with secrets masked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := info
			rec.Capture = tc.capture
			banner, line := GovernanceBanner(rec), GovernanceUserLine(rec)
			if !strings.HasSuffix(banner, tc.wantBanner) || !strings.HasSuffix(line, tc.wantLine) {
				t.Errorf("banner %q or line %q does not end with %q and %q", banner, line, tc.wantBanner, tc.wantLine)
			}
			if tc.wantBanner == "" && (strings.Contains(banner, "Recording") || strings.Contains(line, "recorded")) {
				t.Errorf("banner %q or line %q speaks of recording for a session policy does not record", banner, line)
			}
			for _, retired := range []string{"capture", "ACTIVE", "verbatim", "redact"} {
				if strings.Contains(banner, retired) || strings.Contains(line, retired) {
					t.Errorf("banner %q or line %q still says %q", banner, line, retired)
				}
			}
		})
	}
}

// TestClientSentencesSayAIAgent pins the headless refusal and the doctor's
// identity row to the product's words: an AI agent, its client at the
// identity provider, and never NHI or IdP.
func TestClientSentencesSayAIAgent(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	t.Setenv("STRAZA_CLIENT_ID", "")
	t.Setenv("STRAZA_CLIENT_SECRET", "")
	_, err := fetchHeadlessToken(context.Background(), nil, nil, oidcflow.Flow{}, HeadlessClientCreds, "ci-bot")
	if err == nil {
		t.Fatal("client-credentials enrollment with no client in the environment succeeded")
	}

	store, serr := OpenStore()
	if serr != nil {
		t.Fatal(serr)
	}
	if serr := store.SaveConfig(Config{ServerURL: "http://127.0.0.1:1", SnapshotKeys: map[string]string{"k1": "x"}}); serr != nil {
		t.Fatal(serr)
	}
	if serr := store.SaveIdentity(Identity{Username: "ci-bot", Headless: HeadlessKey}); serr != nil {
		t.Fatal(serr)
	}
	var identity string
	for _, c := range Doctor(context.Background(), store) {
		if c.Name == "identity" {
			identity = c.Detail
		}
	}

	for _, tc := range []struct{ name, text, want string }{
		{"headless refusal", err.Error(), "(the client this AI agent has at your identity provider), or a key of its own instead"},
		{"doctor identity row", identity, "ci-bot: headless AI agent enrollment (nhi-key lane)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.text, tc.want) {
				t.Errorf("%s = %q, want it to say %q", tc.name, tc.text, tc.want)
			}
			for _, retired := range []string{"NHI", "IdP"} {
				if strings.Contains(tc.text, retired) {
					t.Errorf("%s = %q still says %q", tc.name, tc.text, retired)
				}
			}
		})
	}
}
