// The words of rules for tools on a person's own machine: an MCP server a
// harness runs itself, which Straza has not registered, and a harness tool
// Straza does not classify. They live apart from policy-words.ts so that
// the entry page does not load them.
import type { DefaultDenials, DeniedTool } from "./api";
import { absTime } from "./words";

export const ON_MACHINES = "on people's machines";
export const ON_MACHINES_TITLE = "Not registered in Straza. A person's own harness runs this server, and a rule is the only way to let its tools run.";
export const GROUP_REGISTERED = "Registered in Straza";
export const GROUP_MACHINES = "On people's own machines";
export const NOT_SEEN = "A server not seen yet";
export const SERVER_NAME = "Server name";
export const SERVER_NAME_HINT = "The name the server has in the person's MCP settings, for example claude-in-chrome.";
export const MACHINE_SERVER_HINT = "Not registered in Straza. The person's harness starts this server, and no access row can name it, so a rule here is the only way to let it run.";
export const HARNESS_HINT = "A tool of the harness itself, or of an agent built with the kit, that Straza maps to no call type. Claude Code's LSP is one.";
export const HARNESS_NAME_HINT = "The exact name the harness gives the tool. Upper and lower case count.";
export const MCP_NAME_HINT = "The tool's own name, without the server. A * stands for any characters, as in read_*.";
export const NO_PATTERN = "A harness tool is matched by its whole name, so a pattern matches nothing. Type the name in full, for example LSP.";
export const NO_PREFIX = "Type the tool's own name, for example navigate, without the mcp__ prefix and the server's name.";
export const ADD_BY_NAME = "Add a tool by name";
export const TODAY = "Today";
export const HARNESS_COLUMN = "Harness";
export const ASKED_FOR = "Asked for and denied";
export const NOTHING_SEEN = "No call to this server was denied in the last 24 hours, so there is no tool to tick. Add its tools by name.";
export const NO_TOOL_SEEN = "No harness tool was denied in the last 24 hours. Add the tool by name.";
export const WHOLE_MACHINE_SERVER = "Straza cannot list the tools of a server on a person's machine, so this covers tools nobody here has seen. A tool that runs several actions in one call is among them.";
export const TODAY_DENIED = (n: number) => "denied " + (n === 1 ? "once" : n + " times") + ", no rule covers it";
export const NAMELESS_TOOL = "a harness tool with no name";
export const NAMELESS_HELP = "The harness sent no name, so no rule can allow this tool. Update the Straza client or the agent kit on that machine.";

// deniedLine says how often a server or a tool was denied and when last.
export const deniedLine = (count: number, last: string) => "denied " + (count === 1 ? "once" : count + " times") + ", last " + absTime(last);

// HARNESS_WORD names a harness the way the guides do.
const HARNESS_WORD: Record<string, string> = { "claude-code": "Claude Code", codex: "Codex", gemini: "Gemini", "python-sdk": "Python kit" };
export const harnessWords = (names: string[]) => names.map((n) => HARNESS_WORD[n] || n).join(", ");

// The Denied by default view of Policies.
export const DENIED_VIEW = "Denied by default";
export const DENIED_LEDE = "Calls denied in the last 24 hours because no rule covers them. Each row is a tool somebody's agent asked for. It stays denied until a rule allows it.";
export const DENIED_FOOT = "A call that a rule denied is on that policy's Decisions tab, not here. A row leaves once a newer call to the tool was decided by a rule.";
export const DENIED_COLUMN = { asked: "Asked for", runs: "Runs in", count: "Denied", last: "Last denied", record: "Newest record" };
export const DENIED_SERVERS = "MCP servers on people's own machines";
export const DENIED_SERVERS_LINE = "Not registered in Straza. The person's harness starts the server.";
export const DENIED_TOOLS = "Harness tools Straza does not classify";
export const DENIED_TOOLS_LINE = "A tool of the harness itself, or of an agent built with the kit.";
export const DENIED_NONE = "Nothing was denied by default in the last 24 hours.";
export const DENIED_CUT = "More than 200 tools were denied by default, and the list shows the first 200 the scan met.";
export const SUBJECT_DENIED = "The calls denied by default";
export const WRITE_RULE = "Write a rule";
export const writeRuleFor = (name: string) => "Write a rule for " + name;
export const WRITE_RULE_FOR_CALL = "Write a rule for this call";
export const OPEN_RECORD = "Open in Audit";
export const openRecord = (seq: number) => "Open record " + seq + " in Audit";

// doorNote says what a door from a denial answered in the wizard.
export const doorNote = (app: string, tools: string[]) =>
  "Opened from " + (app ? "denied calls to " + app : "a denied call to " + tools.join(", ")) + ". Which calls is answered already, and step 3 changes it.";

// The Who step's third answer.
export const WHO_NEW = "A new role";
export const whoNewLine = (what: string) => "an application role made with this policy, for the people who may use " + what;
export const NEW_ROLE_NAME = "Role name";
export const NEW_ROLE_HINT = "It reaches no registered server. Compose it into a business role, and your identity manager assigns it like any other role.";
export const NEW_ROLE_TAKEN = "A role has this name already. Pick it under A role, or type another name.";
export const NEW_ROLE_MISSING = "Name the role.";
export const ALSO_CREATES = "Also creates";
export const alsoCreates = (role: string) => "the application role " + role + ", in the same draft";
export const everyoneAllow = (subject: string) => "Everyone means every person and agent, whatever roles they hold. This opens " + subject + " to all of them.";
export const THESE_TOOLS = "these tools";
// newRoleDescription is the description of a role a policy makes.
export const newRoleDescription = (app: string) =>
  app ? "Holders may use " + app + ", an MCP server on their own machine, as this role's policy allows." : "Holders may use the harness tools this role's policy allows.";
// newRoleName is the name offered for it: after the server, or the name the guides use for local tools.
export const newRoleName = (app: string) => (app ? app + "-users" : "local-tools");

// serverOf finds a server of the denial list by name.
export const serverOf = (d: DefaultDenials | null, name: string) => (d ? d.servers.find((s) => s.server === name) || null : null);
// toolsOf answers the denied tools a picker offers: a server's, or the
// harness tools, without the one that has no name.
export const toolsOf = (d: DefaultDenials | null, app: string | null): DeniedTool[] => {
  if (!d) return [];
  if (app === null) return d.tools.filter((t) => t.name !== "");
  const s = serverOf(d, app);
  return s ? s.tools : [];
};
