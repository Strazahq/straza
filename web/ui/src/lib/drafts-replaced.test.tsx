import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DraftDetail, DraftSummary } from "./api";
import { publishedFor } from "./drafts-api";
import { type Reads, readLater, replacedBy } from "./drafts-replaced";
import { ALICE, PUBLISHED_DETAIL, demoTools, readersSet } from "@/test/drafts-fixture";

// A published draft is replaced when a later publish wrote one of its
// objects and that object is no longer live as this draft left it. Later
// is by the time a draft was decided, and by the id only between drafts
// decided in the same second.

vi.mock("./drafts-api", async (orig) => ({ ...(await orig<typeof import("./drafts-api")>()), publishedFor: vi.fn() }));

// PUBLISHED_DETAIL is draft 40, published at 10:51. Its change record left
// demo-tools at 20 calls per second, old-readers removed and the readers
// set at readersSet(false).
const AT = "2026-09-24T10:51:00Z";
const row = (id: string, decided: string): DraftSummary => ({
  id, title: "Draft " + id, state: "published", door: "console", revision: 1, proposer: ALICE, items: [], created_at: decided, updated_at: decided, decided_at: decided, decided_by: ALICE,
});
const LATER = row("44", "2026-09-25T09:00:00Z");
const LATEST = row("45", "2026-09-26T09:00:00Z");
const EARLIER = row("39", "2026-09-23T09:00:00Z");

// asLeft is draft 40 with live state as its publish left it.
const asLeft: DraftDetail = {
  ...PUBLISHED_DETAIL,
  live: {
    "App/demo-tools": { op: "put", doc: demoTools(20) },
    "Role/old-readers": { op: "remove", doc: "" },
    "PolicySet/demo-tools-readers-access": { op: "put", doc: readersSet(false) },
  },
};
const moved = (object: string, live: { op: "put" | "remove" | "off"; doc?: string }): DraftDetail => ({ ...asLeft, live: { ...asLeft.live, [object]: live } });

const cases: [string, DraftDetail, Reads, string | null][] = [
  ["no later draft", moved("Role/old-readers", { op: "put", doc: "" }), [[EARLIER], [], [row("40", AT)]], null],
  ["a later draft that left every object as this one did", asLeft, [[LATER], [], []], null],
  ["a later draft that put back a role this one removed", moved("Role/old-readers", { op: "put", doc: "" }), [[], [LATER], []], "44"],
  ["a later draft that turned off its set", moved("PolicySet/demo-tools-readers-access", { op: "off", doc: readersSet(false) }), [[], [], [LATER]], "44"],
  ["a later draft that wrote another text of its set", moved("PolicySet/demo-tools-readers-access", { op: "put", doc: readersSet(true) }), [[], [], [LATER]], "44"],
  ["a later server text, which a server never counts by", moved("App/demo-tools", { op: "put", doc: demoTools(30) }), [[LATER], [], []], null],
  ["two later drafts, the first decided being the successor", moved("Role/old-readers", { op: "put", doc: "" }), [[LATEST], [LATER], []], "44"],
  ["a draft with a higher id decided before this one", moved("Role/old-readers", { op: "put", doc: "" }), [[row("47", "2026-09-24T10:00:00Z")], [], []], null],
  ["a lower id decided after this one, after Check again", moved("Role/old-readers", { op: "put", doc: "" }), [[row("38", "2026-09-24T11:30:00Z")], [], []], "38"],
  ["a higher id decided in the same second, which is the later one", moved("Role/old-readers", { op: "put", doc: "" }), [[row("41", AT), row("42", AT)], [], []], "41"],
  ["a lower id decided in the same second, which came before", moved("Role/old-readers", { op: "put", doc: "" }), [[row("39", AT)], [], []], null],
  ["a failed read, which counts as no later draft", moved("Role/old-readers", { op: "put", doc: "" }), [null, null, null], null],
  ["a failed read beside one that found the successor", moved("Role/old-readers", { op: "put", doc: "" }), [null, [LATER], null], "44"],
];

describe("whether a later publish replaced a published draft", () => {
  it.each(cases)("reads %s", (_name, d, reads, want) => {
    expect(replacedBy(d, reads)?.id ?? null).toBe(want);
  });

  it("never reads a draft with more than eight items as replaced", () => {
    const items = Array.from({ length: 9 }, (_, i) => ({ kind: "App" as const, name: "s" + i, op: "put" as const, existed: true }));
    const d = moved("Role/old-readers", { op: "put", doc: "" });
    expect(replacedBy({ ...d, draft: { ...d.draft, items: [...d.draft.items, ...items] } }, [[LATER]])).toBe(null);
  });

  it("never reads a draft that is not published as replaced", () => {
    const d = moved("Role/old-readers", { op: "put", doc: "" });
    expect(replacedBy({ ...d, draft: { ...d.draft, state: "discarded" } }, [[LATER]])).toBe(null);
  });
});

describe("the reads of the later drafts", () => {
  beforeEach(() => vi.resetAllMocks());

  it("reads the published drafts of each object in parallel, a failed read as null", async () => {
    vi.mocked(publishedFor).mockImplementation(async (object) => {
      if (object === "Role/old-readers") throw new Error("refused");
      return { items: [LATER], next_cursor: "" };
    });
    expect(await readLater(asLeft)).toEqual([[LATER], null, [LATER]]);
    expect(vi.mocked(publishedFor).mock.calls.map((c) => c[0])).toEqual(["App/demo-tools", "Role/old-readers", "PolicySet/demo-tools-readers-access"]);
  });

  it("reads nothing for a draft with more than eight items or one that is not published", async () => {
    const items = Array.from({ length: 9 }, (_, i) => ({ kind: "App" as const, name: "s" + i, op: "put" as const, existed: true }));
    expect(await readLater({ ...asLeft, draft: { ...asLeft.draft, items } })).toEqual([]);
    expect(await readLater({ ...asLeft, draft: { ...asLeft.draft, state: "open" } })).toEqual([]);
    expect(publishedFor).not.toHaveBeenCalled();
  });
});
