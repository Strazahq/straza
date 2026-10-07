import { parse } from "yaml";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { type RoleSpec, accessItems, newRoleItems, roleItem } from "./role-draft";
import { type Plan, emptyPlan, planFor, readOwnRules, withChoice } from "./access-plan";
import { ApiError, type AppRow, type BindingRow, type ToolRow, getPolicy } from "./api";
import type { GrantInput } from "./grant-commit";
import { setMismatch, setUnreadable } from "./role-words";
import { savedEdit } from "./save-words";

// The items the role doors send: a Role put
// is the role's live export with one change, and the access editor adds the
// role's own set when its rules change, so the row and its gate go live
// together. A new role's document never names a pack, because a draft
// never applies one.

vi.mock("./api", async (orig) => ({ ...(await orig<typeof import("./api")>()), getPolicy: vi.fn() }));

// EXPORT is a role as GET /v1/admin/roles/{id}/export serves it: four-space
// indent, the glob quoted, and a pack the draft must carry as it is.
const EXPORT = [
  "apiVersion: straza.dev/v1beta1",
  "kind: Role",
  "metadata:",
  "    name: dev-tools",
  "spec:",
  "    kind: application",
  "    description: Tool reach for the developer seat.",
  "    server: demo-tools",
  "    bindings:",
  "        - app: demo-tools",
  "          tools:",
  "            - '*'",
  "    packs:",
  "        - house-rules",
  "",
].join("\n");

const BUSINESS = ["apiVersion: straza.dev/v1beta1", "kind: Role", "metadata:", "    name: dev", "spec:", "    kind: business", "    implies:", "        - scout-tools", ""].join("\n");

const OWN_SET = [
  "apiVersion: straza.dev/v1beta1",
  "kind: PolicySet",
  "metadata:",
  "  name: dev-tools-access",
  "spec:",
  "  priority: 100",
  "  match:",
  "    roles: [dev-tools]",
  "  rules:",
  "    - id: demo-tools-get-env-deny",
  "      tools: [mcp.call]",
  "      apps: [demo-tools]",
  "      toolNames:",
  "        deny: [get-env]",
  "      effect: deny",
  "",
].join("\n");

const app: AppRow = { id: "app-9", name: "demo-tools", runtime: "http", status: "running", reached_by: [] };
const names = ["get-env", "get-sum", "list-files"];
const tools: ToolRow[] = names.map((n, i) => ({ id: "t" + i, app: "demo-tools", app_id: "app-9", name: n }));
const glob: BindingRow = { id: "b-1", app: "demo-tools", role: "dev-tools", tools: ["*"] };
const before = planFor(glob, names, readOwnRules(OWN_SET, "demo-tools", names));
const input = (plan: Plan, patch: Partial<GrantInput> = {}): GrantInput => ({ role: { name: "dev-tools", id: "r-1" }, app, tools, plan, replace: glob, before, ...patch });

const docOf = (text: string) => parse(text) as { apiVersion: string; kind: string; metadata: { name: string }; spec: RoleSpec };
const ticked = (...tick: string[]): Plan => ({ ...emptyPlan(), picked: Object.fromEntries(tick.map((t) => [t, true])) });

describe("roleItem", () => {
  // Each case is one change and the spec the put carries after it: every
  // other field stays as the export wrote it, packs included.
  const cases: [string, string, (s: RoleSpec) => void, RoleSpec][] = [
    [
      "changes the description alone",
      EXPORT,
      (s) => { s.description = "Reach for the developer seat."; },
      { kind: "application", description: "Reach for the developer seat.", server: "demo-tools", bindings: [{ app: "demo-tools", tools: ["*"] }], packs: ["house-rules"] },
    ],
    [
      "leaves an emptied description out, as the export does",
      EXPORT,
      (s) => { s.description = ""; },
      { kind: "application", server: "demo-tools", bindings: [{ app: "demo-tools", tools: ["*"] }], packs: ["house-rules"] },
    ],
    [
      "replaces the one access row",
      EXPORT,
      (s) => { s.bindings = [{ app: "demo-tools", tools: ["get-sum"] }]; },
      { kind: "application", description: "Tool reach for the developer seat.", server: "demo-tools", bindings: [{ app: "demo-tools", tools: ["get-sum"] }], packs: ["house-rules"] },
    ],
    [
      "adds an implication to the ones the export names",
      BUSINESS,
      (s) => { s.implies = (s.implies || []).concat("dev-tools"); },
      { kind: "business", implies: ["scout-tools", "dev-tools"] },
    ],
  ];
  it.each(cases)("%s", (_case, text, change, want) => {
    const item = roleItem(text, change);
    expect(item.kind).toBe("Role");
    expect(item.op).toBe("put");
    const doc = docOf(item.doc || "");
    expect(item.name).toBe(doc.metadata.name);
    expect(doc.apiVersion).toBe("straza.dev/v1beta1");
    expect(doc.kind).toBe("Role");
    expect(doc.spec).toEqual(want);
  });
});

