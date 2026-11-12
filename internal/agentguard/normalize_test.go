package agentguard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/agentguard/localapi"
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
		{"ReadNotifications", policy.ToolFileRead},
		{"FetchInboxMessage", policy.ToolFileRead},
		// shell lane companions
		{"BashOutput", policy.ToolShellExec},
		{"KillShell", policy.ToolShellExec},
		{"KillBash", policy.ToolShellExec},
		// task/agent lane
		{"Workflow", policy.ToolTaskSpawn},
		{"SendMessage", policy.ToolTaskSpawn},
		{"ListAgents", policy.ToolTaskSpawn},
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
		{"SubagentHandback", policy.ToolTaskSpawn},
		// egress lane
		{"ArtifactCheck", policy.ToolNetFetch},
		{"Artifact", policy.ToolNetFetch},
		{"PushNotification", policy.ToolNetFetch},
		{"DesignSync", policy.ToolNetFetch},
		{"SendFeedback", policy.ToolNetFetch},
		{"ArtifactComments", policy.ToolNetFetch},
		{"ArtifactData", policy.ToolNetFetch},
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

// TestNormalizeCarriesHarnessToolName pins where the harness's own tool
// name lands: on the event as toolName for an unclassified tool, and beside
// the event as NativeTool for a classified one, so the canonical event stays
// harness-neutral. A name past the cap or with a control character is
// dropped, and the tool stays denied as nameless.
func TestNormalizeCarriesHarnessToolName(t *testing.T) {
	adapters := loadTestAdapters(t)
	long := strings.Repeat("x", 129)
	cases := []struct {
		name, harness  string
		payload        map[string]any
		tool, toolName string
		native         string
	}{
		{"claude-code unknown", "claude-code", map[string]any{"tool_name": "FutureTool"}, policy.ToolOther, "FutureTool", ""},
		{"claude-code classified", "claude-code", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}, policy.ToolShellExec, "", "Bash"},
		{"claude-code handback", "claude-code", map[string]any{"tool_name": "SubagentHandback"}, policy.ToolTaskSpawn, "", "SubagentHandback"},
		{"claude-code mcp keeps its own toolName", "claude-code", map[string]any{"tool_name": "mcp__github__get_repo"}, policy.ToolMCPCall, "get_repo", ""},
		{"codex unknown", "codex", map[string]any{"tool_name": "FutureTool"}, policy.ToolOther, "FutureTool", ""},
		{"gemini unknown", "gemini", map[string]any{"tool_name": "future_tool"}, policy.ToolOther, "future_tool", ""},
		{"kit unmapped tool", "python-sdk", map[string]any{"tool_name": "other", "native_tool_name": "refund_order"}, policy.ToolOther, "refund_order", ""},
		{"kit mapped tool", "python-sdk", map[string]any{"tool_name": "net.fetch", "native_tool_name": "search_orders"}, policy.ToolNetFetch, "", "search_orders"},
		{"old kit sends no name", "python-sdk", map[string]any{"tool_name": "other"}, policy.ToolOther, "", ""},
		{"name at the cap", "claude-code", map[string]any{"tool_name": long[:128]}, policy.ToolOther, long[:128], ""},
		{"name past the cap", "claude-code", map[string]any{"tool_name": long}, policy.ToolOther, "", ""},
		{"control character", "claude-code", map[string]any{"tool_name": "Bad\nName"}, policy.ToolOther, "", ""},
		{"format character", "claude-code", map[string]any{"tool_name": "Bad\u202eName"}, policy.ToolOther, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload := map[string]any{"hook_event_name": "PreToolUse", "session_id": "s1", "cwd": "/w"}
			if c.harness == "gemini" {
				payload["hook_event_name"] = "BeforeTool"
			}
			for k, v := range c.payload {
				payload[k] = v
			}
			got, err := Normalize(adapters[c.harness], payload)
			if err != nil {
				t.Fatal(err)
			}
			if got.Event.Tool != c.tool || got.Event.ToolName != c.toolName || got.NativeTool != c.native {
				t.Errorf("got tool %q toolName %q native %q, want %q %q %q",
					got.Event.Tool, got.Event.ToolName, got.NativeTool, c.tool, c.toolName, c.native)
			}
			if got.Event.NativeTool != "" {
				t.Errorf("the canonical event carries nativeTool %q, want it beside the event", got.Event.NativeTool)
			}
		})
	}
}

