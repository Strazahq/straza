// The sentences of the Roles area: the list, the role's page and its
// tabs, the access editor, the New role wizard. Every surface sentence is
// short; the sentence that explains a thing sits behind its help icon
// (HelpTip) or in its dialog, never as prose on a table or a sheet.
import type { ApiError, RoleRow } from "./api";
import { OUTCOME_LABEL, OUTCOME_WORD } from "./chip-words";
import { POLICY_HELP, holders, probeWords } from "./words";
import { durationWords } from "./policy-words";
import { bare, needsArea } from "./say";

export { POLICY_HELP, holders };

// ---- kinds and categories ----

export type Kind = "application" | "business" | "approver" | "straza";
export const KINDS: Kind[] = ["application", "business", "approver", "straza"];

// kindOf reads a row's kind; a row with no kind is a business role, the
// store's own default.
export function kindOf(r: Pick<RoleRow, "kind">): Kind {
  return (KINDS as string[]).includes(r.kind) ? (r.kind as Kind) : "business";
}

// isProductRole says whether the product itself made the role: the root
// role and the two enroll roles, whose names the server reserves.
export const isProductRole = (name: string) => name.toLowerCase().startsWith("straza-");

// MCP_ADMIN is the product role that administers every MCP server. Every
// install has it, and its areas read like any other admin role's.
export const MCP_ADMIN = "straza-global-mcp-admin";

// MINTED_PREFIX opens the name of every server admin role,
// mcp-admin-<namespace>-<name>.
const MINTED_PREFIX = "mcp-admin-";

// isMinted says whether the role is a server admin role, read off the name
// Straza gives it. A role's own page knows it exactly instead, from the
// servers whose admin role it is.
export const isMinted = (r: Pick<RoleRow, "kind" | "name">) => kindOf(r) === "straza" && r.name.startsWith(MINTED_PREFIX);

// isComposable says which roles a business role may compose: the application
// roles that carry tools, and the admin role of a server, so people who
// need several servers hold one business role.
export const isComposable = (r: Pick<RoleRow, "kind" | "name">) => kindOf(r) === "application" || isMinted(r);

// CATEGORY heads each of the four tables of the list: the title, and the
// one line an empty table shows.
// noMatch is what the table says when a search leaves it empty, since a
// kind that exists is not "none yet".
export const CATEGORY: Record<Kind, { title: string; none: string; noMatch: string }> = {
  application: { title: "Application roles", none: "No application roles yet.", noMatch: "No application roles match your search." },
  business: { title: "Business roles", none: "No business roles yet.", noMatch: "No business roles match your search." },
  approver: { title: "Approver roles", none: "No approver roles yet.", noMatch: "No approver roles match your search." },
  straza: { title: "Straza roles", none: "No Straza roles are available.", noMatch: "No Straza roles match your search." },
};

// KIND_HELP is the sentence behind a kind badge.
export const KIND_HELP: Record<Kind, string> = {
  application: "Grants access to MCP tools. Can be assigned directly or included in a business role.",
  business: "Combines application roles and server administration roles into one assignment.",
  approver: "Allows holders to review requests routed to this role. Does not grant tool access.",
  straza: "Grants Straza administration or device enrollment permissions. Does not grant tool access.",
};
// KIND_WORD is the full name a kind badge reads, so a reader never asks
// "application what".
export const KIND_WORD: Record<Kind, string> = { application: "application role", business: "business role", approver: "approver role", straza: "Straza role" };
export const KIND_FIXED = "Kind is fixed when the role is created.";
export const PRODUCT_ROLE = "Made by the product. It cannot be deleted.";
// mintedRole is the rest of the sentence behind a server admin role's kind badge:
// it says why the page offers no delete.
export const mintedRole = (servers: string) => "Made with the MCP server " + servers + ". Removing the server removes it.";

// list joins words the way a sentence does: "a, b and c".
export const list = (xs: string[]) => (xs.length <= 1 ? xs.join("") : xs.slice(0, -1).join(", ") + " and " + xs[xs.length - 1]);
export const tools = (n: number) => (n === 1 ? "1 tool" : n + " tools");
export const rules = (n: number) => (n === 1 ? "1 policy rule" : n + " policy rules");

export const CHECKIN_HELP = "Sessions with live updates get the role in seconds, others within 5 minutes.";

// ---- the list ----

export const SEARCH_ROLES = "Search roles";
// SEARCH_ROLES_BY is the search box's own sentence: it says that a
// description matches too, which the box did not say before.
export const SEARCH_ROLES_BY = "Search by name or description";
export const countWords = (n: number) => (n === 1 ? "1 role" : n + " roles");
export const READING_ROLES = "Reading the roles.";
export const SUBJECT_ROLES = "The roles list";
export const NEW_ROLE = "New role";
export const NO_DESCRIPTION = "No description";
export const RELOAD = "Reload";
export const RELOAD_LIST = "Reload the list";
export const RELOAD_NOW = "Reload now";
export const LOAD_MORE = "Load more";
export const COLUMNS = "Columns";
// NOT_READ stands in a cell whose own read failed, so a column the server
// did not answer never reads as a number the row does not have.
export const NOT_READ = "Unavailable";
export const NOT_READ_WHY = "Not read. The notice above says why.";
// notReadRefused says why a column is empty for a session whose role lacks
// the area its read needs, where a reload would change nothing.
export const notReadRefused = (area: string) => needsArea("This column is", area);
// sideRefused is the one sentence over the table for a side read the server
// refused, where the unreachable heading of a failed read would be untrue.
export const sideRefused = (subject: string, area: string) => needsArea(subject + " is", area);
export const CATEGORY_COLUMN = "Category";
export const NO_ROLE_IN_VIEW = "No roles match this view.";