describe("accessItems", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(getPolicy).mockResolvedValue({ name: "dev-tools-access", status: "active", yaml: OWN_SET });
  });

  it("sends the Role put alone when the row changes and the rules do not", async () => {
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("policy set not found", 404));
    const answer = await accessItems(input(ticked("get-sum"), { replace: null, before: emptyPlan() }), EXPORT);
    if (!("items" in answer)) throw new Error(answer.error);
    expect(answer.items.map((i) => i.kind + "/" + i.name + ":" + i.op)).toEqual(["Role/dev-tools:put"]);
    expect(docOf(answer.items[0].doc || "").spec.bindings).toEqual([{ app: "demo-tools", tools: ["get-sum"] }]);
    // The set is read once, to see that it holds no saved edit, and never
    // written.
    expect(getPolicy).toHaveBeenCalledTimes(1);
  });

  it("merges a new row into the export's rows and drops none the page did not show", async () => {
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("policy set not found", 404));
    const other = EXPORT.replace("        - app: demo-tools\n", "        - app: midpoint\n          tools:\n            - read-user\n        - app: demo-tools\n");
    const files: AppRow = { id: "app-4", name: "files", runtime: "http", status: "running", reached_by: [] };
    const read: ToolRow[] = [{ id: "t9", app: "files", app_id: "app-4", name: "read-file" }];
    const answer = await accessItems(input(ticked("read-file"), { app: files, tools: read, replace: null, before: emptyPlan() }), other);
    if (!("items" in answer)) throw new Error(answer.error);
    // Two rows is the server's to refuse for an application role, with the
    // sentence that names the way out, so nothing live is dropped unseen.
    expect(docOf(answer.items[0].doc || "").spec.bindings).toEqual([
      { app: "midpoint", tools: ["read-user"] },
      { app: "demo-tools", tools: ["*"] },
      { app: "files", tools: ["read-file"] },
    ]);
  });

  it("replaces the row of the server it edits and keeps every other row", async () => {
    const other = EXPORT.replace("        - app: demo-tools\n", "        - app: midpoint\n          tools:\n            - read-user\n        - app: demo-tools\n");
    const answer = await accessItems(input(ticked("get-sum"), { before: emptyPlan() }), other);
    if (!("items" in answer)) throw new Error(answer.error);
    expect(docOf(answer.items[0].doc || "").spec.bindings).toEqual([
      { app: "midpoint", tools: ["read-user"] },
      { app: "demo-tools", tools: ["get-sum"] },
    ]);
  });

  // Each case is a save of the sheet while the role's own set has a saved
  // edit nobody published: whatever the save changes, it sends nothing.
  const saved: [string, GrantInput][] = [
    ["a rules-only save", input(withChoice(before, "get-sum", "approve"), { keep: true, replace: null })],
    ["a row-only save", input(ticked("get-sum"), { replace: null, before: emptyPlan() })],
    ["a save of both", input(withChoice({ ...before, reach: "tick", picked: { "get-sum": true } }, "get-sum", "approve"))],
  ];
  it.each(saved)("sends nothing for %s while the own set has an unpublished saved edit, and says so", async (_case, grant) => {
    vi.mocked(getPolicy).mockResolvedValue({ name: "dev-tools-access", status: "active", drift: true, yaml: OWN_SET });
    expect(await accessItems(grant, EXPORT)).toEqual({ error: savedEdit("dev-tools-access") });
  });

  it("sends the role's own set alone, edited in place, when only the rules change", async () => {
    const answer = await accessItems(input(withChoice(before, "get-sum", "approve"), { keep: true, replace: null }), EXPORT);
    if (!("items" in answer)) throw new Error(answer.error);
    expect(answer.items.map((i) => i.kind + "/" + i.name + ":" + i.op)).toEqual(["PolicySet/dev-tools-access:put"]);
    const text = answer.items[0].doc || "";
    expect(text).toContain("- id: demo-tools-get-env-deny");
    expect(text).toContain("- id: demo-tools-approve");
  });

  it("sends the row and the set together when both change, so neither goes live alone", async () => {
    const plan = withChoice({ ...before, reach: "tick", picked: { "get-sum": true, "get-env": true } }, "get-sum", "approve");
    const answer = await accessItems(input(plan), EXPORT);
    if (!("items" in answer)) throw new Error(answer.error);
    expect(answer.items.map((i) => i.kind + "/" + i.name + ":" + i.op)).toEqual(["Role/dev-tools:put", "PolicySet/dev-tools-access:put"]);
    expect(docOf(answer.items[0].doc || "").spec.bindings).toEqual([{ app: "demo-tools", tools: ["get-env", "get-sum"] }]);
  });

  it("removes the role's own set when the save leaves it with no rule", async () => {
    const answer = await accessItems(input(withChoice(before, "get-env", "allow"), { keep: true, replace: null }), EXPORT);
    expect(answer).toEqual({ items: [{ kind: "PolicySet", name: "dev-tools-access", op: "remove", doc: "" }] });
  });

  // Each case is a read of the own set and the sentence that stops the
  // save before anything is sent.
  const refusals: [string, () => void, string][] = [
    [
      "a set that matches another role",
      () => vi.mocked(getPolicy).mockResolvedValue({ name: "dev-tools-access", status: "active", yaml: OWN_SET.replace("roles: [dev-tools]", "roles: [sre]") }),
      setMismatch("dev-tools-access", "dev-tools"),
    ],
    ["a set strazad did not answer for", () => vi.mocked(getPolicy).mockRejectedValue(new ApiError("unreachable", 0, true)), setUnreadable("dev-tools-access")],
    ["a set the server refused to read", () => vi.mocked(getPolicy).mockRejectedValue(new ApiError("forbidden", 403)), setUnreadable("dev-tools-access")],
  ];
  it.each(refusals)("sends nothing for %s, and says why", async (_case, arrange, sentence) => {
    arrange();
    expect(await accessItems(input(withChoice(before, "get-sum", "approve"), { keep: true, replace: null }), EXPORT)).toEqual({ error: sentence });
  });
});

