import { describe, expect, it } from "vitest";
import { ApiError, type DraftVerdict } from "./api";
import { errorLines, itemsOf, nothingToRead, refusedLines, sameDocs, sameItems } from "./draft-save";
import { HOST_RISK, VERDICT } from "@/test/drafts-fixture";

// The pure half of the editors' saves: what a verdict leaves to read, the
// body an editor sends, and the lines a refusal shows beside the buttons.

const EMPTY: DraftVerdict = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [] };
const WARNING = VERDICT.warnings[0];
const REFUSAL = { code: "approver-role-unknown", class: "refused" as const, object: "PolicySet/w", key: "k-r", sentence: "approve.roles names release-approvers, which is not a role in Straza.", fix: "Name sec-approvers instead." };

describe("nothingToRead", () => {
  it.each([
    { name: "an empty verdict", v: EMPTY, want: true },
    { name: "an unchecked line alone", v: { ...EMPTY, unchecked: VERDICT.unchecked }, want: true },
    { name: "an info line alone", v: { ...EMPTY, info: VERDICT.info }, want: true },
    { name: "a passed line alone", v: { ...EMPTY, passed: VERDICT.passed }, want: true },
    { name: "a risk", v: { ...EMPTY, risks: [HOST_RISK] }, want: false },
    { name: "a warning", v: { ...EMPTY, warnings: [WARNING] }, want: false },
    { name: "a refusal", v: { ...EMPTY, refused: [REFUSAL] }, want: false },
  ])("is $want for $name", ({ v, want }) => {
    expect(nothingToRead(v)).toBe(want);
  });
});

describe("the items an editor sends", () => {
  it("keeps kind, name, op and doc alone, in order, with an empty doc for a removal", () => {
    const answered = [{ kind: "App" as const, name: "demo-tools", op: "put" as const, doc: "x", existed: true, base: "fp" }, { kind: "App" as const, name: "old", op: "remove" as const }];
    expect(itemsOf(answered)).toEqual([{ kind: "App", name: "demo-tools", op: "put", doc: "x" }, { kind: "App", name: "old", op: "remove", doc: "" }]);
  });

  it("tells the same save from a changed one", () => {
    const a = [{ kind: "App" as const, name: "demo-tools", op: "put" as const, doc: "rps: 15" }];
    expect(sameItems(a, [{ ...a[0] }])).toBe(true);
    expect(sameItems(a, [{ ...a[0], doc: "rps: 20" }])).toBe(false);
    expect(sameItems(a, [...a, { kind: "Role", name: "r", op: "put", doc: "" }])).toBe(false);
  });
});

describe("sameDocs", () => {
  // Each case is the items a check answered and the items a draft read
  // answered, both in the answered form, and whether they are one change.
  const role = { kind: "Role" as const, name: "dev", op: "put" as const, doc: "kind: Role\n", existed: true };
  const set = { kind: "PolicySet" as const, name: "dev-access", op: "put" as const, doc: "kind: PolicySet\n", existed: true };
  it.each([
    { name: "the same items in another order", held: [set, role], want: true },
    { name: "a document that differs", held: [{ ...role, doc: "kind: Role\nspec: {}\n" }, set], want: false },
    { name: "an op that differs", held: [{ ...role, op: "remove" as const, doc: "" }, set], want: false },
    { name: "one item more", held: [role, set, { ...role, name: "ops" }], want: false },
    { name: "one item less", held: [role], want: false },
    { name: "a document the reader may not read", held: [{ ...role, doc: undefined, withheld: "Straza leaves out the document of Role/dev." }, set], want: false },
  ])("is $want for $name", ({ held, want }) => {
    expect(sameDocs([role, set], held)).toBe(want);
  });
});

describe("the refusal lines", () => {
  it("reads each refused line of a verdict as its sentence and its fix", () => {
    expect(refusedLines({ ...EMPTY, refused: [REFUSAL, { ...REFUSAL, fix: undefined, sentence: "Second." }] })).toEqual([
      "approve.roles names release-approvers, which is not a role in Straza. Name sec-approvers instead.",
      "Second.",
    ]);
  });

  it("reads a 422 by its findings, and any other refusal by the server's sentence", () => {
    const body = { error: "joined", findings: [REFUSAL] };
    expect(errorLines(new ApiError("joined", 422, false, 0, body))).toEqual([REFUSAL.sentence + " " + REFUSAL.fix]);
    expect(errorLines(new ApiError("Draft 4 is published, so it cannot change.", 409, false, 0, { error: "Draft 4 is published, so it cannot change." }))).toEqual(["Draft 4 is published, so it cannot change."]);
    expect(errorLines(new ApiError("HTTP 500", 500))).toEqual(["HTTP 500"]);
  });
});
