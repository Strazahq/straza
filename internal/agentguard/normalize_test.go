package agentguard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

func loadTestAdapters(t *testing.T) map[string]*Adapter {
	t.Helper()
	a, err := LoadAdapters()
	if err != nil {
		t.Fatalf("LoadAdapters: %v", err)
	}
	return a
}

func TestAdaptersLoad(t *testing.T) {
	a := loadTestAdapters(t)
	for _, h := range []string{"claude-code", "codex", "gemini"} {
		if a[h] == nil {
			t.Errorf("missing adapter %s", h)
		}
	}
}

// fixture is one recorded (payload → expected canonical event) pair.
type fixture struct {
	Harness string         `json:"harness"`
	Payload map[string]any `json:"payload"`
	Expect  policy.Event   `json:"expect"`
	// Provenance names HOW the fixture was verified: "live ..." = the payload
	// was captured from or validated against the real harness binary (say
	// which, when, and by what lane); "doc ..." = derived from vendor
	// docs/schemas, unconfirmed against a binary. Required: the corpus test
	// fails an untagged fixture, so a fixture never claims authority it does
	// not have.
	Provenance string `json:"provenance"`
}

// TestFixtureCorpus replays every fixture under
// spec/conformance/hooks/<harness>/<version>/ and asserts normalization
// matches the golden canonical event.
func TestFixtureCorpus(t *testing.T) {
	adapters := loadTestAdapters(t)
	root := filepath.Join("..", "..", "spec", "conformance", "hooks")
	var count int
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".json" {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Decode with UseNumber so the fixture payload replays through the
		// same number-literal-preserving semantics ParsePayload gives a real
		// hook body (the args-literals fixture pins exactly that).
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var fx fixture
		if err := dec.Decode(&fx); err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		count++
		t.Run(filepath.Base(path), func(t *testing.T) {
			if !strings.HasPrefix(fx.Provenance, "live ") && !strings.HasPrefix(fx.Provenance, "doc ") {
				t.Errorf("%s: no provenance tag (want \"live ...\" or \"doc ...\"); how was this fixture verified?", path)
			}
			a := adapters[fx.Harness]
			if a == nil {
				t.Fatalf("no adapter for %s", fx.Harness)
			}
			got, err := Normalize(a, fx.Payload)
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if !reflect.DeepEqual(got.Event, fx.Expect) {
				t.Errorf("canonical mismatch:\ngot  %+v\nwant %+v", got.Event, fx.Expect)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count < 12 {
		t.Errorf("fixture corpus has %d cases, want ≥12 (≥4 per harness)", count)
	}
}

// TestCrossDialectIdentical is the key cross-dialect contract: the same logical action
// normalizes to the SAME canonical event across all three harnesses
// (ignoring harness identity).
func TestCrossDialectIdentical(t *testing.T) {
	adapters := loadTestAdapters(t)

	// A destructive shell command in each dialect's payload shape.
	payloads := map[string]map[string]any{
		"claude-code": {
			"hook_event_name": "PreToolUse",
			"session_id":      "s1",
			"cwd":             "/work",
			"tool_name":       "Bash",
			"tool_input":      map[string]any{"command": "rm -rf /tmp/x"},
		},
		"codex": {
			"hook_event_name": "PreToolUse",
			"session_id":      "s1",
			"cwd":             "/work",
			"tool_name":       "Shell",
			"tool_input":      map[string]any{"command": "rm -rf /tmp/x"},
		},
		// Shipping gemini (hooks GA, v0.50 mapping): snake_case like the
		// Claude/Codex family; only the event names differ.
		"gemini": {
			"hook_event_name": "BeforeTool",
			"session_id":      "s1",
			"cwd":             "/work",
			"timestamp":       "2026-07-16T00:00:00Z",
			"tool_name":       "run_shell_command",
			"tool_input":      map[string]any{"command": "rm -rf /tmp/x"},
		},
	}
	want := policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolShellExec,
		Command: "rm -rf /tmp/x", Workspace: "/work",
	}
	for harness, payload := range payloads {
		got, err := Normalize(adapters[harness], payload)
		if err != nil {
			t.Fatalf("%s: %v", harness, err)
		}
		if !reflect.DeepEqual(got.Event, want) {
			t.Errorf("%s normalized to %+v, want %+v", harness, got.Event, want)
		}
	}

	// And an MCP call normalizes identically (app/tool parsed from the name).
	mcp := map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "s1", "cwd": "/w",
		"tool_name": "mcp__github__get_repository", "tool_input": map[string]any{},
	}
	got, _ := Normalize(adapters["claude-code"], mcp)
	if got.Event.Tool != policy.ToolMCPCall || got.Event.App != "github" || got.Event.ToolName != "get_repository" {
		t.Errorf("mcp parse: %+v", got.Event)
	}
}

// TestGeminiLegacyCamelCase pins the legacy gemini/Antigravity payload shape
// (event/sessionId/workspaceDir/toolName/args, as in the antigravity fixture
// corpus): the snake_case re-pin (v0.50) must not break older builds, which
// keep normalizing via the hardcoded fallbacks in Normalize.
func TestGeminiLegacyCamelCase(t *testing.T) {
	adapters := loadTestAdapters(t)
	got, err := Normalize(adapters["gemini"], map[string]any{
		"event": "BeforeTool", "sessionId": "s1", "workspaceDir": "/work",
		"toolName": "run_shell_command", "args": map[string]any{"command": "rm -rf /tmp/x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolShellExec,
		Command: "rm -rf /tmp/x", Workspace: "/work",
	}
	if !reflect.DeepEqual(got.Event, want) {
		t.Errorf("legacy gemini normalized to %+v, want %+v", got.Event, want)
	}
	if got.SessionID != "s1" {
		t.Errorf("legacy session id = %q", got.SessionID)
	}
}

// TestGeminiMcpContext: shipping gemini names MCP tools mcp_<server>_<tool>
// with SINGLE underscores (mcp-tool.ts MCP_QUALIFIED_NAME_SEPARATOR='_'),
// unsplittable when either side contains one. The payload's mcp_context
// (server_name + bare tool_name) is the authoritative lane and must win.
func TestGeminiMcpContext(t *testing.T) {
	adapters := loadTestAdapters(t)
	got, err := Normalize(adapters["gemini"], map[string]any{
		"hook_event_name": "BeforeTool", "session_id": "s1", "cwd": "/w",
		"tool_name":  "mcp_github_get_repository",
		"tool_input": map[string]any{},
		"mcp_context": map[string]any{
			"server_name": "github", "tool_name": "get_repository",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Event.Tool != policy.ToolMCPCall || got.Event.App != "github" || got.Event.ToolName != "get_repository" {
		t.Errorf("mcp_context parse: %+v", got.Event)
	}

	// Without mcp_context the single-underscore name is deliberately NOT
	// guessed apart: it degrades to "other" (never a silent wrong app/tool).
	got, err = Normalize(adapters["gemini"], map[string]any{
		"hook_event_name": "BeforeTool", "session_id": "s1", "cwd": "/w",
		"tool_name": "mcp_github_get_repository", "tool_input": map[string]any{},
	})
	if err != nil || got.Event.Tool != policy.ToolOther {
		t.Errorf("no-context degrade: %+v, %v", got.Event, err)
	}
}

// TestGeminiShippingCapture pins the capture inputs on the shipping payload
// shape: BeforeAgent carries prompt; AfterAgent carries the reply IN the
// payload (prompt_response, with no transcript reading).
func TestGeminiShippingCapture(t *testing.T) {
	adapters := loadTestAdapters(t)
	got, err := Normalize(adapters["gemini"], map[string]any{
		"hook_event_name": "BeforeAgent", "session_id": "s1", "cwd": "/w",
		"transcript_path": "/tmp/t.json", "prompt": "list users",
	})
	if err != nil || got.Prompt != "list users" || got.TranscriptPath != "/tmp/t.json" {
		t.Errorf("BeforeAgent capture: %+v, %v", got, err)
	}
	got, err = Normalize(adapters["gemini"], map[string]any{
		"hook_event_name": "AfterAgent", "session_id": "s1", "cwd": "/w",
		"prompt": "list users", "prompt_response": "alice, bob",
	})
	if err != nil || got.Reply != "alice, bob" || got.Event.Kind != policy.EventSessionEnd {
		t.Errorf("AfterAgent capture: %+v, %v", got, err)
	}
}

func TestDetectHarness(t *testing.T) {
	adapters := loadTestAdapters(t)
	if h := DetectHarness("codex", nil, nil, adapters); h != "codex" {
		t.Errorf("override = %q", h)
	}
	if h := DetectHarness("", map[string]string{"CLAUDECODE": "1"}, nil, adapters); h != "claude-code" {
		t.Errorf("env = %q", h)
	}
	if h := DetectHarness("", nil, map[string]any{"hook_event_name": "PreToolUse"}, adapters); h != "claude-code" {
		t.Errorf("snake_case heuristic = %q", h)
	}
	if h := DetectHarness("", nil, map[string]any{"workspaceDir": "/w"}, adapters); h != "gemini" {
		t.Errorf("gemini legacy heuristic = %q", h)
	}
	// Shipping gemini is snake_case too, so the renamed event names are the tell.
	if h := DetectHarness("", nil, map[string]any{"hook_event_name": "BeforeTool"}, adapters); h != "gemini" {
		t.Errorf("gemini event-name heuristic = %q", h)
	}
	// SessionStart/SessionEnd exist in both dialects: the gemini-only base
	// field `timestamp` breaks the tie; without it claude-code wins.
	if h := DetectHarness("", nil, map[string]any{"hook_event_name": "SessionStart", "timestamp": "2026-07-16T00:00:00Z"}, adapters); h != "gemini" {
		t.Errorf("timestamp tiebreak = %q", h)
	}
	if h := DetectHarness("", nil, map[string]any{"hook_event_name": "SessionStart"}, adapters); h != "claude-code" {
		t.Errorf("ambiguous default = %q", h)
	}
}

// TestGatewayProxiedMcpResplit: the Straza gateway registers ONE MCP
// server ("straza") whose tool names are namespaced <app>__<tool>
// (internal/server/catalog.go), so a harness presents a proxied call as
// mcp__straza__<app>__<tool>. Name-based classification must re-attribute
// the call to its real app (every policy grant is written against the
// app's own name), and the split is exact because app names cannot contain
// underscores (manifest nameRe). Un-namespaced inner names keep the gateway
// identity (defer-to-gateway posture).
func TestGatewayProxiedMcpResplit(t *testing.T) {
	adapters := loadTestAdapters(t)
	cases := []struct {
		name     string
		toolName string
		app      string
		tool     string
	}{
		{"gateway proxied", "mcp__straza__demo-tools__echo", "demo-tools", "echo"},
		{"gateway proxied, tool with underscores", "mcp__straza__demo-tools__long_running_operation", "demo-tools", "long_running_operation"},
		{"gateway proxied, tool with double underscore", "mcp__straza__demo-tools__a__b", "demo-tools", "a__b"},
		{"gateway meta-tool defers", "mcp__straza__find_tools", "straza", "find_tools"},
		{"empty inner app defers", "mcp__straza____x", "straza", "__x"},
		{"empty inner tool defers", "mcp__straza__demo-tools__", "straza", "demo-tools__"},
		{"non-gateway server untouched", "mcp__github__get_repository", "github", "get_repository"},
		{"non-gateway server with namespaced-looking tool untouched", "mcp__github__demo-tools__echo", "github", "demo-tools__echo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Normalize(adapters["claude-code"], map[string]any{
				"hook_event_name": "PreToolUse", "session_id": "s1", "cwd": "/w",
				"tool_name": c.toolName, "tool_input": map[string]any{},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Event.Tool != policy.ToolMCPCall || got.Event.App != c.app || got.Event.ToolName != c.tool {
				t.Errorf("%s → tool=%s app=%q toolName=%q, want mcp.call %q/%q",
					c.toolName, got.Event.Tool, got.Event.App, got.Event.ToolName, c.app, c.tool)
			}
		})
	}
}

// TestGeminiGatewayMcpContextResplit: the authoritative mcp_context lane
// needs the same gateway re-attribution: gemini presents server_name
// "straza" + the namespaced bare tool_name for proxied calls.
func TestGeminiGatewayMcpContextResplit(t *testing.T) {
	adapters := loadTestAdapters(t)
	got, err := Normalize(adapters["gemini"], map[string]any{
		"hook_event_name": "BeforeTool", "session_id": "s1", "cwd": "/w",
		"tool_name": "mcp_straza_demo-tools__echo", "tool_input": map[string]any{},
		"mcp_context": map[string]any{
			"server_name": "straza", "tool_name": "demo-tools__echo",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Event.Tool != policy.ToolMCPCall || got.Event.App != "demo-tools" || got.Event.ToolName != "echo" {
		t.Errorf("gateway mcp_context resplit: %+v", got.Event)
	}

	// A non-gateway server whose tool happens to contain "__" stays untouched.
	got, err = Normalize(adapters["gemini"], map[string]any{
		"hook_event_name": "BeforeTool", "session_id": "s1", "cwd": "/w",
		"tool_name": "mcp_github_x", "tool_input": map[string]any{},
		"mcp_context": map[string]any{
			"server_name": "github", "tool_name": "x__y",
		},
	})
	if err != nil || got.Event.App != "github" || got.Event.ToolName != "x__y" {
		t.Errorf("non-gateway mcp_context: %+v, %v", got.Event, err)
	}
}

// TestModernBuiltinMappings: the current Claude Code built-in
// inventory classifies into the canonical taxonomy instead of falling to
// "other" (which the enterprise profile default-denies, so an unmapped
// built-in bricks the harness). Unknown names still
// degrade to "other": TestNormalizeUnknownToolIsOther pins that posture.
func TestModernBuiltinMappings(t *testing.T) {
	adapters := loadTestAdapters(t)
	cases := []struct {
		toolName string
		want     string
	}{
		// read lane: filesystem / catalog / session-state reads and pure-UI
		{"Glob", policy.ToolFileRead},
		{"Grep", policy.ToolFileRead},
		{"LS", policy.ToolFileRead},
		{"NotebookRead", policy.ToolFileRead},
		{"SendUserFile", policy.ToolFileRead},
		{"ToolSearch", policy.ToolFileRead},
		{"Skill", policy.ToolFileRead},
		{"SlashCommand", policy.ToolFileRead},
		{"ListMcpResources", policy.ToolFileRead},
		{"ReadMcpResource", policy.ToolFileRead},
		{"AskUserQuestion", policy.ToolFileRead},
		{"TodoWrite", policy.ToolFileRead},
		{"EnterPlanMode", policy.ToolFileRead},
		{"ExitPlanMode", policy.ToolFileRead},
		{"ReportFindings", policy.ToolFileRead},
		// shell lane companions
		{"BashOutput", policy.ToolShellExec},
		{"KillShell", policy.ToolShellExec},
		{"KillBash", policy.ToolShellExec},
		// task/agent lane
		{"Workflow", policy.ToolTaskSpawn},
		{"SendMessage", policy.ToolTaskSpawn},
		{"Monitor", policy.ToolTaskSpawn},
		{"TaskCreate", policy.ToolTaskSpawn},
		{"TaskGet", policy.ToolTaskSpawn},
		{"TaskList", policy.ToolTaskSpawn},
		{"TaskOutput", policy.ToolTaskSpawn},
		{"TaskStop", policy.ToolTaskSpawn},
		{"TaskUpdate", policy.ToolTaskSpawn},
		{"ScheduleWakeup", policy.ToolTaskSpawn},
		{"CronCreate", policy.ToolTaskSpawn},
		{"CronDelete", policy.ToolTaskSpawn},
		{"CronList", policy.ToolTaskSpawn},
		{"RemoteTrigger", policy.ToolTaskSpawn},
		{"EndConversation", policy.ToolTaskSpawn},
		// egress lane
		{"Artifact", policy.ToolNetFetch},
		{"PushNotification", policy.ToolNetFetch},
		{"DesignSync", policy.ToolNetFetch},
		// worktree isolation mutates the repo
		{"EnterWorktree", policy.ToolFileWrite},
		{"ExitWorktree", policy.ToolFileWrite},
	}
	for _, c := range cases {
		got, err := Normalize(adapters["claude-code"], map[string]any{
			"hook_event_name": "PreToolUse", "session_id": "s1", "cwd": "/w",
			"tool_name": c.toolName, "tool_input": map[string]any{},
		})
		if err != nil {
			t.Fatalf("%s: %v", c.toolName, err)
		}
		if got.Event.Tool != c.want {
			t.Errorf("%s → %q, want %q", c.toolName, got.Event.Tool, c.want)
		}
	}

	// Path capture rides the canonical kind's toolInputs: Grep/Glob carry
	// the directory they read in "path"; SendUserFile carries "files".
	got, err := Normalize(adapters["claude-code"], map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "s1", "cwd": "/w",
		"tool_name": "Grep", "tool_input": map[string]any{"pattern": "x", "path": "/w/src"},
	})
	if err != nil || len(got.Event.Paths) != 1 || got.Event.Paths[0] != "/w/src" {
		t.Errorf("Grep path capture: %+v, %v", got.Event, err)
	}
	got, err = Normalize(adapters["claude-code"], map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "s1", "cwd": "/w",
		"tool_name": "SendUserFile", "tool_input": map[string]any{"files": []any{"/w/a.md", "/w/b.md"}},
	})
	if err != nil || len(got.Event.Paths) != 2 {
		t.Errorf("SendUserFile paths capture: %+v, %v", got.Event, err)
	}
}

// TestNormalizeGatewayProxiedFlag pins where the single-gate deferral gets
// its trigger: only calls the harness routes through the Straza gateway
// registration carry the flag: namespaced or the gateway's own native
// tools, plus gemini's mcp_context naming the gateway server. A direct MCP
// server or a built-in must never set it (there the hook is the only gate).
func TestNormalizeGatewayProxiedFlag(t *testing.T) {
	adapters, err := LoadAdapters()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tool string
		want       bool
	}{
		{"gateway namespaced", "mcp__straza__demo-tools__echo", true},
		{"gateway native tool", "mcp__straza__approval_status", true},
		{"direct MCP server", "mcp__github__create_issue", false},
		{"built-in", "Bash", false},
	} {
		n, err := Normalize(adapters["claude-code"], map[string]any{
			"hook_event_name": "PreToolUse", "session_id": "s-1",
			"tool_name": tc.tool, "tool_input": map[string]any{},
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if n.GatewayProxied != tc.want {
			t.Errorf("%s: GatewayProxied = %v, want %v", tc.name, n.GatewayProxied, tc.want)
		}
	}

	// gemini routes gateway calls through mcp_context (server_name is
	// authoritative there, since the top-level name is unsplittable).
	for _, tc := range []struct {
		name, server string
		want         bool
	}{
		{"gemini via gateway", "straza", true},
		{"gemini direct server", "github", false},
	} {
		n, err := Normalize(adapters["gemini"], map[string]any{
			"hook_event_name": "BeforeTool", "session_id": "s-1",
			"tool_name": "mcp_" + tc.server + "_something",
			"mcp_context": map[string]any{
				"server_name": tc.server, "tool_name": "demo-tools__echo",
			},
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if n.GatewayProxied != tc.want {
			t.Errorf("%s: GatewayProxied = %v, want %v", tc.name, n.GatewayProxied, tc.want)
		}
	}
}

func TestNormalizeUnknownToolIsOther(t *testing.T) {
	adapters := loadTestAdapters(t)
	got, err := Normalize(adapters["claude-code"], map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "SomeNewTool", "tool_input": map[string]any{},
	})
	if err != nil || got.Event.Tool != policy.ToolOther {
		t.Errorf("unknown tool: %+v, %v", got.Event, err)
	}
}

// TestHarnessLabel pins the harness label a record carries: name/version
// when the harness reported a version, the bare name when it did not.
func TestHarnessLabel(t *testing.T) {
	tests := []struct{ name, version, want string }{
		{"claude-code", "2.1", "claude-code/2.1"},
		{"claude-code", "", "claude-code"},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := harnessLabel(tt.name, tt.version); got != tt.want {
			t.Errorf("harnessLabel(%q, %q) = %q, want %q", tt.name, tt.version, got, tt.want)
		}
	}
}
