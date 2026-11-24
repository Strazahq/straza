import { describe, expect, it } from "vitest";
import type { DraftChange, DraftDetail, DraftItem, DraftState, RoleRow } from "./api";
import { type ShortContext, type ShortRow, shortRows } from "./drafts-short";
import { DETAIL, PUBLISHED_DETAIL, WRITERS_SET, demoTools, detailWith, readersSet, role } from "@/test/drafts-fixture";

// In short builds a label and a row of pills per fact, from the draft's
// detail alone, and builds no row at all where it could be wrong.

const CTX: ShortContext = { registered: null, replaced: false, roles: null };
const NO_LINES = { refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [] };

// was is the live op of an object that exists, put unless it says off.
type Pair = { kind: DraftItem["kind"]; name: string; op: DraftItem["op"]; existed: boolean; doc: string; live: string; tag: string | null; was?: DraftItem["op"] };

// draftOf is a one-item draft in a state: an open one reads live state, a
// published one its change record.
function draftOf(state: DraftState, p: Pair): DraftDetail {
  const object = p.kind + "/" + p.name;
  const item: DraftItem = { kind: p.kind, name: p.name, op: p.op, doc: p.op === "remove" ? undefined : p.doc, existed: p.existed };
  const change: DraftChange = {
    kind: p.kind, name: p.name, implied: false,
    before_op: p.existed ? p.was || "put" : "remove", before_doc: p.existed ? p.live : "", before_fp: "a",
    after_op: p.op, after_doc: p.op === "remove" ? "" : p.doc, after_fp: "b",
  };
  return detailWith({
    draft: { state, items: [item], decided_at: state === "open" ? undefined : "2026-11-14T08:42:00Z" },
    verdict: NO_LINES,
    live: p.existed ? { [object]: { op: p.was || "put", doc: p.live } } : {},
    changes: state === "published" ? [change] : undefined,
  });
}

const values = (r: ShortRow | undefined) => (r ? r.values.map((v) => (v.t === "tag" ? "tag:" + v.kind + ":" + v.name : v.t === "pill" ? "pill:" + v.text : v.t === "outcome" ? "outcome:" + v.outcome : v.t === "rule" ? "rule:" + v.name : v.t === "muted" ? "muted:" + v.text : v.t === "assign" ? "assign:" + v.role + ":" + v.id : v.t)) : []);
const row = (rows: ShortRow[], label: string) => rows.find((r) => r.label === label);

// Every pair of item kind and change that intake accepts, each with the
// label and the pills of its row, open and published.
const SERVER = demoTools(20);
const ROLE = role("triage", "tracker", ["read"]);
const pairs: [string, Pair, [string, string], string[]][] = [
  ["a new server", { kind: "App", name: "tracker", op: "put", existed: false, doc: SERVER, live: "", tag: "server" }, ["Adds", "Added"], ["tag:server:tracker"]],
  ["a changed server", { kind: "App", name: "demo-tools", op: "put", existed: true, doc: SERVER, live: demoTools(10), tag: "server" }, ["Changes", "Changed"], ["tag:server:demo-tools"]],
  ["a removed server", { kind: "App", name: "demo-tools", op: "remove", existed: true, doc: "", live: demoTools(10), tag: "server" }, ["Removes", "Removed"], ["tag:server:demo-tools"]],
  ["a new role", { kind: "Role", name: "triage", op: "put", existed: false, doc: ROLE, live: "", tag: "role" }, ["Adds", "Added"], ["tag:role-application:triage"]],
  ["a changed role", { kind: "Role", name: "triage", op: "put", existed: true, doc: ROLE, live: role("triage", "tracker", ["read", "write"]), tag: "role" }, ["Changes", "Changed"], ["tag:role-application:triage"]],
  ["a removed role", { kind: "Role", name: "triage", op: "remove", existed: true, doc: "", live: ROLE, tag: "role" }, ["Removes", "Removed"], ["tag:role-application:triage"]],
  ["a new policy set", { kind: "PolicySet", name: "github-writers-access", op: "put", existed: false, doc: WRITERS_SET, live: "", tag: "set" }, ["Adds", "Added"], ["tag:policy-set:github-writers-access"]],
  ["a changed policy set", { kind: "PolicySet", name: "demo-tools-readers-access", op: "put", existed: true, doc: readersSet(false), live: readersSet(true), tag: "set" }, ["Changes", "Changed"], ["tag:policy-set:demo-tools-readers-access", "pill:2 of 2 policy rules"]],
  ["a policy set turned off", { kind: "PolicySet", name: "demo-tools-readers-access", op: "off", existed: true, doc: readersSet(true), live: readersSet(true), tag: "set" }, ["Turns off", "Turned off"], ["tag:policy-set:demo-tools-readers-access", "pill:2 policy rules"]],
  ["a policy set turned back on", { kind: "PolicySet", name: "demo-tools-readers-access", op: "put", existed: true, was: "off", doc: readersSet(true), live: readersSet(true), tag: "set" }, ["Turns on", "Turned on"], ["tag:policy-set:demo-tools-readers-access", "pill:2 policy rules"]],
  ["a removed policy set", { kind: "PolicySet", name: "demo-tools-readers-access", op: "remove", existed: true, doc: "", live: readersSet(true), tag: "set" }, ["Removes", "Removed"], ["tag:policy-set:demo-tools-readers-access"]],
];

