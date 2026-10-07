package e2ematrix

import (
	"fmt"
	"time"
)

// workspace is the cwd every rendered payload carries, the value the
// hook-profile fixtures use.
const workspace = "/work/proj"

// renderPayload turns a canonical event into the dialect's native hook
// payload. The shapes come from the published mapping tables under
// spec/hook-profile/mappings and the payload corpus under
// spec/conformance/hooks, never from the client's normaliser.
func renderPayload(dialect string, ev event, sessionID string) (map[string]any, error) {
	switch dialect {
	case "claude-code":
		return renderClaudeFamily(ev, sessionID, claudeTool, nil)
	case "codex":
		return renderClaudeFamily(ev, sessionID, codexTool, nil)
	case "python-sdk":
		return renderClaudeFamily(ev, sessionID, pythonTool, map[string]any{"harness_version": "0.1.0"})
	case "gemini":
		return renderGemini(ev, sessionID)
	}
	return nil, fmt.Errorf("no renderer for dialect %q", dialect)
}

// claudeEvents maps canonical kinds to the claude-code family event names
// (claude-code 2.x, codex 0.1x and python-sdk 0.1 share them).
var claudeEvents = map[string]string{
	"session.start": "SessionStart",
	"prompt.submit": "UserPromptSubmit",
	"tool.pre":      "PreToolUse",
	"tool.post":     "PostToolUse",
	"session.end":   "SessionEnd",
}

// toolRender names the native tool and shapes its tool_input.
type toolRender func(ev event) (string, map[string]any)

// claudeTool follows mappings/claude-code/2.x.yaml: Bash, Read, Write and
// the mcp__<app>__<tool> prefix form.
func claudeTool(ev event) (string, map[string]any) {
	switch ev.Tool {
	case "shell.exec":
		return "Bash", map[string]any{"command": ev.Command}
	case "file.read":
		return "Read", map[string]any{"file_path": ev.Path}
	case "file.write":
		return "Write", map[string]any{"file_path": ev.Path, "content": ev.Content}
	default:
		return mcpPrefixed(ev)
	}
}

// codexTool follows mappings/codex/0.1x.yaml: Shell is the shell hook name
// the corpus fixtures carry; Read, Write and the mcp prefix form are shared.
func codexTool(ev event) (string, map[string]any) {
	if ev.Tool == "shell.exec" {
		return "Shell", map[string]any{"command": ev.Command}
	}
	return claudeTool(ev)
}

// pythonTool follows mappings/python-sdk/0.1.yaml: the kit speaks the
// canonical taxonomy natively with tool_input keys command, paths and url,
// and the mcp prefix form.
func pythonTool(ev event) (string, map[string]any) {
	switch ev.Tool {
	case "shell.exec":
		return "shell.exec", map[string]any{"command": ev.Command}
	case "file.read", "file.write":
		return ev.Tool, map[string]any{"paths": []any{ev.Path}}
	default:
		return mcpPrefixed(ev)
	}
}

func mcpPrefixed(ev event) (string, map[string]any) {
	return "mcp__" + ev.App + "__" + ev.ToolName, mcpArgs(ev)
}

func mcpArgs(ev event) map[string]any {
	if ev.Args == nil {
		return map[string]any{}
	}
	return ev.Args
}

func renderClaudeFamily(ev event, sessionID string, tool toolRender, extra map[string]any) (map[string]any, error) {
	name, ok := claudeEvents[ev.Kind]
	if !ok {
		return nil, fmt.Errorf("event kind %q has no claude-family mapping", ev.Kind)
	}
	p := map[string]any{"hook_event_name": name, "session_id": sessionID, "cwd": workspace}
	for k, v := range extra {
		p[k] = v
	}
	switch ev.Kind {
	case "prompt.submit":
		p["prompt"] = ev.Prompt
	case "tool.pre", "tool.post":
		toolName, input := tool(ev)
		p["tool_name"] = toolName
		p["tool_input"] = input
		if ev.Kind == "tool.post" && ev.Output != "" {
			p["tool_response"] = ev.Output
		}
	case "session.end":
		if _, python := extra["harness_version"]; !python {
			p["reason"] = "other"
		}
	}
	return p, nil
}

// geminiEvents maps canonical kinds to the gemini v0.50 event names.
var geminiEvents = map[string]string{
	"session.start": "SessionStart",
	"prompt.submit": "BeforeAgent",
	"tool.pre":      "BeforeTool",
	"tool.post":     "AfterTool",
	"session.end":   "SessionEnd",
}

// renderGemini follows mappings/gemini/v0.50.yaml: snake_case base fields
// plus the dialect's own timestamp, run_shell_command, read_file (absolute_path),
// write_file (file_path), and mcp_<app>_<tool> with the authoritative pair in
// mcp_context.
func renderGemini(ev event, sessionID string) (map[string]any, error) {
	name, ok := geminiEvents[ev.Kind]
	if !ok {
		return nil, fmt.Errorf("event kind %q has no gemini mapping", ev.Kind)
	}
	p := map[string]any{
		"hook_event_name": name, "session_id": sessionID, "cwd": workspace,
		"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	switch ev.Kind {
	case "session.start":
		p["source"] = "startup"
	case "prompt.submit":
		p["prompt"] = ev.Prompt
	case "tool.pre", "tool.post":
		switch ev.Tool {
		case "shell.exec":
			p["tool_name"] = "run_shell_command"
			p["tool_input"] = map[string]any{"command": ev.Command}
		case "file.read":
			p["tool_name"] = "read_file"
			p["tool_input"] = map[string]any{"absolute_path": ev.Path}
		case "file.write":
			p["tool_name"] = "write_file"
			p["tool_input"] = map[string]any{"file_path": ev.Path, "content": ev.Content}
		default:
			p["tool_name"] = "mcp_" + ev.App + "_" + ev.ToolName
			p["tool_input"] = mcpArgs(ev)
			p["mcp_context"] = map[string]any{"server_name": ev.App, "tool_name": ev.ToolName}
		}
		if ev.Kind == "tool.post" && ev.Output != "" {
			p["tool_response"] = ev.Output
		}
	case "session.end":
		p["reason"] = "exit"
	}
	return p, nil
}