// SUMMARY is the Reach cell of the all-roles table, which says what holding
// a role gives. A side read that failed reads Unavailable, as the Server
// and Tools cells do, and one this session may not make names the area.
export const SUMMARY = {
  toolsNotRead: NOT_READ,
  toolsRefused: needsArea("Tool access is", "apps"),
  adminRefused: needsArea("Server administration is", "apps"),
  noAccess: "No server yet",
  noIncluded: "No included roles",
  approver: "Decides approval requests",
  mcpAdmin: "Administers all MCP servers",
  allAreas: "All console areas",
  adminNotRead: NOT_READ,
  noAreas: "No console area grants",
};
export const summaryIncludes = (names: string[]) => "Includes " + names.join(", ");
export const summaryAdministers = (servers: string[]) => "Administers " + servers.join(", ");
export const summaryConsole = (areas: string[]) => "Console: " + areas.join(", ");
export const SUBJECT_REACH = "The Reach column";
export const SUBJECT_POLICY_COUNTS = "The Policies column";
// mintedFold titles the fold at the end of the Straza roles table: a box
// with many servers has one server admin role each, and the ones nobody hold
// would otherwise bury the roles people do hold.
export const mintedFold = (n: number) => "Server admin roles nobody holds (" + n + ")";

// COLUMN names the columns of every category table. The identifier column
// reads Name under a kind heading; the Application table carries Server and
// Tools where the other three carry Reach.
export const COLUMN = { name: "Name", role: "Role", description: "Description", server: "Server", tools: "Tools", reach: "Reach", holders: "Holders", policies: "Policies" };

// COLUMN_HELP is the sentence behind a column's help icon. Name,
// Description and Tools carry none, since they explain themselves.
export const COLUMN_HELP: Partial<Record<keyof typeof COLUMN, string>> = {
  server: "The MCP servers the role reaches. Owned by marks the server whose admin made the role. The role reaches only that server.",
  reach: "What holding the role gives. Policies and credentials can still restrict a call.",
  holders: "People and agents who hold the role, directly or through a business role that composes it, inside their validity window.",
  policies: "Policies that name the role. For an approver role, the live policies it decides for.",
};

export const REACH = { composes: "composes", decides: "decides approvals", everyArea: "every console area", everyServer: "every MCP server", administersEvery: "administers every MCP server", noArea: "no console area", noServer: "no server yet", console: "console" };
// administersWords reads the Reach cell of a role some server names as its
// admin role: the verb and the servers, read off the servers list.
export const administersWords = (servers: string[]) => "administers " + list(servers);
// toolsCell folds what an application role gets on each server into one
// cell: the words alone for one server, a sentence per server for more.
export const toolsCell = (rows: { server: string; words: string }[]) => (rows.length === 1 ? rows[0].words : rows.map((r) => r.server + ": " + r.words).join(". "));
export const TOOLS_NONE = "none yet";

// holdersTitle is the hover of the Holders number.
export function holdersTitle(total: number, direct: number): string {
  if (total === 0) return "No active holders.";
  if (direct >= total) return direct + " assigned directly.";
  return direct + " assigned directly, " + (total - direct) + " through another role.";
}

// policiesTitle is the hover of the Policies number: the sets an approver
// role decides for by name, or how many sets name the role.
export function policiesTitle(n: number, deciderIn?: string[]): string {
  if (deciderIn) return deciderIn.length ? "Decides for " + list(deciderIn) + "." : "No live policy names it as a decider.";
  return n === 0 ? "No policy names it." : n === 1 ? "1 policy names it." : n + " policies name it.";
}

export const roleFileName = (name: string) => "role-" + name + ".yaml";
export const exportTitle = (name: string) => "Download " + roleFileName(name);
export const exportLabel = (name: string) => "Export " + name + " as YAML";
export const EXPORT_YAML = "Export YAML";
// exportFailed is the toast of an export that did not download: what
// failed, why, and to press Export again.
export const exportFailed = (name: string, err: ApiError) =>
  "The export of " + name + " failed" + (err.unreachable ? " because Straza did not answer. Check that it is running, then try Export again." : ": " + bare(err.message) + ". Try Export again.");

// ---- the page ----

export const TAB = { access: "Access", administers: "Administers", composes: "Composes", holders: "Holders", policies: "Policies", areas: "Areas" };
// PRIMARY is the head's own act per kind. An application role's is Edit
// access, offered only where there is a server to edit access to.
export const PRIMARY: Record<Exclude<Kind, "application">, string> = { business: "Compose a role", approver: "Assign to a user", straza: "Assign to a user" };
export const DELETE_ROLE = "Delete role";
export const CHANGE_DESCRIPTION = "Change the description";
export const DESCRIPTION_LABEL = "Description";
export const READING_ROLE = "Reading the role.";
export const SUBJECT_ROLE = "This role";
export const MISSING_TITLE = "Not found";
export const missingRole = (id: string) => "Role " + id + " was not found. It may have been deleted, or the link may be incorrect.";
export const OPEN_ROLES = "Open Roles";

export const deleteTitle = (name: string) => "Delete " + name + "?";
// deleteBody is the one line of consequence: who loses it, what goes with
// it, and what the identity manager sees.
export function deleteBody(holderCount: number, accessRows: number): string {
  const parts = [holderCount ? "Its " + holders(holderCount) + " lose it." : "Nobody holds it."];
  if (accessRows) parts.push("Its " + (accessRows === 1 ? "1 access row goes" : accessRows + " access rows go") + " with it.");
  parts.push("Your identity manager's writes to this role start failing.");
  return parts.join(" ");
}
export const DELETE_HELP = "Your identity manager does not make it again. A live policy that names only this role is turned off. Delete that policy under Policies when you no longer need it.";
// deletedToast names the role and, from the delete's answer, every live
// policy set it turned off because the set's match named only that role.
export const deletedToast = (name: string, setsOff: string[] = []) =>
  name + " is deleted" + (setsOff.length === 0 ? "." : setsOff.length === 1 ? ", and its policy " + setsOff[0] + " is turned off." : ", and its policies " + list(setsOff) + " are turned off.");
