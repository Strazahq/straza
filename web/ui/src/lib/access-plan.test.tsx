import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  type OwnRead,
  type Plan,
  type Shape,
  callSentence,
  choiceOf,
  defaultShape,
  emptyPlan,
  fixedFrom,
  grantMatchers,
  planFor,
  planProblems,
  planSentence,
  policyWord,
  readOwnRules,
  rulesBlock,
  rulesFor,
  rulesText,
  sameRules,
  withCall,
  withChoice,
  withOwn,
  withReach,
} from "./access-plan";
import type { BindingRow } from "./api";
import { EVERY_RULE_FIRST, everyCallReason, shapeProblemWords } from "./role-words";

const repo = (rel: string) => fileURLToPath(new URL("../../../../" + rel, import.meta.url));
const seed = (name: string) => readFileSync(repo("deploy/compose/eval-stack/seed/policies/" + name + ".yaml"), "utf8");

// The tools of the two eval servers, in the order each server lists them,
// which is not name order: what a grant stores is sorted, what the table
// draws is not.
const DEMO = ["echo", "get-annotated-message", "get-env", "get-resource-links", "get-resource-reference", "get-roots-list", "get-structured-content", "get-sum", "get-tiny-image", "gzip-file-as-resource", "simulate-research-query", "toggle-simulated-logging", "toggle-subscriber-updates", "trigger-long-running-operation"];
const READS = ["whoami", "search_users", "get_user", "get_user_assignments", "list_roles", "get_role", "list_requestable_roles", "list_work_items", "search_audit"];
const WRITES = ["request_role", "create_user", "enable_user", "disable_user", "assign_role", "unassign_role", "recompute_user", "decide_work_item"];
const MIDPOINT = READS.concat(WRITES);
const TOOLS = ["get-sum", "list-files", "get-env"];

const plan = (patch: Partial<Plan>): Plan => ({ ...emptyPlan(), ...patch });
const hold = (pool: string, seconds: number): Shape => ({ ...defaultShape(), pool, how: "hold", hold: seconds });
const ticket = (pool: string, decide: number, run: number): Shape => ({ ...defaultShape(), pool, how: "ticket", ticket: decide, grant: run });
const row = (tools: string[]): BindingRow => ({ id: "b-1", app: "demo-tools", role: "dev-tools", tools });
const ids = (rules: Record<string, unknown>[]) => rules.map((r) => r.id);
const approveOf = (r: Record<string, unknown>) => r.approve as Record<string, unknown>;

