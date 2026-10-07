import { describe, expect, it } from "vitest";
import type { DraftFinding, DraftGain, DraftVerdict } from "./api";
import {
  type Acks,
  acknowledged,
  appFacts,
  appRows,
  changeCounts,
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
import { CUT_RISK, DETAIL, GITHUB_APP, HOST_RISK, PUBLISHED_DETAIL, REACH_RISK, STALE, VERDICT, demoTools, detailWith, readersSet, role, WRITERS_SET } from "@/test/drafts-fixture";

// The pure reading of a draft the review page and the publish dialog share.
// The publish body is the one place a mistake would publish the wrong thing,
// so it is pinned on keys that are labels: a key computed from the words
// instead of echoed would not match them.

const acks = (ticked: Record<string, boolean>, typed: Record<string, string>): Acks => ({ ticked, typed });

describe("the publish body", () => {
  it("echoes the risk keys, the digest and the typed text from the verdict on screen", () => {
    const body = publishBody(2, VERDICT, acks({ "k-reach": true }, { "k-host": " API.githubcopilot.com " }));
    expect(body).toEqual({ revision: 2, risk_digest: VERDICT.risk_digest, ticked: ["k-host", "k-reach"], typed: { "k-host": " API.githubcopilot.com " } });
  });

  it("leaves out a line the person did not acknowledge", () => {
    const body = publishBody(2, VERDICT, acks({}, {}));
    expect(body).toEqual({ revision: 2, risk_digest: VERDICT.risk_digest, ticked: [], typed: {} });
  });

  it("sends a verdict with no risks as an empty acknowledgment", () => {
    const v: DraftVerdict = { ...VERDICT, risks: [], risk_digest: "" };
    expect(publishBody(3, v, acks({}, {}))).toEqual({ revision: 3, risk_digest: "", ticked: [], typed: {} });
  });
});

describe("whether a line is acknowledged", () => {
  const cases: [string, DraftFinding, Acks, boolean][] = [
    ["a tick left empty", REACH_RISK, acks({}, {}), false],
    ["a tick ticked", REACH_RISK, acks({ "k-reach": true }, {}), true],
    ["a tick ticked under another key", REACH_RISK, acks({ "k-host": true }, {}), false],
    ["a typed line left empty", HOST_RISK, acks({}, {}), false],
    ["a typed line with another host", HOST_RISK, acks({}, { "k-host": "api.github.com" }), false],
    ["a typed line with spaces and capitals", HOST_RISK, acks({}, { "k-host": "  Api.GithubCopilot.com " }), true],
    ["a typed line whose text this reader cannot see", CUT_RISK, acks({}, {}), true],
  ];
  it.each(cases)("%s", (_name, f, a, want) => {
    expect(acknowledged(f, a)).toBe(want);
  });

  it("compares typed text the way strazactl does", () => {
    expect(typedMatches("x.example", " X.EXAMPLE ")).toBe(true);
    expect(typedMatches("x.example", "x.example.")).toBe(false);
  });

  it("names the first line left, in the verdict's order", () => {
    expect(firstMissing(VERDICT.risks, acks({}, {}))).toBe("k-host");
    expect(firstMissing(VERDICT.risks, acks({}, { "k-host": "api.githubcopilot.com" }))).toBe("k-reach");
    expect(firstMissing(VERDICT.risks, acks({ "k-reach": true }, { "k-host": "api.githubcopilot.com" }))).toBe(null);
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

  it("tells a wrong typed text from an empty one", () => {
    expect(typedWrongly(HOST_RISK, acks({}, { "k-host": "api.github.com" }))).toBe(true);
    expect(typedWrongly(HOST_RISK, acks({}, { "k-host": " " }))).toBe(false);
    expect(typedWrongly(HOST_RISK, acks({}, { "k-host": "api.githubcopilot.com" }))).toBe(false);
    expect(typedWrongly(REACH_RISK, acks({}, {}))).toBe(false);
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
