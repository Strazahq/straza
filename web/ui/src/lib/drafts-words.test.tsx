import { describe, expect, it } from "vitest";
import type { DraftPrincipal } from "./api";
import { objectsMove } from "./drafts-access";
import { changeCounts, confirmBlocks } from "./drafts-model";
import {
  ackWords,
  agoPill,
  appliesTo,
  blockTitle,
  changesLine,
  checkLines,
  decidedWords,
  doesOf,
  doorPill,
  doorWords,
  fromClient,
  goLive,
  groupLine,
  holderCount,
  keepDraft,
  keepLive,
  checkedLine,
  coveredWords,
  everyChip,
  machineNote,
  moreCount,
  needLine,
  nowAfter,
  notesCount,
  objectCount,
  objectsChip,
  outcomeFoot,
  outcomesWords,
  passedChecks,
  passedCount,
  pickWas,
  proposerWords,
  reachesLine,
  revisionLine,
  revisionPill,
  roleKindPill,
  ruleCount,
  rulesChanged,
  rulesLine,
  rulesUnchanged,
  showPattern,
  sponsorLine,
  sponsoredBy,
  startsLine,
  toConfirm,
  toolsChip,
  typedWrong,
  unchangedFold,
  undoLine,
  widenCount,
  yamlName,
} from "./drafts-words";
import { COVERAGE, CUT_RISK, DETAIL, HOST_RISK, OWN_REACH_RISK, REACH_RISK } from "@/test/drafts-fixture";

// The sentences of the Drafts area that are built from values: each one a
// full sentence in the console's own words.

const p = (over: Partial<DraftPrincipal>): DraftPrincipal => ({ user_id: "u", username: "carol", agent: false, via: "session", client: "console", ...over });

describe("who drafted it", () => {
  const cases: [string, DraftPrincipal, string | undefined, { name: string; line: string }][] = [
    ["an agent the reader sponsors", p({ username: "joe", agent: true, sponsor: "alice" }), undefined, { name: "joe", line: "an agent, sponsored by you" }],
    ["an agent someone else sponsors", p({ username: "sam", agent: true, sponsor: "bob" }), undefined, { name: "sam", line: "an agent, sponsored by bob" }],
    ["an agent with no sponsor on record", p({ username: "sam", agent: true }), undefined, { name: "sam", line: "an agent" }],
    ["the reader", p({ username: "alice" }), undefined, { name: "alice", line: "you" }],
    ["an admin API token", p({ username: "ci-bundles", via: "api-token" }), undefined, { name: "ci-bundles", line: "an admin API token" }],
    ["another person", p({}), undefined, { name: "carol", line: "" }],
    ["a file of the apps directory", p({ username: "strazad", via: "file" }), "/etc/straza/apps/demo-tools.yaml", { name: "demo-tools.yaml", line: "" }],
    ["the upgrade's conversion", p({ username: "strazad", via: "upgrade" }), undefined, { name: "Straza's upgrade", line: "a saved edit moved into a draft" }],
  ];
  it.each(cases)("%s", (_name, who, source, want) => {
    expect(proposerWords(who, "alice", source)).toEqual(want);
  });

  it("names every door in words", () => {
    expect(["console", "strazactl", "straza-app", "apps-directory", "api"].map(doorWords)).toEqual(["the console", "strazactl", "the straza app", "the apps directory", "the API"]);
  });
});

