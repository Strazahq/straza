import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { parse } from "yaml";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { type GrantInput, planRows, runGrant, storedRules } from "./grant-commit";
import { type Plan, emptyPlan, planFor, readOwnRules, withCall, withChoice, withOwn } from "./access-plan";
import { ApiError, type AppRow, type BindingRow, type ToolRow, applyPolicy, createBinding, createRole, deactivatePolicy, deletePolicy, getPolicy, removeBinding } from "./api";
import { UNREACHABLE_STEP, retireKept, rowRetire, setMismatch, setUnparsed } from "./role-words";

vi.mock("./api", async (orig) => ({
  ...(await orig<typeof import("./api")>()),
  createRole: vi.fn(),
  createBinding: vi.fn(),
  removeBinding: vi.fn(),
  getPolicy: vi.fn(),
  applyPolicy: vi.fn(),
  deactivatePolicy: vi.fn(),
  deletePolicy: vi.fn(),
}));

const repo = (rel: string) => fileURLToPath(new URL("../../../../" + rel, import.meta.url));
const seed = (name: string) => readFileSync(repo("deploy/compose/eval-stack/seed/policies/" + name + ".yaml"), "utf8");
const comments = (text: string) => text.split("\n").map((l) => l.trim()).filter((l) => l.startsWith("#"));

const app: AppRow = { id: "app-9", name: "demo-tools", runtime: "http", status: "running", reached_by: [] };
const midpoint: AppRow = { id: "app-7", name: "midpoint", runtime: "http", status: "running", reached_by: [] };
const toolRows = (server: AppRow, names: string[]): ToolRow[] => names.map((n, i) => ({ id: "t" + i, app: server.name, app_id: server.id, name: n }));
const tools: ToolRow[] = [
  { id: "t1", app: "demo-tools", app_id: "app-9", name: "get-sum", description: "Adds two numbers" },
  { id: "t2", app: "demo-tools", app_id: "app-9", name: "list-files" },
];
const DEMO = ["echo", "get-annotated-message", "get-env", "get-resource-links", "get-resource-reference", "get-roots-list", "get-structured-content", "get-sum", "get-tiny-image", "gzip-file-as-resource", "simulate-research-query", "toggle-simulated-logging", "toggle-subscriber-updates", "trigger-long-running-operation"];
const MIDPOINT = ["whoami", "search_users", "get_user", "list_requestable_roles", "request_role", "create_user", "enable_user", "disable_user", "assign_role", "unassign_role", "recompute_user", "decide_work_item", "cancel_request"];
const stored: BindingRow = { id: "b-1", app: "demo-tools", role: "dev-tools", tools: ["get-sum"] };
const glob = (role: string, server = "demo-tools"): BindingRow => ({ id: "b-2", app: server, role, tools: ["*"] });

// gated is the plan the role page sends when both tools are ticked and
// get-sum needs approval from the person behind the agent.
const gated: Plan = { ...emptyPlan(), picked: { "get-sum": true, "list-files": true }, call: "per", choice: { "get-sum": "approve" } };

const input = (patch: Partial<GrantInput> = {}): GrantInput => ({ role: { name: "dev-tools", id: "r-1" }, app, tools, plan: gated, ...patch });

// readBack is what a door holds after it read the role's own set: the
// plan the editor opens with.
const readBack = (text: string, binding: BindingRow, names: string[], server = "demo-tools") => planFor(binding, names, readOwnRules(text, server, names));
const sent = () => vi.mocked(applyPolicy).mock.calls[0][0];
// block is one rule's text, from its dash to the next rule's.
const block = (text: string, id: string) => {
  const from = text.indexOf("    - id: " + id + "\n");
  const next = text.indexOf("    - id: ", from + 1);
  return text.slice(from, next === -1 ? undefined : next);
};
const ruleIds = (text: string) => (parse(text).spec.rules as { id: string }[]).map((r) => r.id);

const NEW_SET = [
  "apiVersion: straza.dev/v1beta1",
  "kind: PolicySet",
  "metadata:",
  "  name: dev-tools-access",
  "  description: Gates for the role dev-tools, written in the console",
  "spec:",
  "  priority: 100",
  "  match:",
  "    roles: [dev-tools]",
  "  rules:",
  "    - id: demo-tools-approve",
  "      tools: [mcp.call]",
  "      apps: [demo-tools]",
  "      toolNames:",
  "        allow: [get-sum]",
  "      effect: allow",
  "      mode: approve",
  "      approve:",
  "        deciders: [sponsor]",
  "        timeoutSeconds: 120",
  '      reason: "Straza: get-sum on demo-tools needs approval, set in the console"',
  "",
].join("\n");

