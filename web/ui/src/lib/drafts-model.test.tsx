import { describe, expect, it } from "vitest";
import type { DraftFinding, DraftGain, DraftVerdict } from "./api";
import {
  type Acks,
  acknowledged,
  appFacts,
  appRows,
  changeCounts,
  confirmBlocks,
  findingGroups,
  firstMissing,
  gainGroups,
  gainMark,
  gainStory,
  gainsHidden,
  hostOf,
  impliedObjects,
  impliedOf,
  manifestOf,
  pickKey,
  publishBody,
  punyURL,
  remoteApp,
  roleOf,
  roleToolRows,
  ruleMarks,
  sidesOf,
  stateOf,
  typedMatches,
  typedWrongly,
} from "./drafts-model";
import { CUT_RISK, DETAIL, GITHUB_APP, PUBLISHED_DETAIL, STALE, VERDICT, demoTools, detailWith, readersSet, role, WRITERS_SET } from "@/test/drafts-fixture";

// The pure reading of a draft the review page and the publish dialog share.
// The publish body is the one place a mistake would publish the wrong thing,
// so it is pinned on keys that are labels: a key computed from the words
// instead of echoed would not match them.

const acks = (ticked: Record<string, boolean>, typed: Record<string, string>): Acks => ({ ticked, typed });

// The risks of a newer server, which groups them: a server removal typed by
// its name, three loosened sets typed by their count and two widened roles
// under one tick. The object type is an invented one, so a noun the
// console hardcoded would not match.
const risk = (key: string, code: string, object: string, over: Partial<DraftFinding> = {}): DraftFinding =>
  ({ code, class: "risk", ack: "tick", object, key, sentence: key + " widens access.", ...over });
const REMOVAL = risk("k-rm", "server.removal", "App/legacy-desk", { ack: "typed", typed: "legacy-desk" });
const SETS = ["a", "b", "c"].map((s) => risk("k-set-" + s, "policy.guardrail-changed", "PolicySet/" + s + "-access", { ack: "typed", typed: s + "-access", group: "policy.guardrail-changed" }));
const WIDER = ["x", "y"].map((r) => risk("k-wide-" + r, "role.covers-wider", "Role/desk-" + r, { group: "role.covers-wider", sentence: "desk-" + r + " will reach 2 more tickets on desk: T-1 and T-2." }));
const block = (risks: DraftFinding[], i = 0) => confirmBlocks(risks)[i];

describe("the confirm blocks", () => {
  it("types a removal by its name, a group by its count and ticks a group once, names first", () => {
    const blocks = confirmBlocks([WIDER[0], ...SETS, REMOVAL, WIDER[1]]);
    expect(blocks.map((b) => [b.kind, b.expect, b.lines.map((f) => f.key), b.blocked])).toEqual([
      ["name", "legacy-desk", ["k-rm"], false],
      ["count", "3", ["k-set-a", "k-set-b", "k-set-c"], false],
      ["tick", "", ["k-wide-x", "k-wide-y"], false],
    ]);
  });

  // A name block is keyed by the object and the text, so a server and a set
  // that share a name are still two things to confirm.
  it("keeps a removal and an agent's loosening of a set with the same name as two blocks", () => {
    const removal = risk("k-rm-desk", "server.removal", "App/desk", { ack: "typed", typed: "desk" });
    const loosen = risk("k-loosen-desk", "agent.guardrail", "PolicySet/desk", { ack: "typed", typed: "desk" });
    expect(confirmBlocks([removal, loosen]).map((b) => [b.kind, b.expect, b.lines.map((f) => f.key)])).toEqual([
      ["name", "desk", ["k-rm-desk"]],
      ["name", "desk", ["k-loosen-desk"]],
    ]);
  });

  it("types a group of one line by that line's name", () => {
    expect(confirmBlocks([SETS[1]]).map((b) => [b.kind, b.expect])).toEqual([["name", "b-access"]]);
  });

  it("reads a verdict whose lines carry no group line by line", () => {
    expect(confirmBlocks(VERDICT.risks).map((b) => [b.kind, b.expect, b.lines.map((f) => f.key)])).toEqual([
      ["name", "api.githubcopilot.com", ["k-host"]],
      ["tick", "", ["k-reach"]],
    ]);
  });

  // A member whose text was cut stays in its group, so the count a reader
  // sees is the whole group's and the group as a whole cannot be confirmed.
  it("keeps a group with a line cut for this reader whole, and blocks it", () => {
    const cut = { ...SETS[2], typed: undefined, object: undefined };
    expect(confirmBlocks([SETS[0], SETS[1], cut]).map((b) => [b.kind, b.lines.length, b.blocked])).toEqual([["count", 3, true]]);
    expect(confirmBlocks([CUT_RISK]).map((b) => [b.kind, b.blocked])).toEqual([["name", true]]);
  });
});

