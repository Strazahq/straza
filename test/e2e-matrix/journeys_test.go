package e2ematrix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestJourneys builds the binaries, boots strazad and runs the corpus. It
// skips unless STRAZA_E2E_MATRIX=1 so the commit gate stays fast.
func TestJourneys(t *testing.T) {
	if os.Getenv("STRAZA_E2E_MATRIX") != "1" {
		t.Skip("set STRAZA_E2E_MATRIX=1 (or run make e2e-matrix) to build the binaries, boot strazad and run the journey corpus")
	}
	runJourneys(t)
}

// TestCorpusParses loads every scenario file under corpus/ and refuses an
// empty corpus, so a broken scenario fails at commit time.
func TestCorpusParses(t *testing.T) {
	corpus, err := loadCorpus("corpus")
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range corpus {
		if len(sc.Steps) == 0 {
			t.Errorf("%s: no steps", sc.ID)
		}
		t.Logf("%s: %d steps under %v", sc.ID, len(sc.Steps), sc.Harnesses)
	}
}

// TestEmptyCorpusRefused pins that a corpus directory without a scenario is
// an error, never a silent green.
func TestEmptyCorpusRefused(t *testing.T) {
	if _, err := loadCorpus(t.TempDir()); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty corpus accepted: %v", err)
	}
}