// liveDocument names the role's live export in a sentence that says it
// could not be read, the one document a role editor's save starts from.
export const liveDocument = (role: string) => "The live document of " + role;

// ---- the Access tab ----

export const ACCESS_HEAD = { server: "Server", tools: "Tools", policy: "Policy" };
// NO_SERVER_ROLE is the Access tab of an application role that belongs to
// no server and has no row, which no door gives a server.
export const NO_SERVER_ROLE = "This role reaches no MCP server.";
// ownedNoAccess is the Access tab of a role its server owns once its row
// is gone.
export const ownedNoAccess = (server: string) => "No tool of " + server + " yet. Edit access to pick the tools this role reaches.";
export const EDIT_ACCESS = "Edit access";
export const REMOVE_ACCESS = "Remove access";
export const READING_POLICY = "reading";
export const POLICY_UNREAD = "could not be read";
export const NOT_RUNNING = "the server is not running";
export const NO_TOOL_KNOWN = "no tool is known yet";

// isGlob says whether a stored matcher list means every tool.
export const isGlob = (matchers: string[] | undefined) => !matchers || matchers.length === 0 || matchers[0] === "*";
// hasPattern says whether a stored list carries a prefix pattern the
// editor does not offer.
export const hasPattern = (matchers: string[] | undefined) => (matchers || []).some((m) => m !== "*" && m.includes("*"));

// toolsWords is the Tools cell of an access row and the review's count:
// the glob in words, every tool by name, or the names folded to a count.
export function toolsWords(matchers: string[] | undefined, total: number | null): string {
  if (isGlob(matchers)) return "every tool" + (total != null ? " (" + total + ")" : "") + ", and tools added later";
  const ms = matchers || [];
  if (hasPattern(ms)) return "tools matching " + list(ms);
  if (total != null && ms.length === total) return "every tool (" + total + ")";
  if (ms.length <= 3) return list(ms);
  return total != null ? ms.length + " of " + total + " tools" : tools(ms.length);
}
export const matchersTitle = (matchers: string[] | undefined) => (isGlob(matchers) ? "stored as *: every tool, including ones added later" : (matchers || []).join(", "));

// PolicyCounts is the Policy cell of an access row as counts per outcome,
// with the hover of the needs approval chip and of the denied chip.
export type PolicyCounts = { approval: number; denied: number; allowed: number; approvalTitle: string; deniedTitle: string };

// byWords names the policies behind a chip: "by agent-guardrails".
const byWords = (sets: string[]) => (sets.length ? "by " + list(sets) : "");

// policySummary folds one server's preview into the Policy cell: the
// counts per outcome and the two hovers, or the word of a cell that has
// no counts. The needs approval hover splits holds from tickets where
// kinds classifies them, and both hovers name the policies that decide.
export function policySummary(byTool: Record<string, { status?: string; setName?: string }> | null | undefined, kinds: Record<string, { how: "hold" | "ticket" }> = {}): string | PolicyCounts {
  if (byTool === undefined) return READING_POLICY;
  if (byTool === null) return POLICY_UNREAD;
  const n = { visible: 0, approve_gated: 0, hidden_policy: 0, not_running: 0 };
  const held = { hold: 0, ticket: 0 };
  const sets: Record<string, string[]> = { approve_gated: [], hidden_policy: [] };
  for (const t of Object.keys(byTool)) {
    const e = byTool[t];
    const st = e.status as keyof typeof n;
    if (n[st] === undefined) continue;
    n[st]++;
    if (st === "approve_gated" && kinds[t]) held[kinds[t].how]++;
    if (sets[st] && e.setName && !sets[st].includes(e.setName)) sets[st].push(e.setName);
  }
  if (n.not_running) return NOT_RUNNING;
  if (!n.visible && !n.approve_gated && !n.hidden_policy) return NO_TOOL_KNOWN;
  // The split is said only when a hold or a ticket is known, since a bare
  // count of needs approval repeats the chip.
  const split = held.hold || held.ticket ? list(heldCounts(held.hold, held.ticket, n.approve_gated - held.hold - held.ticket)) : "";
  return {
    approval: n.approve_gated,
    denied: n.hidden_policy,
    allowed: n.visible,
    approvalTitle: [split, byWords(sets.approve_gated)].filter(Boolean).join(", "),
    deniedTitle: byWords(sets.hidden_policy),
  };
}

export const removeAccessTitle = (server: string) => "Remove access to " + server + "?";
export const removeAccessBody = (role: string, server: string) => role + " loses every tool of " + server + " in every live session.";
export const staleRules = (role: string, server: string, ids: string[]) => "Policy rules in " + role + "'s own policy still name " + server + ": " + list(ids) + ". They stay and decide nothing until access is given again.";
export const REMOVE_ACCESS_HELP = "Other roles with access to the server keep it.";
export const accessRemovedToast = (role: string, server: string) => role + " no longer reaches " + server + ".";

// The first-call watcher under a server's row after a grant lands.
export const firstCallWatching = (server: string) => "Watching the audit chain for the first call to " + server + ", up to 5 minutes.";
export const firstCallHit = (server: string, who: string, tool: string, when: string, outcome: string) => "First call to " + server + ": " + (who || "someone") + " called " + tool + " " + when + ", " + outcome + ".";
export const firstCallStopped = (server: string) => "First call to " + server + ": none in 5 minutes, stopped watching.";
export const firstCallOff = (server: string) => "First call to " + server + ": not watched, because your role does not include Audit.";
export const OPEN_IN_AUDIT = "Open it in Audit";
export const OPEN_AUDIT = "Open Audit";
// outcomeWords is the outcome of an audit record in the watcher's line. An
// effect the console has no outcome word for is quoted as the server sent
// it.
export function outcomeWords(effect: string, setName?: string, ruleId?: string): string {
  if (effect === "deny") return OUTCOME_WORD.denied + (setName ? " by " + setName : "");
  if (effect === "approve" || effect === "confirm") return OUTCOME_WORD.approval + (setName ? " (" + setName + ")" : "");
  if (effect === "allow") return ruleId ? OUTCOME_WORD.allowed + " by " + (setName || ruleId) : OUTCOME_WORD.allowed + ", no policy rule";
  return effect;
}

