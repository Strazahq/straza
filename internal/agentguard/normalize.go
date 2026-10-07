package agentguard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/strazahq/straza/internal/policy"
)

// harnessLabel joins a harness name and version as name/version, or returns
// the bare name when the harness reported no version, so no record ends in
// a dangling slash.
func harnessLabel(name, version string) string {
	if version == "" {
		return name
	}
	return name + "/" + version
}

// Normalized is a canonical event plus the session/harness metadata a hook
// invocation carries (spec/hook-profile canonical-event.schema.json).
type Normalized struct {
	Event          policy.Event
	HarnessName    string
	HarnessVersion string
	SessionID      string
	// NativeEvent is the harness-native hook event name (e.g. "UserPromptSubmit").
	// The response must echo it back as hookEventName (Claude Code rejects a
	// mismatch), and the canonical Kind can't reconstruct it (Stop and
	// SessionEnd both map to session.end), so it is carried verbatim.
	NativeEvent string
	// Capture inputs. Empty when the
	// dialect has no verified key for them; capture degrades to a no-op.
	Prompt         string // prompt.submit: the submitted text
	TranscriptPath string // harness transcript file (reply capture reads it)
	Reply          string // session.end: payload-borne model reply (preferred over the transcript when present)
	// Delegation attribution + capture inputs. AgentTranscriptPath is the
	// DELEGATE's own transcript, carried by subagent.stop, never derived from TranscriptPath, which
	// names the parent's file there (and flips to the child's at
	// SubagentStart on codex only: the pinned polarity divergence).
	AgentTranscriptPath string // subagent.stop: the delegate's own transcript file
	AgentReply          string // subagent.stop: payload-borne final message (fallback lane)
	AgentID             string // delegate instance id, when the harness attributes the payload
	AgentType           string // delegate class: the capture tag ("capture, tagged")
	// GatewayProxied marks a tool call the harness routes through the Straza
	// gateway registration (mcp__straza__*, or a gemini mcp_context naming
	// the gateway server). Such a call cannot bypass the gateway PEP, which
	// evaluates current policy server-side with the TRUE app/tool names it
	// serves, so the hook lane defers the WHOLE decision to it (single-gate,
	// extending the approve-only deferral: codex hook payloads carry
	// sanitized names the hook must not decide on,
	// see LocalPDP.Decide). Trust boundary: the flag reflects the harness's
	// MCP registration, which straza install writes and managed installs pin
	// and attest; a box owner editing their own harness config is outside the
	// agent-governance threat model (they could equally unhook straza).
	GatewayProxied bool
}

// DetectHarness picks the dialect from an explicit override, then env, then
// payload heuristics. Returns "" if undetermined.
func DetectHarness(override string, env map[string]string, payload map[string]any, adapters map[string]*Adapter) string {
	if override != "" {
		return override
	}
	// STRAZA_HARNESS names the dialect outright. Nothing straza installs
	// writes it: the conformance runner sets it per case, and a launcher for
	// a harness that sets no marker of its own can set it.
	if v := env["STRAZA_HARNESS"]; v != "" {
		return v
	}
	switch {
	case env["CLAUDE_CODE"] != "" || env["CLAUDECODE"] != "":
		return "claude-code"
	case env["CODEX_HARNESS"] != "" || env["CODEX_SANDBOX"] != "":
		return "codex"
	case env["GEMINI_CLI"] != "" || env["GOOGLE_ANTIGRAVITY"] != "":
		return "gemini"
	}
	// Heuristic: every dialect speaks snake_case now: shipping gemini
	// (hooks GA, mappings/gemini/v0.50.yaml) renamed the EVENTS instead
	// (BeforeTool/AfterAgent/…), so the event VALUE is the tell; the
	// gemini-only base field `timestamp` breaks the SessionStart/SessionEnd
	// tie. Legacy gemini/Antigravity used camelCase keys (workspaceDir).
	if v, ok := payload["hook_event_name"].(string); ok {
		if g := adapters["gemini"]; g != nil {
			_, inGemini := g.Events[v]
			inClaude := false
			if c := adapters["claude-code"]; c != nil {
				_, inClaude = c.Events[v]
			}
			if inGemini && (!inClaude || payload["timestamp"] != nil) {
				return "gemini"
			}
		}
		return "claude-code"
	}
	if _, ok := payload["workspaceDir"]; ok {
		return "gemini"
	}
	return ""
}

