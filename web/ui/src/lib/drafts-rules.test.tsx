import { describe, expect, it } from "vitest";
import { hasPatterns, machineRows, namesMove, ruleAspects, rulesNaming, setReadings } from "./drafts-rules";
import { type Plain, ruleView } from "./policy-model";
import { EVERY_WORD } from "./policy-words";
import { DETAIL, PUBLISHED_DETAIL, detailWith } from "@/test/drafts-fixture";

// The fields an edited policy rule changes, one line each with the label
// the policy page's rule sheet gives it, and the rules of a draft's sets
// that name a role or a server on a person's machine.

const HOLD: Plain = { id: "curl-needs-yes", tools: ["shell.exec"], command: { allowPatterns: ["curl *"] }, effect: "allow", mode: "approve", approve: { deciders: ["sponsor"], timeoutSeconds: 120 }, reason: "Ask first." };
const view = (over: Plain) => ruleView({ ...HOLD, ...over }, 0);
const was = view({});
const labels = (now: ReturnType<typeof view>) => ruleAspects(was, now).map((a) => a.aspect + ":" + a.label);

describe("the lines of an edited rule", () => {
  const cases: [string, Plain, string[]][] = [
    ["an outcome", { effect: "deny", mode: undefined, approve: undefined }, ["outcome:Outcome"]],
    ["who decides", { approve: { roles: ["sec-approvers"], timeoutSeconds: 120 } }, ["outcome:Outcome"]],
    ["the time to answer", { approve: { deciders: ["sponsor"], timeoutSeconds: 300 } }, ["outcome:Outcome"]],
    ["the patterns", { command: { allowPatterns: ["curl *", "wget *"] } }, ["names:Command patterns"]],
    ["the reason", { reason: "Ask the sponsor first." }, ["reason:Reason the agent reads"]],
    ["the events", { events: ["tool.post"] }, ["events:Events"]],
    ["the call type and what it matches", { tools: ["file.write"], command: undefined, paths: { allow: ["/tmp/**"] } }, ["lane:Call type", "names:Paths"]],
    ["two fields at once", { effect: "deny", mode: undefined, approve: undefined, reason: "No." }, ["outcome:Outcome", "reason:Reason the agent reads"]],
    ["only a field the table does not show", { approve: { deciders: ["sponsor"], timeoutSeconds: 120, notify: ["push"] } }, ["other:Other fields"]],
  ];
  it.each(cases)("gives %s its own line", (_name, over, want) => {
    expect(labels(view(over))).toEqual(want);
  });

  it("names the servers of an MCP rule as their own line", () => {
    const a = ruleView({ id: "m", tools: ["mcp.call"], apps: ["github"], effect: "deny" }, 0);
    const b = ruleView({ id: "m", tools: ["mcp.call"], apps: ["github", "jira"], effect: "deny" }, 0);
    expect(ruleAspects(a, b).map((x) => x.label)).toEqual(["Server"]);
  });

  it("shows the names that leave and the names that join, and a side that names nothing as its lane's every word", () => {
    expect(namesMove(view({ command: { allowPatterns: ["a", "b"] } }), view({ command: { allowPatterns: ["b", "c"] } }), (r) => EVERY_WORD[r.lane])).toEqual({ leave: ["a"], join: ["c"] });
    expect(namesMove(view({ command: undefined }), view({}), (r) => EVERY_WORD[r.lane])).toEqual({ leave: ["every command"], join: ["curl *"] });
  });

  it("offers the patterns of a rule outside the MCP lane on demand", () => {
    expect(hasPatterns(was)).toBe(true);
    expect(hasPatterns(ruleView({ id: "m", tools: ["mcp.call"], toolNames: { allow: ["x"] }, effect: "allow" }, 0))).toBe(false);
    expect(hasPatterns(view({ command: undefined }))).toBe(false);
  });
});

describe("the rules of a draft's sets", () => {
  it("reads every set item it can, a published one from its change record", () => {
    expect(setReadings(DETAIL).map((s) => [s.item.name, s.what, s.marks.map((m) => m.rule.id + ":" + m.mark)])).toEqual([
      ["github-writers-access", "new", ["github-pr-hold:new"]],
      ["demo-tools-readers-access", "changed", ["get-sum:edited", "get-env:removed"]],
    ]);
    expect(setReadings(PUBLISHED_DETAIL).map((s) => s.what)).toEqual(["changed"]);
  });

  it("names the rules of each set that names a role, and the servers on a person's machine they open", () => {
    const sets = setReadings(DETAIL);
    expect(rulesNaming(sets, "github-writers")).toEqual([{ set: "github-writers-access", rules: ["github-pr-hold"] }]);
    expect(rulesNaming(sets, "nobody")).toEqual([]);
    expect(machineRows(sets, "github-writers", ["demo-tools"]).map((m) => [m.rule.id, m.server, m.outcome])).toEqual([["github-pr-hold", "github", "approval"]]);
    expect(machineRows(sets, "github-writers", ["github"])).toEqual([]);
  });

  it("opens nothing through a set turned off or removed", () => {
    const off = detailWith({ draft: { items: [{ ...DETAIL.draft.items[4], op: "off" }] } });
    expect(machineRows(setReadings(off), "github-writers", [])).toEqual([]);
  });
});
