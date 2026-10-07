// The sentences of a server's own roles (rung 2 of delegated server
// governance): the Roles tab of an MCP server's page, the Add role sheet,
// the owned-by chip on the Roles list, and the name check that keeps a
// server's prefix for that server. A server-owned role is named after its
// server, reaches that server alone, and is assigned by the identity
// manager, never here.
import type { AppRow } from "./api";
import { holders } from "./words";

// foldName is the fold a server's name goes through before it opens a role
// name: lower case, anything outside a to z, 0 to 9 and the hyphen becomes
// a hyphen, runs of hyphens collapse, and the ends are trimmed. It is the
// fold the server itself applies, so the preview reads as what is stored.
export function foldName(raw: string): string {
  return (raw || "").toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/-+/g, "-").replace(/^-|-$/g, "");
}

// rolePrefix opens the name of every role a server owns.
export const rolePrefix = (server: string) => foldName(server) + "-";

// roleNameOf is the stored name of a role of this server: the prefix the
// field fixes, then the typed suffix folded the same way.
export const roleNameOf = (server: string, suffix: string) => rolePrefix(server) + foldName(suffix);

// ---- the Roles tab ----

export const TAB_ROLES = "Server roles";
export const ADD_ROLE = "Add role";
export const emptyTitle = (server: string) => "No role of " + server + " yet.";
export const emptyBody = (server: string) =>
  "A role made here reaches this server only, is named " + rolePrefix(server) +
  " and a word of yours, and is assigned by your identity manager, not here. Give it the tools it should reach.";

export const TAB_HEAD = { name: "Name", role: "Role", tools: "Tools", holders: "Holders" };
export const ROLE_HELP = "Named after this server and owned by it. The name cannot change. It goes with the server if the server is removed.";
export const countLine = (n: number) => (n === 1 ? "1 role of this server" : n + " roles of this server");

export const EDIT_TOOLS = "Edit tools";
export const DELETE_ROLE = "Delete";

// frozenTools and heldDelete are the server's own refusals, shown before
// the call as the tooltip of a greyed action and after a click as the
// refusal line, so the console never promises what the server refuses.
export const frozenTools = (name: string, held: number) =>
  "The role " + name + " has " + holders(held) + ". Its tools change only by the global admin or by a new role.";
export const heldDelete = (name: string, held: number) =>
  "The role " + name + " has " + holders(held) + ". The identity manager removes them first, then delete it.";
// certifiedHint is what a global admin reads instead: they may widen a
// held role, and the sentence says what that costs.
export const certifiedHint = (held: number) =>
  (held === 1 ? "1 holder was" : held + " holders were") + " certified on the current list. Widening it changes what they were certified for.";

// unheldLine sits under the row of a role nobody holds. Nobody is assigned
// here, so the line names the identity manager and the name it shows.
// heldRule is what holding freezes, said once under the table.
export const unheldLine = (name: string) =>
  "Nobody holds it yet. Your identity manager assigns it: in midPoint it is AR:" + name + " within a sync cycle.";
export const heldRule = (global: boolean) =>
  "A held role keeps its tools" + (global ? " for its server admin" : "") + " and cannot be deleted until the identity manager removes the holders.";
// reachHead is the headline over the picture of a role's reach, split so
// the count is drawn at title size and the rest beside it. EVERY_TOOL is
// the line under the picture of a role that takes every tool, which names
// no tool.
export const reachHead = (reached: number, total: number, later: boolean) =>
  later ? ["every tool", "(" + total + "), and tools added later"] : [reached + " of " + total, total === 1 ? "tool" : "tools"];
export const EVERY_TOOL = "every tool the server lists, and tools added later";

export const deleteTitle = (name: string) => "Delete " + name + "?";
export const deleteBody = (server: string) =>
  "It leaves Straza with its access row to " + server + ". Nobody holds it, so no session loses a tool.";

// TOOLS_TAB_LINE stands where a global admin's door to an existing role's
// reach is: a server admin gives reach through a role of their own server.
export const TOOLS_TAB_LINE = "Reach for existing roles stays with the global admin. A role for this server alone: the Roles tab.";

// ---- the Add role sheet ----

export const addTitle = (server: string) => "Add a role of " + server;
export const ADD_LEDE = "It reaches this server only. Your identity manager decides who holds it.";
export const editTitle = (name: string) => "Edit the tools of " + name;
export const EDIT_LEDE = "The name cannot change. Its tools can, until someone holds it.";

export const NAME = "Name";
export const NAME_SUFFIX = "role name suffix";
export const DESCRIPTION = "Description";
export const DESCRIPTION_HINT = "Optional. Shows on the role's page and in your identity manager.";
export const CREATE_FOOTER = "Creates the role, gives it these tools and, when a call requires approval, publishes its rules, under your name on the audit chain.";
export const CREATE_ROLE = "Create role";

// previewParts is the live canonical preview under the name field, split
// so the two names are drawn as code: the name as it will be stored, and
// the name the identity manager shows.
export const previewParts = (name: string) => ({ lead: "Stored as ", stored: name, mid: ", in midPoint as ", ar: "AR:" + name, end: "." });
// cliCreateRole is the same role as one strazactl line, so the console
// teaches the grammar instead of asking for it. The owning server rides
// the --app flag, because --server is strazactl's own address flag. The
// field on the wire stays server. Every tool, and tools added later, is
// quoted so the shell hands strazactl the star itself.
export const cliCreateRole = (name: string, server: string, tools: string[], description = "") =>
  "strazactl roles create " + name + " --app " + server + " --tools " + (!tools.length ? "…" : tools[0] === "*" ? "'*'" : tools.join(",")) +
  (description ? ' --description "' + description + '"' : "");
// newRoleCommand is the same line with its blanks named, for the door at
// the end of Add MCP server, where no name or tool is picked yet.
export const newRoleCommand = (server: string) => cliCreateRole(rolePrefix(server) + "<word>", server, ["<tool>", "<tool>"]);

// ---- the Roles list and the New role page ----

// OWNED_BY is the ownership word, shared by the role page's chip and the
// Roles list's marked server, so both surfaces say one thing.
export const OWNED_BY = "owned by";
export const ownedChip = (server: string) => OWNED_BY + " " + server;
export const ownedChipTitle = (server: string) => "Defined by the admin of " + server + ", reaches it only";

// serverPrefixCheck answers the server whose prefix the typed name takes,
// the longest match when two servers share an opening, else null. The
// browser's own server list is the source and the server's refusal at
// create stays the authority.
export function serverPrefixCheck(raw: string, apps: AppRow[] | null | undefined): AppRow | null {
  const low = (raw || "").trim().toLowerCase();
  if (!low) return null;
  const matches = (apps || []).filter((a) => a.name && low.startsWith(rolePrefix(a.name)) && low.length > rolePrefix(a.name).length);
  return matches.sort((a, b) => b.name.length - a.name.length)[0] || null;
}

export const serverPrefixRefusal = (server: string) =>
  "Role names beginning with " + rolePrefix(server) + " belong to the server " + server +
  ". Create it on that server's page so it becomes server-owned.";
export const openServer = (server: string) => "Open " + server;