describe("the publish body", () => {
  it("sends every key of a count block with each line's own name, for the count typed once", () => {
    const body = publishBody(2, { ...VERDICT, risks: SETS }, acks({}, { [block(SETS).id]: " 3 " }));
    expect(body).toEqual({
      revision: 2, risk_digest: VERDICT.risk_digest,
      ticked: ["k-set-a", "k-set-b", "k-set-c"],
      typed: { "k-set-a": "a-access", "k-set-b": "b-access", "k-set-c": "c-access" },
    });
  });

  it("sends every key of a ticked group and no text for its lines", () => {
    const body = publishBody(2, { ...VERDICT, risks: WIDER }, acks({ [block(WIDER).id]: true }, {}));
    expect(body).toEqual({ revision: 2, risk_digest: VERDICT.risk_digest, ticked: ["k-wide-x", "k-wide-y"], typed: {} });
  });

  it("echoes the keys of a verdict with no groups in its order, with each line's own text", () => {
    const [host, reach] = confirmBlocks(VERDICT.risks);
    const body = publishBody(2, VERDICT, acks({ [reach.id]: true }, { [host.id]: " API.githubcopilot.com " }));
    expect(body).toEqual({ revision: 2, risk_digest: VERDICT.risk_digest, ticked: ["k-host", "k-reach"], typed: { "k-host": "api.githubcopilot.com" } });
  });

  it("leaves out a block left, typed wrong or blocked", () => {
    expect(publishBody(2, VERDICT, acks({}, {}))).toEqual({ revision: 2, risk_digest: VERDICT.risk_digest, ticked: [], typed: {} });
    expect(publishBody(2, { ...VERDICT, risks: SETS }, acks({}, { [block(SETS).id]: "2" })).ticked).toEqual([]);
    expect(publishBody(2, { ...VERDICT, risks: [CUT_RISK] }, acks({ [block([CUT_RISK]).id]: true }, { [block([CUT_RISK]).id]: "" })).ticked).toEqual([]);
  });

  it("sends a verdict with no risks as an empty acknowledgment", () => {
    const v: DraftVerdict = { ...VERDICT, risks: [], risk_digest: "" };
    expect(publishBody(3, v, acks({}, {}))).toEqual({ revision: 3, risk_digest: "", ticked: [], typed: {} });
  });
});

describe("whether a block is confirmed", () => {
  const tick = block(WIDER);
  const name = block([REMOVAL]);
  const count = block(SETS);
  const cases: [string, ReturnType<typeof block>, Acks, boolean][] = [
    ["a tick left empty", tick, acks({}, {}), false],
    ["a tick ticked", tick, acks({ [tick.id]: true }, {}), true],
    ["a tick ticked under another block's id", tick, acks({ [name.id]: true }, {}), false],
    ["a name left empty", name, acks({}, {}), false],
    ["a name typed as another name", name, acks({}, { [name.id]: "legacy-dex" }), false],
    ["a name with spaces and capitals", name, acks({}, { [name.id]: "  Legacy-Desk " }), true],
    ["a count typed as the count", count, acks({}, { [count.id]: "3" }), true],
    ["a count typed as one of its names", count, acks({}, { [count.id]: "a-access" }), false],
    ["a block with a line this reader cannot see", block([CUT_RISK]), acks({ [block([CUT_RISK]).id]: true }, {}), false],
  ];
  it.each(cases)("%s", (_name, b, a, want) => {
    expect(acknowledged(b, a)).toBe(want);
  });

  it("compares typed text the way strazactl does", () => {
    expect(typedMatches("x.example", " X.EXAMPLE ")).toBe(true);
    expect(typedMatches("x.example", "x.example.")).toBe(false);
  });

  it("names the first block left, in the dialog's order", () => {
    const blocks = confirmBlocks([...WIDER, REMOVAL]);
    const [rm, wide] = blocks;
    expect(firstMissing(blocks, acks({}, {}))).toBe(rm.id);
    expect(firstMissing(blocks, acks({}, { [rm.id]: "legacy-desk" }))).toBe(wide.id);
    expect(firstMissing(blocks, acks({ [wide.id]: true }, { [rm.id]: "legacy-desk" }))).toBe(null);
  });
});