// ---- the access editor ----

export const editTitle = (role: string, server: string) => "Edit " + role + "'s access to " + server;
export const SERVER_MISSING = "Pick the server to give access to.";
export const FILTER_TOOLS = "Filter tools";
export const shownWords = (shown: number, total: number) => shown + " of " + total + " shown";
export const NO_TOOL_MATCHES = "No tool on this server matches this filter.";
export const EDITOR_HEAD = { tool: "Tool", what: "What it does", policy: "Policy" };

// The policy choice per tool: the effect a rule has. deny is offered only
// under Every tool, and tools added later, as the exception to the glob.
export type Choice = "allow" | "approve" | "deny";
export const CHOICE_LABEL: Record<Choice, string> = { allow: OUTCOME_LABEL.allowed, approve: OUTCOME_LABEL.approval, deny: OUTCOME_LABEL.denied };
export const choiceGroup = (tool: string) => "policy for " + tool;
export const fixedWords = (word: string, set: string) => word + " (" + set + ")";
export const fixedHelp = (set: string) => "Set by a policy rule in " + set + ". Change it under Policies.";
export const SPONSOR_POOL = "the agent's sponsor";
// approverRoleWords says a role pool so that it never reads as a person.
export const approverRoleWords = (name: string) => "the approver role " + name;
export const PATTERN_NOTE = (matchers: string[]) => "This access row uses a pattern (" + list(matchers) + ") the console does not edit. Saving replaces it with the tools ticked below.";

export const SAVE_ACCESS = "Save access";
export const NOTHING_CHANGED = "Nothing changed yet.";
export const PICK_A_TOOL = "Tick at least one tool.";
export const UNCHANGED_TOOLS = "Only the policy rules change.";

// What landed: the rows of the stepwise commit and their state words.
export const LANDED = "Saved changes";
export const ROW_REMOVE = "Remove the old access row";
export const rowGrant = (words: string) => "Write the new access row: " + words;
export const rowPublish = (set: string, gates: string) => "Publish " + set + ": " + gates;
export const rowCreate = (role: string) => "Create " + role;
// rowCreateOwned is the first row of an application role, which belongs to
// the one server it reaches.
export const rowCreateOwned = (role: string, server: string) => "Create " + role + ", owned by " + server;
export const rowCompose = (other: string) => "Compose " + other;
export const STATE_WORD: Record<string, string> = { pending: "waiting", running: "working", done: "done", failed: "failed", draft: "draft", refused: "refused" };
export const DRAFT_NOTE = "Stored as a draft. It gates nothing until it is published under Policies.";
export const UNREACHABLE_STEP = "Straza is unreachable, so this step may or may not have landed";

export const accessSetName = (role: string) => role + "-access";
export const accessSetDescription = (role: string) => "Approvals and denials for the role " + role + ", written in the console";
export const setMismatch = (name: string, role: string) => name + " exists but does not match exactly this role, so nothing was written into it. Open it under Policies and set its match to " + role + ", or rename it.";
export const setUnreadable = (name: string) => name + " could not be read, so no policy rule was written. Save again once Straza answers.";
export const accessToast = (role: string, server: string) => role + " reaches " + server + " now.";
export const publishedToast = (set: string) => set + " is live.";

// ---- approval where a role gets its tools ----

// The policyset's bounds on a window, in seconds.
export const HOLD_MAX = 3600;
export const TICKET_MAX = 2592000;
export const GRANT_MAX = 86400;
export const shapeProblemWords = {
  hold: "A hold can wait at most 1 hour, so Straza would refuse this one. Use a ticket for anything longer.",
  ticket: "A ticket can wait at most 30 days for a decision, so Straza would refuse this one. Shorten the first window.",
  grant: "A granted ticket must be used within 24 hours, so Straza would refuse this one. Shorten the second window.",
};