// MIXED is a hand-written set for dev-tools: one rule this editor owns, a
// rule for another server, and a rule the editor cannot say.
const MIXED = [
  "# dev-tools-access: kept by hand.",
  "apiVersion: straza.dev/v1beta1",
  "kind: PolicySet",
  "metadata:",
  "  name: dev-tools-access",
  "spec:",
  "  priority: 100",
  "  match:",
  "    roles: [dev-tools]",
  "  rules:",
  "    - id: sum-hold",
  "      # The showcase hold.",
  "      tools: [mcp.call]",
  "      apps: [demo-tools]",
  "      toolNames:",
  '        allow: ["get-sum"]',
  "      effect: allow",
  "      mode: approve",
  "      approve:",
  "        deciders: [sponsor]",
  "        timeoutSeconds: 120",
  "        notify: [push]",
  '      reason: "Straza: get-sum is the showcase"',
  "    - id: midpoint-writes",
  "      tools: [mcp.call]",
  "      apps: [midpoint]",
  "      toolNames:",
  "        allow: [create_user]",
  "      effect: allow",
  "      mode: approve",
  "      approve:",
  "        roles: [sec-approvers]",
  "        timeoutSeconds: 600",
  "    - id: env-attested",
  "      tools: [mcp.call]",
  "      apps: [demo-tools]",
  "      toolNames:",
  "        allow: [get-env]",
  "      require:",
  "        attestation: managed",
  "      effect: allow",
  "",
].join("\n");