describe("In short, per kind of item and change", () => {
  it.each(pairs)("says what publishing does to %s, open and published", (_name, p, [openWord, pastWord], want) => {
    for (const [state, word] of [["open", openWord], ["published", pastWord], ["discarded", openWord]] as const) {
      const rows = shortRows(draftOf(state, p), CTX);
      expect(rows[0].label).toBe(word);
      expect(values(rows[0])).toEqual(want);
    }
  });

  it("orders the rows new, changed, off, removed, whatever the order of the items", () => {
    const rows = shortRows(DETAIL, CTX);
    expect(rows.filter((r) => r.key.startsWith("does-")).map((r) => r.label)).toEqual([]);
    const known = detailWith({ draft: { items: DETAIL.draft.items.slice(0, 7) } });
    expect(shortRows(known, CTX).filter((r) => r.key.startsWith("does-")).map((r) => r.label)).toEqual(["Adds", "Changes", "Removes"]);
  });
});

describe("In short builds no row where it could be wrong", () => {
  const cases: [string, DraftDetail][] = [
    ["a draft with no item", detailWith({ draft: { items: [] } })],
    ["a draft with an item withheld from the reader", DETAIL],
    ["a server turned off, which intake refuses", detailWith({ draft: { items: [{ kind: "App", name: "demo-tools", op: "off", doc: SERVER, existed: true }] } })],
    ["a policy set whose text does not read", detailWith({ draft: { items: [{ kind: "PolicySet", name: "broken", op: "put", doc: "spec: [unclosed", existed: false }] }, live: {} })],
    ["a role whose text does not read", detailWith({ draft: { items: [{ kind: "Role", name: "broken", op: "put", doc: "spec: [unclosed", existed: false }] }, live: {} })],
  ];
  it.each(cases)("for %s", (_name, d) => {
    expect(shortRows(d, CTX)).toEqual([]);
  });
});

describe("what a removal takes along", () => {
  it("names a role its removed server owns as removed, on an open draft", () => {
    const d = detailWith({ draft: { items: [DETAIL.draft.items[6]] }, verdict: NO_LINES });
    expect(values(row(shortRows(d, CTX), "Also removes"))).toEqual(["tag:role-application:old-tools-readers"]);
  });

  it("names a role that only loses its row as changed, on an open draft", () => {
    const d = detailWith({ draft: { items: [DETAIL.draft.items[6]] }, verdict: NO_LINES, live: { "App/old-tools": { op: "put", doc: "" }, "Role/readers": { op: "put", doc: role("readers", "other", ["run"]) } } });
    expect(values(row(shortRows(d, CTX), "Also changes"))).toEqual(["tag:role-application:readers"]);
  });

  it("reads what went from the change record of a published draft", () => {
    const rows = shortRows(PUBLISHED_DETAIL, CTX);
    expect(values(row(rows, "Also removed"))).toEqual(["tag:policy-set:old-readers-access"]);
  });
});