describe("the sentences of the page", () => {
  it("counts what goes live", () => {
    const counts = changeCounts(DETAIL.draft.items);
    expect(goLive(counts)).toBe("Publishing adds 1 MCP server, 1 role and 1 policy, changes 1 MCP server, 2 roles and 1 policy and removes 1 MCP server.");
    expect(goLive([{ kind: "App", what: "removed", n: 1 }])).toBe("Publishing removes 1 MCP server.");
    expect(goLive([{ kind: "PolicySet", what: "off", n: 1 }])).toBe("Publishing turns off 1 policy.");
    expect(goLive([{ kind: "PolicySet", what: "on", n: 1 }])).toBe("Publishing turns on 1 policy.");
    expect(goLive([{ kind: "Role", what: "changed", n: 1 }])).toBe("Publishing changes 1 role.");
  });

  // Each case is the risks of one confirm block and how the page says the
  // publish dialog asks for it, split around the text set as code.
  const grouped = (key: string, typed: string) => ({ ...HOST_RISK, key, typed, object: "App/" + typed, group: HOST_RISK.code });
  const role = (r: string) => ({ ...HOST_RISK, key: "k-" + r, code: "access.ungated", typed: r, object: "Role/" + r, group: "access.ungated" });
  const acks: [string, (typeof HOST_RISK)[], [string, string, string]][] = [
    ["a tick", [REACH_RISK], ["Tick to confirm.", "", ""]],
    ["a group of ticks", [{ ...REACH_RISK, group: "reach.new" }, { ...REACH_RISK, key: "k2", group: "reach.new" }], ["Tick to confirm.", "", ""]],
    ["a typed line", [HOST_RISK], ["Type", "api.githubcopilot.com", "when you publish."]],
    ["a group of typed lines", [grouped("a", "a.example"), grouped("b", "b.example"), grouped("c", "c.example")], ["Type", "3", "when you publish, the number of MCP servers it widens access through."]],
    ["a group of loosened policies", ["a", "b"].map((s) => ({ ...grouped(s, s + "-access"), code: "policy.guardrail-changed", object: "PolicySet/" + s + "-access", group: "policy.guardrail-changed" })), ["Type", "2", "when you publish, the number of policies it loosens."]],
    ["a group of roles", [role("demo-tools-readers"), role("operator")], ["Type", "2", "when you publish, the number of roles that gain access."]],
    ["an agent's loosening", [{ ...HOST_RISK, key: "k-l", code: "agent.guardrail", object: "PolicySet/desk-gate", typed: "desk-gate" }], ["Type", "desk-gate", "when you publish to accept it."]],
    ["an agent's own reach", [OWN_REACH_RISK], ["Type", "github", "when you publish to accept it."]],
    ["a typed line cut for the reader", [CUT_RISK], ["Only someone who may read what this line names can confirm it, so ask an admin who may read it to publish this draft.", "", ""]],
  ];
  it.each(acks)("words how %s is confirmed", (_name, risks, want) => {
    expect(ackWords(confirmBlocks(risks)[0])).toEqual(want);
  });

  it("says once whether publishing the old version undoes the confirmed lines", () => {
    expect(undoLine(true)).toBe("Publishing the old version again does not undo it.");
    expect(undoLine(false)).toBe("Publishing the old version again undoes it.");
  });

  it("says Now and After, with no Now where a line has none", () => {
    expect(nowAfter("reached", "leaves the role.")).toBe("Now: reached. After publishing: leaves the role.");
    expect(nowAfter("", "notes.example.com")).toBe("After publishing: notes.example.com.");
  });

  it("words the pills and the Details of Widens access", () => {
    expect(appliesTo(null)).toBe("applies to you");
    expect(appliesTo("alice")).toBe("applies to alice");
    expect(machineNote("elevenlabs")).toBe("It matches any MCP server a person's own harness runs under the name elevenlabs.");
    expect(checkLines([{ code: "policy.guardrail-changed", objects: ["demo-tools-readers-access"] }, { code: "access.ungated", objects: ["demo-tools-readers", "operator"] }], []))
      .toBe("Check lines: policy.guardrail-changed on demo-tools-readers-access, and access.ungated on demo-tools-readers and on operator.");
    expect(checkLines([{ code: "policy.local-allow", objects: ["ux-walk-audio-access"] }], ["ready.rule-names-nothing"]))
      .toBe("Check lines: policy.local-allow on ux-walk-audio-access, with the warning ready.rule-names-nothing folded into it.");
    expect(checkLines([{ code: "host.changed", objects: [] }], [])).toBe("Check lines: host.changed.");
  });

  it("counts the lines that widen access", () => {
    expect(widenCount(1)).toBe("1 widens access");
    expect(widenCount(3)).toBe("3 widen access");
  });

  it("words a pick's values, an unset one included", () => {
    expect(pickWas("5")).toBe("It was 5 when the draft was checked.");
    expect(pickWas("")).toBe("It was unset when the draft was checked.");
    expect(keepDraft("")).toBe("Draft: unset");
    expect(keepLive("10")).toBe("Live: 10");
  });

  it("says what a typed block needs when the entry cannot match", () => {
    expect(typedWrong("20", "confirm")).toBe("That is not 20. Type 20 to confirm.");
    expect(typedWrong("agent-guardrails", "accept it")).toBe("That is not agent-guardrails. Type agent-guardrails to accept it.");
  });

  // A confirm block's heading says what publishing does by the code of its
  // lines, else by the kind of its object. A line carries no access, so the
  // lines a reader may not read are counted as lines that widen access, and
  // an object of a kind the console does not know is not counted.
  const titles: [string, string, string | undefined, number, string][] = [
    ["a removed server", "server.removal", "App/legacy-desk", 1, "Removes 1 MCP server"],
    ["loosened policies", "policy.guardrail-changed", "PolicySet/a", 20, "Loosens 20 policies"],
    ["the agent's own reach", "agent.own-reach", "App/github", 2, "Widens the agent's own reach on 2 MCP servers"],
    ["a code the table does not know, on a server", "some.future-code", "App/desk", 1, "Widens access through 1 MCP server"],
    ["a role", "access.gated", "Role/desk-y", 2, "Widens 2 roles"],
    ["one line the reader may not read", "x.y", undefined, 1, "1 line you cannot read widens access"],
    ["lines the reader may not read", "server.runs-code", undefined, 2, "2 lines you cannot read widen access"],
    ["an object of a kind the console does not know", "x.y", "Ticket/T-1", 2, "Widens access"],
    ["a code named like a property of every object", "constructor", "toString/x", 1, "Widens access"],
  ];
  it.each(titles)("heads a block of %s", (_name, code, object, n, want) => {
    expect(blockTitle(doesOf(code, object), n)).toBe(want);
  });

  const signs: [string, boolean, string][] = [
    ["a draft with a risk", true, "You sponsor joe. When second-person publishing is on, another admin must publish this."],
    ["a draft with no risk", false, "You sponsor joe, which proposed this draft."],
  ];
  it.each(signs)("tells the agent's sponsor about %s", (_name, risky, want) => {
    expect(sponsorLine("joe", risky)).toBe(want);
  });

  it("words the other lines built from values", () => {
    expect(needLine("App/github", "the scope apps:write or the role straza-global-mcp-admin.")).toBe("The server github needs the scope apps:write or the role straza-global-mcp-admin.");
    expect(checkedLine("2026-10-30 06:44:03 UTC", "1188e760")).toBe("2026-10-30 06:44:03 UTC, against live policy 1188e760");
    expect(revisionLine(3, "alice", "console", "10:49", true)).toBe("revision 3 by alice in the console at 10:49, checked again against live state");
    expect(outcomeFoot("needs-approval", "a hold, up to 10 minutes, decided by sec-approvers")).toBe("needs approval: a hold, up to 10 minutes, decided by sec-approvers.");
    expect(groupLine("demo-tools-readers", "", ["sam-sre-agent"], 1, "", [], ["1 tool"])).toBe("demo-tools-readers · held by sam-sre-agent · loses 1 tool");
    expect(groupLine("demo-tools-readers", "demo-tools", undefined, 3, "", ["2 tools"], [])).toBe("demo-tools-readers on demo-tools · 3 people · gains 2 tools");
    expect(groupLine("desk-new", "", [], 0, "1 ticket: T-9", ["13 tools"], [])).toBe("desk-new · nobody holds it · on 1 ticket: T-9 · gains 13 tools");
    const tickets = { type: "ticket", plural: "tickets" };
    expect(coveredWords(tickets, false, [])).toBe("no tickets");
    expect(coveredWords(tickets, false, [{ id: "t2", value: "T-2" }])).toBe("1 ticket: T-2");
    expect(decidedWords("published", "alice")).toBe("Published by alice");
    expect(decidedWords("expired", undefined)).toBe("Expired");
  });
});

