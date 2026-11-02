import { describe, expect, it } from "vitest";
import type { DraftPrincipal } from "./api";
import { coverageNouns, leadOf, objectsMove } from "./drafts-access";
import { changeCounts, confirmBlocks } from "./drafts-model";
import {
  ackBar,
  ackWords,
  blockTitle,
  changesLede,
  decidedWords,
  doesOf,
  doorWords,
  goLive,
  groupLine,
  keepDraft,
  keepLive,
  checkedLine,
  coverLead,
  coveredWords,
  docsFold,
  holdsWords,
  leadLines,
  needLine,
  outcomeFoot,
  pickWas,
  proposerWords,
  publishLead,
  revisionLine,
  signLine,
  typedWrong,
  widenCount,
} from "./drafts-words";
import { COVERAGE, COVERS_DETAIL, CUT_RISK, DETAIL, HOST_RISK, REACH_RISK, detailWith } from "@/test/drafts-fixture";

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
    ["a file of the apps directory", p({ username: "strazad", via: "file" }), "/etc/straza/apps/demo-tools.yaml", { name: "the file demo-tools.yaml", line: "no person" }],
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
  it("counts what changes and what goes live", () => {
    const counts = changeCounts(DETAIL.draft.items);
    expect(changesLede(counts)).toBe("1 new MCP server, 1 changed MCP server, 1 MCP server removed, 1 new role, 2 changed roles, 1 new approval set and 1 changed approval set. They go live together or not at all.");
    expect(goLive(counts)).toBe("Publishing adds 1 MCP server, 1 role and 1 approval set, changes 1 MCP server, 2 roles and 1 approval set and removes 1 MCP server in one step, or nothing changes.");
    expect(goLive([{ kind: "App", what: "removed", n: 1 }])).toBe("Publishing removes 1 MCP server in one step, or nothing changes.");
    expect(goLive([{ kind: "PolicySet", what: "off", n: 1 }])).toBe("Publishing turns off 1 approval set in one step, or nothing changes.");
    expect(goLive([{ kind: "Role", what: "changed", n: 1 }])).toBe("Publishing changes 1 role in one step, or nothing changes.");
  });

  // Each case is the risks of one confirm block and how the page says the
  // publish dialog asks for it.
  const grouped = (key: string, typed: string) => ({ ...HOST_RISK, key, typed, object: "App/" + typed, group: HOST_RISK.code });
  const acks: [string, (typeof HOST_RISK)[], string][] = [
    ["a tick", [REACH_RISK], "Tick it when you publish: republishing the old state undoes it."],
    ["a group of ticks", [{ ...REACH_RISK, group: "reach.new" }, { ...REACH_RISK, key: "k2", group: "reach.new" }], "One tick confirms the 2 lines of this kind when you publish: republishing the old state undoes them."],
    ["a typed line", [HOST_RISK], "Type api.githubcopilot.com when you publish: republishing the old state cannot undo it."],
    ["a group of typed lines", [grouped("a", "a.example"), grouped("b", "b.example"), grouped("c", "c.example")], "Type 3 when you publish, once for the 3 lines of this kind: republishing the old state cannot undo them."],
    ["an agent's loosening", [{ ...HOST_RISK, key: "k-l", code: "agent.guardrail", object: "PolicySet/desk-gate", typed: "desk-gate" }], "Type desk-gate when you publish to accept it."],
    ["a typed line cut for the reader", [CUT_RISK], "Only someone who can read what this line names can confirm it, so ask an admin who can read it to publish this draft."],
  ];
  it.each(acks)("words how %s is confirmed", (_name, risks, want) => {
    expect(ackWords(confirmBlocks(risks)[0])).toBe(want);
  });

  it("words the bar: what to do before Publish, counted by confirm block", () => {
    expect(ackBar(1, 1, 0)).toBe("1 line needs your confirmation when you publish.");
    expect(ackBar(3, 3, 2)).toBe("Read 2 warnings first. 3 lines need your confirmation when you publish.");
    expect(ackBar(1, 100, 0)).toBe("Publishing asks you for 1 confirmation.");
    expect(ackBar(2, 21, 1)).toBe("Read 1 warning first. Publishing asks you for 2 confirmations.");
    expect(ackBar(0, 0, 1)).toBe("Read 1 warning before you publish.");
    expect(ackBar(0, 0, 0)).toBe("Nothing to confirm.");
    expect(widenCount(1)).toBe("1 widens access");
    expect(widenCount(3)).toBe("3 widen access");
  });

  it("words a pick's values, an unset one included", () => {
    expect(pickWas("5")).toBe("It was 5 when the draft was checked.");
    expect(pickWas("")).toBe("It was unset when the draft was checked.");
    expect(keepDraft("")).toBe("Keep the draft's value, unset");
    expect(keepLive("10")).toBe("Keep live's value, 10");
  });

  it("says what a typed block needs when the entry cannot match", () => {
    expect(typedWrong("20", "confirm")).toBe("That is not 20. Type 20 to confirm.");
    expect(typedWrong("agent-guardrails", "accept it")).toBe("That is not agent-guardrails. Type agent-guardrails to accept it.");
  });

  // The lead of the publish dialog counts the objects each kind of line
  // names, so two lines on one object count once, and a code the table
  // does not know reads by the kind of its object.
  it("says what publishing does by kind of line, in the order of the blocks", () => {
    const line = (code: string, object?: string, key = code + object) => ({ code, object, key });
    const sets = ["a", "b"].map((s) => line("policy.guardrail-changed", "PolicySet/" + s));
    expect(publishLead([line("server.removal", "App/legacy-desk"), ...sets, line("role.covers-wider", "Role/desk-x"), line("access.gated", "Role/desk-y")]))
      .toBe("Publishing removes 1 MCP server, loosens 2 approval sets and widens 2 roles, in one step, or nothing changes.");
    expect(publishLead([line("agent.guardrail", "PolicySet/agent-guardrails"), line("policy.guardrail-changed", "PolicySet/agent-guardrails")])).toBe("Publishing loosens 1 approval set, or nothing changes.");
    expect(publishLead([line("some.future-code", "App/desk"), line("other.code", "App/desk"), line("x.y", "Ticket/T-1"), line("x.y", "Ticket/T-2")]))
      .toBe("Publishing widens access through 1 MCP server and widens access through 2 objects, in one step, or nothing changes.");
    expect(doesOf("constructor", "toString/x")).toEqual(["widens access through", "object", "objects"]);
  });

  // A line this reader may not read comes without its object, so two of
  // them may sit on one server: the lead counts them as lines, never as
  // objects.
  it("counts the lines a reader may not read as lines", () => {
    const cut = (code: string, key: string) => ({ code, key });
    expect(publishLead([cut("server.runs-code", "k1"), cut("server.plain-http", "k2")])).toBe("Publishing widens access through 2 lines, or nothing changes.");
    expect(doesOf("x.y", undefined)).toEqual(["widens access through", "line", "lines"]);
    expect(blockTitle(doesOf("policy.guardrail-changed", "PolicySet/a"), 20)).toBe("Loosens 20 approval sets");
  });

  const signs: [string, string | null, boolean, boolean, string][] = [
    ["a person's draft", null, false, true, "The publish record names you."],
    ["an agent's self-loosening the reader accepts", null, true, true, "The publish record names you as the person who accepted it."],
    ["the sponsor of an agent's self-loosening", "joe", true, true, "You sponsor joe. When second-person publishing is on, another admin must publish this. The publish record names you as the person who accepted it."],
    ["the sponsor of an agent's draft with a risk", "joe", false, true, "You sponsor joe. When second-person publishing is on, another admin must publish this. The publish record names you."],
    ["the sponsor of an agent's draft with no risk", "joe", false, false, "You sponsor joe, which proposed this draft. The publish record names you."],
  ];
  it.each(signs)("says who the publish record names for %s", (_name, sponsored, accepts, risky, want) => {
    expect(signLine(sponsored, accepts, risky)).toBe(want);
  });

  it("words the other lines built from values", () => {
    expect(needLine("App/github", "the scope apps:write or the role straza-global-mcp-admin.")).toBe("The server github needs the scope apps:write or the role straza-global-mcp-admin.");
    expect(checkedLine("2026-10-30 06:44:03 UTC", "1188e760")).toBe("2026-10-30 06:44:03 UTC, against live policy 1188e760");
    expect(revisionLine(3, "alice", "console", "10:49", true)).toBe("rev 3 by alice in the console at 10:49, checked again against live state");
    expect(outcomeFoot("needs-approval", "a hold, up to 10 minutes, decided by sec-approvers")).toBe("needs approval: a hold, up to 10 minutes, decided by sec-approvers.");
    expect(groupLine("demo-tools-readers", "", ["sam-sre-agent"], 1, "", [], ["1 tool"])).toBe("demo-tools-readers · held by sam-sre-agent · loses 1 tool");
    expect(groupLine("demo-tools-readers", "demo-tools", undefined, 3, "", ["2 tools"], [])).toBe("demo-tools-readers on demo-tools · 3 people · gains 2 tools");
    expect(groupLine("desk-new", "", [], 0, "1 ticket: T-9", ["13 tools"], [])).toBe("desk-new · nobody holds it · on 1 ticket: T-9 · gains 13 tools");
    const tickets = { type: "ticket", plural: "tickets" };
    expect(coveredWords(tickets, false, [])).toBe("no tickets");
    expect(coveredWords(tickets, false, [{ id: "t2", value: "T-2" }])).toBe("1 ticket: T-2");
    expect(holdsWords(1)).toBe("1 person holds it.");
    expect(holdsWords(2)).toBe("2 people hold it.");
    expect(docsFold(2, "open")).toBe("2 files, as they will be published");
    expect(docsFold(1, "open")).toBe("1 file, as it will be published");
    expect(docsFold(3, "published")).toBe("3 files, as they were published");
    expect(docsFold(2, "discarded")).toBe("2 files, as they were drafted");
    expect(decidedWords("published", "alice")).toBe("Published by alice");
    expect(decidedWords("expired", undefined)).toBe("Expired");
  });
});

