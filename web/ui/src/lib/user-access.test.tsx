import { describe, expect, it } from "vitest";
import type { AssignmentRow, BindingRow, PreviewEntry, PreviewMachine, RoleRow } from "./api";
import { type AccessRow, type MachineAccess, type ServerAccess, directRoles, reachedCount, throughOf, userAccess } from "./user-access";

const NOW = Date.parse("2026-11-14T10:00:00Z");

const roles: RoleRow[] = [
  { id: "r-gh", name: "github-readers", kind: "application" },
  { id: "r-demo", name: "demo-tools-sandbox", kind: "application" },
  { id: "r-mid", name: "midpoint-operations", kind: "application" },
  { id: "r-pe", name: "platform-engineer", kind: "business", implies: ["github-readers", "seat"] },
  { id: "r-seat", name: "seat", kind: "business", implies: ["demo-tools-sandbox"] },
  { id: "r-sec", name: "sec-approvers", kind: "approver" },
];
const bindings: BindingRow[] = [
  { id: "b1", app: "github", role: "github-readers", tools: ["list_issues", "create_pull_request"] },
  { id: "b2", app: "demo-tools", role: "demo-tools-sandbox", tools: ["*"] },
  { id: "b3", app: "midpoint", role: "midpoint-operations", tools: ["*"] },
  { id: "b4", app: "github", role: "midpoint-operations", tools: ["get_file"] },
];
const assign = (id: string, role: string, extra: Partial<AssignmentRow> = {}): AssignmentRow => ({ id, subject_kind: "user", subject_id: "u1", role_id: role, ...extra });
const tool = (app: string, name: string, status: string): PreviewEntry => ({ app, tool: name, status, reason: "" });
const row = (server: string, extra: Partial<ServerAccess>): ServerAccess => ({ server, running: true, total: 0, allowed: [], approval: [], denied: [], through: [], ...extra });

const github = [
  tool("github", "list_issues", "visible"),
  tool("github", "create_pull_request", "approve_gated"),
  tool("github", "delete_repo", "hidden_policy"),
  tool("github", "get_file", "matcher_miss"),
];

describe("userAccess", () => {
  it.each<[string, PreviewEntry[], AssignmentRow[], AccessRow[]]>([
    [
      "sorts each tool under its outcome and counts every tool in the total",
      github,
      [assign("a1", "r-gh")],
      [row("github", { total: 4, allowed: ["list_issues"], approval: ["create_pull_request"], denied: ["delete_repo"], through: ["github-readers"] })],
    ],
    [
      "leaves out a server whose only entry is no_binding",
      [{ app: "jira", tool: "", status: "no_binding", reason: "" }, ...github],
      [assign("a1", "r-gh")],
      [row("github", { total: 4, allowed: ["list_issues"], approval: ["create_pull_request"], denied: ["delete_repo"], through: ["github-readers"] })],
    ],
    [
      "walks a role held through composition back to the assigned role",
      [tool("demo-tools", "echo", "visible")],
      [assign("a1", "r-pe")],
      [row("demo-tools", { total: 1, allowed: ["echo"], through: ["platform-engineer"] })],
    ],
    [
      "names both roles of a server reached through two",
      github,
      [assign("a1", "r-gh"), assign("a2", "r-mid")],
      [row("github", { total: 4, allowed: ["list_issues"], approval: ["create_pull_request"], denied: ["delete_repo"], through: ["github-readers", "midpoint-operations"] })],
    ],
    [
      "lets a role assigned directly stand for itself, even when another role composes it",
      [tool("github", "list_issues", "visible")],
      [assign("a1", "r-gh"), assign("a2", "r-pe")],
      [row("github", { total: 1, allowed: ["list_issues"], through: ["github-readers"] })],
    ],
    [
      "keeps a server that is not running, with no tool",
      [{ app: "midpoint", tool: "", status: "not_running", reason: "connection refused" }],
      [assign("a1", "r-mid")],
      [row("midpoint", { running: false, through: ["midpoint-operations"] })],
    ],
    [
      "reads every tool reached with an exception above the allowed line",
      [tool("midpoint", "modify_user", "approve_gated"), tool("midpoint", "get_user", "visible"), tool("midpoint", "list_users", "visible")],
      [assign("a1", "r-mid")],
      [row("midpoint", { total: 3, allowed: ["get_user", "list_users"], approval: ["modify_user"], through: ["midpoint-operations"] })],
    ],
    [
      "reads every tool reached with no exception",
      [tool("demo-tools", "echo", "visible"), tool("demo-tools", "now", "visible")],
      [assign("a1", "r-demo")],
      [row("demo-tools", { total: 2, allowed: ["echo", "now"], through: ["demo-tools-sandbox"] })],
    ],
    [
      "answers no row for a user who reaches nothing",
      [{ app: "github", tool: "", status: "no_binding", reason: "" }],
      [],
      [],
    ],
    [
      "orders the servers by name and ignores an assignment outside its window",
      [tool("midpoint", "get_user", "visible"), tool("demo-tools", "echo", "visible")],
      [assign("a1", "r-demo"), assign("a2", "r-mid", { valid_to: "2026-01-01T00:00:00Z" })],
      [row("demo-tools", { total: 1, allowed: ["echo"], through: ["demo-tools-sandbox"] }), row("midpoint", { total: 1, allowed: ["get_user"] })],
    ],
  ])("%s", (_name, entries, assignments, want) => {
    expect(userAccess({ entries }, bindings, roles, assignments, NOW)).toEqual(want);
  });

  it("counts the tools a user can call, allowed and needing approval, over every server", () => {
    const rows = userAccess({ entries: [...github, tool("demo-tools", "echo", "visible")] }, bindings, roles, [assign("a1", "r-gh"), assign("a2", "r-demo")], NOW);
    expect(reachedCount(rows)).toBe(3);
  });
});