export const WHICH_TOOLS = "Which tools";
export const WHICH_TOOLS_HELP = "What the access row stores: the names you tick, every name the server has today, or a pattern that also reaches the tools it gains later.";
export const REACH_CHOICE = {
  tick: { label: "Only the tools you tick", line: "A tool the server gains later stays out." },
  today: { label: "Every tool it has today", line: (n: number) => "All " + n + " by name. A tool it gains later stays out." },
  later: { label: "Every tool, and tools added later", line: "A new tool is reached the day it appears." },
};
export const ON_A_CALL = "On a call";
export const ON_A_CALL_HELP = "What a call to a tool does for a session holding this role: allowed, needs approval, or denied. Policy rules written here live in the role's own policy. A policy rule from another policy shows its policy and is changed there.";
export const CALL_CHOICE = {
  allow: { label: "Allow every call", line: "Another policy can still deny a tool or make it need approval." },
  every: { label: "Require approval for every call", line: "Tools added later included." },
  per: { label: "Choose per tool", line: "Allowed, needs approval or denied, tool by tool." },
};
export const approvalFor = (server: string) => "Approval for " + server;
export const APPROVAL_FOR_HELP = "One setting for every tool here that requires approval. A tool can have its own through Set for this tool in its row.";
export const EVERY_NOTE = "A tool can still be denied, or get its own approval through Set for this tool.";
export const newToolsRunWords = (server: string) => "A tool " + server + " gains later is reached and allowed without approval until someone sets it here. To make new tools need approval too, pick Require approval for every call. To keep them out, pick Only the tools you tick.";
export const NO_ALLOW_UNDER_EVERY = "A policy rule that requires approval for every call has no exceptions. Pick Choose per tool to allow this tool.";
export const DENY_NEEDS_LATER = "Denied is offered under Every tool, and tools added later. With a list of names, a tool left out is already out of reach.";
export const SET_FOR_TOOL = "Set for this tool";
export const USE_SERVER_SETTING = "Use the approval above";
export const forToolOnly = (tool: string) => "For " + tool + " only";
export const NOT_REACHED = OUTCOME_WORD.none;
export const policyReadOnly = (role: string) => "You may change which tools " + role + " reaches. The Policy column needs the right to publish policies.";
// NOT_READABLE is a read-only row's Policy word when the policies that
// decide the call could not be read, and policyUnread the note above the
// table that says which read failed.
export const NOT_READABLE = "not readable here";
export const policyUnread = (role: string, set: string, own: boolean, others: boolean) =>
  "You may change which tools " + role + " reaches. Your session cannot read " +
  (own && others ? set + " or the other policies that decide its calls" : own ? set + ", the role's own policy rules" : "the other policies that decide its calls") +
  ", so the Policy column cannot say what a call does.";
export const OTHERS_UNREAD = "Other policies could not be read, so the rows show only this role's own policy rules. A policy rule in another policy may still deny a tool or make it need approval. Reload to read them again.";
// NAME_FIRST stands in for the sentence and the rules while a new role has
// no name, and THE_NEW_ROLE names it in the read-only note.
export const NAME_FIRST = "Type the rest of the role's name above to see what its holders reach and the policy rules this writes.";
export const THE_NEW_ROLE = "the new role";
export const rulesFoldIn = (n: number, set: string) => (n === 1 ? "The policy rule this writes" : "The " + n + " policy rules this writes") + ", in " + set;

// poolWords names who decides a setting, so a role never reads as a person.
export const poolWords = (pool: string) => (pool === "sponsor" ? SPONSOR_POOL : approverRoleWords(pool));

type ShapeLike = { pool: string; how: "hold" | "ticket"; hold: number; ticket: number; grant: number };

// shapeWords says a setting in a sentence: approval by the person behind the
// agent within 2 minutes, or a ticket the approver role x grants within a
// day, after which the same call is allowed for an hour.
export const shapeWords = (s: ShapeLike) =>
  s.how === "hold"
    ? "approval by " + poolWords(s.pool) + " within " + durationWords(s.hold)
    : "a ticket " + poolWords(s.pool) + " grants within " + durationWords(s.ticket) + ", after which the same call is allowed for " + durationWords(s.grant);

// shortShapeWords is the setting in a table cell: hold, up to 2 minutes.
export const shortShapeWords = (s: ShapeLike) =>
  s.how === "hold" ? "hold, up to " + durationWords(s.hold) : "ticket within " + durationWords(s.ticket) + ", then " + durationWords(s.grant) + " to run";

// shapeLine is the line under a held tool's row: the setting and who decides.
export const shapeLine = (s: ShapeLike) => shortShapeWords(s) + ", decided by " + poolWords(s.pool);

export const POLICY_WORD = { allow: OUTCOME_WORD.allowed, deny: OUTCOME_WORD.denied };
export const heldWord = (short: string) => "needs approval: " + short;

// The reasons the rules the editor writes carry; the agent reads them.
// A rule that holds one tool names it.
export const approveGroupReason = (server: string, tools: string[]) =>
  "Straza: " + (tools.length === 1 ? tools[0] + " on " + server : "this call to " + server) + " needs approval, set in the console";
export const everyCallReason = (server: string) => "Straza: every call to " + server + " needs approval, set in the console";
export const denyGroupReason = (server: string, role: string) => "Straza: this tool is denied on " + server + " for " + role + ", set in the console";

// SentenceParts is what planSentenceParts says, computed by the model.
export type SentenceParts = {
  role: string;
  app: string;
  reach: "tick" | "today" | "later";
  total: number;
  ticked: number;
  call: "allow" | "every" | "per";
  every: string;
  allowed: number;
  shared: { count: number; words: string } | null;
  own: { tool: string; words: string }[];
  denied: string[];
  newToolsRun: boolean;
};

// planSentenceParts is the sentence under the editor: what the row reaches,
// then what a call does, the approval shape named.
export function planSentenceParts(p: SentenceParts): string {
  if (p.reach === "tick" && !p.ticked) return "No tool is ticked.";
  const reach = p.reach === "later" ? "every tool of " + p.app + ", tools added later included" : p.reach === "today" ? "every tool " + p.app + " has today (" + p.total + ")" : p.ticked + " of " + p.total + " tools of " + p.app;
  const out = ["Holders of " + p.role + " reach " + reach + "."];
  if (p.call === "every") out.push("Every call needs " + p.every + ".");
  if (p.call === "per" && p.allowed) out.push(p.allowed + (p.allowed === 1 ? " tool is" : " tools are") + " allowed.");
  if (p.shared) out.push(p.shared.count + (p.shared.count === 1 ? " needs " : " need ") + p.shared.words + ".");
  for (const o of p.own) out.push(o.tool + " needs " + o.words + ".");
  if (p.denied.length) out.push(list(p.denied) + (p.denied.length === 1 ? " is" : " are") + " denied.");
  if (p.call === "allow") out.push("No call needs approval from this role.");
  if (p.newToolsRun) out.push("A tool " + p.app + " gains later is allowed without approval until someone sets it here.");
  return out.join(" ");
}

// ---- the Composes tab ----

