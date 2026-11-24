// Words for the facts about one MCP server: its transport, how its
// credential travels, the tools it exposes and its limits. The Overview
// cards, the Change sheets and the list's quick sheet all read them here,
// so every surface names a fact the same way.
import type { Credential } from "./api";
import { needsArea } from "./say";

const TRANSPORT: Record<string, string> = {
  remote: "Streamable HTTP",
  command: "stdio, a process Straza starts on the strazad host",
  oci: "A container Straza runs with docker on the strazad host",
};

// transportWords names how Straza reaches a server of a runtime kind.
export function transportWords(kind: string): string {
  return TRANSPORT[kind] || "The " + kind + " runtime";
}

// inline is a label's words inside a sentence: the first letter in lower
// case, unless the label opens with a name in capitals such as HTTP.
const inline = (s: string) => (/^[A-Z][A-Z]/.test(s) ? s : s.charAt(0).toLowerCase() + s.slice(1));

// runtimeWords is transportWords inside a sentence.
export const runtimeWords = (kind: string) => inline(transportWords(kind));

// KIND_NAME is the short name of each credential kind, as the cards say it.
export const KIND_NAME: Record<string, string> = {
  none: "None",
  static: "One shared secret",
  token: "Each person's own token",
  oauth: "Each person's own sign-in",
};

// credentialWords is a credential kind's short name inside a sentence.
export const credentialWords = (kind: string) => inline(KIND_NAME[kind] || kind);

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
  if (inj.as === "env") return "Variable " + name;
  // The Authorization shapes read as the wizard's preview of what travels.
  const held = kind === "static" ? "<secret>" : "<the person's token>";
  if (name === "Authorization" && template === "Bearer " + SECRET) return "Authorization: Bearer " + held;
  if (name === "Authorization" && template === "Basic " + SECRET) return "Authorization: Basic " + held;
  if (template === SECRET) return "The header " + name + ", carrying " + who + ".";
  return "The header " + name + ", written as " + template.split(SECRET).join("<" + who + ">") + ".";
}

// exposeWords says which of a server's tools Straza lets through.
export function exposeWords(tools: string[] | undefined): string {
  const t = tools || [];
  if (t.length === 0 || (t.length === 1 && t[0] === "*")) return "All tools, including ones it adds later.";
  return "Only " + t.join(", ") + ".";
}