// The words of In this draft and the card heads, built from values. The
// fixture's type is invented, ticket and tickets, so a hardcoded noun fails.
describe("the rows and the card heads", () => {
  const tickets = { type: "ticket", plural: "tickets" };

  const chips: [string, string, string][] = [
    ["one tool that joins", toolsChip("+", 1), "+1 tool"],
    ["two tools that leave", toolsChip("-", 2), "-2 tools"],
    ["one ticket that joins", objectsChip("+", 1, tickets), "+1 ticket"],
    ["three tickets that leave", objectsChip("-", 3, tickets), "-3 tickets"],
    ["every ticket", everyChip("+", tickets), "+every ticket"],
    ["the unchanged fold", unchangedFold(9), "9 unchanged"],
    ["a set that asks and denies", outcomesWords(["hum", "deny"]), "needs approval · denied"],
    ["a set that allows", outcomesWords(["allow"]), "allowed"],
  ];
  it.each(chips)("words %s", (_name, got, want) => {
    expect(got).toBe(want);
  });

  const lines: [string, string, string][] = [
    ["a new server whose tools were read", startsLine(true, 17, false), "Starts it and offers its 17 tools to the roles that reach them."],
    ["a new server whose tools were not read", startsLine(true, null, false), "Starts it. Its tools are not read yet."],
    ["a new command server", startsLine(false, null, false), "Starts it."],
    ["a published new server", startsLine(true, 2, true), "Started it and offered its 2 tools to the roles that reach them."],
    ["a first pin", changesLine("tickets", [], false, "none"), "Pins tickets."],
    ["a first pin and a field", changesLine("tickets", ["Rate limit"], false, "none"), "Pins tickets and changes rate limit."],
    ["two fields, published", changesLine("", ["Rate limit", "Per-call timeout"], true, "none"), "Changed rate limit and per-call timeout."],
    ["no field", changesLine("", [], false, "No field the server page shows changes."), "No field the server page shows changes."],
    ["a new role on one server", reachesLine([{ app: "tracker", tools: ["ticket_read", "ticket_search"] }], []), "Reaches 2 tools of tracker."],
    ["a new role on every tool of two servers", reachesLine([{ app: "tracker", tools: ["*"] }, { app: "desk", tools: ["a"] }], []), "Reaches every tool of tracker and 1 tool of desk."],
    ["a new business role", reachesLine([], ["tracker-triage"]), "Implies tracker-triage."],
    ["a new set's rules", rulesLine({ new: 2, edited: 0, removed: 0 }), "2 new policy rules."],
    ["a changed set's rules", rulesLine({ new: 1, edited: 1, removed: 1 }), "1 new policy rule, 1 edited and 1 removed."],
    ["a set that only edits and removes", rulesLine({ new: 0, edited: 2, removed: 1 }), "2 edited policy rules and 1 removed."],
    ["a set whose rules stay", rulesLine({ new: 0, edited: 0, removed: 0 }), "Changes no policy rule."],
  ];
  it.each(lines)("says what publishing does to %s", (_name, got, want) => {
    expect(got).toBe(want);
  });

  it("reads a rename of a value as no change, by id", () => {
    expect(objectsMove({ ...COVERAGE, after: [{ id: "t1", value: "T-1-renamed" }] }).changed).toBe(false);
  });
});

