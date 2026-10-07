// Words for the facts about one MCP server: its transport, how its
// credential travels, the tools it exposes and its limits. The Overview
// cards, the Change sheets and the list's quick sheet all read them here,
// so every surface names a fact the same way.
import type { Credential } from "./api";

export const NO_DESCRIPTION = "No description in its manifest.";

const TRANSPORT: Record<string, string> = {
  remote: "HTTP, streamable",
  command: "stdio, a process Straza starts on the strazad host",
  oci: "A container Straza runs with docker on the strazad host",
};

// transportWords names how Straza reaches a server of a runtime kind.
export function transportWords(kind: string): string {
  return TRANSPORT[kind] || "The " + kind + " runtime";
}

// KIND_NAME is the short name of each credential kind, as the cards say it.
export const KIND_NAME: Record<string, string> = {
  none: "None",
  static: "One shared secret",
  token: "Each caller's own token",
  oauth: "Each caller's own sign-in",
};

const SECRET = "{{secret}}";

// sentWords says how the credential travels, in words: a header shape, a
// named header, an environment variable, or a template the cards cannot
// write, which is quoted as it stands.
export function sentWords(cred: Credential | null | undefined): string {
  const kind = (cred && cred.kind) || "none";
  if (kind === "none") return "Nothing.";
  const who = kind === "token" || kind === "oauth" ? "the person's token" : "the secret";
  const inj = (cred && cred.inject) || {};
  const name = inj.name || "";
  const template = inj.template || SECRET;
  if (inj.as === "env") return "The environment variable " + name + ", which the process reads.";
  if (name === "Authorization" && template === "Bearer " + SECRET) return "The Authorization header, as Bearer and " + who + ".";
  if (name === "Authorization" && template === "Basic " + SECRET) return "The Authorization header, as Basic and " + who + ".";
  if (template === SECRET) return "The header " + name + ", carrying " + who + ".";
  return "The header " + name + ", written as " + template.split(SECRET).join("<" + who + ">") + ".";
}

// exposeWords says which of a server's tools Straza lets through.
export function exposeWords(tools: string[] | undefined): string {
  const t = tools || [];
  if (t.length === 0 || (t.length === 1 && t[0] === "*")) return "All tools, including ones it adds later.";
  return "Only " + t.join(", ") + ".";
}

// rpsWords says the manifest's rate limit, counted per session.
export function rpsWords(rps: number | undefined): string {
  return rps && rps > 0 ? rps + " calls per second, per session." : "Not limited.";
}

// timeoutWords says the per-call ceiling: the manifest's own, else the
// server-wide default when the config answered, else the default unnamed.
export function timeoutWords(seconds: number | undefined, serverWide: number | null): string {
  if (seconds && seconds > 0) return seconds + " s per call.";
  return serverWide ? serverWide + " s per call, the server-wide default." : "The server-wide default.";
}

// fileParts splits the path of the apps directory file that names a server
// into the file's name and its directory.
export function fileParts(path: string): { base: string; dir: string } {
  const i = path.lastIndexOf("/");
  return i < 0 ? { base: path, dir: "" } : { base: path.slice(i + 1), dir: path.slice(0, i) || "/" };
}

// The Administered by card and the two locked panels of delegated MCP
// administration: every server names the role that may change it, made at
// registration.
export const ADMIN_ROLE_HELP = "The Straza role whose holders may change this server: its connection, credential, settings and secrets, and pause it. Never who reaches it, never its policies, and never its removal, which stays with a global admin. It was made with the server and goes with it.";
export const ADMIN_ROLE_HINT = "One role per server. Give it to people or to a business role in your identity manager.";
export const ABOVE_IT = "straza-admin and straza-global-mcp-admin administer every server, this one included.";
export const NOBODY_HOLDS = "Nobody yet.";
export const HOLDERS_NEXT = "They change the server from their next check-in, at most 5 minutes after an assignment.";
export const HOLDERS_NOT_READABLE = "Not readable with this account: listing who holds a role needs the scope identity:read.";
export const HOLDERS_NOT_READ = "Not read: the user list did not answer.";

// adminRoleSub is the line under the admin role: where it came from and,
// once the holders are read, how many people hold it.
export function adminRoleSub(holders: number | null): string {
  const held = holders === null ? "" : " Held by " + (holders === 1 ? "1 person" : holders + " people") + ", assigned in your identity manager.";
  return "Made for this server at registration and named after it." + held;
}

// serverAdminLine says, above the list of a session that holds admin roles
// and no apps grant, what it administers and who registers new servers.
export function serverAdminLine(count: number, roles: string[]): string {
  const n = count === 1 ? "1 server" : count + " servers";
  const through = roles.length ? " through " + roles.join(", ") : "";
  return "You administer " + n + through + " and may change " + (count === 1 ? "it" : "them") + ". Registering a new server or importing one from the registry needs straza-global-mcp-admin.";
}