export const SUBJECT_COMPOSES = "The composed roles";
export const COMPOSES_EMPTY = "Nothing composed yet. Compose a role to give holders its tools.";
export const REMOVE = "Remove";
export const composeTitle = (role: string) => "Compose a role into " + role;
export const COMPOSE_PICK = "Pick a role to compose";
export const COMPOSE_MISSING = "Pick the role to compose.";
export const NO_COMPOSABLE = "No role is left to compose. Create an application role first.";
export const composeBody = (role: string, other: string) => "Holders of " + role + " gain every tool of " + other + ".";
// composeAdminBody is the same consequence for a server admin role: the holders
// administer its server and gain no tool of it.
export const composeAdminBody = (role: string, other: string) => "Holders of " + role + " gain " + other + ". They administer its MCP server and gain no tool.";
export const COMPOSE_ROLE = "Compose role";
export const uncomposeTitle = (other: string) => "Stop composing " + other + "?";
export const uncomposeBody = (role: string, other: string) => "Holders of " + role + " lose " + other + " unless they hold it another way.";
export const UNCOMPOSE = "Stop composing";
export const HOLDERS_REACH = "What holders reach";
export const HOLDERS_REACH_HELP = "Tools arrive through the application roles above. Edit them there.";
export const REACH_EMPTY = "Nothing yet. Compose an application role and its servers appear here.";
export const REACH_UNREAD = "What the holders reach could not be read. Reload to try again.";
// glob says the access row stores every tool, so the pill says that tools
// added later count too, as the Tools cell of an access row does.
export const reachPill = (n: number, via: string, glob = false) => tools(n) + " via " + via + (glob ? ", and tools added later" : "");
export const COMPOSE_HELP = "Application roles can be composed, and the admin role of an MCP server. Straza refuses a role that would compose itself through others.";
export const LEGACY_ROWS = "These direct access rows predate composition. Remove them here.";
export const composedToast = (role: string, other: string) => role + " composes " + other + " now.";
export const uncomposedToast = (role: string, other: string) => role + " no longer composes " + other + ".";

// ---- the Holders tab ----

export const SUBJECT_HOLDERS = "The holders list";
export const holdersCount = (n: number, more: boolean) => holders(n) + (more ? " shown" : "");
export const HOLDERS_TAB_HELP = "Everyone who holds the role, directly or through a business role that composes it, inside their validity window.";
// nobodyHolds is the empty Holders tab. assignable says the head offers
// Assign to a user, which it does for an approver and a Straza role.
export const nobodyHolds = (role: string, assignable: boolean) => "Nobody holds " + role + "." + (assignable ? " Assign to a user above." : "");
export const SEARCH_HOLDERS = "Search by name, email or external id";
export const GRANT_TO_USER = "Assign to a user";
export const grantTitle = (role: string) => "Assign " + role + " to a user";
export const USER_LABEL = "User";
export const USER_MISSING = "Pick the user to assign the role to.";
export function grantBody(kind: Kind, user: string, role: string): string {
  if (kind === "application") return user + " gets " + role + ".";
  if (kind === "business") return user + " holds " + role + " and reaches what it composes.";
  if (kind === "approver") return user + " answers approval requests from the next check-in.";
  return user + " gets " + role + "'s rights in this console from the next check-in.";
}
export const GRANT_HELP = CHECKIN_HELP + " The assignment has no end date.";
export const DRIFT_SHORT = "Your identity manager owns this membership. A change made here lasts until its next sync.";
export const GRANT_ROLE = "Assign role";
export const grantedToast = (user: string, role: string) => user + " holds " + role + " now.";

// ---- the Areas tab ----

export const AREAS: [string, string][] = [
  ["identity", "Users, roles, devices, locks"],
  ["sessions", "Live sessions, write revokes them"],
  ["policy", "Policies: edit, validate, publish"],
  ["apps", "MCP servers, access rows, secrets"],
  ["audit", "The audit chain"],
  ["transcripts", "Recorded conversations"],
  ["approvals", "Waiting requests, channels, approver devices"],
  ["drafts", "Drafts of servers, roles, access and policies"],
  ["config", "Settings and the attestation registry"],
  ["tokens", "Admin API tokens, write can do anything an admin can"],
  ["changes", "The feed of changes for an identity manager"],
  ["scim", "Provisioning by an identity manager"],
];
export const LEVEL_WORD = { none: "none", read: "read", write: "read+write" };
export const AREAS_LINE = "Set in Straza's config, admin.roleAreas.";
export const EVERY_AREA = "Every area, read and write: this is the root role.";
export const NO_AREAS = "No console area is mapped to this role in Straza's config.";

// ---- the Administers tab ----

export const ADMINISTERS_HINT = "One role per server, made with it and named after it. Holders change the server, its credential and its secrets, never who reaches it and never its policies.";
export const OPEN_SERVER = "Open server";

// ---- the Policies tab ----

export const POLICIES_HELP = "Policies that name this role. Their other match criteria still apply.";
export const noPolicy = (role: string) => "No policy names " + role + ".";
export const deciderLine = (n: number) => "a decider in " + rules(n);
export const DECIDER_LINE = "decider";
export const noDecider = (role: string) => "No live policy names " + role + " as a decider.";
export const DECIDER_HELP = "The policies this role decides for. Deleting the role is refused while one of them is live.";
// postureWords folds a policy's summary into "11 policy rules: 4 allowed,
// 3 needs approval, 2 denied", each part left out at zero. A hold, a
// ticket and a confirm all need approval, so they count together.
export function postureWords(count: number | undefined, postures: Record<string, number> | undefined): string {
  if (count == null) return "";
  const p = postures || {};
  const parts: [number, string][] = [
    [p.allow || 0, OUTCOME_WORD.allowed],
    [(p.hold || 0) + (p.ticket || 0) + (p.confirm || 0), OUTCOME_WORD.approval],
    [p.deny || 0, OUTCOME_WORD.denied],
    [p.serverCheck || 0, "server check"],
    [p.classify || 0, "classifier"],
  ];
  const said = parts.filter(([n]) => n > 0).map(([n, w]) => n + " " + w);
  return rules(count) + (said.length ? ": " + said.join(", ") : "");
}
export const OPEN_POLICY = "Open";
export const LIVE = "live";
export const DRAFT = "off";
export const READING_POLICIES = "Reading the policies.";
export const SUBJECT_POLICIES = "The policies";