// AUDIO is a new role and its set, whose rule allows two tools of a server
// that is not registered, which only a person's own harness runs.
const AUDIO_SET = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: audio-gen-access
spec:
  match:
    roles: [audio-gen]
  rules:
    - id: elevenlabs-audio-allow
      tools: [mcp.call]
      apps: [elevenlabs]
      toolNames:
        allow: [play_audio, compose_music]
      effect: allow
    - id: elevenlabs-voice-hold
      tools: [mcp.call]
      apps: [elevenlabs]
      effect: allow
      mode: approve
`;
const AUDIO_ROLE = "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n  name: audio-gen\nspec:\n  kind: application\n  bindings: []\n";
const audio = (state: DraftState, extra: Partial<DraftDetail> = {}) => detailWith({
  draft: {
    state, decided_at: state === "open" ? undefined : "2026-11-14T08:42:00Z",
    items: [{ kind: "Role", name: "audio-gen", op: "put", doc: AUDIO_ROLE, existed: false }, { kind: "PolicySet", name: "audio-gen-access", op: "put", doc: AUDIO_SET, existed: false }],
  },
  verdict: { ...NO_LINES, info: [{ code: "info.nobody-holds", class: "info", object: "Role/audio-gen", key: "k-n", sentence: "Nobody holds audio-gen yet." }] },
  live: {},
  changes: state === "published" ? [
    { kind: "Role", name: "audio-gen", implied: false, before_op: "remove", before_doc: "", before_fp: "", after_op: "put", after_doc: AUDIO_ROLE, after_fp: "a" },
    { kind: "PolicySet", name: "audio-gen-access", implied: false, before_op: "remove", before_doc: "", before_fp: "", after_op: "put", after_doc: AUDIO_SET, after_fp: "b" },
  ] : undefined,
  ...extra,
});

describe("what a role gets", () => {
  it("reads the gain rows of an open draft, needs approval first, and leaves out the rows that lose or stay", () => {
    const d = detailWith({ draft: { items: DETAIL.draft.items.slice(0, 7) } });
    expect(shortRows(d, CTX).filter((r) => r.label === "Gives").map(values)).toEqual([
      ["tag:null:github-writers", "arrow", "outcome:approval", "pill:1 tool", "muted:on", "tag:server:github"],
      ["tag:role-application:github-readers", "arrow", "outcome:allowed", "pill:1 tool", "muted:on", "tag:server:github"],
    ]);
  });

  it("reads a set's new rules that name a server on a person's machine, once the page can tell", () => {
    const rows = shortRows(audio("open"), { ...CTX, registered: ["github"] });
    expect(rows.filter((r) => r.label === "Gives").map(values)).toEqual([
      ["tag:role-application:audio-gen", "arrow", "outcome:approval", "pill:every tool", "muted:on", "tag:local-server:elevenlabs"],
      ["tag:role-application:audio-gen", "arrow", "outcome:allowed", "pill:2 tools", "muted:on", "tag:local-server:elevenlabs"],
    ]);
  });

  it("leaves those rows out where the page cannot tell a server on a person's machine apart, or the server is registered", () => {
    expect(row(shortRows(audio("open"), CTX), "Gives")).toBeUndefined();
    expect(row(shortRows(audio("open"), { ...CTX, registered: ["elevenlabs"] }), "Gives")).toBeUndefined();
  });

  it("keeps them on a published draft, and drops them on a replaced, a discarded or an expired one", () => {
    expect(shortRows(audio("published"), { ...CTX, registered: [] }).filter((r) => r.label === "Gives")).toHaveLength(2);
    expect(row(shortRows(audio("published"), { ...CTX, registered: [], replaced: true }), "Gives")).toBeUndefined();
    expect(row(shortRows(audio("discarded"), { ...CTX, registered: [] }), "Gives")).toBeUndefined();
    expect(row(shortRows(audio("expired"), { ...CTX, registered: [] }), "Gives")).toBeUndefined();
  });
});

describe("what a changed set loosens", () => {
  const loose = readersSet(false).replace("      mode: approve\n      approve:\n        roles: [sec-approvers]\n        timeoutSeconds: 300\n", "");
  it("names each edited rule whose outcome moves up, with its outcome before and after", () => {
    const d = draftOf("open", { kind: "PolicySet", name: "demo-tools-readers-access", op: "put", existed: true, doc: loose, live: readersSet(true), tag: "set" });
    expect(values(row(shortRows(d, CTX), "Loosens"))).toEqual(["rule:get-sum", "outcome:approval", "arrow", "outcome:allowed"]);
    const past = draftOf("published", { kind: "PolicySet", name: "demo-tools-readers-access", op: "put", existed: true, doc: loose, live: readersSet(true), tag: "set" });
    expect(row(shortRows(past, CTX), "Loosened")).toBeDefined();
  });

  it("says nothing of a rule that tightens or keeps its outcome", () => {
    const d = draftOf("open", { kind: "PolicySet", name: "demo-tools-readers-access", op: "put", existed: true, doc: readersSet(false), live: readersSet(true), tag: "set" });
    expect(row(shortRows(d, CTX), "Loosens")).toBeUndefined();
  });
});

describe("who is affected", () => {
  it("says nobody holds a new role yet, from the verdict of an open draft", () => {
    expect(values(row(shortRows(audio("open"), CTX), "Affects"))).toEqual(["pill:nobody yet"]);
  });

  it("names each role when the verdict tells the holders of more than one", () => {
    const d = detailWith({ draft: { items: DETAIL.draft.items.slice(0, 7) } });
    expect(values(row(shortRows(d, CTX), "Affects"))).toEqual(["tag:role-application:github-readers", "pill:nobody yet", "tag:role-application:demo-tools-readers", "pill:1 holder", "tag:null:github-writers", "pill:nobody yet"]);
  });

  it("adds everyone for a set with no match, and builds no row when nothing is known", () => {
    const everyone = WRITERS_SET.replace("  match:\n    roles: [github-writers]\n", "");
    const d = draftOf("open", { kind: "PolicySet", name: "github-writers-access", op: "put", existed: false, doc: everyone, live: "", tag: "set" });
    expect(values(row(shortRows(d, CTX), "Affects"))).toEqual(["pill:everyone"]);
    const unknown = draftOf("open", { kind: "PolicySet", name: "github-writers-access", op: "put", existed: false, doc: WRITERS_SET, live: "", tag: "set" });
    expect(row(shortRows(unknown, CTX), "Affects")).toBeUndefined();
  });

  const roles = (n: number): RoleRow[] => [{ id: "r-9", name: "audio-gen", kind: "application", holder_count: n }];
  it("reads the live role list of a published draft, and offers to assign a role it added that nobody holds", () => {
    expect(values(row(shortRows(audio("published"), { ...CTX, roles: roles(0) }), "Holders now"))).toEqual(["pill:nobody yet", "assign:audio-gen:r-9"]);
    expect(values(row(shortRows(audio("published"), { ...CTX, roles: roles(1200) }), "Holders now"))).toEqual(["pill:1,200 holders"]);
  });

  it("says nothing of holders on a replaced draft, while the list is unread, or for a role the list does not hold", () => {
    expect(row(shortRows(audio("published"), { ...CTX, roles: roles(0), replaced: true }), "Holders now")).toBeUndefined();
    expect(row(shortRows(audio("published"), CTX), "Holders now")).toBeUndefined();
    expect(row(shortRows(audio("published"), { ...CTX, roles: [] }), "Holders now")).toBeUndefined();
    expect(row(shortRows(audio("published"), { ...CTX, roles: roles(0) }), "Affects")).toBeUndefined();
  });
});