describe("the plan", () => {
  it("reads a tool's choice from the reach, the call and the tool's own choice", () => {
    const cases: [string, Plan, string, string | null][] = [
      ["a tool not ticked is out of reach", plan({ picked: { "get-sum": true }, call: "per", choice: { "get-env": "approve" } }), "get-env", null],
      ["allow every call ignores a tool's choice", plan({ reach: "later", call: "allow", choice: { "get-sum": "approve" } }), "get-sum", "allow"],
      ["every call holds a tool with no choice", plan({ reach: "later", call: "every" }), "get-sum", "approve"],
      ["every call holds a tool picked as allow", plan({ reach: "later", call: "every", choice: { "get-sum": "allow" } }), "get-sum", "approve"],
      ["every call still denies", plan({ reach: "later", call: "every", choice: { "get-env": "deny" } }), "get-env", "deny"],
      ["per tool holds what is picked", plan({ reach: "today", call: "per", choice: { "get-sum": "approve" } }), "get-sum", "approve"],
      ["per tool allows what is not picked", plan({ reach: "today", call: "per", choice: { "get-sum": "approve" } }), "list-files", "allow"],
      ["a deny read back under a name list still denies", plan({ picked: { "get-env": true }, call: "per", choice: { "get-env": "deny" } }), "get-env", "deny"],
    ];
    for (const [name, p, tool, want] of cases) expect(choiceOf(p, tool), name).toBe(want);
  });

  it("moves between states the same way on every door", () => {
    const later = plan({ reach: "later", call: "per", choice: { "get-sum": "approve", "get-env": "deny" }, own: { "get-sum": ticket("sponsor", 86400, 3600) } });
    expect(withReach(later, "tick").choice).toEqual({ "get-sum": "approve" });
    expect(withReach(later, "later").choice).toEqual(later.choice);
    expect(withChoice(plan({ reach: "later" }), "get-env", "deny").call).toBe("per");
    expect(withChoice(plan({ reach: "later" }), "get-env", "allow").call).toBe("allow");
    expect(withChoice(later, "get-sum", "allow").own).toEqual({});
    expect(withChoice(later, "get-sum", "deny").own).toEqual({});
    expect(withChoice(later, "get-sum", "approve").own).toEqual(later.own);
    expect(withCall(later, "every").call).toBe("every");
    const owned = withOwn(later, "get-env", hold("sec-approvers", 300));
    expect(owned.own["get-env"]).toEqual(hold("sec-approvers", 300));
    expect(withOwn(owned, "get-env", null).own).toEqual(later.own);
  });

  it("stores the names it ticks, every name it has today, or a glob", () => {
    const cases: [string, Plan, string[]][] = [
      ["nothing ticked", plan({}), []],
      ["two ticked", plan({ picked: { "list-files": true, "get-sum": true } }), ["get-sum", "list-files"]],
      ["every tool it has today", plan({ reach: "today" }), ["get-env", "get-sum", "list-files"]],
      ["tools added later too", plan({ reach: "later" }), ["*"]],
    ];
    for (const [name, p, want] of cases) expect(grantMatchers(p, TOOLS), name).toEqual(want);
  });

  it("seeds a stored row: a glob reads as later, every name as today, a pattern ticks what it matches", () => {
    expect(planFor(row(["*"]), TOOLS).reach).toBe("later");
    expect(planFor(row(["get-env", "get-sum", "list-files"]), TOOLS).reach).toBe("today");
    const some = planFor(row(["get-sum", "get-env"]), TOOLS);
    expect([some.reach, grantMatchers(some, TOOLS)]).toEqual(["tick", ["get-env", "get-sum"]]);
    const dotted = ["jira.create", "jira.search", "get-sum"];
    expect(grantMatchers(planFor(row(["jira.*"]), dotted), dotted)).toEqual(["jira.create", "jira.search"]);
    expect(planFor(null, TOOLS)).toEqual(emptyPlan());
    const own: OwnRead = { plan: { call: "every", choice: {}, shape: hold("sec-approvers", 600), own: {} }, owned: ["x"], fixed: {} };
    expect(planFor(row(["*"]), TOOLS, own)).toMatchObject({ reach: "later", call: "every", shape: hold("sec-approvers", 600) });
  });
});