describe("the reading of a verdict", () => {
  it("groups the lines by class in the page's order, reading a missing list as empty", () => {
    const v = { ...VERDICT, info: null } as unknown as DraftVerdict;
    expect(findingGroups(v).map((g) => [g.cls, g.lines.length])).toEqual([
      ["refused", 0], ["risk", 2], ["warning", 2], ["unchecked", 1], ["passed", 1], ["info", 0],
    ]);
  });

  it("keeps a line whose code it does not know, under the class it came in", () => {
    const v = { ...VERDICT, warnings: [{ code: "some.future-code", class: "warning", key: "k-f", sentence: "A line from a newer server." }] } as DraftVerdict;
    expect(findingGroups(v).find((g) => g.cls === "warning")?.lines.map((l) => l.sentence)).toEqual(["A line from a newer server."]);
  });

  const states: [string, Parameters<typeof detailWith>[0], string][] = [
    ["an open draft with nothing refused is ready", {}, "ready"],
    ["an open draft with a refusal is refused", { verdict: { refused: [{ code: "role.name", class: "refused", key: "r", sentence: "No." }] } }, "refused"],
    ["an open draft whose live state moved is out of date", { verdict: { refused: [STALE] } }, "stale"],
    ["a published draft", { draft: { state: "published" } }, "published"],
    ["a discarded draft", { draft: { state: "discarded" } }, "discarded"],
    ["an expired draft", { draft: { state: "expired" } }, "expired"],
  ];
  it.each(states)("%s", (_name, patch, want) => {
    expect(stateOf(detailWith(patch))).toBe(want);
  });

  it("keys a pick by the object and the field, as the rebase body reads it", () => {
    expect(pickKey({ object: "App/demo-tools", field: "straza.limits.rps", base: "5", draft: "20", live: "10" })).toBe("App/demo-tools straza.limits.rps");
  });
});

describe("who gains what", () => {
  const g = (before: DraftGain["before"], after: DraftGain["after"], beforeWords?: string, afterWords?: string): DraftGain =>
    ({ role: "r", server: "s", tool: "t", holders_count: 0, before, after, before_words: beforeWords, after_words: afterWords });
  const marks: [string, DraftGain, string][] = [
    ["a tool a role starts to reach gains", g("not-reachable", "runs"), "gains"],
    ["a gate that opens gains", g("needs-approval", "runs"), "gains"],
    ["a denied tool that needs approval now gains", g("denied", "needs-approval"), "gains"],
    ["a tool that leaves loses", g("needs-approval", "not-reachable"), "loses"],
    ["a tool that becomes denied loses", g("runs", "denied"), "loses"],
    ["the same outcome in other words is a gate change", g("needs-approval", "needs-approval", "a hold, 2 minutes", "a hold, 5 minutes"), "gate"],
    ["the same outcome in the same words is the same", g("runs", "runs"), "same"],
    ["an outcome the engine could not read is not known", g("unknown", "runs"), "unknown"],
  ];
  it.each(marks)("%s", (_name, row, want) => {
    expect(gainMark(row)).toBe(want);
  });

  it("groups the rows by role and server, in the order they came", () => {
    expect(gainGroups(VERDICT.gains).map((x) => [x.role, x.server, x.rows.map((r) => r.tool)])).toEqual([
      ["github-readers", "github", ["get_me"]],
      ["github-writers", "github", ["create_pull_request"]],
      ["demo-tools-readers", "demo-tools", ["echo", "get-env"]],
    ]);
  });

  it("tells who gains and who loses from the holders the rows carry", () => {
    expect(gainStory(VERDICT.gains)).toEqual({ nobodyHolds: ["github-readers", "github-writers"], gain: [], lose: ["demo-tools-readers"], none: false });
    expect(gainStory([]).none).toBe(true);
    const held = { ...VERDICT.gains[0], holders_count: 2 };
    expect(gainStory([held]).gain).toEqual(["github-readers"]);
  });
});

describe("the before and after of an item", () => {
  it("reads a published draft from its change record, never from live state", () => {
    const [app, role] = PUBLISHED_DETAIL.draft.items;
    expect(sidesOf(PUBLISHED_DETAIL, app)).toEqual({ before: { op: "put", doc: demoTools(10) }, after: { op: "put", doc: demoTools(20) } });
    expect(sidesOf(PUBLISHED_DETAIL, role).after.op).toBe("remove");
    expect(impliedOf(PUBLISHED_DETAIL)).toEqual(["PolicySet/old-readers-access"]);
  });

  it("reads a published item the record does not name as no change", () => {
    const d = { ...PUBLISHED_DETAIL, changes: [] };
    const it = PUBLISHED_DETAIL.draft.items[0];
    expect(sidesOf(d, it)).toEqual({ before: { op: "put", doc: it.doc }, after: { op: "put", doc: it.doc } });
  });

  it("reads an open draft against live state, and a new object as having no before", () => {
    expect(sidesOf(DETAIL, DETAIL.draft.items[1])).toEqual({ before: { op: "put", doc: demoTools(10) }, after: { op: "put", doc: demoTools(20) } });
    expect(sidesOf(DETAIL, DETAIL.draft.items[0]).before).toBe(null);
    expect(impliedOf(DETAIL)).toEqual(["Role/old-tools-readers"]);
  });

  it("finds the line that says rows of who gains what are hidden", () => {
    expect(gainsHidden(VERDICT)).toBe(null);
    expect(gainsHidden({ ...VERDICT, info: [{ code: "info.gains-hidden", class: "info", key: "h", sentence: "2 rows are hidden." }] })).toBe("2 rows are hidden.");
  });

  it("tells a typed text that can no longer match from an empty one or a start that fits", () => {
    const name = block([REMOVAL]);
    const count = block(SETS);
    const wrong = (b: ReturnType<typeof block>, text: string) => typedWrongly(b, acks({}, { [b.id]: text }));
    expect([wrong(name, "legacy-dex"), wrong(name, " "), wrong(name, "Legacy-D"), wrong(name, "legacy-desk ")]).toEqual([true, false, false, false]);
    expect([wrong(count, "4"), wrong(count, "3")]).toEqual([true, false]);
    expect(wrong(block(WIDER), "x")).toBe(false);
    expect(wrong(block([CUT_RISK]), "x")).toBe(false);
  });
});