describe("newRoleItems", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("policy set not found", 404));
  });

  it("puts an application role owned by its row's server, with the row and a new own set with its rules", async () => {
    const plan = withChoice(ticked("get-sum", "list-files"), "get-sum", "approve");
    const answer = await newRoleItems({ name: "scout-role", kind: "application", description: "Scout seat.", implies: [] }, { role: { name: "scout-role" }, app, tools, plan });
    if (!("items" in answer)) throw new Error(answer.error);
    expect(answer.items.map((i) => i.kind + "/" + i.name + ":" + i.op)).toEqual(["Role/scout-role:put", "PolicySet/scout-role-access:put"]);
    const doc = docOf(answer.items[0].doc || "");
    expect(doc.metadata.name).toBe("scout-role");
    expect(doc.spec).toEqual({ kind: "application", description: "Scout seat.", server: "demo-tools", bindings: [{ app: "demo-tools", tools: ["get-sum", "list-files"] }] });
    const set = parse(answer.items[1].doc || "") as { metadata: { name: string }; spec: { match: { roles: string[] } } };
    expect(set.metadata.name).toBe("scout-role-access");
    expect(set.spec.match.roles).toEqual(["scout-role"]);
  });

  // Each case is a role with no access row: its document and nothing else.
  const cases: [string, Parameters<typeof newRoleItems>[0], RoleSpec][] = [
    ["a business role names what it composes, sorted", { name: "dev", kind: "business", description: "", implies: ["scout-tools", "dev-tools"] }, { kind: "business", implies: ["dev-tools", "scout-tools"] }],
    ["an approver role is its kind and description", { name: "sec", kind: "approver", description: "Deciders.", implies: [] }, { kind: "approver", description: "Deciders." }],
    ["an application role with no server picked reaches nothing", { name: "qa", kind: "application", description: "", implies: [] }, { kind: "application" }],
  ];
  it.each(cases)("%s, and never a pack", async (_case, role, spec) => {
    const answer = await newRoleItems(role, null);
    if (!("items" in answer)) throw new Error(answer.error);
    expect(answer.items).toHaveLength(1);
    expect(docOf(answer.items[0].doc || "").spec).toEqual(spec);
    expect(getPolicy).not.toHaveBeenCalled();
  });

  it("sends nothing when the new role's set cannot be read", async () => {
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("unreachable", 0, true));
    const plan = withChoice(ticked("get-sum"), "get-sum", "approve");
    expect(await newRoleItems({ name: "scout-role", kind: "application", description: "", implies: [] }, { role: { name: "scout-role" }, app, tools, plan })).toEqual({ error: setUnreadable("scout-role-access") });
  });
});
