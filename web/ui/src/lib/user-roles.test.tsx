import { describe, expect, it } from "vitest";
import type { AssignmentRow, BindingRow, RoleRow, ToolRow } from "./api";
import { approverRoles, consoleFact, givesLine, offerGroups, roleLines } from "./user-roles";
import { DECIDES_REQUESTS, NOT_FOR_AGENT, NO_SERVER_TOOL } from "./user-words";

const NOW = Date.parse("2026-11-14T10:00:00Z");

const gh: RoleRow = { id: "r-gh", name: "github-readers", kind: "application", description: "Reads GitHub." };
const demo: RoleRow = { id: "r-demo", name: "demo-tools-sandbox", kind: "application" };
const audio: RoleRow = { id: "r-audio", name: "audio-gen", kind: "application", description: "Speech on the person's machine." };
const bare: RoleRow = { id: "r-bare", name: "qa-seat", kind: "application" };
const pe: RoleRow = { id: "r-pe", name: "platform-engineer", kind: "business", implies: ["github-readers", "demo-tools-sandbox"] };
const empty: RoleRow = { id: "r-empty", name: "new-seat", kind: "business" };
const sec: RoleRow = { id: "r-sec", name: "sec-approvers", kind: "approver" };
const admin: RoleRow = { id: "r-admin", name: "straza-admin", kind: "straza", areas: ["full"], description: "Straza administration" };
const auditor: RoleRow = { id: "r-aud", name: "auditor", kind: "straza", areas: ["audit:read", "sessions:read"] };
const enroll: RoleRow = { id: "r-enroll", name: "straza-enroll", kind: "straza", areas: [] };
const minted: RoleRow = { id: "r-m", name: "mcp-admin-finance-jira", kind: "straza", areas: [] };
const roles = [gh, demo, audio, bare, pe, empty, sec, admin, auditor, enroll, minted];

const bindings: BindingRow[] = [
  { id: "b1", app: "github", role: "github-readers", tools: ["list_issues", "get_file"] },
  { id: "b2", app: "demo-tools", role: "demo-tools-sandbox", tools: ["*"] },
  { id: "b3", app: "midpoint", role: "github-readers", tools: ["get_user"] },
];
const tools: ToolRow[] = [
  ...["list_issues", "get_file", "search_code", "create_pull_request"].map((n, i) => ({ id: "g" + i, app: "github", app_id: "app-gh", name: n })),
  ...["echo", "now"].map((n, i) => ({ id: "d" + i, app: "demo-tools", app_id: "app-demo", name: n })),
  ...["get_user", "modify_user"].map((n, i) => ({ id: "m" + i, app: "midpoint", app_id: "app-mid", name: n })),
];
const assign = (id: string, role: string, extra: Partial<AssignmentRow> = {}): AssignmentRow => ({ id, subject_kind: "user", subject_id: "u1", role_id: role, origin: "admin", ...extra });

describe("roleLines", () => {
  it("lists a direct, a composed and an out-of-window role, ordered by kind then name", () => {
    const lines = roleLines(
      ["platform-engineer", "github-readers", "demo-tools-sandbox", "straza-admin"],
      [assign("a1", "r-pe", { origin: "scim" }), assign("a2", "r-admin"), assign("a3", "r-sec", { valid_to: "2026-01-01T00:00:00Z" })],
      roles,
      bindings,
      NOW,
    );
    expect(lines.map((l) => [l.name, l.direct?.id || "", l.window, l.through.join(","), l.servers.join(",")])).toEqual([
      ["straza-admin", "a2", "", "", ""],
      ["sec-approvers", "a3", "expired", "", ""],
      ["platform-engineer", "a1", "", "", ""],
      ["demo-tools-sandbox", "", "", "platform-engineer", "demo-tools"],
      ["github-readers", "", "", "platform-engineer", "github,midpoint"],
    ]);
  });

  it("makes one row of a role held from Entra and by hand, whose direct assignment is the one made here", () => {
    const lines = roleLines(["github-readers", "demo-tools-sandbox"], [assign("e1", "r-gh", { origin: "entra" }), assign("a1", "r-gh"), assign("e2", "r-demo", { origin: "entra" })], roles, bindings, NOW);
    expect(lines.map((l) => [l.name, l.direct?.id || "", l.entra?.id || "", l.through.join(",")])).toEqual([
      ["demo-tools-sandbox", "", "e2", ""],
      ["github-readers", "a1", "e1", ""],
    ]);
  });

  it("leaves the servers out when the bindings were not read, and keeps a name the catalog lacks", () => {
    const lines = roleLines(["github-readers", "gone"], [assign("a1", "r-gh")], roles, null, NOW);
    expect(lines.map((l) => [l.name, l.servers.length, l.role ? l.role.id : null])).toEqual([["github-readers", 0, "r-gh"], ["gone", 0, null]]);
  });
});

