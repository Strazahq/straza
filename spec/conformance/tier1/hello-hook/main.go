// Command hello-hook is the external Tier-1 reference implementation:
// a minimal hook PEP written against nothing but the published spec,
// spec/hook-profile/SPEC.md (dialects, decision encodings, fail-closed)
// and spec/conformance/tier1/policy.yaml (the enforced rules, transcribed
// below by hand as any third-party implementation would).
//
// It passes `strazactl spec conformance --suite hook-profile --cmd hello-hook`
// and exists to prove a third party can: read one JSON payload from stdin,
// normalize the three harness dialects, decide, and answer in the caller's
// encoding. It is deliberately small; it implements exactly the published
// conformance policy, not a general engine.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

func main() {
	dialect := os.Getenv("STRAZA_HARNESS") // set by the conformance runner
	raw, err := io.ReadAll(os.Stdin)
	var payload map[string]any
	if err == nil {
		err = json.Unmarshal(raw, &payload)
	}
	if err != nil {
		// Fail closed (hook-profile SPEC.md): an unreadable payload blocks.
		fmt.Fprintln(os.Stderr, "hello-hook: unreadable payload. Denied")
		os.Exit(2)
	}
	str := func(key string) string { v, _ := payload[key].(string); return v }
	args := func(key string) map[string]any { m, _ := payload[key].(map[string]any); return m }

	if dialect == "" { // dialect autodetection per the mapping tables
		switch {
		case payload["workspaceDir"] != nil: // legacy gemini/Antigravity camelCase
			dialect = "gemini"
		case str("hook_event_name") == "BeforeTool" || str("hook_event_name") == "AfterTool" ||
			str("hook_event_name") == "BeforeAgent" || str("hook_event_name") == "AfterAgent":
			dialect = "gemini" // shipping gemini (v0.50): renamed events are the tell
		default:
			dialect = "claude-code"
		}
	}

	// Normalize: event kind, tool, command, path. Shipping gemini (v0.50
	// mapping table) speaks the same snake_case keys as claude-code/codex;
	// only legacy gemini/Antigravity used event/toolName/args.
	event, tool, command, file := str("hook_event_name"), str("tool_name"), "", ""
	input := args("tool_input")
	if dialect == "gemini" && payload["event"] != nil {
		event, tool, input = str("event"), str("toolName"), args("args")
	}
	if v, ok := input["command"].(string); ok {
		command = v
	}
	for _, k := range []string{"file_path", "absolute_path"} {
		if v, ok := input[k].(string); ok {
			file = v
		}
	}

	// Only tool.pre / permission.request block; every other event is
	// observational and must ACK WITHOUT DECIDING: claude-code and codex
	// strict-parse hook stdout per event and reject a decision document on
	// non-enforceable events (live 2026-07-31; SPEC.md §Decision encodings);
	// the correct ack there is silence. Gemini always answers strict JSON.
	enforceable := event == "PreToolUse" || event == "BeforeTool" || event == "PermissionRequest"
	if !enforceable {
		respond(dialect, event, "allow", "", false)
		return
	}

	// mcp.call identification: gemini v0.50 ships the authoritative
	// server/tool split in mcp_context (its top-level tool_name is
	// mcp_<server>_<tool> with single underscores, never split by
	// guessing); the claude-code/codex family (and legacy gemini)
	// prefix-encode mcp__<app>__<tool> instead.
	mcpApp, mcpName, isMCP := "", "", false
	if mc := args("mcp_context"); mc != nil {
		a, _ := mc["server_name"].(string)
		n, _ := mc["tool_name"].(string)
		if a != "" && n != "" {
			mcpApp, mcpName, isMCP = a, n, true
		}
	} else if rest, ok := strings.CutPrefix(tool, "mcp__"); ok {
		mcpApp, mcpName, _ = strings.Cut(rest, "__")
		isMCP = true
	}

	// The published Tier-1 conformance policy (tier1/policy.yaml), by hand.
	decision, reason := "allow", ""
	argv := strings.Fields(command)
	switch {
	case len(argv) >= 2 && argv[0] == "rm" && (argv[1] == "-rf" || argv[1] == "-fr"):
		decision, reason = "deny", "Tier-1 conformance policy: destructive commands are blocked"
	case file != "" && strings.HasPrefix(path.Base(strings.ReplaceAll(file, "\\", "/")), ".env"):
		decision, reason = "deny", "Tier-1 conformance policy: environment files are protected"
	case isMCP: // mcp.call: standalone default-deny unless granted
		if mcpApp != "github" || (!strings.HasPrefix(mcpName, "get_") && !strings.HasPrefix(mcpName, "list_")) {
			decision, reason = "deny", "hello-hook: no policy grants MCP tool "+mcpApp+"/"+mcpName
		}
	}
	respond(dialect, event, decision, reason, true)
}

// respond encodes the decision in the caller's dialect (SPEC.md §Decision
// encodings, live-corrected 2026-07-31):
//
//   - gemini reads strict JSON on stdout for every event; the exit code is
//     ignored, so the deny document IS the block.
//   - codex reads NOTHING on success (empty stdout, exit 0, for every
//     event; it rejects even a well-formed permissionDecision:"allow")
//     and blocks ONLY on exit code 2 with the reason on stderr (stdout is
//     ignored on exit 2).
//   - claude-code (and python-sdk, which mirrors it) allows non-enforceable
//     events with silence, allows enforceable ones with a hookSpecificOutput
//     document echoing the NATIVE event name (never a hardcoded one; the
//     harness validates hookEventName against the event it invoked), and
//     denies with the document plus exit code 2 and the reason on stderr.
func respond(dialect, event, decision, reason string, enforceable bool) {
	if dialect == "gemini" {
		out := map[string]any{"decision": decision}
		if reason != "" {
			out["reason"] = reason
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
		return
	}
	if decision == "deny" {
		if dialect != "codex" {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": event, "permissionDecision": "deny", "permissionDecisionReason": reason,
			}})
		}
		fmt.Fprintln(os.Stderr, reason)
		os.Exit(2)
	}
	if dialect == "codex" || !enforceable {
		return // silence is the ack
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": event, "permissionDecision": "allow",
	}})
}