// TestHarnessName pins the harness label doctor shows for a session:
// name/version when the harness reported a version, the bare name when it
// did not.
func TestHarnessName(t *testing.T) {
	tests := []struct{ name, version, want string }{
		{"claude-code", "2.1", "claude-code/2.1"},
		{"claude-code", "", "claude-code"},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := harnessName(localapi.Harness{Name: tt.name, Version: tt.version}); got != tt.want {
			t.Errorf("harnessName(%q, %q) = %q, want %q", tt.name, tt.version, got, tt.want)
		}
	}
}

// TestNormalizeForwardsToolInput pins where the harness's tool input
// travels, in every dialect. An mcp.call and an unclassified tool carry it
// on the event as args, which the approval key binds. A classified local
// tool carries it beside the event for the audit record alone, because a
// key that bound it would no longer match the ticket a described command
// opened. An absent input stays absent on both, so the server can tell a
// call it did not observe from a call with no arguments, and a number keeps
// its literal.
func TestNormalizeForwardsToolInput(t *testing.T) {
	adapters := loadTestAdapters(t)
	cases := []struct {
		name, harness, payload string
		tool                   string
		args                   string // "" means the event carries none
		input                  string // "" means none travels beside the event
	}{
		{"claude-code language server query", "claude-code",
			`{"hook_event_name":"PreToolUse","tool_name":"LSP","tool_input":{"operation":"goToDefinition","filePath":"/w/main.go","line":12,"character":7}}`,
			policy.ToolOther, `{"character":7,"filePath":"/w/main.go","line":12,"operation":"goToDefinition"}`, ""},
		{"codex unclassified", "codex",
			`{"hook_event_name":"PreToolUse","tool_name":"future_tool","tool_input":{"target":"db"}}`,
			policy.ToolOther, `{"target":"db"}`, ""},
		{"gemini unclassified", "gemini",
			`{"hook_event_name":"BeforeTool","tool_name":"future_tool","tool_input":{"target":"db"}}`,
			policy.ToolOther, `{"target":"db"}`, ""},
		{"kit unclassified keeps a large integer", "python-sdk",
			`{"hook_event_name":"PreToolUse","tool_name":"other","native_tool_name":"refund_order","tool_input":{"order":"A-1","cents":9007199254740993}}`,
			policy.ToolOther, `{"cents":9007199254740993,"order":"A-1"}`, ""},
		{"an observed call with no arguments", "claude-code",
			`{"hook_event_name":"PreToolUse","tool_name":"FutureTool","tool_input":{}}`,
			policy.ToolOther, `{}`, ""},
		{"an input that was not sent", "claude-code",
			`{"hook_event_name":"PreToolUse","tool_name":"FutureTool"}`,
			policy.ToolOther, "", ""},
		{"an input that is not an object", "claude-code",
			`{"hook_event_name":"PreToolUse","tool_name":"FutureTool","tool_input":"text"}`,
			policy.ToolOther, "", ""},
		{"mcp call still forwards", "claude-code",
			`{"hook_event_name":"PreToolUse","tool_name":"mcp__claude-in-chrome__navigate","tool_input":{"url":"https://example.com"}}`,
			policy.ToolMCPCall, `{"url":"https://example.com"}`, ""},
		{"a shell command's input travels beside the event", "claude-code",
			`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls","description":"list"}}`,
			policy.ToolShellExec, "", `{"command":"ls","description":"list"}`},
		{"a written file's content travels beside the event", "claude-code",
			`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/w/a.txt","content":"secret body"}}`,
			policy.ToolFileWrite, "", `{"content":"secret body","file_path":"/w/a.txt"}`},
		{"a skill call names its skill", "claude-code",
			`{"hook_event_name":"PreToolUse","tool_name":"Skill","tool_input":{"skill":"deploy"}}`,
			policy.ToolFileRead, "", `{"skill":"deploy"}`},
		{"a spawn keeps its prompt", "claude-code",
			`{"hook_event_name":"PreToolUse","tool_name":"Agent","tool_input":{"description":"sweep","prompt":"Find every caller."}}`,
			policy.ToolTaskSpawn, "", `{"description":"sweep","prompt":"Find every caller."}`},
		{"codex classified", "codex",
			`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/w/a.txt","content":"body"}}`,
			policy.ToolFileWrite, "", `{"content":"body","file_path":"/w/a.txt"}`},
		{"gemini classified", "gemini",
			`{"hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"ls"}}`,
			policy.ToolShellExec, "", `{"command":"ls"}`},
		{"kit classified keeps a large integer", "python-sdk",
			`{"hook_event_name":"PreToolUse","tool_name":"shell.exec","tool_input":{"command":"ls","timeout":9007199254740993}}`,
			policy.ToolShellExec, "", `{"command":"ls","timeout":9007199254740993}`},
		{"a classified call with no arguments", "claude-code",
			`{"hook_event_name":"PreToolUse","tool_name":"ListAgents","tool_input":{}}`,
			policy.ToolTaskSpawn, "", `{}`},
		{"a classified input that was not sent", "claude-code",
			`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`,
			policy.ToolShellExec, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload, err := ParsePayload([]byte(c.payload))
			if err != nil {
				t.Fatal(err)
			}
			got, err := Normalize(adapters[c.harness], payload)
			if err != nil {
				t.Fatal(err)
			}
			if got.Event.Tool != c.tool || string(got.Event.Args) != c.args {
				t.Errorf("got tool %q args %s, want %q %s", got.Event.Tool, got.Event.Args, c.tool, c.args)
			}
			if c.args == "" && got.Event.Args != nil {
				t.Errorf("args = %q, want nil for an input that was not observed", got.Event.Args)
			}
			if (got.ToolInput != nil) != (c.input != "") {
				t.Fatalf("input beside the event = %+v, want %q", got.ToolInput, c.input)
			}
			if c.input != "" && *got.ToolInput != (localapi.ToolInput{Text: c.input}) {
				t.Errorf("input beside the event = %+v, want the whole text %s and no hash", *got.ToolInput, c.input)
			}
		})
	}
}