describe("the two facts", () => {
  it.each<[string, string[], string[], string]>([
    ["no Straza role with an area", ["github-readers", "straza-enroll"], [], "no area"],
    ["the root role", ["straza-admin", "github-readers"], ["straza-admin"], "every area"],
    ["two narrower roles together", ["auditor"], ["auditor"], "audit, sessions"],
  ])("reads the Console fact for %s", (_name, effective, want, areas) => {
    const fact = consoleFact(effective, roles);
    expect(fact.roles.map((r) => r.name)).toEqual(want);
    if (want.length) expect(fact.areas).toBe(areas);
  });

  it("reads the approver roles the user holds", () => {
    expect(approverRoles(["sec-approvers", "github-readers"], roles).map((r) => r.name)).toEqual(["sec-approvers"]);
    expect(approverRoles(["github-readers"], roles)).toEqual([]);
  });
});

describe("offerGroups", () => {
  it("groups the roles not held directly by kind, application first, and drops an empty group", () => {
    const groups = offerGroups([gh, pe, sec, admin, minted], [assign("a1", "r-gh"), assign("a2", "r-sec", { valid_to: "2026-01-01T00:00:00Z" })], "", NOW);
    expect(groups.map((g) => [g.kind, g.roles.map((r) => r.name)])).toEqual([
      ["business", ["platform-engineer"]],
      ["approver", ["sec-approvers"]],
      ["straza", ["mcp-admin-finance-jira", "straza-admin"]],
    ]);
  });

  it("keeps the roles whose name or description holds the search", () => {
    expect(offerGroups(roles, [], "github", NOW).flatMap((g) => g.roles.map((r) => r.name))).toEqual(["github-readers"]);
    expect(offerGroups(roles, [], "ADMINISTRATION", NOW).flatMap((g) => g.roles.map((r) => r.name))).toEqual(["straza-admin"]);
    expect(offerGroups(roles, [], "nothing like this", NOW)).toEqual([]);
  });
});

describe("givesLine", () => {
  it.each<[string, RoleRow, boolean, BindingRow[] | null, { text: string; disabled: boolean } | null]>([
    ["an application role on two servers", gh, false, bindings, { text: "2 of 4 tools on github and 1 of 2 tools on midpoint.", disabled: false }],
    ["an application role on every tool", demo, false, bindings, { text: "Every tool on demo-tools.", disabled: false }],
    ["an application role with no binding and a description", audio, false, bindings, { text: "Speech on the person's machine.", disabled: false }],
    ["an application role with no binding and no description", bare, false, bindings, { text: NO_SERVER_TOOL, disabled: false }],
    ["an application role for a reader without the servers", gh, false, null, { text: "Reads GitHub.", disabled: false }],
    ["a business role", pe, false, bindings, { text: "Includes github-readers and demo-tools-sandbox.", disabled: false }],
    ["a business role that includes nothing", empty, false, bindings, { text: "Includes no role yet.", disabled: false }],
    ["an approver role for a person", sec, false, bindings, { text: DECIDES_REQUESTS, disabled: false }],
    ["an approver role for an agent", sec, true, bindings, { text: DECIDES_REQUESTS + " " + NOT_FOR_AGENT, disabled: true }],
    ["a Straza role with a description", admin, false, bindings, { text: "Straza administration", disabled: false }],
    ["a server admin role with none", minted, false, bindings, null],
  ])("says what %s gives", (_name, role, machine, b, want) => {
    expect(givesLine(role, machine, b, b ? tools : null)).toEqual(want);
  });
});