// The pills of the review page: each built from a value, with no period,
// and a count of one in the singular.
describe("the pills of the review page", () => {
  const pills: [string, string, string][] = [
    ["another sponsor", sponsoredBy("carol"), "sponsored by carol"],
    ["the straza app", doorPill("straza-app"), "through the straza app"],
    ["a door this console does not know", doorPill("bundle"), "bundle"],
    ["a published draft's age", agoPill("published", "5 h ago"), "published 5 h ago"],
    ["one passed check", passedChecks(1), "1 check passed"],
    ["six passed checks", passedChecks(6), "6 checks passed"],
    ["what to confirm", toConfirm(2), "2 to confirm"],
    ["one policy rule", ruleCount(1), "1 policy rule"],
    ["a changed share of policy rules", rulesChanged(2, 8), "2 of 8 policy rules"],
    ["one holder", holderCount(1), "1 holder"],
    ["many holders", holderCount(1200), "1,200 holders"],
    ["one object", objectCount(1), "1 object"],
    ["two objects", objectCount(2), "2 objects"],
    ["an approver role", roleKindPill("approver"), "approver role"],
    ["a role of a kind this console does not know", roleKindPill("future"), "role"],
    ["the rest of a list", moreCount(8), "+8 more"],
    ["one pattern, closed", showPattern(1, false), "Show the pattern"],
    ["two patterns, open", showPattern(2, true), "Hide the patterns"],
    ["the rules a changed set keeps", rulesUnchanged(6), "6 policy rules unchanged"],
    ["a file", yamlName("audio-gen"), "audio-gen.yaml"],
    ["passed lines", passedCount(4), "4 passed"],
    ["one note", notesCount(1), "1 note"],
    ["a stored check's revision", revisionPill(2), "revision 2"],
    ["the console as a client", fromClient("console"), "from the console"],
    ["a client nobody named", fromClient(undefined), "from a client Straza did not name"],
  ];
  it.each(pills)("words %s", (_name, got, want) => {
    expect(got).toBe(want);
  });
});