// TestNormalizeCutsLongToolInput pins the bound on the input that travels
// beside a classified tool's event: at most ArgumentsMax bytes, shortened
// the way CutArguments shortens a record's arguments, so a long value never
// costs the record a short one, with the hash of the whole text only when
// it was shortened. The cut happens in the hook, so a written file of any
// size still fits the decide request the service and strazad accept.
func TestNormalizeCutsLongToolInput(t *testing.T) {
	a := loadTestAdapters(t)["claude-code"]
	fixed := len(`{"content":"","file_path":"/w/a"}`)
	for _, c := range []struct {
		name, content string
		cut           bool
	}{
		{"an input at the bound stays whole", strings.Repeat("a", ArgumentsMax-fixed), false},
		{"an input one byte past the bound is shortened", strings.Repeat("a", ArgumentsMax-fixed+1), true},
		{"a cut never splits a character", strings.Repeat("é", ArgumentsMax), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Normalize(a, map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Write",
				"tool_input": map[string]any{"file_path": "/w/a", "content": c.content}})
			if err != nil {
				t.Fatal(err)
			}
			whole := `{"content":"` + c.content + `","file_path":"/w/a"}`
			sum := sha256.Sum256([]byte(whole))
			in := got.ToolInput
			if in == nil || len(in.Text) > ArgumentsMax {
				t.Fatalf("input = %+v, want at most %d bytes of the %d sent", in, ArgumentsMax, len(whole))
			}
			if !c.cut && (in.Text != whole || in.Hash != "") {
				t.Errorf("kept %d of %d bytes with hash %q, want the whole text and no hash", len(in.Text), len(whole), in.Hash)
			}
			var kept struct {
				Content string `json:"content"`
				Path    string `json:"file_path"`
			}
			if err := json.Unmarshal([]byte(in.Text), &kept); err != nil || kept.Path != "/w/a" || !utf8.ValidString(kept.Content) {
				t.Fatalf("the kept text decodes with path %q (%v), want JSON that still names /w/a", kept.Path, err)
			}
			prefix, marked := strings.CutSuffix(kept.Content, fmt.Sprintf("... (%d bytes)", len(c.content)))
			if c.cut && (!marked || !strings.HasPrefix(c.content, prefix) || len(in.Text) < ArgumentsMax-3 || in.Hash != "sha256:"+hex.EncodeToString(sum[:])) {
				t.Errorf("kept %d bytes with hash %q and content ending %q, want a prefix with its whole length, a text at the bound and the whole text's hash",
					len(in.Text), in.Hash, kept.Content[max(0, len(kept.Content)-24):])
			}
			if got.Event.Args != nil {
				t.Errorf("args = %d bytes, want none on a classified tool's event", len(got.Event.Args))
			}
		})
	}
}
