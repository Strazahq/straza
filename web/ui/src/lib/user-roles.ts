// The roles side of a user's page: the rows of the Roles list, the two
// facts under the Access table, and what the Assign a role dialog offers
// with the line that says what each role gives.
import type { AssignmentRow, BindingRow, RoleRow, ToolRow } from "./api";
import { consoleAreasWords } from "./config-words";
import { type Kind, kindOf } from "./role-words";
import { directRoles, throughOf } from "./user-access";
import { DECIDES_REQUESTS, NOT_FOR_AGENT, NO_SERVER_TOOL, givesTools, includesRoles, toolsOn, windowWord } from "./user-words";
import { matchesTool } from "./words";

// RoleLine is one row of the Roles list. role is null when the catalog does
// not hold the name, so the row shows the name alone. direct is the
// assignment behind a row the user holds directly or one outside its
// window, never one from Entra, which is entra. A role held from Entra and
// by hand is one row with both. through names the assigned roles a role
// held only through others comes through.
export type RoleLine = {
  key: string;
  name: string;
  role: RoleRow | null;
  servers: string[];
  direct: AssignmentRow | null;
  entra?: AssignmentRow | null;
  window: "" | "expired" | "not yet valid";
  through: string[];
};

// KIND_RANK orders the Roles list: Straza, approver, business, application.
const KIND_RANK: Record<Kind, number> = { straza: 0, approver: 1, business: 2, application: 3 };

const rank = (l: RoleLine) => (l.role ? KIND_RANK[kindOf(l.role)] : 4);

// serversOf names the servers a role is bound to, in name order, or none
// when the bindings were not read.
const serversOf = (name: string, bindings: BindingRow[] | null) =>
  [...new Set((bindings || []).filter((b) => b.role === name).map((b) => b.app))].sort();

// roleLines lists every role the user holds, plus every direct assignment
// outside its validity window, ordered by kind and then by name.
export function roleLines(effective: string[], assignments: AssignmentRow[], roles: RoleRow[], bindings: BindingRow[] | null, now: number = Date.now()): RoleLine[] {
  const byName = new Map(roles.map((r) => [r.name, r]));
  const byID = new Map(roles.map((r) => [r.id, r]));
  const direct = directRoles(assignments, roles, now);
  const live = (name: string, entra: boolean) =>
    assignments.find((a) => byID.get(a.role_id)?.name === name && windowWord(a, now) === "" && (a.origin === "entra") === entra) || null;
  const lines: RoleLine[] = effective.map((name) => {
    const row = live(name, false);
    const fromEntra = live(name, true);
    return { key: "role:" + name, name, role: byName.get(name) || null, servers: serversOf(name, bindings), direct: row, entra: fromEntra, window: "", through: row || fromEntra ? [] : throughOf(name, direct, roles) };
  });
  for (const a of assignments) {
    const window = windowWord(a, now);
    if (!window) continue;
    const role = byID.get(a.role_id) || null;
    const name = role ? role.name : a.role_id;
    lines.push({ key: "row:" + a.id, name, role, servers: serversOf(name, bindings), direct: a, window, through: [] });
  }
  return lines.sort((x, y) => rank(x) - rank(y) || x.name.localeCompare(y.name));
}

const heldOf = (effective: string[], roles: RoleRow[]) => {
  const byName = new Map(roles.map((r) => [r.name, r]));
  return effective.map((n) => byName.get(n)).filter((r): r is RoleRow => !!r);
};

// consoleFact reads the Console fact: the user's Straza roles that open an
// area of the console, and the areas of all of them together in words.
export function consoleFact(effective: string[], roles: RoleRow[]): { roles: RoleRow[]; areas: string } {
  const held = heldOf(effective, roles).filter((r) => kindOf(r) === "straza" && (r.areas || []).some((a) => a.split(":")[0]));
  return { roles: held, areas: consoleAreasWords(held.flatMap((r) => r.areas || [])) };
}

// approverRoles are the approver roles the user holds.
export const approverRoles = (effective: string[], roles: RoleRow[]) => heldOf(effective, roles).filter((r) => kindOf(r) === "approver");

// OFFER_ORDER is the order of the dialog's groups.
export const OFFER_ORDER: Kind[] = ["application", "business", "approver", "straza"];

// offerGroups groups the roles the user does not hold directly inside a
// validity window by kind, as the Roles list of the Roles area does, and
// keeps those whose name or description holds the needle. A group with no
// role is left out.
export function offerGroups(roles: RoleRow[], assignments: AssignmentRow[], needle: string, now: number = Date.now()): { kind: Kind; roles: RoleRow[] }[] {
  const held = new Set(assignments.filter((a) => windowWord(a, now) === "").map((a) => a.role_id));
  const q = needle.trim().toLowerCase();
  const offer = roles.filter((r) => !held.has(r.id) && (!q || r.name.toLowerCase().includes(q) || (r.description || "").toLowerCase().includes(q)));
  return OFFER_ORDER
    .map((kind) => ({ kind, roles: offer.filter((r) => kindOf(r) === kind).sort((a, b) => a.name.localeCompare(b.name)) }))
    .filter((g) => g.roles.length > 0);
}

// givesLine says what a role gives, under its name in the dialog. bindings
// and tools are null when they were not read. disabled marks an approver
// role offered to a machine, which the console does not assign.
export function givesLine(role: RoleRow, machine: boolean, bindings: BindingRow[] | null, tools: ToolRow[] | null): { text: string; disabled: boolean } | null {
  const kind = kindOf(role);
  if (kind === "business") return { text: includesRoles(role.implies || []), disabled: false };
  if (kind === "approver") return machine ? { text: DECIDES_REQUESTS + " " + NOT_FOR_AGENT, disabled: true } : { text: DECIDES_REQUESTS, disabled: false };
  if (kind === "straza") return role.description ? { text: role.description, disabled: false } : null;
  const mine = bindings && tools ? bindings.filter((b) => b.role === role.name).sort((a, b) => a.app.localeCompare(b.app)) : [];
  if (!mine.length || !tools) return { text: role.description || NO_SERVER_TOOL, disabled: false };
  // A server that lists no tool yet has nothing to count, so it is left out.
  const parts = mine.flatMap((b) => {
    const all = tools.filter((t) => t.app === b.app);
    return all.length ? [toolsOn(all.filter((t) => matchesTool(b.tools, t.name)).length, all.length, b.app)] : [];
  });
  if (!parts.length) return { text: role.description || NO_SERVER_TOOL, disabled: false };
  return { text: givesTools(parts), disabled: false };
}
