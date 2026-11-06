import { describe, expect, it } from "vitest";
import type { DraftCoverage } from "./api";
import { chipNotes, roleHolders } from "./drafts-access";
import { itemFacts } from "./drafts-items";
import { COVERAGE, COVERS_DETAIL, DETAIL, PUBLISHED_DETAIL, VERDICT, detailWith, trackerRole } from "@/test/drafts-fixture";

// What the row of In this draft and the card head say about each item:
// the kind in full, the server that owns a role, who holds it, the
// outcomes of a policy set, the summary chips of a changed role and one
// line of what publishing does, from the fixture's eight items.

const facts = (d = DETAIL) => Object.fromEntries(d.draft.items.map((it) => [it.kind + "/" + it.name, itemFacts(d, it)]));

describe("the facts of each item", () => {
  const f = facts();
  const cases: [string, string, string, string, string][] = [
    ["App/github", "MCP server", "", "new", "Starts it. Its tools are not read yet."],
    ["App/demo-tools", "MCP server", "", "changed", "Changes rate limit."],
    ["Role/github-readers", "Application role", "github", "new", "Reaches 2 tools of github."],
    ["Role/demo-tools-readers", "Application role", "demo-tools", "changed", ""],
    ["PolicySet/github-writers-access", "Policy set", "", "new", "1 rule new."],
    ["PolicySet/demo-tools-readers-access", "Policy set", "", "changed", "1 rule edited and 1 rule removed."],
    ["App/old-tools", "MCP server", "", "removed", "Publishing removes it with everything live that belongs to it."],
    ["Role/secret-role", "Role", "", "changed", DETAIL.draft.items[7].withheld as string],
  ];
  it.each(cases)("reads %s", (object, kind, owner, what, line) => {
    expect([f[object].kind, f[object].owner, f[object].what, f[object].line]).toEqual([kind, owner, what, line]);
  });

  it("sums up a changed role's tools as chips and leaves its line to them", () => {
    expect(f["Role/demo-tools-readers"].summary).toEqual([{ sign: "-", word: "-1 tool" }]);
  });

  it("names who holds each role and the outcomes of each set", () => {
    expect([f["Role/github-readers"].holders, f["Role/demo-tools-readers"].holders, f["App/github"].holders]).toEqual([0, 1, null]);
    expect([f["PolicySet/github-writers-access"].outcomes, f["PolicySet/demo-tools-readers-access"].outcomes]).toEqual(["needs approval", "needs approval"]);
  });

  it("links an object that is live now, and never one the reader may not read", () => {
    expect(Object.entries(f).filter(([, x]) => x.live).map(([o]) => o)).toEqual(["App/demo-tools", "Role/demo-tools-readers", "PolicySet/demo-tools-readers-access", "App/old-tools"]);
  });

  it("counts the tools a contact read for a new server", () => {
    const d = detailWith({ contacted: { "App/github": { at: "2026-09-24T10:47:00Z", tools: ["get_me", "list_issues"] } } });
    expect(itemFacts(d, d.draft.items[0]).line).toBe("Starts it and offers its 2 tools to the roles that reach them.");
  });

  it("says a first pin pins the server's own plural", () => {
    const live = "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: tracker\nstraza:\n  runtime:\n    kind: remote\n    remote:\n      url: http://tracker:8080/mcp\n";
    const pinned = live + "  objects:\n    type: ticket\n    plural: tickets\n    list:\n      tool: ticket_list\n";
    const d = detailWith({ draft: { items: [{ kind: "App", name: "tracker", op: "put", doc: pinned, existed: true }] }, live: { "App/tracker": { op: "put", doc: live } } });
    expect(itemFacts(d, d.draft.items[0]).line).toBe("Pins tickets.");
  });

  it("reads a published draft's items in the past tense from the change record", () => {
    const p = facts(PUBLISHED_DETAIL);
    expect(p["App/demo-tools"].line).toBe("Changed rate limit.");
    expect(p["Role/old-readers"].line).toBe("This publish removed it, with everything live that belonged to it.");
    expect(p["App/demo-tools"].holders).toBe(null);
    expect(p["Role/old-readers"].live).toBe(false);
  });
});

// The objects of a changed role come from the verdict's coverage entry, in
// the server's own nouns: an invented ticket type, and the postgres words.
describe("the objects of a changed role", () => {
  const ofCoverage = (c: DraftCoverage) => {
    const d = detailWith({ ...COVERS_DETAIL, verdict: { ...COVERS_DETAIL.verdict, objects: [c] } });
    return itemFacts(d, d.draft.items[0]);
  };
  const t = (n: number) => ({ id: "t" + n, value: "T-" + n });
  const cases: [string, Partial<DraftCoverage>, string[]][] = [
    ["a widening", {}, ["+2 tickets"]],
    ["a swap", { before: [t(1)], after: [t(2)] }, ["+1 ticket", "-1 ticket"]],
    ["a widening to every ticket", { every_after: true, after: [] }, ["+every ticket"]],
    ["every narrowed to some", { every_before: true, before: [], after: [t(1)] }, ["-every ticket"]],
    ["the postgres nouns", { type: "database", plural: "databases", before: [], after: [{ id: "d1", value: "sales_eu" }] }, ["+1 database"]],
  ];
  it.each(cases)("sums up %s", (_name, over, want) => {
    const f = ofCoverage({ ...COVERAGE, ...over });
    expect(f.summary.map((c) => c.word)).toEqual(want);
    expect(f.holders).toBe(1);
  });

  it("names the fields of a role whose tools and objects stay", () => {
    const doc = trackerRole("tracker-triage", ["ticket_read"], ["T-1"]);
    const d = detailWith({ ...COVERS_DETAIL, draft: { ...COVERS_DETAIL.draft, items: [{ kind: "Role", name: "tracker-triage", op: "put", doc: doc.replace("spec:\n", "spec:\n    description: Triage desk.\n"), existed: true }] }, live: { "Role/tracker-triage": { op: "put", doc } }, verdict: { ...COVERS_DETAIL.verdict, objects: [{ ...COVERAGE, after: COVERAGE.before }] } });
    const f = itemFacts(d, d.draft.items[0]);
    expect([f.summary, f.line]).toEqual([[], "Changes description."]);
  });
});

describe("who holds a role", () => {
  it("reads the holders from a gain row, else a coverage entry, else the note that nobody holds it", () => {
    expect(roleHolders(VERDICT, "demo-tools-readers")).toBe(1);
    expect(roleHolders(COVERS_DETAIL.verdict, "tracker-triage")).toBe(1);
    expect(roleHolders({ ...VERDICT, gains: [] }, "github-readers")).toBe(0);
    expect(roleHolders(VERDICT, "nobody-knows")).toBe(null);
  });

  it("leaves out the note a role's holders chip already says", () => {
    expect(chipNotes(DETAIL)).toEqual(["k-i1"]);
    expect(chipNotes(COVERS_DETAIL)).toEqual([]);
  });
});