// TestScenarioValidation pins the refusals a scenario author meets first.
func TestScenarioValidation(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"no provenance", "id: x-one\ngroup: X\ntitle: t\nsteps:\n  - name: s\n    server: {action: stop}\n", "provenance"},
		{"two actions", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    server: {action: stop}\n    audit: {subject: a}\n", "two actions"},
		{"unknown identity", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    enroll: {as: nobody}\n", "not a declared identity"},
		{"bad kind", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  a: {kind: human, password: p}\nsteps:\n  - name: s\n    hook: {as: a, event: {kind: tool.pre, tool: shell.exec}}\n", "needs command"},
		{"payload outside harnesses", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nharnesses: [codex]\nidentities:\n  a: {kind: human, password: p}\nsteps:\n  - name: s\n    hook: {as: a, payload: {gemini: '[1'}}\n", "harnesses does not list"},
		{"unknown key", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    server: {action: stop}\n    wants: {status: 200}\n", "unknown key"},
		{"governance posture knob", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\ngovernance: {localToolDefault: deny}\nsteps:\n  - name: s\n    server: {action: stop}\n", "governance.localToolDefault is not one of"},
		{"governance bad duration", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\ngovernance: {deviceTokenTTL: soon}\nsteps:\n  - name: s\n    server: {action: stop}\n", "positive duration"},
		{"governance zero lifetime", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\ngovernance: {sessionMaxLifetime: 0s}\nsteps:\n  - name: s\n    server: {action: stop}\n", "positive duration"},
		{"governance custody of another word", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\ngovernance: {minCustody: root}\nsteps:\n  - name: s\n    server: {action: stop}\n", "governance.minCustody must be account or user"},
		{"governance sign-in floor of another word", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\ngovernance: {minSignIn: device-code}\nsteps:\n  - name: s\n    server: {action: stop}\n", "governance.minSignIn must be code or browser"},
		{"enroll by another sign-in", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  a: {kind: human, password: p}\nsteps:\n  - name: s\n    enroll: {as: a, signIn: phone}\n", "enroll signIn must be code or browser"},
		{"enroll an NHI by a sign-in", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  bot: {kind: nhi}\nsteps:\n  - name: s\n    enroll: {as: bot, signIn: browser}\n", "an NHI enrolls headless with its key"},
		{"daemon action retired", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  a: {kind: human, password: p}\nsteps:\n  - name: s\n    daemon: {as: a, action: start}\n", "has no action"},
		{"wait without a duration", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    wait: soon\n", "wait must be a positive duration"},
		{"wait of zero", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    wait: 0s\n", "wait must be a positive duration"},
		{"wait that polls", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    wait: 5s\n    within: 10s\n", "a wait records nothing"},
		{"as on an undeclared identity", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    admin: {method: POST, path: /v1/admin/approvals/x/approve, as: nobody}\n", "not a declared identity"},
		{"as on a non-human identity", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  bot: {kind: nhi}\nsteps:\n  - name: s\n    admin: {method: POST, path: /v1/admin/approvals/x/approve, as: bot}\n", "must be a human identity"},
		{"approver as a non-human identity", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  a: {kind: human, password: p}\n  bot: {kind: nhi}\nsteps:\n  - name: s\n    approver: {as: bot, action: decide, request: r, verdict: approve}\n", "a non-human identity never decides"},
		{"approver with an unknown verb", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  a: {kind: human, password: p}\n  bot: {kind: nhi}\nsteps:\n  - name: s\n    approver: {as: a, action: revoke}\n", "approver.action must be enroll or decide"},
		{"approver enroll without a token", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  a: {kind: human, password: p}\n  bot: {kind: nhi}\nsteps:\n  - name: s\n    approver: {as: a, action: enroll}\n", "approver enroll needs token"},
		{"approver decide without a request", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  a: {kind: human, password: p}\n  bot: {kind: nhi}\nsteps:\n  - name: s\n    approver: {as: a, action: decide, verdict: approve}\n", "approver decide needs request"},
		{"approver decide with a bad verdict", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  a: {kind: human, password: p}\n  bot: {kind: nhi}\nsteps:\n  - name: s\n    approver: {as: a, action: decide, request: r, verdict: maybe}\n", "verdict approve or deny"},
		{"approver decide that polls", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  a: {kind: human, password: p}\n  bot: {kind: nhi}\nsteps:\n  - name: s\n    approver: {as: a, action: decide, request: r, verdict: approve}\n    within: 10s\n", "takes no within"},
		{"as on a scim step", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nidentities:\n  a: {kind: human, password: p}\nsteps:\n  - name: s\n    scim: {method: GET, path: /scim/v2/Users, as: a}\n", "as belongs on an admin step"},
		{"audit object of an unknown field", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    audit: {object: {repo: acme/payments}}\n", "audit object.repo is not one of"},
		{"audit object id unquoted", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    audit: {object: {id: 1001}}\n", "quote a number"},
		{"audit object not a map", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    audit: {object: acme/payments}\n", "audit object must be a map"},
		{"audit with a misspelled key", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    audit: {subject: a, objcet: {value: acme/payments}}\n", "audit.objcet is not one of subject, kind, action, decision, reasonContains, object, signIn, count"},
		{"audit without a key", "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\nsteps:\n  - name: s\n    audit: {}\n", "audit needs at least one of"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "x-one.yaml"), []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadCorpus(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

// TestDecideAsAndWaitParse pins the two step forms a scenario about a named
// decider and a lapsing window is written in: an admin step that carries the
// person it acts as, and a wait that carries a plain duration.
func TestDecideAsAndWaitParse(t *testing.T) {
	doc := "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\n" +
		"identities:\n  nina: {kind: human, password: p}\n" +
		"steps:\n" +
		"  - name: the window lapses\n    wait: 1m5s\n" +
		"  - name: nina decides her own request\n    admin: {method: POST, path: /v1/admin/approvals/x/approve, as: nina}\n    want: {status: 403}\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x-one.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	corpus, err := loadCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	steps := corpus[0].Steps
	if steps[0].Action != "wait" || steps[0].Wait != 65*time.Second {
		t.Errorf("wait step: action %q, duration %s", steps[0].Action, steps[0].Wait)
	}
	if steps[0].Within != 0 || steps[0].Want != nil {
		t.Errorf("wait step polls: within %s, want %v", steps[0].Within, steps[0].Want)
	}
	if steps[1].Action != "admin" || steps[1].str("as") != "nina" {
		t.Errorf("admin step: action %q, as %q", steps[1].Action, steps[1].str("as"))
	}
}

// TestGovernanceFile pins that a scenario's lifetimes and custody floor
// reach the server as its config file, zero grace included, and that no
// file is written when a scenario declares none.
func TestGovernanceFile(t *testing.T) {
	doc := "id: x-one\ngroup: X\ntitle: t\nprovenance: doc t\ngovernance: {offlineGraceTTL: 0s, sessionMaxLifetime: 3m30s, minCustody: account}\nsteps:\n  - name: s\n    server: {action: stop}\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x-one.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	corpus, err := loadCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(governanceYAML(corpus[0].Governance)); got != "governance:\n  minCustody: account\n  offlineGraceTTL: 0s\n  sessionMaxLifetime: 3m30s\n" {
		t.Errorf("governance file:\n%s", got)
	}
	s, err := newStrazad("strazad", dir, corpus[0].Governance)
	if err != nil {
		t.Fatal(err)
	}
	if s.configPath != filepath.Join(dir, "straza.yaml") {
		t.Errorf("config path %q", s.configPath)
	}
	if plain, _ := newStrazad("strazad", dir, nil); plain.configPath != "" {
		t.Errorf("a scenario without governance got a config file %q", plain.configPath)
	}
}

// TestPollCadence pins the cadence rule: 100 ms up to a 20 s wait, then one
// two-hundredth of the wait.
func TestPollCadence(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		0:                100 * time.Millisecond,
		10 * time.Second: 100 * time.Millisecond,
		20 * time.Second: 100 * time.Millisecond,
		30 * time.Second: 150 * time.Millisecond,
		4 * time.Minute:  1200 * time.Millisecond,
	}
	for within, want := range cases {
		if got := pollCadence(within); got != want {
			t.Errorf("pollCadence(%s) = %s, want %s", within, got, want)
		}
	}
}

// TestRenderers pins the native shape of every canonical event on every
// dialect against the mapping tables and the payload corpus.
func TestRenderers(t *testing.T) {
	events := []event{
		{Kind: "session.start"},
		{Kind: "prompt.submit", Prompt: "say hi"},
		{Kind: "tool.pre", Tool: "shell.exec", Command: "rm -rf /tmp/x"},
		{Kind: "tool.pre", Tool: "file.read", Path: "/work/proj/a.txt"},
		{Kind: "tool.pre", Tool: "file.write", Path: "/work/proj/main.go", Content: "package main"},
		{Kind: "tool.pre", Tool: "mcp.call", App: "github", ToolName: "get_repository", Args: map[string]any{"owner": "x"}},
		{Kind: "tool.post", Tool: "shell.exec", Command: "ls", Output: "a b"},
		{Kind: "session.end"},
	}
	want := map[string]map[string][2]string{
		"claude-code": {"session.start": {"SessionStart", ""}, "prompt.submit": {"UserPromptSubmit", ""}, "tool.pre": {"PreToolUse", "Bash"}, "tool.post": {"PostToolUse", "Bash"}, "session.end": {"SessionEnd", ""}},
		"codex":       {"session.start": {"SessionStart", ""}, "prompt.submit": {"UserPromptSubmit", ""}, "tool.pre": {"PreToolUse", "Shell"}, "tool.post": {"PostToolUse", "Shell"}, "session.end": {"SessionEnd", ""}},
		"gemini":      {"session.start": {"SessionStart", ""}, "prompt.submit": {"BeforeAgent", ""}, "tool.pre": {"BeforeTool", "run_shell_command"}, "tool.post": {"AfterTool", "run_shell_command"}, "session.end": {"SessionEnd", ""}},
		"python-sdk":  {"session.start": {"SessionStart", ""}, "prompt.submit": {"UserPromptSubmit", ""}, "tool.pre": {"PreToolUse", "shell.exec"}, "tool.post": {"PostToolUse", "shell.exec"}, "session.end": {"SessionEnd", ""}},
	}
	for _, d := range dialects {
		for _, ev := range events {
			p, err := renderPayload(d, ev, "s1")
			if err != nil {
				t.Fatalf("%s %s: %v", d, ev.Kind, err)
			}
			if _, err := json.Marshal(p); err != nil {
				t.Fatalf("%s %s: not encodable: %v", d, ev.Kind, err)
			}
			if p["session_id"] != "s1" || p["cwd"] != workspace {
				t.Errorf("%s %s: base fields %v", d, ev.Kind, p)
			}
			exp := want[d][ev.Kind]
			if p["hook_event_name"] != exp[0] {
				t.Errorf("%s %s: event %v, want %s", d, ev.Kind, p["hook_event_name"], exp[0])
			}
			if ev.Tool == "shell.exec" && p["tool_name"] != exp[1] {
				t.Errorf("%s %s: tool %v, want %s", d, ev.Kind, p["tool_name"], exp[1])
			}
			if d == "gemini" && p["timestamp"] == nil {
				t.Errorf("gemini %s: no timestamp", ev.Kind)
			}
			if d == "python-sdk" && p["harness_version"] != "0.1.0" {
				t.Errorf("python-sdk %s: no harness_version", ev.Kind)
			}
		}
	}
	mcp := event{Kind: "tool.pre", Tool: "mcp.call", App: "github", ToolName: "get_repository", Args: map[string]any{}}
	g, _ := renderPayload("gemini", mcp, "s1")
	if g["tool_name"] != "mcp_github_get_repository" {
		t.Errorf("gemini mcp tool_name %v", g["tool_name"])
	}
	if ctxm, _ := g["mcp_context"].(map[string]any); ctxm["server_name"] != "github" || ctxm["tool_name"] != "get_repository" {
		t.Errorf("gemini mcp_context %v", g["mcp_context"])
	}
	c, _ := renderPayload("claude-code", mcp, "s1")
	if c["tool_name"] != "mcp__github__get_repository" {
		t.Errorf("claude-code mcp tool_name %v", c["tool_name"])
	}
	py, _ := renderPayload("python-sdk", event{Kind: "tool.pre", Tool: "file.write", Path: "/p"}, "s1")
	if in, _ := py["tool_input"].(map[string]any); in == nil || in["paths"] == nil {
		t.Errorf("python-sdk file.write tool_input %v", py["tool_input"])
	}
	if _, err := renderPayload("vim", event{Kind: "session.start"}, "s1"); err == nil {
		t.Error("unknown dialect rendered")
	}
}

// TestJudge pins the per-dialect decision rules of the cases.yaml header.
func TestJudge(t *testing.T) {
	claudeDeny := []byte(`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"Straza: recursive delete refused"}}`)
	cases := []struct {
		name    string
		dialect string
		want    map[string]any
		out     hookOutcome
		pass    bool
	}{
		{"claude deny by document and exit 2", "claude-code", map[string]any{"decision": "deny", "reasonContains": "recursive"}, hookOutcome{Exit: 2, Stdout: claudeDeny, Stderr: []byte("Straza: recursive delete refused")}, true},
		{"claude deny by exit 2 with stderr reason", "claude-code", map[string]any{"decision": "deny", "reasonContains": "recursive"}, hookOutcome{Exit: 2, Stderr: []byte("recursive delete refused")}, true},
		{"claude allow with document", "claude-code", map[string]any{"decision": "allow"}, hookOutcome{Stdout: []byte(`{"hookSpecificOutput":{"permissionDecision":"allow"}}`)}, true},
		{"claude allow but silent wanted", "claude-code", map[string]any{"decision": "allow", "silent": true}, hookOutcome{Stdout: []byte(`{"hookSpecificOutput":{"permissionDecision":"allow"}}`)}, false},
		{"codex deny needs exit 2", "codex", map[string]any{"decision": "deny"}, hookOutcome{Exit: 0, Stdout: claudeDeny}, false},
		{"codex deny needs stderr", "codex", map[string]any{"decision": "deny"}, hookOutcome{Exit: 2, Stdout: claudeDeny}, false},
		{"codex deny exit 2 and stderr", "codex", map[string]any{"decision": "deny", "reasonContains": "refused"}, hookOutcome{Exit: 2, Stderr: []byte("Straza: refused")}, true},
		{"codex silent allow", "codex", map[string]any{"decision": "allow", "silent": true}, hookOutcome{}, true},
		{"gemini deny needs strict json", "gemini", map[string]any{"decision": "deny"}, hookOutcome{Exit: 2, Stderr: []byte("refused")}, false},
		{"gemini deny json with reason", "gemini", map[string]any{"decision": "deny", "reasonContains": "REFUSED"}, hookOutcome{Stdout: []byte(`{"decision":"deny","reason":"Straza: refused"}`)}, true},
		{"gemini allow ignores exit code", "gemini", map[string]any{"decision": "allow"}, hookOutcome{Stdout: []byte(`{"decision":"allow"}`)}, true},
		{"python-sdk deny like claude", "python-sdk", map[string]any{"decision": "deny", "reasonContains": "recursive"}, hookOutcome{Exit: 2, Stdout: claudeDeny, Stderr: []byte("x")}, true},
		{"block on exit 1", "claude-code", map[string]any{"decision": "block"}, hookOutcome{Exit: 1, Stderr: []byte("Straza: malformed")}, true},
		{"block on gemini deny document", "gemini", map[string]any{"decision": "block"}, hookOutcome{Stdout: []byte(`{"decision":"deny","reason":"bad"}`)}, true},
		{"block refuses a clean allow", "codex", map[string]any{"decision": "block"}, hookOutcome{}, false},
		{"context present", "claude-code", map[string]any{"decision": "allow", "contextContains": "role dev"}, hookOutcome{Stdout: []byte(`{"hookSpecificOutput":{"additionalContext":"you hold role dev"}}`)}, true},
		{"context missing", "claude-code", map[string]any{"decision": "allow", "contextContains": "role dev"}, hookOutcome{Stdout: []byte(`{"hookSpecificOutput":{"additionalContext":"nothing"}}`)}, false},
		{"allow refused on exit 2", "claude-code", map[string]any{"decision": "allow"}, hookOutcome{Exit: 2}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, detail := judgeHook(tc.dialect, tc.want, tc.out)
			if ok != tc.pass {
				t.Fatalf("pass=%v (%s), want %v", ok, detail, tc.pass)
			}
		})
	}
}

// TestRedact pins that bearer values and secret fields never survive into
// a log line or the report.
func TestRedact(t *testing.T) {
	in := `Authorization: Bearer abc.def-ghi {"password":"hunter2","id_token":"x.y.z","session_token":"s","name":"keep"}`
	out := redact(in)
	for _, leak := range []string{"abc.def-ghi", "hunter2", "x.y.z", `"s"`} {
		if strings.Contains(out, leak) {
			t.Errorf("redact left %q in %q", leak, out)
		}
	}
	if !strings.Contains(out, `"name":"keep"`) {
		t.Errorf("redact damaged a plain field: %q", out)
	}
}

// TestLookupAndExpand pins the json path and placeholder grammar the steps use.
func TestLookupAndExpand(t *testing.T) {
	var doc any
	_ = json.Unmarshal([]byte(`[{"id":"a1","approval":{"id":"r9"}}]`), &doc)
	if v, ok := lookup(doc, "0.approval.id"); !ok || v != "r9" {
		t.Errorf("lookup 0.approval.id = %v %v", v, ok)
	}
	if _, ok := lookup(doc, "1.id"); ok {
		t.Error("lookup past the end succeeded")
	}
	got, err := expand(map[string]any{"path": "/v1/x/${id}", "body": map[string]any{"yaml": "url: ${upstream}"}, "n": 3},
		map[string]string{"id": "a1", "upstream": "http://u"})
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["path"] != "/v1/x/a1" || m["body"].(map[string]any)["yaml"] != "url: http://u" || m["n"] != 3 {
		t.Errorf("expand = %v", m)
	}
	if _, err := expand("${nope}", nil); err == nil {
		t.Error("unknown placeholder accepted")
	}
}

// TestAuditObjectMatcher pins the audit step's object matcher against the
// object member of an mcp record: every named field must be present with
// the same value, and a record without an object matches no matcher.
func TestAuditObjectMatcher(t *testing.T) {
	record := map[string]any{"type": "repo", "reach": "argument", "value": "acme/payments", "id": "1001"}
	cases := []struct {
		name string
		want map[string]any
		got  map[string]any
		pass bool
	}{
		{"no matcher, no object", nil, nil, true},
		{"no matcher, an object", nil, record, true},
		{"the same value and id", map[string]any{"value": "acme/payments", "id": "1001"}, record, true},
		{"every field", record, record, true},
		{"another value", map[string]any{"value": "acme/site"}, record, false},
		{"another id", map[string]any{"id": "1004"}, record, false},
		{"an id the record lacks", map[string]any{"id": "1004"}, map[string]any{"type": "repo", "reach": "all"}, false},
		{"a record without an object", map[string]any{"type": "repo"}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := objectMatches(tc.want, tc.got); got != tc.pass {
				t.Fatalf("objectMatches(%v, %v) = %v, want %v", tc.want, tc.got, got, tc.pass)
			}
		})
	}
}

// TestUpstreamPaths pins the two paths of the hermetic upstream: /mcp lists
// exactly its three tools, so the scenarios that bind every tool there see
// the inventory they always saw, and /mcp-objects pages its seven repos
// through nextCursor and echoes the ai.straza/object-id a call carried.
func TestUpstreamPaths(t *testing.T) {
	u, err := startUpstream()
	if err != nil {
		t.Fatal(err)
	}
	defer u.stop()
	rpc := func(url, method string, params any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		code, body, err := httpCall(t.Context(), "POST", url, "", "application/json", raw)
		if err != nil || code != 200 {
			t.Fatalf("%s %s: %d %v", url, method, code, err)
		}
		var out struct {
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal(body, &out); err != nil || out.Result == nil {
			t.Fatalf("%s %s: %s", url, method, body)
		}
		return out.Result
	}
	names := func(res map[string]any) []string {
		var out []string
		for _, tl := range res["tools"].([]any) {
			out = append(out, tl.(map[string]any)["name"].(string))
		}
		return out
	}
	if got := strings.Join(names(rpc(u.URL, "tools/list", nil)), ","); got != "echo,get-sum,get-env" {
		t.Errorf("/mcp tools %s", got)
	}
	if got := strings.Join(names(rpc(u.ObjectsURL, "tools/list", nil)), ","); got != "repo_list,repo_read,search_code,calls_seen" {
		t.Errorf("/mcp-objects tools %s", got)
	}
	var values []string
	args := map[string]any{}
	for page := 1; ; page++ {
		sc := rpc(u.ObjectsURL, "tools/call", map[string]any{"name": "repo_list", "arguments": args})["structuredContent"].(map[string]any)
		for _, o := range sc["objects"].([]any) {
			values = append(values, o.(map[string]any)["value"].(string))
		}
		next, _ := sc["nextCursor"].(string)
		if next == "" {
			if page != 3 {
				t.Errorf("the listing took %d pages, want 3", page)
			}
			break
		}
		args = map[string]any{"cursor": next}
	}
	if len(values) != 7 || values[0] != "acme/payments" || values[6] != "acme/ledger" {
		t.Errorf("listed %v", values)
	}
	text := func(res map[string]any) string {
		return res["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	withID := map[string]any{"name": "repo_read", "arguments": map[string]any{"repo": "acme/payments"}, "_meta": map[string]any{metaObjectID: "1001"}}
	if got := text(rpc(u.ObjectsURL, "tools/call", withID)); got != "repo_read on acme/payments with ai.straza/object-id 1001." {
		t.Errorf("repo_read with an id: %q", got)
	}
	noID := map[string]any{"name": "repo_read", "arguments": map[string]any{"repo": "acme/site"}}
	if got := text(rpc(u.ObjectsURL, "tools/call", noID)); got != "repo_read on acme/site with no ai.straza/object-id." {
		t.Errorf("repo_read without an id: %q", got)
	}
	seen := text(rpc(u.ObjectsURL, "tools/call", map[string]any{"name": "calls_seen", "arguments": map[string]any{}}))
	if !strings.HasPrefix(seen, "repo_read calls: 2. repo_read on acme/payments with ai.straza/object-id 1001.") {
		t.Errorf("calls_seen %q", seen)
	}
}