describe("the rules a plan writes", () => {
  const server = hold("sponsor", 120);
  const envTicket = ticket("sec-approvers", 86400, 3600);
  const cases: [string, Plan, string[], Record<string, unknown>[]][] = [
    ["allow every call writes nothing", plan({ reach: "later", call: "allow", choice: { "get-sum": "approve" } }), [], []],
    ["every call is one rule with no tool list", plan({ reach: "later", call: "every", shape: server }), ["demo-tools-every-call-approve"], [{ toolNames: undefined, reason: everyCallReason("demo-tools") }]],
    [
      "every call puts a tool's own setting first and the denial last",
      plan({ reach: "later", call: "every", shape: server, choice: { "list-files": "deny" }, own: { "get-env": envTicket } }),
      ["demo-tools-get-env-approve", "demo-tools-every-call-approve", "demo-tools-deny"],
      [{ toolNames: { allow: ["get-env"] } }, { toolNames: undefined }, { toolNames: { deny: ["list-files"] }, effect: "deny" }],
    ],
    [
      "per tool shares the server's setting, then own settings come first",
      plan({ reach: "later", call: "per", shape: server, choice: { "get-sum": "approve", "get-env": "approve", "list-files": "deny" }, own: { "get-env": envTicket } }),
      ["demo-tools-get-env-approve", "demo-tools-approve", "demo-tools-deny"],
      [{ toolNames: { allow: ["get-env"] } }, { toolNames: { allow: ["get-sum"] } }, { toolNames: { deny: ["list-files"] } }],
    ],
    [
      "an own setting equal to the server's shares its rule",
      plan({ reach: "later", call: "per", shape: server, choice: { "get-sum": "approve", "get-env": "approve" }, own: { "get-env": { ...server } } }),
      ["demo-tools-approve"],
      [{ toolNames: { allow: ["get-sum", "get-env"] } }],
    ],
  ];
  it.each(cases)("%s", (_name, p, wantIds, want) => {
    const rules = rulesFor(p, "demo-tools", TOOLS, "dev-tools");
    expect(ids(rules)).toEqual(wantIds);
    rules.forEach((r, i) => {
      const { toolNames, ...rest } = want[i];
      expect(r.toolNames, String(r.id)).toEqual(toolNames);
      expect(r).toMatchObject(rest);
    });
    for (const r of rules.filter((x) => x.approve)) {
      const a = approveOf(r);
      if (a.class === "ticket") expect(a.timeoutSeconds, String(r.id)).toBeUndefined();
      else expect(a.timeoutSeconds, String(r.id)).toBeDefined();
    }
  });

  it("writes a ticket with its two windows and never a hold's, in the layout of the stored set", () => {
    const p = plan({ reach: "later", call: "per", shape: envTicket, choice: { "get-env": "approve" } });
    expect(rulesText(rulesFor(p, "demo-tools", TOOLS, "dev-tools"))).toBe([
      "  rules:",
      "    - id: demo-tools-approve",
      "      tools: [mcp.call]",
      "      apps: [demo-tools]",
      "      toolNames:",
      "        allow: [get-env]",
      "      effect: allow",
      "      mode: approve",
      "      approve:",
      "        roles: [sec-approvers]",
      "        class: ticket",
      "        ticketTTLSeconds: 86400",
      "        grantTTLSeconds: 3600",
      '      reason: "Straza: get-env on demo-tools needs approval, set in the console"',
    ].join("\n"));
  });

  it("names the tool in the reason of a rule that holds one, and says this call for more", () => {
    const one = rulesFor(plan({ reach: "later", call: "per", choice: { "get-sum": "approve" } }), "demo-tools", TOOLS, "dev-tools");
    expect(one[0].reason).toBe("Straza: get-sum on demo-tools needs approval, set in the console");
    const two = rulesFor(plan({ reach: "later", call: "per", choice: { "get-sum": "approve", "get-env": "approve" } }), "demo-tools", TOOLS, "dev-tools");
    expect(two[0].reason).toBe("Straza: this call to demo-tools needs approval, set in the console");
    const denied = rulesFor(plan({ reach: "later", call: "per", choice: { "get-env": "deny" } }), "demo-tools", TOOLS, "dev-tools");
    expect(denied[0].reason).toBe("Straza: this tool is denied on demo-tools for dev-tools, set in the console");
  });

  it("cuts the rules out of a stored set, up to the next key of the spec", () => {
    const text = ["apiVersion: straza.dev/v1beta1", "spec:", "  priority: 100", "  rules:", "    - id: a", "", "      effect: deny", "  # the recording", "  capture:", "    conversations: true", ""].join("\n");
    expect(rulesBlock(text)).toBe("  rules:\n    - id: a\n\n      effect: deny");
    expect(rulesBlock("spec:\n  priority: 1\n")).toBe("");
  });

  it("keeps a rule id clear of the ids the set already carries", () => {
    const p = plan({ reach: "later", call: "per", choice: { "get-sum": "approve" } });
    expect(ids(rulesFor(p, "demo-tools", TOOLS, "dev-tools", ["demo-tools-approve"]))).toEqual(["demo-tools-approve-2"]);
  });

  it("tells a change of rules from a change that writes the same ones", () => {
    const a = plan({ reach: "later", call: "per", choice: { "get-sum": "approve" } });
    expect(sameRules(a, { ...a, shape: { ...a.shape } }, "demo-tools", TOOLS, "dev-tools")).toBe(true);
    expect(sameRules(a, { ...a, shape: { ...a.shape, ticket: 7200 } }, "demo-tools", TOOLS, "dev-tools")).toBe(true);
    expect(sameRules(a, { ...a, shape: { ...a.shape, hold: 300 } }, "demo-tools", TOOLS, "dev-tools")).toBe(false);
    expect(sameRules(a, withCall(a, "allow"), "demo-tools", TOOLS, "dev-tools")).toBe(false);
  });

  it("names the windows the server would refuse, once each", () => {
    const at = (s: Shape, own?: Shape) => planProblems(plan({ reach: "later", call: "per", shape: s, choice: { "get-sum": "approve", "get-env": "approve" }, own: own ? { "get-env": own } : {} }), TOOLS);
    const cases: [string, string[], string[]][] = [
      ["a hold of 1 hour", at(hold("sponsor", 3600)), []],
      ["a hold over 1 hour", at(hold("sponsor", 3601)), [shapeProblemWords.hold]],
      ["a ticket decided within 30 days", at(ticket("sponsor", 2592000, 86400)), []],
      ["a ticket decided later than 30 days", at(ticket("sponsor", 2592001, 3600)), [shapeProblemWords.ticket]],
      ["a grant over 24 hours", at(ticket("sponsor", 86400, 86401)), [shapeProblemWords.grant]],
      ["a tool's own setting", at(hold("sponsor", 120), hold("sponsor", 7200)), [shapeProblemWords.hold]],
      ["the same problem twice", at(hold("sponsor", 7200), hold("sponsor", 9000)), [shapeProblemWords.hold]],
    ];
    for (const [name, got, want] of cases) expect(got, name).toEqual(want);
  });
});