// lockedServerTitle heads the panel for a server this account does not
// administer; the body is the server's own refusal sentence.
export function lockedServerTitle(name: string): string {
  return name + " is not available to this account.";
}

// lockedAreaLine is the first sentence of an area's locked panel for a
// session that administers servers and holds no area grant.
export function lockedAreaLine(count: number, label: string): string {
  return "This account administers " + (count === 1 ? "1 MCP server" : count + " MCP servers") + " and holds no area scope, which does not reach " + label + ". Ask for a wider role, or use strazactl.";
}

// REACH_NOT_READABLE replaces the Tools table's closing sentence for a
// session that may read the server but not its access rows.
export const REACH_NOT_READABLE = "Which roles reach each tool is not readable with this account, because the access rows belong to the apps area. The tools shown are the server's own list.";

// The sentences a server admin reads where a global read or verb is not
// theirs: the events of a server, the last call in the banner, the wizard
// door and the runtime options that would put a process on the host.
export const EVENTS_NOT_READABLE = "Events are not readable with this account, because the audit chain and the change feed belong to the audit and changes areas. Your own changes to this server still land on the chain under your name.";
export const LAST_CALL_NOT_READABLE = "Last call: not readable with this account.";
export const RUNTIME_NEEDS_GLOBAL = "Needs straza-global-mcp-admin: a command or container runtime is a process on the gateway host.";
export function registerNeedsGlobal(count: number): string {
  const n = count === 1 ? "1 server" : count + " servers";
  return "Registering a new server or importing one from the registry needs the role straza-global-mcp-admin. You administer " + n + " and may change " + (count === 1 ? "it" : "them") + " from the list.";
}

// ---- the status banner, the Overview cards and Test a call ----

export const BANNER = {
  title: "Server status", paused: "Paused by administrator", tools: "Tools", checked: "Last checked", runningSince: "Running since",
  statusSince: "Status since", access: "Role access", lastCall: "Last call", diagnostics: "Diagnostics",
};
export const NOT_CHECKED_YET = "Not checked yet";
// A server with no live instance is answered from its stored row, which
// carries no tool list and no status time, so those facts wait for it to
// run. A running server omits a tool list that is empty.
export const toolsWhile = (status: string) => "Not listed while " + status;
export const sinceWhile = (status: string) => "Not recorded while " + status;
export const SINCE_NOT_RECORDED = "Not recorded yet";
export const FROM_SUMMARY = " (from server summary)";
export const ROLE_ACCESS_NOT_READABLE = "Not readable with this account: which roles have access needs the scope apps:read.";
export const NO_ROLE_ACCESS = "No role has access yet";

export const CARD = {
  connection: { title: "Connection", hint: "Transport and address" },
  credential: { title: "Server authentication", hint: "Credential type, ownership and delivery" },
  settings: { title: "Tools and limits", hint: "Exposed tools, rate limit and timeout" },
  admin: { title: "Server administration", hint: "Who can change this server" },
};
export const OTHER_ADMIN_ROLES = "Other administrator roles";
export const CHECKS_LABEL = "Access and policy checks";
export const ACCESS_CHECK = { title: "Role access", line: "Access rows give roles access to this server and say which of its tools each role reaches.", action: "Inspect tool access" };
export const POLICY_CHECK = { title: "Policy checks", line: "Policies can add approval requirements or deny a call. Test the outcome for a specific user or role.", action: "Test a call" };

// The verdict words of Test a call, and the line under its answer.
export const OUTCOME = {
  deny: "Denied", approve: "Needs approval", checks: "Requires additional checks", allow: "Allowed",
  serverCheck: "Server check required", classify: "Classification required",
};
export const unknownOutcome = (effect: string) =>
  "No verdict: the server answered the effect " + (effect || "none") + ", which this console cannot read. The line below holds the full answer.";
export const SIMULATION_ONLY = " · Simulation only. No tool call was executed. Role access was checked before policy evaluation.";

// ---- the Check step's door into New role ----

// The card at the end of Add MCP server for a server that runs: nobody
// reaches it until an application role gives access, so the one act offered
// is a role for it, with the same role as one command in a fold.
export const nobodyReaches = (server: string) => "Nobody reaches " + server + " yet.";
export const NOBODY_REACHES_LINE = "An application role gives access to its tools and says which calls need a person's approval.";
export const createRoleFor = (server: string) => "Create a role for " + server;
export const SAME_AS_COMMANDS = "The same as commands";