// ---- the New role wizard ----

export const WIZARD_TITLE = "New role";
export const STEP = { name: "Kind and name", access: "Access", compose: "Compose", review: "Review", done: "Done" };
// SERVER_STEP and SERVER_LEDE are the first step of an application role,
// which picks the server before the name the server's prefix opens.
export const SERVER_STEP = "Kind, server and name";
export const SERVER_LEDE = "Kind, server and name cannot change later.";
// LEDE is the line under a step's heading. A step whose controls say what
// it asks has none.
export const LEDE = {
  name: "The name cannot change later.",
  access: "",
  compose: "Pick the roles its holders also get.",
  review: "",
  done: "",
};
export const WIZARD_KINDS: { kind: Kind; name: string; line: string }[] = [
  { kind: "application", name: "Application role", line: "Grants access to MCP tools." },
  { kind: "business", name: "Business role", line: KIND_HELP.business },
  { kind: "approver", name: "Approver role", line: KIND_HELP.approver },
];
export const KIND_GROUP = "role kind";
export const STRAZA_NOT_HERE = "Straza roles are not made here.";
export const STRAZA_NOT_HERE_HELP = "straza-admin and the enroll roles come with the product. A delegated admin role comes from strazactl roles create --kind straza and an admin.roleAreas line in the config.";
export const NAME_LABEL = "Name";
// NAME_FREE is the chip under a name no role has, which a screen reader
// hears after the word Name. storedAs and importedAs are the tags beside
// it: the name as stored, and the name the identity manager imports.
export const NAME_FREE = "available";
export const storedAs = (name: string) => "stored as " + name;
export const importedAs = (iga: string) => "identity manager: " + iga;
export const NAME_MISSING = "Name the role.";
export const nameTaken = (name: string) => "A role named " + name + " already exists.";
export const OPEN_IT = "Open it";
export const nameNear = (names: string[]) => "Close to existing: " + list(names) + ".";
export const NAME_RESERVED_ADMIN = "straza-admin is the root role and Straza owns that name. Pick another name.";
export const NAME_RESERVED_PREFIX = "Names beginning with straza- are reserved for product-defined roles. Pick a name without the prefix.";
export const NAME_UNCHECKED = "Existing roles could not be read, so this name is not checked here. Saving still refuses a name that a role already has.";
export const PICK_ANOTHER = "Pick another name.";
export const DESCRIPTION_OPTIONAL = "Description (optional)";
// accessOpensOn is the Name step's line for a wizard opened from a new
// server's Check step.
export const accessOpensOn = (server: string) => "Access opens on " + server + ", the server you just added.";

export type NameCheck = { level: "error" | "warn" | "note"; text: string; exact?: RoleRow } | null;

// nameCheck is the live check of a typed name against the roles the
// browser holds: a reserved name and an exact match are hard stops, a near
// name a soft warning, an unread list a note. The server's own refusal at
// create stays the authority. prefix is the server prefix an application
// role's name opens with: every role of that server shares it, so the near
// check compares the words after it, among that server's roles only.
export function nameCheck(raw: string, roles: RoleRow[] | null | undefined, prefix = ""): NameCheck {
  const v = (raw || "").trim();
  if (!v) return null;
  const low = v.toLowerCase();
  if (low === "straza-admin") return { level: "error", text: NAME_RESERVED_ADMIN };
  if (low.startsWith("straza-")) return { level: "error", text: NAME_RESERVED_PREFIX };
  if (!roles) return { level: "note", text: NAME_UNCHECKED };
  const exact = roles.find((r) => (r.name || "").toLowerCase() === low);
  if (exact) return { level: "error", exact, text: nameTaken(exact.name) };
  const word = low.slice(prefix.length);
  if (word.length < 3) return null;
  const near = roles.filter((r) => {
    const full = (r.name || "").toLowerCase();
    if (!full.startsWith(prefix)) return false;
    const n = full.slice(prefix.length);
    return n.length >= 3 && (n.includes(word) || word.includes(n) || n.slice(0, 3) === word.slice(0, 3));
  }).slice(0, 3);
  if (near.length) return { level: "warn", text: nameNear(near.map((r) => r.name)) };
  return null;
}

export const RAIL_TITLE = "MCP servers";
// RAIL_SERVER heads the rail: one server is picked, and the rest are listed
// with their tool counts so the choice is made with the whole menu in view.
export const RAIL_SERVER = "Server";
export const ONE_SERVER_HELP = "An application role reaches one server. People who need two servers hold a business role that composes one role per server.";
export const railCount = (picked: number, total: number) => picked + "/" + total;
export const RAIL_EVERY = "every tool";
export const railLabel = (server: string) => "server " + server;
// switchWarning is the line above the editor while ticks sit on a server
// that is not the picked one: they stay in the draft and are never written.
export const switchWarning = (n: number, from: string, to: string) =>
  tools(n) + " ticked on " + from + (n === 1 ? " is" : " are") + " not saved while " + to + " is picked.";