// The lead of the review page: what publishing does to the objects of
// each role, in the server's own nouns, then who holds the roles. The
// fixture's type is invented, ticket and tickets, so a hardcoded noun
// fails.
describe("the lead of the page", () => {
  const lead = (c = COVERAGE, isNew = false) => coverLead({ coverage: c, move: objectsMove(c), isNew }, coverageNouns(c));
  const t = (n: number) => ({ id: "t" + n, value: "T-" + n });

  const covers: [string, Partial<typeof COVERAGE>, boolean, string][] = [
    ["a widening", {}, false, "Changes the role tracker-triage: it will reach 2 more tickets on tracker, T-2 and T-3."],
    ["a widening to every ticket", { every_after: true, after: [] }, false, "Changes the role tracker-triage: it will reach every ticket on tracker, including tickets added later."],
    ["a narrowing", { before: [t(1), t(2)], after: [t(1)] }, false, "Changes the role tracker-triage: it will stop reaching 1 ticket on tracker, T-2."],
    ["a swap", { before: [t(1)], after: [t(2)] }, false, "Changes the role tracker-triage: it will reach 1 more ticket on tracker, T-2, and stop reaching 1 ticket, T-1."],
    ["every narrowed to some", { every_before: true, before: [], after: [t(1), t(2)] }, false, "Changes the role tracker-triage: it will reach only 2 tickets on tracker instead of every ticket: T-1 and T-2."],
    ["a new role", { before: [], after: [t(9)] }, true, "Adds the role tracker-triage on tracker, for 1 ticket: T-9."],
    ["a new role on every ticket", { before: [], every_after: true, after: [] }, true, "Adds the role tracker-triage on tracker, for every ticket, including tickets added later."],
  ];
  it.each(covers)("says %s", (_name, over, isNew, want) => {
    expect(lead({ ...COVERAGE, ...over }, isNew)).toBe(want);
  });

  it("reads a rename of a value as no change, by id", () => {
    expect(objectsMove({ ...COVERAGE, after: [{ id: "t1", value: "T-1-renamed" }] }).changed).toBe(false);
  });

  it("says the widening and who holds the role for a draft no gain row shows", () => {
    expect(leadLines(leadOf(COVERS_DETAIL, false), coverageNouns, false).map((l) => l.text)).toEqual([
      "Changes the role tracker-triage: it will reach 2 more tickets on tracker, T-2 and T-3.", "1 person holds it.",
    ]);
  });

  it("says nobody gains when nobody holds the roles, and who loses what they reach today", () => {
    expect(leadLines(leadOf(DETAIL, false), coverageNouns, false)).toEqual([
      { text: "Nobody holds github-readers or github-writers yet, so nobody gains access until the identity manager assigns them." },
      { text: "Holders of demo-tools-readers lose tools they reach today, and their calls to them are refused after publishing.", loses: true },
    ]);
  });

  it("claims nothing about the hidden rows, and keeps what it can say about the rows it shows", () => {
    expect(leadLines(leadOf(DETAIL, true), coverageNouns, true).map((l) => l.text)).toEqual([
      "Nobody holds github-readers or github-writers yet.",
      "Holders of demo-tools-readers lose tools they reach today, and their calls to them are refused after publishing.",
    ]);
  });

  it("says a held role gains, and nobody gains only when no held role gains", () => {
    const held = detailWith({ verdict: { gains: [{ ...DETAIL.verdict.gains[0], holders_count: 2 }] } });
    expect(leadLines(leadOf(held, false), coverageNouns, false).map((l) => l.text)).toEqual(["Holders of github-readers gain access when this is published."]);
    const loses = detailWith({ verdict: { gains: [DETAIL.verdict.gains[3]] } });
    expect(leadLines(leadOf(loses, false), coverageNouns, false).map((l) => l.text)[0]).toBe("Nobody gains access when this is published.");
  });
});
