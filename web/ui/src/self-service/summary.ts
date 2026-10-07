// The one-line call identity of an approver row, in two readings. The
// server sends the redacted tool identity as an object (SummaryView: tool,
// app, tool_name); summaryText names it in the words a person decides by
// and machineText keeps the exact wire identity. The phone app mirrors
// summaryText byte for byte, and the service worker composes it into a
// notification, so this module imports nothing and its output never moves.

export type SummaryView = { tool?: string; app?: string; tool_name?: string };

const KIND: Record<string, string> = {
  "shell.exec": "run a shell command",
  "file.read": "read files",
  "file.write": "write files",
  "file.edit": "edit files",
  "net.fetch": "fetch a URL",
  "task.spawn": "start a background task",
  "mcp.call": "an MCP tool call",
  other: "a tool call",
};

// summaryText reads "request role in midpoint" for an MCP call, a plain
// verb phrase for a bare kind, and the raw tool string for anything else.
export function summaryText(s: SummaryView | null | undefined): string {
  if (!s || !s.tool) return "a tool call";
  if (s.tool === "mcp.call" && s.tool_name) {
    const words = s.tool_name.replace(/_/g, " ");
    return s.app ? words + " in " + s.app : words;
  }
  return KIND[s.tool] || s.tool;
}

// machineText is the exact wire identity, "mcp.call midpoint:request_role",
// the string the console, Slack and the audit record all speak.
export function machineText(s: SummaryView | null | undefined): string {
  if (!s || !s.tool) return "";
  if (s.app && s.tool_name) return s.tool + " " + s.app + ":" + s.tool_name;
  if (s.tool_name) return s.tool + " " + s.tool_name;
  return s.tool;
}