const named = (name: string, status: string, setName?: string): PreviewEntry => ({ app: "elevenlabs", tool: name, status, reason: "", ...(setName ? { setName } : {}) });
const machine = (app: string, tools: PreviewEntry[], open?: boolean, policies?: string[]): PreviewMachine => ({ app, tools, ...(open ? { open } : {}), ...(policies ? { policies } : {}) });
const local = (server: string, extra: Partial<MachineAccess>): MachineAccess => ({ server, onMachine: true, open: false, allowed: [], approval: [], denied: [], policies: [], ...extra });

describe("userAccess on the servers on the person's machine", () => {
  it.each<[string, PreviewMachine[], AccessRow[]]>([
    [
      "sorts each named tool under its outcome",
      [machine("elevenlabs", [named("text_to_speech", "visible", "audio-access"), named("clone_voice", "approve_gated", "audio-access"), named("delete_voice", "hidden_policy", "audio-access")])],
      [local("elevenlabs", { allowed: ["text_to_speech"], approval: ["clone_voice"], denied: ["delete_voice"], policies: ["audio-access"] })],
    ],
    [
      "keeps a server that a pattern reaches further, with the tools named",
      [machine("elevenlabs", [named("text_to_speech", "visible", "audio-access")], true)],
      [local("elevenlabs", { open: true, allowed: ["text_to_speech"], policies: ["audio-access"] })],
    ],
    [
      "keeps a server that a pattern reaches, with no tool named",
      [machine("filesystem", [], true)],
      [local("filesystem", { open: true })],
    ],
    [
      "names each policy that decides a tool once, in name order",
      [machine("elevenlabs", [named("speak", "visible", "voice-b"), named("clone", "approve_gated", "voice-a"), named("delete", "hidden_policy", "voice-b"), named("list", "visible")])],
      [local("elevenlabs", { allowed: ["speak", "list"], approval: ["clone"], denied: ["delete"], policies: ["voice-a", "voice-b"] })],
    ],
    [
      "names the policies of a server that only a pattern reaches, from the server's own list",
      [machine("filesystem", [], true, ["files-b", "files-a"])],
      [local("filesystem", { open: true, policies: ["files-a", "files-b"] })],
    ],
    [
      "joins the policies that name the server with the ones that decide a tool, each once",
      [machine("elevenlabs", [named("speak", "visible", "voice"), named("drop_database", "hidden_policy", "floor")], false, ["voice"])],
      [local("elevenlabs", { allowed: ["speak"], denied: ["drop_database"], policies: ["floor", "voice"] })],
    ],
    [
      "keeps the answer's order",
      [machine("zeta", [], true), machine("alpha", [], true)],
      [local("zeta", { open: true }), local("alpha", { open: true })],
    ],
  ])("%s", (_name, servers, want) => {
    expect(userAccess({ entries: [], machine: servers }, bindings, roles, [], NOW)).toEqual(want);
  });

  it("puts them after the registered servers, and counts what the user can call on them", () => {
    const answer = {
      entries: [...github, tool("demo-tools", "echo", "visible")],
      machine: [machine("elevenlabs", [named("speak", "visible", "voice"), named("clone", "approve_gated", "voice"), named("delete", "hidden_policy", "voice")]), machine("filesystem", [], true)],
    };
    const rows = userAccess(answer, bindings, roles, [assign("a1", "r-gh"), assign("a2", "r-demo")], NOW);
    expect(rows.map((r) => r.server)).toEqual(["demo-tools", "github", "elevenlabs", "filesystem"]);
    expect(rows.map((r) => !!r.onMachine)).toEqual([false, false, true, true]);
    expect(reachedCount(rows)).toBe(5);
  });

  it("answers the registered servers alone for an answer without machine", () => {
    const rows = userAccess({ entries: github }, bindings, roles, [assign("a1", "r-gh")], NOW);
    expect(rows).toEqual([row("github", { total: 4, allowed: ["list_issues"], approval: ["create_pull_request"], denied: ["delete_repo"], through: ["github-readers"] })]);
  });
});

describe("throughOf and directRoles", () => {
  it.each<[string, string, string[], string[]]>([
    ["a role assigned directly", "github-readers", ["r-gh"], ["github-readers"]],
    ["a role one step down", "github-readers", ["r-pe"], ["platform-engineer"]],
    ["a role two steps down", "demo-tools-sandbox", ["r-pe"], ["platform-engineer"]],
    ["a role nobody composes", "sec-approvers", ["r-pe"], []],
  ])("walks %s back to the assigned role", (_name, role, assigned, want) => {
    const direct = directRoles(assigned.map((r, i) => assign("a" + i, r)), roles, NOW);
    expect(throughOf(role, direct, roles)).toEqual(want);
  });

  it("holds only the assignments inside their window", () => {
    const direct = directRoles([assign("a1", "r-gh"), assign("a2", "r-mid", { valid_from: "2027-01-01T00:00:00Z" })], roles, NOW);
    expect([...direct]).toEqual(["github-readers"]);
  });
});