describe("the documents of the items", () => {
  it("reads a host in its punycode form and leaves a text that is no address as it is", () => {
    expect(punyURL("https://bücher.example/mcp")).toBe("https://xn--bcher-kva.example/mcp");
    expect(punyURL("not an address")).toBe("not an address");
    expect(hostOf("https://bücher.example:8443/mcp", "x")).toBe("xn--bcher-kva.example:8443");
    expect(hostOf("http://", "github")).toBe("github");
  });

  it("reads an App document's facts in the server page's words", () => {
    const facts = appFacts(manifestOf(GITHUB_APP) || {});
    expect(facts).toEqual([
      ["Transport", "HTTP, streamable"],
      ["Address", "https://api.githubcopilot.com/mcp/"],
      ["Type", "Each caller's own token"],
      ["Sent as", "The Authorization header, as Bearer and the person's token."],
      ["Agents with nothing of their own", "Run on their sponsor's token once the sponsor allowed it (agents sponsor)."],
      ["Description", "GitHub's remote MCP server"],
      ["Tools exposed", "All tools, including ones it adds later."],
      ["Rate limit", "Not limited."],
    ]);
    expect(remoteApp(GITHUB_APP)).toBe(true);
    expect(remoteApp(demoTools(5).replace("kind: remote", "kind: command"))).toBe(false);
  });

  it("reads a changed App as the Now and After rows of the change sheets", () => {
    expect(appRows(manifestOf(demoTools(10)) || {}, manifestOf(demoTools(20)) || {})).toEqual([["Rate limit", "10 calls per second, per session.", "20 calls per second, per session."]]);
  });

  it("reads a document that does not parse as nothing", () => {
    expect(manifestOf("key: [unclosed")).toBe(null);
    expect(roleOf("key: [unclosed")).toBe(null);
  });

  it("reads a Role's reach and marks each tool that joins or leaves it", () => {
    const after = roleOf(role("demo-tools-readers", "demo-tools", ["echo", "get-sum", "get-tiny"]));
    const before = roleOf(role("demo-tools-readers", "demo-tools", ["echo", "get-env", "get-sum"]));
    expect(after?.bindings).toEqual([{ app: "demo-tools", tools: ["echo", "get-sum", "get-tiny"] }]);
    expect(roleToolRows(before, after)).toEqual([
      { server: "demo-tools", tool: "echo", before: true, after: true },
      { server: "demo-tools", tool: "get-env", before: true, after: false },
      { server: "demo-tools", tool: "get-sum", before: true, after: true },
      { server: "demo-tools", tool: "get-tiny", before: false, after: true },
    ]);
    expect(roleToolRows(null, after).every((r) => !r.before && r.after)).toBe(true);
  });

  it("marks each rule of an approval set against the live text", () => {
    expect(ruleMarks(readersSet(true), readersSet(false)).map((r) => [r.rule.id, r.mark])).toEqual([["get-sum", "edited"], ["get-env", "removed"]]);
    expect(ruleMarks(null, WRITERS_SET).map((r) => [r.rule.id, r.mark])).toEqual([["github-pr-hold", "new"]]);
    expect(ruleMarks(WRITERS_SET, WRITERS_SET).map((r) => r.mark)).toEqual(["same"]);
  });

  it("names the live objects a removal takes along", () => {
    expect(impliedObjects(DETAIL)).toEqual(["Role/old-tools-readers"]);
  });

  it("counts the items by kind and by what they do", () => {
    expect(changeCounts(DETAIL.draft.items)).toEqual([
      { kind: "App", what: "new", n: 1 },
      { kind: "App", what: "changed", n: 1 },
      { kind: "App", what: "removed", n: 1 },
      { kind: "Role", what: "new", n: 1 },
      { kind: "Role", what: "changed", n: 2 },
      { kind: "PolicySet", what: "new", n: 1 },
      { kind: "PolicySet", what: "changed", n: 1 },
    ]);
  });
});