describe("the sentence under the editor", () => {
  // Two fixture scenes: identity-helpdesk on midpoint, every tool
  // and tools added later.
  const every = plan({ reach: "later", call: "every", shape: ticket("sec-approvers", 86400, 3600) });
  const writes: Record<string, "approve" | "deny"> = Object.fromEntries(WRITES.map((t) => [t, "approve" as const]));
  const per = plan({ reach: "later", call: "per", shape: hold("sponsor", 120), choice: { ...writes, decide_work_item: "deny" }, own: { unassign_role: ticket("sec-approvers", 86400, 3600) } });

  it("says every call's ticket once", () => {
    expect(planSentence(every, "midpoint", MIDPOINT, "identity-helpdesk")).toBe(
      "Holders of identity-helpdesk reach every tool of midpoint, tools added later included. Every call needs a ticket the approver role sec-approvers grants within a day; the same call within an hour runs.",
    );
  });

  it("says the counts per tool, a tool's own ticket, the denial and the new tools", () => {
    const tail = "9 tools are allowed. 6 need approval by the person behind the agent within 2 minutes. unassign_role needs a ticket the approver role sec-approvers grants within a day; the same call within an hour runs. decide_work_item is denied. A tool midpoint gains later runs without approval until someone sets it here.";
    expect(planSentence(per, "midpoint", MIDPOINT, "identity-helpdesk")).toBe("Holders of identity-helpdesk reach every tool of midpoint, tools added later included. " + tail);
    expect(callSentence(per, "midpoint", MIDPOINT, "identity-helpdesk")).toBe(tail);
    expect(callSentence(plan({ reach: "today" }), "midpoint", MIDPOINT, "identity-helpdesk")).toBe("No call needs approval from this role.");
  });

  it("words each tool for the Review", () => {
    expect([policyWord(per, "whoami"), policyWord(per, "create_user"), policyWord(per, "unassign_role"), policyWord(per, "decide_work_item")]).toEqual([
      "allowed", "needs approval: hold, up to 2 minutes", "needs approval: ticket within a day, then an hour to run", "denied",
    ]);
    expect(policyWord(plan({}), "whoami")).toBe("");
  });
});