describe("the stepwise grant", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(createBinding).mockResolvedValue(stored);
    vi.mocked(removeBinding).mockResolvedValue({ status: "deleted" });
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("no such policy set", 404));
    vi.mocked(applyPolicy).mockResolvedValue({ name: "dev-tools-access", status: "draft" });
    vi.mocked(deactivatePolicy).mockResolvedValue({});
    vi.mocked(deletePolicy).mockResolvedValue({ status: "deleted" });
  });

  it("names the rows before it runs any of them, the publish row in the words of what a call does", () => {
    const rows = planRows(input({ role: { name: "scout", create: { kind: "application", description: "" } }, replace: stored }));
    expect(rows.map((r) => [r.key, r.label, r.state])).toEqual([
      ["create", "Create the role scout", "pending"],
      ["remove", "Remove the old access row", "pending"],
      ["grant", "Write the new access row: every tool (2)", "pending"],
      ["publish", "Publish scout-access: 1 tool is allowed. 1 needs approval by the person behind the agent within 2 minutes.", "pending"],
    ]);
  });

  it("writes the rules only when they differ from what the editor opened with", () => {
    const cases: [string, Partial<GrantInput>, string[]][] = [
      ["no rules and nothing read back", { plan: { ...gated, call: "allow" } }, ["grant"]],
      ["the same rules as read back", { keep: true, before: gated }, []],
      ["a changed window", { keep: true, before: gated, plan: { ...gated, shape: { ...gated.shape, hold: 300 } } }, ["publish"]],
      ["back to allow", { keep: true, before: gated, plan: withCall(gated, "allow") }, ["publish"]],
    ];
    for (const [name, patch, want] of cases) expect(planRows(input(patch)).map((r) => r.key), name).toEqual(want);
    expect(planRows(input({ keep: true, before: gated, plan: withCall(gated, "allow") }))[0].label).toBe("Publish dev-tools-access: No call needs approval from this role.");
  });

  it("runs create, remove, grant and the set write in order, and stops with the publish row running", async () => {
    const seen: string[][] = [];
    vi.mocked(createRole).mockResolvedValue({ id: "r-2", name: "scout", kind: "application" });
    const result = await runGrant(input({ role: { name: "scout", create: { kind: "application", description: "Scout tools" } }, replace: stored }), (rows) => seen.push(rows.map((r) => r.state)));
    expect(createRole).toHaveBeenCalledWith("scout", "application", "Scout tools");
    expect(removeBinding).toHaveBeenCalledWith("b-1");
    expect(createBinding).toHaveBeenCalledWith("app-9", "scout", ["get-sum", "list-files"]);
    expect(getPolicy).toHaveBeenCalledWith("scout-access");
    expect(applyPolicy).toHaveBeenCalledTimes(1);
    expect(result.role?.name).toBe("scout");
    expect(result.rows.map((r) => r.state)).toEqual(["done", "done", "done", "running"]);
    expect(result.published).toEqual({ name: "scout-access", yaml: expect.stringContaining("name: scout-access"), baseYaml: null, replaces: false });
    expect(seen.length).toBeGreaterThan(0);
  });

  it("writes the whole document when the role has no set of its own yet", async () => {
    await runGrant(input(), () => undefined);
    expect(applyPolicy).toHaveBeenCalledWith(NEW_SET);
  });

  it.each([
    ["demo-tools-sandbox-access", "demo-tools-sandbox", app, glob("demo-tools-sandbox"), DEMO],
    ["demo-tools-readers-access", "demo-tools-readers", app, { ...stored, role: "demo-tools-readers", tools: ["echo", "get-annotated-message", "get-env", "get-sum"] }, DEMO],
    ["midpoint-self-service-access", "midpoint-self-service", midpoint, glob("midpoint-self-service", "midpoint"), MIDPOINT],
  ] as [string, string, AppRow, BindingRow, string[]][])("saves %s unchanged to the byte", async (set, role, server, binding, names) => {
    const text = seed(set);
    vi.mocked(getPolicy).mockResolvedValue({ name: set, status: "active", yaml: text });
    const result = await runGrant({ role: { name: role }, app: server, tools: toolRows(server, names), plan: readBack(text, binding, names, server.name), keep: true }, () => undefined);
    expect(sent()).toBe(text);
    expect(result.published).toEqual({ name: set, yaml: text, baseYaml: text, replaces: true });
  });

  it("keeps the meaning, the comments and the notify of midpoint-operations when nothing changes", async () => {
    const text = seed("midpoint-operations-access");
    vi.mocked(getPolicy).mockResolvedValue({ name: "midpoint-operations-access", status: "active", yaml: text });
    await runGrant({ role: { name: "midpoint-operations" }, app: midpoint, tools: toolRows(midpoint, MIDPOINT), plan: readBack(text, glob("midpoint-operations", "midpoint"), MIDPOINT, "midpoint"), keep: true }, () => undefined);
    expect(parse(sent())).toEqual(parse(text));
    expect(comments(sent())).toEqual(comments(text));
  });

  it("turns get-sum's hold into a ticket in place, keeping its reason, comment and binding", async () => {
    const text = seed("demo-tools-sandbox-access");
    vi.mocked(getPolicy).mockResolvedValue({ name: "demo-tools-sandbox-access", status: "active", yaml: text });
    const before = readBack(text, glob("demo-tools-sandbox"), DEMO);
    const plan = { ...before, shape: { ...before.shape, how: "ticket" as const } };
    await runGrant({ role: { name: "demo-tools-sandbox" }, app, tools: toolRows(app, DEMO), plan, before, keep: true }, () => undefined);
    const sum = block(sent(), "demo-tools-get-sum-approve");
    expect(sum).toContain("# binding call: the approval covers these exact arguments");
    expect(sum).toContain("reason: \"Straza: get-sum needs approval, the 2-minute MCP showcase");
    expect(sum).toContain("approve:\n        deciders: [sponsor]\n        class: ticket\n        ticketTTLSeconds: 86400\n        grantTTLSeconds: 3600\n        binding: call\n");
    expect(sum).not.toContain("timeoutSeconds:");
    expect(sum).not.toContain("retryTTLSeconds");
    expect(block(sent(), "demo-tools-get-env-approve")).toBe(block(text, "demo-tools-get-env-approve"));
    expect(comments(sent())).toEqual(comments(text));
  });

  it("switches a server to every call: one rule with no tool list, after the tool that keeps its own ticket", async () => {
    const text = seed("demo-tools-sandbox-access");
    vi.mocked(getPolicy).mockResolvedValue({ name: "demo-tools-sandbox-access", status: "active", yaml: text });
    const before = readBack(text, glob("demo-tools-sandbox"), DEMO);
    await runGrant({ role: { name: "demo-tools-sandbox" }, app, tools: toolRows(app, DEMO), plan: withCall(before, "every"), before, keep: true }, () => undefined);
    expect(ruleIds(sent())).toEqual(["demo-tools-get-env-approve", "demo-tools-every-call-approve"]);
    const every = parse(sent()).spec.rules[1];
    expect(every).toEqual({
      id: "demo-tools-every-call-approve", tools: ["mcp.call"], apps: ["demo-tools"], effect: "allow", mode: "approve",
      approve: { deciders: ["sponsor"], timeoutSeconds: 120 }, reason: "Straza: every call to demo-tools needs approval, set in the console",
    });
    expect(block(sent(), "demo-tools-get-env-approve")).toBe(block(text, "demo-tools-get-env-approve"));
  });

  it("puts a tool's new own rule before a kept rule for every call", async () => {
    const text = seed("demo-tools-sandbox-access");
    vi.mocked(getPolicy).mockResolvedValue({ name: "demo-tools-sandbox-access", status: "active", yaml: text });
    const base = readBack(text, glob("demo-tools-sandbox"), DEMO);
    const every: Plan = { ...withCall(base, "every"), own: {} };
    vi.mocked(applyPolicy).mockImplementation(async (yaml: string) => ({ name: "demo-tools-sandbox-access", status: "draft", yaml }));
    await runGrant({ role: { name: "demo-tools-sandbox" }, app, tools: toolRows(app, DEMO), plan: every, keep: true }, () => undefined);
    const onlyEvery = sent();
    expect(ruleIds(onlyEvery)).toEqual(["demo-tools-every-call-approve"]);
    vi.mocked(applyPolicy).mockClear();
    vi.mocked(getPolicy).mockResolvedValue({ name: "demo-tools-sandbox-access", status: "active", yaml: onlyEvery });
    const own = withOwn(every, "echo", { ...every.shape, pool: "sec-approvers", how: "ticket" });
    await runGrant({ role: { name: "demo-tools-sandbox" }, app, tools: toolRows(app, DEMO), plan: own, before: every, keep: true }, () => undefined);
    expect(ruleIds(sent())).toEqual(["demo-tools-echo-approve", "demo-tools-every-call-approve"]);
    expect(parse(sent()).spec.rules[0].approve).toEqual({ roles: ["sec-approvers"], class: "ticket", ticketTTLSeconds: 86400, grantTTLSeconds: 3600 });
  });

  it("switching back to allow removes the server's own rules and leaves other servers' and hand-written ones", async () => {
    vi.mocked(getPolicy).mockResolvedValue({ name: "dev-tools-access", status: "draft", yaml: MIXED });
    const before = readBack(MIXED, glob("dev-tools"), DEMO);
    expect(before.choice).toEqual({ "get-sum": "approve" });
    const result = await runGrant({ role: { name: "dev-tools" }, app, tools: toolRows(app, DEMO), plan: withCall(before, "allow"), before, keep: true }, () => undefined);
    expect(ruleIds(sent())).toEqual(["midpoint-writes", "env-attested"]);
    expect(sent()).toContain("# dev-tools-access: kept by hand.");
    expect(block(sent(), "midpoint-writes")).toBe(block(MIXED, "midpoint-writes"));
    expect(block(sent(), "env-attested")).toBe(block(MIXED, "env-attested"));
    expect(result.published).toMatchObject({ baseYaml: MIXED, replaces: false });
  });

  it("deletes the set when the save leaves it with no rule, turning it off first when it is live", async () => {
    const text = seed("demo-tools-sandbox-access");
    vi.mocked(getPolicy).mockResolvedValue({ name: "demo-tools-sandbox-access", status: "active", yaml: text });
    const before = readBack(text, glob("demo-tools-sandbox"), DEMO);
    const result = await runGrant({ role: { name: "demo-tools-sandbox" }, app, tools: toolRows(app, DEMO), plan: withCall(before, "allow"), before, keep: true }, () => undefined);
    expect(applyPolicy).not.toHaveBeenCalled();
    expect(deactivatePolicy).toHaveBeenCalledWith("demo-tools-sandbox-access");
    expect(deletePolicy).toHaveBeenCalledWith("demo-tools-sandbox-access");
    expect(result.rows).toEqual([{ key: "publish", label: rowRetire("demo-tools-sandbox-access"), state: "done" }]);
    expect(result.published).toBeUndefined();
  });

  it("leaves a set it would empty alone when it also records conversations", async () => {
    const text = seed("demo-tools-sandbox-access").replace("  rules:\n", "  capture:\n    conversations: true\n  rules:\n");
    vi.mocked(getPolicy).mockResolvedValue({ name: "demo-tools-sandbox-access", status: "active", yaml: text });
    const before = readBack(text, glob("demo-tools-sandbox"), DEMO);
    const result = await runGrant({ role: { name: "demo-tools-sandbox" }, app, tools: toolRows(app, DEMO), plan: withCall(before, "allow"), before, keep: true }, () => undefined);
    expect([applyPolicy, deactivatePolicy, deletePolicy].map((f) => vi.mocked(f).mock.calls.length)).toEqual([0, 0, 0]);
    expect(result.rows[0]).toMatchObject({ state: "failed", error: retireKept("demo-tools-sandbox-access") });
  });

  it("writes nothing into a set that does not match exactly this role, or does not parse", async () => {
    const cases: [string, string][] = [
      [MIXED.replace("roles: [dev-tools]", "roles: [dev-tools, sre]"), setMismatch("dev-tools-access", "dev-tools")],
      [MIXED.replace("    roles: [dev-tools]", "    roles: [dev-tools]\n    users: [ana]"), setMismatch("dev-tools-access", "dev-tools")],
      ["spec:\n  rules:\n    - id: a\n   bad: [\n", setUnparsed("dev-tools-access")],
    ];
    for (const [yaml, want] of cases) {
      vi.mocked(getPolicy).mockResolvedValue({ name: "dev-tools-access", status: "active", yaml });
      const result = await runGrant(input(), () => undefined);
      expect(applyPolicy).not.toHaveBeenCalled();
      const publish = result.rows.find((r) => r.key === "publish");
      expect([publish?.state, publish?.error]).toEqual(["failed", want]);
    }
  });

  it("renames the tool in a reason it wrote when the rule's tools change, and never a hand-written one", async () => {
    await runGrant(input(), () => undefined);
    const first = sent();
    expect(first).toContain('reason: "Straza: get-sum on demo-tools needs approval, set in the console"');
    vi.mocked(applyPolicy).mockClear();
    vi.mocked(getPolicy).mockResolvedValue({ name: "dev-tools-access", status: "draft", yaml: first });
    const both = withChoice(gated, "list-files", "approve");
    await runGrant(input({ plan: both, before: gated, keep: true }), () => undefined);
    expect(ruleIds(sent())).toEqual(["demo-tools-approve"]);
    expect(parse(sent()).spec.rules[0]).toMatchObject({ toolNames: { allow: ["get-sum", "list-files"] }, reason: "Straza: this call to demo-tools needs approval, set in the console" });

    vi.mocked(applyPolicy).mockClear();
    vi.mocked(getPolicy).mockResolvedValue({ name: "dev-tools-access", status: "draft", yaml: MIXED });
    const before = readBack(MIXED, glob("dev-tools"), DEMO);
    await runGrant({ role: { name: "dev-tools" }, app, tools: toolRows(app, DEMO), plan: withChoice(before, "echo", "approve"), before, keep: true }, () => undefined);
    expect(parse(sent()).spec.rules[0]).toMatchObject({ id: "sum-hold", toolNames: { allow: ["get-sum", "echo"] }, reason: "Straza: get-sum is the showcase" });
  });

  it("reads what a save would store for one server without saving it", () => {
    const before = readBack(MIXED, glob("dev-tools"), DEMO);
    const rules = storedRules(MIXED, withChoice(before, "echo", "approve"), "demo-tools", DEMO, "dev-tools");
    expect(rules?.map((r) => r.id)).toEqual(["sum-hold"]);
    expect(rules?.[0]).toMatchObject({ toolNames: { allow: ["get-sum", "echo"] }, approve: { notify: ["push"] }, reason: "Straza: get-sum is the showcase" });
    expect(storedRules(MIXED, withCall(before, "allow"), "demo-tools", DEMO, "dev-tools")).toEqual([]);
    expect(storedRules("spec:\n  rules:\n    - id: a\n   bad: [\n", before, "demo-tools", DEMO, "dev-tools")).toBeNull();
    expect(applyPolicy).not.toHaveBeenCalled();
    expect(getPolicy).not.toHaveBeenCalled();
  });

  it("keeps the stored access row when only the rules changed", async () => {
    await runGrant(input({ keep: true }), () => undefined);
    expect(createBinding).not.toHaveBeenCalled();
    expect(applyPolicy).toHaveBeenCalledTimes(1);
  });

  it("stops at a refused access row, leaving the publish row waiting", async () => {
    vi.mocked(createBinding).mockRejectedValue(new ApiError("role dev-tools has no such server", 409));
    const result = await runGrant(input(), () => undefined);
    expect(getPolicy).not.toHaveBeenCalled();
    expect(result.rows.map((r) => [r.key, r.state])).toEqual([["grant", "failed"], ["publish", "pending"]]);
    expect(result.rows[0].error).toBe("The server refused it: role dev-tools has no such server. Fix what it names, then try again.");
  });

  it("says a step may or may not have landed when the server never answered", async () => {
    vi.mocked(createBinding).mockRejectedValue(new ApiError("unreachable", 0, true));
    const result = await runGrant(input(), () => undefined);
    expect(result.rows[0].error).toBe(UNREACHABLE_STEP);
  });
});