// noToolsWhy is the second line under a rail entry with no known tools:
// the server's state in plain words, so a degraded server never vanishes.
export function noToolsWhy(row: { paused?: boolean; status?: string; detail?: string; url?: string } | undefined): string {
  if (!row) return "the server lists no tool";
  if (row.paused) return "the server is paused";
  if (row.status === "stopped") return "the server is stopped";
  if (row.status === "running") return "the server lists no tool";
  const w = probeWords(row.detail, row.url);
  return w ? w.charAt(0).toLowerCase() + w.slice(1).replace(/\.$/, "") : "the server is " + (row.status || "unknown");
}
export const noToolsKnown = (why: string) => "no tools known: " + why;
// OTHER_SERVER is the Access step's line above the editor: the server was
// picked on the first step, and that is where it changes.
export const OTHER_SERVER = "To pick another server, go back to the first step.";
export const NO_SERVER_WITH_TOOLS = "No MCP server with tools is running. Add one under MCP servers first.";

export const REVIEW_REACH = "What it reaches";
export const REVIEW_HAPPEN = "What will happen";
export const REVIEW_COMMANDS = "The same role as strazactl commands";
export const reviewCount = (picked: number, total: number) => picked + " of " + total + " tools";
export const reviewEvery = (total: number) => "every tool (" + total + "), and tools added later";
export const NOTHING_BOUND = "No server picked, so this role reaches no tool yet.";
export const NOTHING_COMPOSED = "Nothing composed, so this role reaches no tool yet.";
export const COMPOSES_ROW = "Composes";
export const APPROVER_NEXT = "Next, name it as the decider in a policy that needs approval.";
export const BACK = "Back";
export const CANCEL = "Cancel";
export const ADD_ANOTHER = "Add another role";
export const openRole = (name: string) => "Open " + name;
export const exportFile = (name: string) => "Export " + roleFileName(name);

// The canonical commands, fully substituted, for the Review fold.
export const cliCreate = (name: string, kind: string, desc: string) => "strazactl roles create " + name + " --kind " + kind + (desc ? ' --description "' + desc + '"' : "");
export const cliImply = (role: string, implied: string) => "strazactl roles implications add " + role + " " + implied;

export const DISCARD_TITLE = "Discard the new role?";
export const discardBody = (bits: string[]) => "Discarding loses " + (bits.length ? list(bits) : "the answers") + ".";
export const lossName = (name: string) => "the name " + name;
export const lossServer = (server: string) => "the tools ticked on " + server;
export const lossComposed = (n: number) => (n === 1 ? "1 composed role" : n + " composed roles");
export const KEEP_EDITING = "Keep editing";
export const DISCARD = "Discard";

export const liveRole = (name: string) => name + " is live.";
// publishNewRole titles the publish question of a role that does not
// exist yet, where "this change to" would name nothing.
export const publishNewRole = (name: string) => "Publish the new role " + name + "?";
export const AFTER_CREATE_HELP = "Assign it under Users.";

// ---- added by the access editor lane ----

export const toolTick = (tool: string) => "tool " + tool;
// STEP_SUBJECT names the step a refusal heads, so the remove dialog reads
// "Removing the old access row refused."
export const STEP_SUBJECT: Record<string, string> = { remove: "Removing the old access row" };
export const CLOSE = "Close";

// ---- added by the New role wizard lane ----

// The accessible names of the wizard's own controls, in the shape of
// toolTick and railLabel: the word for the thing, then its name.
export const roleTick = (role: string) => "role " + role;
// rowOn names the server an access row is written on, since one run of the
// wizard can write a row on more than one server.
export const rowOn = (label: string, server: string) => label + " (" + server + ")";

// ---- the doors into the access editor ----

// heldCounts says the held tools of a Policy cell: holds and tickets where
// their rule was read, needs approval for the rest.
export function heldCounts(holds: number, tickets: number, rest: number): string[] {
  const out: string[] = [];
  if (holds) out.push(holds + (holds === 1 ? " hold" : " holds"));
  if (tickets) out.push(tickets + (tickets === 1 ? " ticket" : " tickets"));
  if (rest) out.push(rest + " " + OUTCOME_WORD.approval);
  return out;
}

// heldTitle is the hover of a Policy cell's needs approval chip: its line,
// then each held tool with its window and who decides.
export const heldTitle = (summary: string, kinds: Record<string, ShapeLike>) =>
  [summary].concat(Object.keys(kinds).sort().map((t) => t + ": " + shapeLine(kinds[t]))).filter(Boolean).join("\n");

export const cliApply = (set: string) => "strazactl policy apply -f " + set + ".yaml";
export const cliActivate = (set: string) => "strazactl policy activate " + set;


// ---- the model and the commit behind the access editor ----

// EVERY_RULE_FIRST is the Policy word of a tool whose own rule sits after
// the rule for every call in the role's own set, where it never decides.
export const EVERY_RULE_FIRST = "needs approval: the policy rule for every call comes first";
export const setUnparsed = (name: string) => name + " does not parse, so no policy rule was written. Fix it under Policies, then save again.";
// A save that leaves the role's own set with no rule deletes the set,
// because the server refuses a set with none.
export const rowRetire = (set: string) => "Delete " + set + ": no policy rule is left in it";
export const retireHalf = (set: string) => set + " is turned off, so it decides nothing, but it was not deleted.";
export const retireKept = (set: string) => set + " would be left with no policy rule, and Straza refuses a policy with none. It also turns on recording or carries a check written in Rego, so it was left as it is. Open it under Policies and decide what stays.";

// ---- the access editor ----

// NAMES_ONLY is the line of the third Which tools card for a server admin,
// whose access row the server refuses as a glob. LATER_LOCKED is its line
// on a role a global admin gave every tool, which the server admin may
// narrow and never widen again.
const LATER_GLOBAL = "Only a global admin turns tools added later on or off.";
export const NAMES_ONLY = LATER_GLOBAL + " Pick Every tool it has today, and add a new tool here when the server gains one.";
export const LATER_LOCKED = LATER_GLOBAL + " Tick tools below to narrow this role to them.";