describe("reading the role's own set back", () => {
  it("reads the sandbox and readers sets as per tool: get-sum's hold is the server's, get-env keeps its ticket", () => {
    for (const set of ["demo-tools-sandbox-access", "demo-tools-readers-access"]) {
      const read = readOwnRules(seed(set), "demo-tools", DEMO);
      expect(read.plan, set).toEqual({
        call: "per",
        choice: { "get-sum": "approve", "get-env": "approve" },
        shape: hold("sponsor", 120),
        own: { "get-env": ticket("sponsor", 86400, 3600) },
      });
      expect(read.owned, set).toEqual(["demo-tools-get-sum-approve", "demo-tools-get-env-approve"]);
      expect(read.fixed, set).toEqual({});
    }
  });

  it("reads midpoint-operations with the sponsor deciding, owning the rule that narrows notify", () => {
    const served = MIDPOINT.concat("cancel_request");
    const read = readOwnRules(seed("midpoint-operations-access"), "midpoint", served);
    expect(read.plan).toEqual({ call: "per", choice: Object.fromEntries(WRITES.concat("cancel_request").map((t) => [t, "approve"])), shape: hold("sponsor", 600), own: {} });
    expect(read.owned).toEqual(["midpoint-work-item-approve", "midpoint-request-role", "midpoint-cancel-request", "midpoint-write-approve"]);
    expect(read.fixed).toEqual({});
    const self = readOwnRules(seed("midpoint-self-service-access"), "midpoint", served);
    expect([self.plan.choice, self.plan.shape, self.plan.own, self.owned]).toEqual([{ request_role: "approve", decide_work_item: "approve" }, hold("sponsor", 600), {}, ["midpoint-request-role-approve", "midpoint-decide-work-item-sponsor"]]);
  });

  it("reads agent-guardrails as fixed for the server it names and silent for the others", () => {
    const tools = ["approval_request", "approval_status", "approval_await"];
    const read = readOwnRules(seed("agent-guardrails"), "straza", tools);
    const allowed = { set: "agent-guardrails", ruleId: "straza-approval-tools", word: "allowed", status: "visible" };
    expect(read).toEqual({ plan: { call: "allow", choice: {}, shape: defaultShape(), own: {} }, owned: [], fixed: Object.fromEntries(tools.map((t) => [t, allowed])) });
    expect(readOwnRules(seed("agent-guardrails"), "demo-tools", DEMO)).toEqual(readOwnRules(null, "demo-tools", DEMO));
  });

  const set = (rules: string[]) => ["apiVersion: straza.dev/v1beta1", "kind: PolicySet", "metadata:", "  name: dev-tools-access", "spec:", "  match:", "    roles: [dev-tools]", "  rules:"].concat(rules).join("\n") + "\n";
  const rule = (id: string, body: string[]) => ["    - id: " + id].concat(body.map((l) => "      " + l));
  const mcp = ["tools: [mcp.call]", "apps: [demo-tools]"];
  const approve = (names: string[] | null, block: string[]) => mcp.concat(names ? ["toolNames:", "  allow: [" + names.join(", ") + "]"] : []).concat(["effect: allow", "mode: approve", "approve:"]).concat(block.map((l) => "  " + l));

  it("reads a rule for every call, a tool's own rule before it, and a rule after it that never decides", () => {
    const text = set([
      ...rule("env-ticket", approve(["get-env"], ["roles: [sec-approvers]", "class: ticket", "ticketTTLSeconds: 7200"])),
      ...rule("every-call", approve(null, ["deciders: [sponsor]", "timeoutSeconds: 300"])),
      ...rule("sum-late", approve(["get-sum"], ["timeoutSeconds: 60"])),
      ...rule("no-list-files", mcp.concat(["toolNames:", "  deny: [list-files]", "effect: deny"])),
    ]);
    const read = readOwnRules(text, "demo-tools", TOOLS);
    expect(read.plan).toEqual({ call: "every", shape: hold("sponsor", 300), choice: { "get-env": "approve", "list-files": "deny" }, own: { "get-env": ticket("sec-approvers", 7200, 3600) } });
    expect(read.owned).toEqual(["env-ticket", "every-call", "no-list-files"]);
    expect(read.fixed).toEqual({ "get-sum": { set: "dev-tools-access", ruleId: "sum-late", word: EVERY_RULE_FIRST, status: "approve_gated" } });
  });

  it("takes the setting most tools hold as the server's, the earliest rule on a tie, and drops tools the server lost", () => {
    const text = set([
      ...rule("a", approve(["get-env"], ["timeoutSeconds: 60"])),
      ...rule("b", approve(["get-sum", "gone-tool"], ["timeoutSeconds: 300"])),
      ...rule("c", approve(["list-files"], ["timeoutSeconds: 300"])),
    ]);
    expect(readOwnRules(text, "demo-tools", TOOLS).plan).toMatchObject({ shape: hold("sponsor", 300), own: { "get-env": hold("sponsor", 60) } });
    const tie = set([...rule("a", approve(["get-env"], ["timeoutSeconds: 60"])), ...rule("b", approve(["get-sum"], ["timeoutSeconds: 300"]))]);
    expect(readOwnRules(tie, "demo-tools", TOOLS).plan).toMatchObject({ shape: hold("sponsor", 60), own: { "get-sum": hold("sponsor", 300) } });
    const stale = readOwnRules(set(rule("old", approve(["gone-tool"], ["timeoutSeconds: 60"]))), "demo-tools", TOOLS);
    expect([stale.owned, stale.plan.call, stale.plan.choice]).toEqual([["old"], "per", {}]);
  });

  it("leaves every rule it cannot say fixed, with the word the row shows", () => {
    const text = set([
      ...rule("two-pools", approve(["get-sum"], ["deciders: [sponsor]", "roles: [sec-approvers]", "class: ticket"])),
      ...rule("attested", approve(["get-env"], ["timeoutSeconds: 60"]).concat(["require:", "  attestation: managed"])),
      ...rule("confirm-files", mcp.concat(["toolNames:", "  allow: [list-files]", "effect: allow", "mode: confirm"])),
      ...rule("plain-allow", mcp.concat(["toolNames:", "  allow: [get-sum, list-files]", "effect: allow"])),
      ...rule("glob-deny", mcp.concat(["toolNames:", '  deny: ["get-*"]', "effect: deny"])),
      ...rule("other-server", ["tools: [mcp.call]", "apps: [midpoint]", "effect: deny"]),
      ...rule("shell-only", ["tools: [shell.exec]", "effect: deny"]),
    ]);
    const read = readOwnRules(text, "demo-tools", TOOLS);
    expect(read.owned).toEqual([]);
    expect(read.plan.call).toBe("allow");
    expect(read.fixed).toEqual({
      "get-sum": { set: "dev-tools-access", ruleId: "glob-deny", word: "denied", status: "hidden_policy" },
      "get-env": { set: "dev-tools-access", ruleId: "glob-deny", word: "denied", status: "hidden_policy" },
      "list-files": { set: "dev-tools-access", ruleId: "confirm-files", word: "needs approval: hold, up to 2 minutes", status: "approve_gated" },
    });
    const pools = readOwnRules(set(rule("two-pools", approve(["get-sum"], ["deciders: [sponsor]", "roles: [sec-approvers]", "class: ticket"]))), "demo-tools", TOOLS);
    expect(pools.fixed["get-sum"].word).toBe("needs approval: ticket within a day, then an hour to run");
  });

  it("shows a plain allow only where no owned rule decides the tool", () => {
    const text = set([...rule("sum-hold", approve(["get-sum"], ["timeoutSeconds: 60"])), ...rule("plain-allow", mcp.concat(["toolNames:", "  allow: [get-sum, get-env]", "effect: allow"]))]);
    const read = readOwnRules(text, "demo-tools", TOOLS);
    expect(Object.keys(read.fixed)).toEqual(["get-env"]);
    expect(read.plan.choice).toEqual({ "get-sum": "approve" });
  });

  it("reads a set that does not parse as saying nothing", () => {
    expect(readOwnRules("spec:\n  rules:\n    - id: a\n   bad: [\n", "demo-tools", TOOLS)).toEqual(readOwnRules(null, "demo-tools", TOOLS));
  });

  it("merges the preview of other sets, the stronger rule per tool", () => {
    const own = readOwnRules(seed("demo-tools-sandbox-access"), "demo-tools", DEMO);
    own.fixed.echo = { set: "demo-tools-sandbox-access", ruleId: "x", word: "needs approval: hold, up to a minute", status: "approve_gated" };
    const preview = {
      "get-sum": { app: "demo-tools", tool: "get-sum", status: "approve_gated", reason: "", setName: "demo-tools-sandbox-access", ruleId: "demo-tools-get-sum-approve" },
      "get-env": { app: "demo-tools", tool: "get-env", status: "hidden_policy", reason: "", setName: "agent-guardrails", ruleId: "no-env" },
      echo: { app: "demo-tools", tool: "echo", status: "approve_gated", reason: "", setName: "demo-tools-sandbox-access", ruleId: "x" },
      "get-tiny-image": { app: "demo-tools", tool: "get-tiny-image", status: "visible", reason: "" },
    };
    expect(fixedFrom(preview, "demo-tools-sandbox-access", own)).toEqual({
      "get-env": { set: "agent-guardrails", ruleId: "no-env", word: "denied", status: "hidden_policy" },
      echo: own.fixed.echo,
    });
  });
});
