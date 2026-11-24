// What one user reaches on the registered MCP servers and on the servers
// on the person's machine that the user's policies name, read off the user
// lane of the catalog preview, and which of the user's assigned roles each
// registered server comes through.
import type { AssignmentRow, BindingRow, PreviewAnswer, PreviewEntry, PreviewMachine, RoleRow } from "./api";
import { windowWord } from "./user-words";

// AccessRow is one row of a user's Access table: a registered server, or a
// server on the person's machine.
export type AccessRow = ServerAccess | MachineAccess;

// ServerAccess is a registered server. total counts every tool of the
// server the preview names, reached or not. A server that is not running
// carries no tool.
export type ServerAccess = {
  server: string;
  onMachine?: false;
  running: boolean;
  total: number;
  allowed: string[];
  approval: string[];
  denied: string[];
  through: string[];
};

// MachineAccess is a server on the person's machine. Its tools are the ones
// the policy rules name one by one, open says a rule reaches further tools
// of it, and policies names the policies that name it or decide its
// tools. The whole of the server is not known, so it carries no total, and
// no role binds it.
export type MachineAccess = {
  server: string;
  onMachine: true;
  open: boolean;
  allowed: string[];
  approval: string[];
  denied: string[];
  policies: string[];
};

// directRoles names the roles the user is assigned directly, inside each
// assignment's validity window.
export function directRoles(assignments: AssignmentRow[], roles: RoleRow[], now: number = Date.now()): Set<string> {
  const byID = new Map(roles.map((r) => [r.id, r.name]));
  const out = new Set<string>();
  for (const a of assignments) {
    const name = byID.get(a.role_id);
    if (name && windowWord(a, now) === "") out.add(name);
  }
  return out;
}

// reaches says whether the role from implies the role to, directly or
// through further roles.
function reaches(from: string, to: string, implies: Map<string, string[]>): boolean {
  const seen = new Set<string>([from]);
  const walk = [from];
  while (walk.length) {
    for (const next of implies.get(walk.pop() as string) || []) {
      if (next === to) return true;
      if (!seen.has(next)) { seen.add(next); walk.push(next); }
    }
  }
  return false;
}

// throughOf walks a held role back to the roles the user is assigned. A
// role assigned directly stands for itself. A role held only because an
// assigned role composes it answers every assigned role that does, in name
// order.
export function throughOf(role: string, direct: Set<string>, roles: RoleRow[]): string[] {
  if (direct.has(role)) return [role];
  const implies = new Map(roles.map((r) => [r.name, r.implies || []]));
  return [...direct].filter((d) => reaches(d, role, implies)).sort();
}

// Holding is how one user holds one role: the origins of the user's
// assignments of the role inside their window, entra, scim or admin, and,
// when there are none, the assigned roles that compose it.
export type Holding = { origins: string[]; through: string[] };

// holdingOf reads a user's holding of role out of every assignment.
export function holdingOf(userID: string, role: RoleRow, assignments: AssignmentRow[], roles: RoleRow[], now: number = Date.now()): Holding {
  const mine = assignments.filter((a) => a.subject_id === userID && windowWord(a, now) === "");
  const origins = [...new Set(mine.filter((a) => a.role_id === role.id).map((a) => a.origin || "admin"))];
  return { origins, through: origins.length ? [] : throughOf(role.name, directRoles(mine, roles, now), roles) };
}

// heldRoles is the closure of the direct roles over implies.
function heldRoles(direct: Set<string>, roles: RoleRow[]): Set<string> {
  const implies = new Map(roles.map((r) => [r.name, r.implies || []]));
  const held = new Set(direct);
  const walk = [...direct];
  while (walk.length) {
    for (const next of implies.get(walk.pop() as string) || []) {
      if (!held.has(next)) { held.add(next); walk.push(next); }
    }
  }
  return held;
}

const TOOL_STATUS = new Set(["visible", "approve_gated", "hidden_policy", "matcher_miss", "no_binding"]);

// toolsOf names the tools of a list that carry the status, in the list's
// order.
const toolsOf = (list: PreviewEntry[], status: string) => list.filter((e) => e.status === status && e.tool).map((e) => e.tool);

// userAccess builds the Access table: one row per registered server the
// preview holds an entry for that is not no_binding, in the order of the
// server names, then one row per server on the person's machine, in the
// answer's order. The tools keep the preview's order under their outcome.
// Through names the roles that bind the server and that the user holds,
// each walked back to the role the user is assigned, each name once.
export function userAccess(answer: PreviewAnswer, bindings: BindingRow[], roles: RoleRow[], assignments: AssignmentRow[], now: number = Date.now()): AccessRow[] {
  const byServer = new Map<string, PreviewEntry[]>();
  for (const e of answer.entries || []) byServer.set(e.app, (byServer.get(e.app) || []).concat(e));
  const direct = directRoles(assignments, roles, now);
  const held = heldRoles(direct, roles);
  const rows: ServerAccess[] = [];
  for (const [server, list] of byServer) {
    if (!list.some((e) => e.status !== "no_binding")) continue;
    const through = new Set<string>();
    for (const b of bindings) {
      if (b.app === server && held.has(b.role)) for (const r of throughOf(b.role, direct, roles)) through.add(r);
    }
    rows.push({
      server,
      running: !list.some((e) => e.status === "not_running"),
      total: list.filter((e) => e.tool && TOOL_STATUS.has(e.status)).length,
      allowed: toolsOf(list, "visible"),
      approval: toolsOf(list, "approve_gated"),
      denied: toolsOf(list, "hidden_policy"),
      through: [...through].sort(),
    });
  }
  rows.sort((a, b) => a.server.localeCompare(b.server));
  return [...rows, ...(answer.machine || []).map(machineRow)];
}

// machineRow is the row of a server on the person's machine: each tool the
// rules name under its outcome, and the policies that name the server or
// decide one of its tools, each once, in name order.
function machineRow(m: PreviewMachine): MachineAccess {
  const list = m.tools || [];
  const policies = new Set([...(m.policies || []), ...list.map((e) => e.setName || "")].filter(Boolean));
  return {
    server: m.app,
    onMachine: true,
    open: !!m.open,
    allowed: toolsOf(list, "visible"),
    approval: toolsOf(list, "approve_gated"),
    denied: toolsOf(list, "hidden_policy"),
    policies: [...policies].sort(),
  };
}

// reachedCount is every tool the user can call over all servers, allowed
// and needing approval.
export const reachedCount = (rows: AccessRow[]) => rows.reduce((n, r) => n + r.allowed.length + r.approval.length, 0);