// rpsWords says the rate limit, counted per session: the manifest's own,
// else for a remote server the default, named when the config answered.
// A server on any other runtime with none is not limited.
export function rpsWords(rps: number | undefined, remote: boolean, remoteDefault: number | null): string {
  if (rps && rps > 0) return rps + " calls per second, per session.";
  if (!remote) return "Not limited.";
  return remoteDefault ? remoteDefault + " calls per second, per session, the default." : "The default for a remote server.";
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
export const ADMIN_ROLE_HELP = "Holders may change its connection, credential, settings and secrets, and pause it. Not who reaches it, its policies or its removal.";
export const ADMIN_ROLE_HINT = "Assign it in your identity manager.";
// ABOVE_IT names the roles that administer every server, drawn as tags.
export const ABOVE_IT = ["straza-admin", "straza-global-mcp-admin"];
export const NOBODY_HOLDS = "Nobody yet.";
export const HOLDERS_NEXT = "Takes effect within 5 minutes of an assignment.";
export const HOLDERS_NOT_READABLE = needsArea("The holders are", "identity");
export const HOLDERS_NOT_READ = "Not read, because the user list did not answer. Reload to read it again.";

// serverAdminLine says, above the list of a session that holds admin roles
// and no apps grant, what it administers and who registers new servers.
export function serverAdminLine(count: number, roles: string[]): string {
  const n = count === 1 ? "1 server" : count + " servers";
  const through = roles.length ? " through " + roles.join(", ") : "";
  return "You administer " + n + through + ". Adding a server needs straza-global-mcp-admin.";
}

// lockedServerTitle heads the panel for a server this account does not
// administer; the body is the server's own refusal sentence.
export function lockedServerTitle(name: string): string {
  return name + " is not available to this account.";
}

// REACH_NOT_READABLE stands under the Tools table for a session that may
// read the server but not its access rows.
export const REACH_NOT_READABLE = "Which roles reach each tool is not shown, because your role does not include MCP servers as a whole.";
// POLICY_NOT_READABLE stands there for a server's own admin, who reads the
// access rows of their servers but not the tool list or the policy preview,
// which are reads of the whole apps area.
export const POLICY_NOT_READABLE = "The Policy column is not shown, because your role does not include MCP servers as a whole.";

// The sentences a server admin reads where a global read or verb is not
// theirs: the events of a server, the last call in the banner, the wizard
// door and the runtime options that would put a process on the host.
export const EVENTS_NOT_READABLE = "The events are not shown, because your role does not include Audit.";
export const LAST_CALL_NOT_READABLE = "Last call: not shown, because your role does not include Audit.";
// LAST_CALL_UNREAD is the banner's last call when the audit chain did not
// answer.
export const LAST_CALL_UNREAD = "Last call: not read, because the audit chain did not answer. Reload to read it again.";
export const RUNTIME_NEEDS_GLOBAL = "Needs straza-global-mcp-admin: a command or container runtime is a process on the strazad host.";
export const REGISTER_NEEDS_GLOBAL = "Adding a server needs straza-global-mcp-admin.";

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
export const ROLE_ACCESS_NOT_READABLE = needsArea("The roles with access are", "apps");
export const NO_ROLE_ACCESS = "No role has access yet";

export const CARD = {
  connection: { title: "Connection" },
  credential: { title: "Server authentication" },
  settings: { title: "Tools and limits" },
  admin: { title: "Server administration" },
};
export const OTHER_ADMIN_ROLES = "Other administrator roles";
export const CHECKS_LABEL = "Access and policy checks";
export const ACCESS_CHECK = { title: "Role access", action: "Inspect tool access" };
export const POLICY_CHECK = { title: "Policy checks", action: "Test a call" };

// The outcome words of Test a call, and the line under its answer.
export const OUTCOME = {
  deny: "Denied", approve: "Needs approval", checks: "Needs more checks", allow: "Allowed",
  serverCheck: "Needs a server check", classify: "Needs a classification",
};
export const unknownOutcome = (effect: string) =>
  "No outcome: Straza answered " + (effect || "none") + ", which this console cannot read. The line below holds the full answer.";

// ---- the Check step's door into New role ----

// The card at the end of Add MCP server for a server that runs: nobody
// reaches it until an application role gives access, so the one act offered
// is a role for it, with the same role as one command in a fold.
export const nobodyReaches = (server: string) => "Nobody reaches " + server + " yet.";
export const CREATE_ROLE = "Create a role";
export const SAME_AS_COMMANDS = "As a strazactl command";

// ---- the server's page and its list ----

// PAUSED is the list's status chip of a server an administrator paused.
export const PAUSED = "paused";
// NO_MANIFEST_LINE stands on the Manifest tab of a server whose row carries
// no stored manifest.
export const NO_MANIFEST_LINE = "No stored manifest. It is in the file you installed or in the apps directory.";
// The Remove server dialog: what a publish of the removal deletes, with
// the roles whose access rows go when a role reaches the server, and what
// becomes of the policy rules that name it.
export const REMOVE_DELETES = ", its secrets, its own roles and its admin role are deleted.";
export const REMOVE_NO_ROLE = "No role has access to it. Its secrets, its own roles and its admin role are deleted.";
export const REMOVE_RULES_STAY = " Policy rules that name it stay and apply again if a server with this name is added.";

// ---- the Activity tab ----

export const EVENTS_EMPTY = "No events yet.";
// logLines is the log's line count, since the last check or over every
// line Straza keeps.
export const logLines = (n: number, since: boolean) => n + (n === 1 ? " line" : " lines") + (since ? " since the last check" : " kept");
export const LOG_HELP = "The last lines the server's process wrote to its error output, each stamped when Straza received it. A repeated line shows once with its count. The dashed rows mark the last health check and every status change.";
// The accessible names of the log's two filters.
export const LOG_SEVERITY = "Lines to show";
export const LOG_SPAN = "How far back";

// ---- the Change the settings sheet ----

// timeoutHint is the unit beside the per-call timeout, with what an empty
// field means.
export const timeoutHint = (serverWide: string) => "seconds. Empty means " + serverWide + ".";
// rpsHint is the unit beside a remote server's rate limit, with what an
// empty field means.
export const rpsHint = (remoteDefault: string) => "calls per second, per session. Empty means " + remoteDefault + ".";

// didNotRun heads the error block of an action on a server that never
// reached Straza, such as Recheck, where "could not be read" would be wrong.
export const didNotRun = (what: string) => what + " did not run.";