// eventName extracts the harness-native event name from the payload. The
// Claude/Codex family AND shipping gemini put it in "hook_event_name";
// legacy gemini/Antigravity used "event".
func eventName(payload map[string]any) string {
	for _, key := range []string{"hook_event_name", "event", "eventName", "hookEventName"} {
		if v, ok := payload[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// Normalize converts a raw hook payload into a canonical event using the
// dialect adapter. Unknown events/tools degrade to "other"
// rather than failing: a hook must always produce a decision.
func Normalize(a *Adapter, payload map[string]any) (Normalized, error) {
	native := eventName(payload)
	kind, ok := a.Events[native]
	if !ok {
		return Normalized{}, fmt.Errorf("agentguard: unmapped %s event %q", a.Harness, native)
	}

	n := Normalized{
		HarnessName:    a.Harness,
		HarnessVersion: strField(payload, "harness_version", "version"),
		SessionID:      strField(payload, a.Fields.SessionID, "session_id", "sessionId"),
		NativeEvent:    native,
	}
	if a.Fields.Prompt != "" {
		n.Prompt = strField(payload, a.Fields.Prompt)
	}
	if a.Fields.TranscriptPath != "" {
		n.TranscriptPath = strField(payload, a.Fields.TranscriptPath)
	}
	if a.Fields.PromptResponse != "" {
		n.Reply = strField(payload, a.Fields.PromptResponse)
	}
	if a.Fields.AgentTranscriptPath != "" {
		n.AgentTranscriptPath = strField(payload, a.Fields.AgentTranscriptPath)
	}
	if a.Fields.AgentReply != "" {
		n.AgentReply = strField(payload, a.Fields.AgentReply)
	}
	if a.Fields.AgentID != "" {
		n.AgentID = strField(payload, a.Fields.AgentID)
	}
	if a.Fields.AgentType != "" {
		n.AgentType = strField(payload, a.Fields.AgentType)
	}
	ev := policy.Event{
		Kind:      kind,
		Workspace: strField(payload, a.Fields.Workspace, "cwd", "workspaceDir"),
	}

	// The pinned key first, then the cross-family snake_case spelling and
	// the legacy gemini/Antigravity camelCase one; payloads never carry a
	// conflicting alternate spelling, so the fallbacks are safe (same
	// pattern as SessionID/Workspace above).
	rawToolName := strField(payload, a.Fields.ToolName, "tool_name", "toolName")
	if rawToolName != "" {
		ev.Tool, ev.App, ev.ToolName = a.classifyTool(rawToolName)
		// The gateway prefix on the RAW name is what proves the harness will
		// route this call through the Straza gateway registration (its PEP
		// then enforces server-side), recorded before the re-split discards
		// the prefix. Covers namespaced tools AND the gateway's own natives.
		n.GatewayProxied = strings.HasPrefix(rawToolName, "mcp__"+mcpServerName+"__")
		toolInput := mapField(payload, a.Fields.ToolInput, "tool_input", "args")
		a.extractToolInput(&ev, toolInput)
		// A harness-provided server/tool split beats name parsing: gemini
		// names MCP tools mcp_<server>_<tool> with single underscores
		// (unsplittable) and ships the authoritative pair in mcp_context.
		if mc := mapField(payload, a.Fields.McpContext); mc != nil {
			if app, tn := strField(mc, "server_name"), strField(mc, "tool_name"); app != "" && tn != "" {
				if app == mcpServerName {
					n.GatewayProxied = true
				}
				app, tn = splitGatewayTool(app, tn)
				ev.Tool, ev.App, ev.ToolName = policy.ToolMCPCall, app, tn
			}
		}
		// mcp.call: forward the harness tool_input verbatim as the canonical
		// `args` attribute (spec/hook-profile); the server's approval
		// fingerprint binds it (v2, binding: call). Runs AFTER the mcp_context
		// re-attribution so a gemini-named tool is already mcp.call here.
		// Absent tool_input stays nil = "args not observed", which the server
		// keys as tool scope. encoding/json marshals maps with sorted keys and
		// ParsePayload preserved number literals, so the bytes are
		// deterministic.
		if ev.Tool == policy.ToolMCPCall && toolInput != nil {
			if b, err := json.Marshal(toolInput); err == nil {
				ev.Args = b
			}
		}
	}
	// Interpreter tagging (spec/hook-profile `interpreter`):
	// shell.exec events carry the interpreter they invoke so policy can
	// match interpreter rules without any model. Same DetectInterpreter the
	// server PEP uses: client- and server-side tags cannot drift.
	if ev.Tool == policy.ToolShellExec {
		ev.Interpreter = policy.DetectInterpreter(ev.Command, ev.Argv)
	}
	n.Event = ev
	return n, nil
}

// classifyTool maps a harness-native tool name to the canonical taxonomy.
// mcp__<app>__<tool> → mcp.call with app/tool parsed.
func (a *Adapter) classifyTool(name string) (tool, app, toolName string) {
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		parts := strings.SplitN(rest, "__", 2)
		app = parts[0]
		if len(parts) == 2 {
			toolName = parts[1]
		}
		app, toolName = splitGatewayTool(app, toolName)
		return policy.ToolMCPCall, app, toolName
	}
	// For non-MCP tools the canonical event does not carry the harness-native
	// name: it is dialect-specific (Bash vs Shell vs run_shell_command) and
	// belongs in the audit raw payload, not the policy-relevant event. Keeping
	// it out is what makes cross-dialect events identical.
	if canon, ok := a.Tools[name]; ok {
		return canon, "", ""
	}
	return policy.ToolOther, "", ""
}

// splitGatewayTool re-attributes a gateway-proxied MCP call to its real app.
// The Straza gateway registers as ONE MCP server (mcpServerName, the
// key `straza install` writes) whose tool names are namespaced
// <app>__<tool> (internal/server/catalog.go), so name-based classification
// lands on app="straza" + an unsplit tool, a vocabulary no policy grant
// uses. When the server key is the gateway registration and the inner name
// is namespaced, the split is exact: app names cannot contain underscores
// (manifest nameRe: lowercase alnum + hyphens), so the first "__" always
// terminates the app, mirroring the server's own catalog identity. An
// un-namespaced or degenerate inner name keeps the gateway identity: the
// documented defer-to-gateway posture (server-side enforcement still
// resolves it precisely via its target map).
func splitGatewayTool(app, toolName string) (string, string) {
	if app != mcpServerName {
		return app, toolName
	}
	if inner := strings.SplitN(toolName, "__", 2); len(inner) == 2 && inner[0] != "" && inner[1] != "" {
		return inner[0], inner[1]
	}
	return app, toolName
}

// extractToolInput pulls command/paths out of the harness tool_input using
// the adapter's per-tool field map.
func (a *Adapter) extractToolInput(ev *policy.Event, input map[string]any) {
	spec, ok := a.ToolInputs[ev.Tool]
	if !ok || input == nil {
		return
	}
	for _, key := range spec["command"] {
		if v, ok := input[key].(string); ok && v != "" {
			ev.Command = v
			break
		}
	}
	for _, key := range spec["paths"] {
		switch v := input[key].(type) {
		case string:
			if v != "" {
				ev.Paths = append(ev.Paths, v)
			}
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok && s != "" {
					ev.Paths = append(ev.Paths, s)
				}
			}
		}
		if len(ev.Paths) > 0 {
			break
		}
	}
}

func strField(payload map[string]any, keys ...string) string {
	for _, k := range keys {
		if k == "" {
			continue
		}
		if v, ok := payload[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func mapField(payload map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if k == "" {
			continue
		}
		if v, ok := payload[k].(map[string]any); ok {
			return v
		}
	}
	return nil
}

// ParsePayload decodes a raw hook stdin body into a generic map. Numbers
// decode as json.Number (literal-preserving), not float64: mcp.call tool_input
// is re-marshaled into Event.Args for the server-side approval fingerprint,
// and a float64 round-trip would mangle large-integer literals into a
// different canonical form than the gateway lane's raw bytes.
func ParsePayload(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("agentguard: parse payload: %w", err)
	}
	return m, nil
}
